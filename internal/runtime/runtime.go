// Package runtime is stado's UI-independent core: session lifecycle, tool
// executor wiring, and the headless agent loop. Both the TUI and the
// `stado run` headless surface compose this.
//
// PLAN.md §9.1 calls this "internal/core/runtime.go"; kept as "runtime" so the
// CLI import path reads naturally (internal/runtime.AgentLoop).
package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/lspfind"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/rg"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// RootContext returns the base context.Context callers should use as
// the ancestor of every span they create for this stado process.
// Normally this is context.Background(). When cwd contains a
// `.stado-span-context` written by a prior `stado session fork`, the
// base context is wrapped with the parent trace reference so Jaeger
// renders one fork tree instead of two disconnected ones.
//
// PLAN §9.4/9.5 cross-process span link. Safe to call from any
// caller (TUI, run, ACP, headless) at boot; no-op when no traceparent
// file is present.
func RootContext(cwd string) context.Context {
	ctx, _ := telemetry.LoadParentTraceparent(context.Background(), cwd)
	return ctx
}

// OpenSession creates a new session + sidecar rooted at cwd's repo.
// Non-fatal callers can swallow the error and carry on without state.
//
// Loads (or creates on first use) the agent signing key and attaches it to
// the session so every trace/tree commit carries an Ed25519 signature.
func OpenSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	userRepo := FindRepoRoot(cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		return nil, err
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
	if err != nil {
		return nil, err
	}

	priv, err := audit.LoadOrCreateKey(SigningKeyPath(cfg))
	if err == nil {
		sess.Signer = audit.NewSigner(priv)
	}
	// Signer is optional — unsigned commits still work; audit verify will
	// flag them.

	// Mirror every committed event to slog so operators get a structured
	// log line per tool call alongside the commit. OTel log exporter (PLAN
	// §5.5) bridges slog → OTLP when enabled; until then the lines land in
	// whatever sink slog.Default points at.
	sess.OnCommit = func(ev stadogit.CommitEvent) {
		slog.Info("stado.commit",
			slog.String("ref", ev.Ref),
			slog.String("hash", ev.Hash),
			slog.String("tool", ev.Meta.Tool),
			slog.String("short_arg", ev.Meta.ShortArg),
			slog.Int("turn", ev.Meta.Turn),
			slog.Int64("duration_ms", ev.Meta.DurationMs),
			slog.String("error", ev.Meta.Error),
		)
	}

	// Drop a pid file so `stado agents list` / `stado agents kill` can find
	// this process. Best-effort: ignore write errors (worktree might be
	// read-only or similar).
	pidPath := filepath.Join(sess.WorktreePath, ".stado-pid")
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)

	return sess, nil
}

// SigningKeyPath returns the path to stado's agent signing key.
func SigningKeyPath(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "keys", audit.KeyFileName)
}

// FindRepoRoot walks up from start looking for a .git dir; falls back to start.
func FindRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// BuildDefaultRegistry returns a Registry preloaded with stado's bundled tools
// (bash, fs, webfetch). Separate from Executor so callers can add/remove tools
// before constructing the Executor.
func BuildDefaultRegistry() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(fs.ReadTool{})
	r.Register(fs.WriteTool{})
	r.Register(fs.EditTool{})
	r.Register(fs.GlobTool{})
	r.Register(fs.GrepTool{})
	r.Register(bash.BashTool{Timeout: 60 * time.Second})
	r.Register(webfetch.WebFetchTool{})
	r.Register(rg.Tool{})
	r.Register(astgrep.Tool{})
	r.Register(readctx.Tool{})
	def := &lspfind.FindDefinition{}
	r.Register(def)
	r.Register(&lspfind.FindReferences{Definition: def})
	r.Register(&lspfind.DocumentSymbols{Definition: def})
	r.Register(&lspfind.Hover{Definition: def})
	return r
}

// BuildExecutor wires the tool registry + session + sandbox runner.
//
// Also loads any MCP servers from config and registers their tools. Failed
// MCP connections are logged to stderr, not fatal — stado should boot
// without them if the endpoint is down.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) *tools.Executor {
	reg := BuildDefaultRegistry()

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			fmt.Fprintf(os.Stderr, "stado: MCP setup: %v\n", err)
		}
	}

	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
		ReadLog:  tools.NewReadLog(),
	}
}

// attachMCP is defined in mcp_glue.go — kept in a separate file so pulling
// the MCP SDK in is a single-file diff and easier to #ifdef out on airgap
// builds later.

// ToolDefs renders the registry as []agent.ToolDef for a TurnRequest.
func ToolDefs(reg *tools.Registry) []agent.ToolDef {
	if reg == nil {
		return nil
	}
	all := reg.All()
	out := make([]agent.ToolDef, 0, len(all))
	for _, t := range all {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

// AgentLoopOptions parameterises a headless agent loop. Callers typically
// pre-build Executor (which owns the registry + session) and feed initial
// messages; the loop streams turn → tool calls → tool exec → next turn until
// no tool calls remain, or MaxTurns is hit.
type AgentLoopOptions struct {
	Provider agent.Provider
	Executor *tools.Executor
	Model    string
	Messages []agent.Message

	MaxTurns int // default 20

	// OnEvent receives every provider event before any accumulation happens.
	// Useful for stdout streaming in `stado run`.
	OnEvent func(agent.Event)

	// OnTurnComplete fires after a turn streams (text + tool-calls). Callers
	// can log or inspect without intervening.
	OnTurnComplete func(text string, toolCalls []agent.ToolUseBlock)

	// Host implements tool.Host during tool execution. Defaults to an
	// auto-approve host using Session.WorktreePath as workdir.
	Host tool.Host

	// Thinking controls extended-thinking injection. Values mirror
	// cfg.Agent.Thinking: "auto" / "on" / "off" / "" (same as auto).
	// Auto respects Capabilities.SupportsThinking on the active
	// provider.
	Thinking string
	// ThinkingBudgetTokens is threaded through to the provider when
	// Thinking resolves to on. 0 means "use a sensible default."
	ThinkingBudgetTokens int
}

// AgentLoop runs the headless multi-turn loop. Returns the final assistant
// text (concatenated across turns) and the final accumulated message
// history. Error is returned unchanged from the provider or executor.
func AgentLoop(ctx context.Context, opts AgentLoopOptions) (string, []agent.Message, error) {
	if opts.Provider == nil {
		return "", opts.Messages, errors.New("runtime: provider required")
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 20
	}
	if opts.Host == nil {
		workdir := ""
		var rlog *tools.ReadLog
		if opts.Executor != nil {
			if opts.Executor.Session != nil {
				workdir = opts.Executor.Session.WorktreePath
			}
			rlog = opts.Executor.ReadLog
		}
		opts.Host = autoApproveHost{workdir: workdir, readLog: rlog}
	}

	msgs := opts.Messages
	var finalText string

	// Append-only guardrail (DESIGN §"Context management" → "Append-only
	// history"). Prior messages are the cached prefix; any in-place mutation
	// invalidates every downstream cache entry. We record a hash at each
	// turn boundary and verify it survived the tool-execution interlude.
	// Mismatch panics under `go test` (fail loudly in CI); in release it
	// logs slog.Warn and continues — we cannot undo the mutation from here,
	// but the session's prompt-cache hit ratio will visibly collapse and
	// operators will see the warning line next to the metric.
	var priorHash string
	var priorLen int

	caps := opts.Provider.Capabilities()
	tracer := otel.Tracer(telemetry.TracerName)

	for turn := 0; turn < opts.MaxTurns; turn++ {
		if turn > 0 {
			got := hashMessagesPrefix(msgs, priorLen)
			if got != priorHash {
				violationMsg := fmt.Sprintf(
					"runtime: append-only invariant violated at turn %d (prior_len=%d, expected=%s, got=%s)",
					turn, priorLen, priorHash, got)
				if testing.Testing() {
					panic(violationMsg)
				}
				slog.Warn("stado.runtime.append_only_violation",
					slog.Int("turn", turn),
					slog.Int("prior_len", priorLen),
					slog.String("expected_hash", priorHash),
					slog.String("got_hash", got),
				)
			}
		}

		turnCtx, turnSpan := tracer.Start(ctx, telemetry.SpanTurn,
			trace.WithAttributes(
				attribute.Int("turn.index", turn),
				attribute.Int("turn.messages", len(msgs)),
				attribute.String("provider.name", opts.Provider.Name()),
				attribute.String("provider.model", opts.Model),
			),
		)

		req := agent.TurnRequest{
			Model:    opts.Model,
			Messages: msgs,
		}
		if opts.Executor != nil {
			req.Tools = ToolDefs(opts.Executor.Registry)
		}
		if caps.SupportsPromptCache && len(msgs) > 0 {
			// Single breakpoint at the end of the stable prefix — everything
			// up through the last prior message is the cache candidate.
			// DESIGN §"Prompt-cache awareness".
			req.CacheHints = []agent.CachePoint{{MessageIndex: len(msgs) - 1}}
		}
		// Extended-thinking injection (Phase 1.6 — capability-driven
		// branching). "auto" + supported → enable; "on" forces it even
		// when the provider might reject (for debugging); "off" hard
		// disables. Default budget of 16K when the caller didn't pin
		// one mirrors cfg.Agent.ThinkingBudgetTokens.
		if wantThinking(opts.Thinking, caps.SupportsThinking) {
			budget := opts.ThinkingBudgetTokens
			if budget <= 0 {
				budget = 16384
			}
			req.Thinking = &agent.ThinkingConfig{BudgetTokens: budget}
		}
		// Vision filtering. When the provider can't accept images,
		// quietly strip ImageBlocks before the request so the model
		// sees only what it can process. A slog.Warn surfaces each
		// dropped block so the caller can detect silent data loss.
		if !caps.SupportsVision {
			req.Messages = stripImageBlocks(req.Messages, opts.Provider.Name())
		}
		turnSpan.SetAttributes(attribute.Int("turn.tools", len(req.Tools)))
		ch, err := opts.Provider.StreamTurn(turnCtx, req)
		if err != nil {
			turnSpan.RecordError(err)
			turnSpan.SetStatus(codes.Error, err.Error())
			turnSpan.End()
			return finalText, msgs, fmt.Errorf("stream: %w", err)
		}

		text, calls, err := collectTurn(ch, opts.OnEvent)
		if err != nil {
			turnSpan.RecordError(err)
			turnSpan.SetStatus(codes.Error, err.Error())
			turnSpan.End()
			return finalText, msgs, err
		}
		turnSpan.SetAttributes(
			attribute.Int("turn.text_bytes", len(text)),
			attribute.Int("turn.tool_calls", len(calls)),
		)
		if opts.OnTurnComplete != nil {
			opts.OnTurnComplete(text, calls)
		}

		// Flush assistant turn (text + tool_uses) into history.
		var asst []agent.Block
		if text != "" {
			asst = append(asst, agent.Block{Text: &agent.TextBlock{Text: text}})
		}
		for i := range calls {
			tc := calls[i]
			asst = append(asst, agent.Block{ToolUse: &tc})
		}
		if len(asst) > 0 {
			msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Content: asst})
		}
		finalText += text

		if len(calls) == 0 {
			turnSpan.End()
			return finalText, msgs, nil
		}
		if opts.Executor == nil {
			turnSpan.End()
			return finalText, msgs, errors.New("runtime: tool calls requested but executor is nil")
		}

		// Execute tool calls, build role=tool message.
		var results []agent.Block
		for _, c := range calls {
			res, runErr := opts.Executor.Run(turnCtx, c.Name, c.Input, opts.Host)
			content := res.Content
			isErr := res.Error != ""
			if runErr != nil {
				content = runErr.Error()
				isErr = true
			} else if isErr {
				content = res.Error
			}
			results = append(results, agent.Block{ToolResult: &agent.ToolResultBlock{
				ToolUseID: c.ID,
				Content:   content,
				IsError:   isErr,
			}})
		}
		msgs = append(msgs, agent.Message{Role: agent.RoleTool, Content: results})

		priorLen = len(msgs)
		priorHash = hashMessagesPrefix(msgs, priorLen)
		turnSpan.End()
	}
	return finalText, msgs, fmt.Errorf("runtime: exceeded %d turns", opts.MaxTurns)
}

// wantThinking resolves the agent-loop Thinking knob against the
// provider's capability. Empty / "auto" → enable iff supported. "on"
// → enable unconditionally. "off" → always disabled.
func wantThinking(mode string, supported bool) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	default: // "", "auto"
		return supported
	}
}

// stripImageBlocks removes Image blocks from every message. Logs a
// slog.Warn per drop so callers notice when vision-laden input is
// being sent to a non-vision provider — better than a silent pass
// through that fails at provider-side with a less-specific error.
func stripImageBlocks(msgs []agent.Message, providerName string) []agent.Message {
	dropped := 0
	out := make([]agent.Message, len(msgs))
	for i, m := range msgs {
		if !hasImage(m.Content) {
			out[i] = m
			continue
		}
		filtered := make([]agent.Block, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Image != nil {
				dropped++
				continue
			}
			filtered = append(filtered, b)
		}
		out[i] = agent.Message{Role: m.Role, Content: filtered}
	}
	if dropped > 0 {
		slog.Warn("stado.runtime.vision_not_supported",
			slog.String("provider", providerName),
			slog.Int("image_blocks_dropped", dropped),
		)
	}
	return out
}

func hasImage(blocks []agent.Block) bool {
	for _, b := range blocks {
		if b.Image != nil {
			return true
		}
	}
	return false
}

// hashMessagesPrefix returns a short, stable fingerprint of msgs[:n]. Used by
// the append-only guardrail to detect in-place mutation of prior turns
// between StreamTurn calls. Hashes the JSON encoding; Go's encoding/json
// sorts map keys so ordering within Block/Message is deterministic.
func hashMessagesPrefix(msgs []agent.Message, n int) string {
	if n > len(msgs) {
		n = len(msgs)
	}
	h := fnv.New64a()
	enc := json.NewEncoder(h)
	for i := 0; i < n; i++ {
		_ = enc.Encode(msgs[i])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// collectTurn drains an event stream into (assistant_text, tool_calls).
func collectTurn(ch <-chan agent.Event, onEvent func(agent.Event)) (string, []agent.ToolUseBlock, error) {
	var text string
	var calls []agent.ToolUseBlock
	for ev := range ch {
		if onEvent != nil {
			onEvent(ev)
		}
		switch ev.Kind {
		case agent.EvTextDelta:
			text += ev.Text
		case agent.EvToolCallEnd:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case agent.EvError:
			return text, calls, ev.Err
		}
	}
	return text, calls, nil
}

type autoApproveHost struct {
	workdir string
	readLog *tools.ReadLog
}

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h autoApproveHost) Workdir() string { return h.workdir }

func (h autoApproveHost) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	if h.readLog == nil {
		return tool.PriorReadInfo{}, false
	}
	return h.readLog.PriorRead(key)
}

func (h autoApproveHost) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	if h.readLog == nil {
		return
	}
	h.readLog.RecordRead(key, info)
}
