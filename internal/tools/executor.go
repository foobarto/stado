package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/pkg/tool"
)

// Executor runs tools with sandboxing + git-native state commits (PLAN §4.6).
//
// Invariants per call:
//   - trace ref always gets a commit (metadata-only, empty-tree).
//   - tree ref gets a commit iff the tool is Mutating, or Exec and the
//     worktree tree hash changed.
//   - stado_tool_latency_ms is recorded on every call.
//   - failures still emit trace commits with an Error trailer.
type Executor struct {
	Registry *Registry
	Session  *stadogit.Session
	Runner   sandbox.Runner
	Metrics  telemetry.Metrics
	// Agent is the bot identity recorded in commit trailers (e.g. "claude-code-acp").
	Agent string
	// Model is the current LLM model for trailer recording.
	Model string
	// ReadLog records reads surfaced by the read tool so subsequent calls
	// this run can return a reference response rather than re-spending
	// tokens. See DESIGN §"Context management" → "In-turn deduplication".
	// Nil means dedup is disabled (tests, headless bootstrap).
	ReadLog *ReadLog
}

// Run invokes a tool by name. Returns the tool result and writes the commit
// trailers for audit. If the tool isn't registered, returns an error without
// touching refs.
func (e *Executor) Run(ctx context.Context, name string, args json.RawMessage, h tool.Host) (tool.Result, error) {
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	t, ok := e.Registry.Get(name)
	if !ok {
		return tool.Result{Error: "unknown tool"}, fmt.Errorf("unknown tool: %s", name)
	}
	class := e.Registry.ClassOf(name)

	ctx, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanToolCall,
		trace.WithAttributes(
			attribute.String("tool.name", name),
			attribute.String("tool.class", class.String()),
		),
	)
	defer span.End()

	// Install a fresh per-call progress collector. Bundled wasm
	// plugins that wire stado_progress also Append to this collector;
	// after the tool returns, we prepend the collected entries to
	// result.Content so the model sees the trail. EP-0038i.
	ctx, progCollector := tool.ContextWithProgress(ctx)

	// Capture pre-state for Exec diff-then-commit.
	var preTree plumbing.Hash
	if e.Session != nil && class == tool.ClassExec {
		pre, err := e.Session.CurrentTree()
		if err == nil {
			preTree = pre
		}
	}

	start := time.Now()
	res, runErr := t.Run(ctx, args, h)
	duration := time.Since(start)

	// Drain any progress emissions buffered during the tool call and
	// prepend them to the result envelope so the model sees the
	// trail. Operator-side ProgressEmitter delivery (TUI sidebar,
	// stderr) already happened live; this only adds the model-facing
	// channel. Skip when there's nothing to add or the tool errored.
	if entries := progCollector.Drain(); len(entries) > 0 && runErr == nil && res.Error == "" {
		res.Content = renderProgressLog(entries) + res.Content
	}

	outcome := "ok"
	if runErr != nil || res.Error != "" {
		outcome = "error"
	}
	span.SetAttributes(
		attribute.String("tool.outcome", outcome),
		attribute.Int64("tool.duration_ms", duration.Milliseconds()),
		attribute.Int("tool.result_bytes", len(res.Content)),
	)
	if runErr != nil {
		span.RecordError(runErr)
		span.SetStatus(codes.Error, runErr.Error())
	} else if res.Error != "" {
		span.SetStatus(codes.Error, res.Error)
	}
	if e.Metrics.ToolLatency != nil {
		e.Metrics.ToolLatency.Record(ctx, float64(duration.Milliseconds()))
	}

	meta := stadogit.CommitMeta{
		Tool:       name,
		ShortArg:   shortArgOf(args),
		Summary:    fmt.Sprintf("%s [%s]", class.String(), outcome),
		ArgsSHA:    sha256Of(args),
		ResultSHA:  sha256Of([]byte(res.Content)),
		Agent:      e.Agent,
		Model:      e.Model,
		DurationMs: duration.Milliseconds(),
	}
	if e.Session != nil {
		meta.Turn = e.Session.Turn()
	}
	if runErr != nil {
		meta.Error = runErr.Error()
	} else if res.Error != "" {
		meta.Error = res.Error
	}

	if e.Session == nil {
		return res, runErr
	}

	// trace ref always.
	if _, err := e.Session.CommitToTrace(meta); err != nil {
		return res, fmt.Errorf("commit trace: %w", err)
	}

	// tree ref policy.
	var treeHash plumbing.Hash
	switch class {
	case tool.ClassMutating:
		if runErr == nil && res.Error == "" {
			post, err := e.Session.BuildTreeFromDir(e.Session.WorktreePath)
			if err != nil {
				return res, fmt.Errorf("build tree: %w", err)
			}
			treeHash = post
		}
	case tool.ClassExec:
		post, err := e.Session.BuildTreeFromDir(e.Session.WorktreePath)
		if err != nil {
			return res, fmt.Errorf("build tree: %w", err)
		}
		if post != preTree && !post.IsZero() {
			treeHash = post
		}
	}
	if !treeHash.IsZero() {
		if _, err := e.Session.CommitToTree(treeHash, meta); err != nil {
			return res, fmt.Errorf("commit tree: %w", err)
		}
	}

	return res, runErr
}

func shortArgOf(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	// Prefer common key names that identify the operation.
	for _, k := range []string{"path", "file", "pattern", "command", "name", "url"} {
		if v, ok := m[k]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 40 {
				s = s[:40] + "…"
			}
			return s
		}
	}
	return ""
}

func sha256Of(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// renderProgressLog formats progress entries as a plain-text prefix
// the model can read. Each entry is one line tagged `[progress]`
// followed by a blank line separating from the actual result.
// Format chosen to round-trip cleanly through any tool-result
// transport (no JSON wrap, no markdown that might collide with
// tool-emitted markdown).
func renderProgressLog(entries []tool.ProgressEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("[progress] ")
		if e.Plugin != "" {
			b.WriteString(e.Plugin)
			b.WriteString(": ")
		}
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}
