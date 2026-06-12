package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/hooks"
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
	// Hooks is the lifecycle-hook runner fired around tool dispatch:
	// pre_tool (deny -> skip the tool + surface the reason; mutate ->
	// run the tool with rewritten args) and post_tool (mutate -> rewrite
	// the result/error the model sees; deny -> replace the result with
	// the reason). Nil / empty is a no-op — the common case. F1 seam.
	Hooks *hooks.LifecycleRunner
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

	// #030: in TUI live-cwd mode tools write the real checkout (h.Workdir()),
	// not the sidecar worktree, so auditing WorktreePath records an unchanged
	// tree that doesn't match what was mutated. Audit the directory actually
	// written. Scoped to live-cwd (workdir != worktree) so isolated/headless
	// sessions keep their existing semantics.
	auditDir := ""
	liveCwd := false
	if e.Session != nil {
		auditDir = e.Session.WorktreePath
		if wd := h.Workdir(); wd != "" && wd != e.Session.WorktreePath {
			auditDir = wd
			liveCwd = true
		}
	}

	// Capture pre-state for Exec diff-then-commit.
	var preTree plumbing.Hash
	if e.Session != nil && class == tool.ClassExec {
		var pre plumbing.Hash
		var err error
		if liveCwd {
			pre, err = e.Session.BuildTreeFromDir(auditDir)
		} else {
			pre, err = e.Session.CurrentTree()
		}
		if err == nil {
			preTree = pre
		}
	}

	// pre_tool hook seam (F1). Fires before the tool runs:
	//   - Deny  → skip the tool entirely; surface the reason to the model
	//     as an errored tool result, AND write a trace commit so the
	//     denial is auditable (pre-fix, a denied call early-returned with
	//     NO commit — denials were invisible in the signed audit chain).
	//   - Mutate → replace args with the rewritten JSON and run the tool
	//     with it; the audit trailers record the mutated args.
	if e.Hooks.HasPoint(hooks.PointPreTool) {
		pre := hooks.PreTool(e.turnIndex(), name, class.String(), string(args))
		decision, out := e.Hooks.Fire(ctx, hooks.PointPreTool, pre)
		switch decision.Decision {
		case hooks.DecisionDeny:
			span.SetAttributes(attribute.String("tool.outcome", "denied"))
			span.SetStatus(codes.Error, decision.Reason)
			reason := fmt.Sprintf("denied by pre_tool hook: %s", decision.Reason)
			// Audit the denial (spec STAGE 3). The tool never ran, so
			// there's no result/tree to record — just the deny
			// provenance: who vetoed, why, and the surfaced error. Guard
			// on a real session (headless/test executors have none).
			if e.Session != nil {
				denyMeta := stadogit.CommitMeta{
					Tool:         name,
					ShortArg:     shortArgOf(args),
					Summary:      fmt.Sprintf("%s [denied]", class.String()),
					ArgsSHA:      sha256Of(args),
					Agent:        e.Agent,
					Model:        e.Model,
					Turn:         e.Session.Turn(),
					Error:        reason,
					DenyReason:   decision.Reason,
					DeniedByHook: decision.HookName,
				}
				if _, err := e.Session.CommitToTrace(denyMeta); err != nil {
					return tool.Result{Error: reason}, fmt.Errorf("commit deny trace: %w", err)
				}
			}
			return tool.Result{Error: reason}, nil
		case hooks.DecisionMutate:
			if mp, ok := out.(*hooks.PreToolPayload); ok {
				args = json.RawMessage(mp.Args)
			}
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

	// post_tool hook seam (F1). Fires after the tool runs, before audit:
	//   - Mutate → rewrite the result/error the model sees; the rewritten
	//     bytes are what ResultSHA hashes (audit reflects what was
	//     returned).
	//   - Deny   → the action already happened; treat as a request to
	//     replace the result content with the reason and flag it as an
	//     error so the model sees the policy verdict.
	// The runErr (a Go-level execution error, distinct from a tool's
	// res.Error) is left intact — hooks rewrite the model-facing
	// result, not the host-level error path.
	//
	// Mutation provenance (spec STAGE 4, SHA-only): a post_tool MUTATE
	// rewrites res before audit, so the audited Result-SHA would hash the
	// MUTATED bytes and the original would be lost from the signed chain.
	// We capture the pre-seam result here and, on a mutation, emit TWO
	// linked trace commits below (original-result → mutation) so the
	// original SHA stays recoverable. Captured unconditionally (cheap) so
	// the commit block can branch on mutationByHook != "".
	originalContent := res.Content
	originalError := res.Error
	mutationByHook := ""
	if e.Hooks.HasPoint(hooks.PointPostTool) {
		post := hooks.PostTool(e.turnIndex(), name, class.String(), string(args), res.Content, res.Error)
		decision, out := e.Hooks.Fire(ctx, hooks.PointPostTool, post)
		switch decision.Decision {
		case hooks.DecisionDeny:
			res.Content = ""
			res.Error = fmt.Sprintf("denied by post_tool hook: %s", decision.Reason)
		case hooks.DecisionMutate:
			if mp, ok := out.(*hooks.PostToolPayload); ok {
				res.Content = mp.Result
				res.Error = mp.Error
				// Record the mutation for the two-commit provenance
				// model below. A mutation that's a no-op (rewrote to the
				// same bytes) still counts — the hook fired and is
				// attributable; cheap to record honestly.
				mutationByHook = decision.HookName
			}
		}
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

	// Mutation provenance (spec STAGE 4, SHA-only): a post_tool hook
	// rewrote res, so meta.ResultSHA above hashes the MUTATED bytes
	// (canonical for the model + turn accounting). Record the pre-mutation
	// digest + the attributing hook so the original is recoverable from
	// the signed chain. The two linked trace commits below preserve both.
	originalResultSHA := ""
	if mutationByHook != "" {
		originalResultSHA = sha256Of([]byte(originalContent))
		meta.OriginalResultSHA = originalResultSHA
		meta.MutatedByHook = mutationByHook
	}

	if e.Session == nil {
		return res, runErr
	}

	// Build the audit-tree snapshot BEFORE the trace commit so its
	// failure can surface in the trace metadata AND on the wire to
	// the model. Codex validated finding (MEDIUM): pre-fix, snapshot
	// failure on a successful mutating tool produced only a slog
	// warning and the tool was reported back as a normal success —
	// the signed tree ref was silently absent, breaking the
	// tamper-evident-audit invariant for that mutation. An
	// attacker-influenced prompt could force the snapshot cap
	// (256 MiB blob or 200000 entries via BuildTreeFromDir's limits)
	// then mutate; the audit log captured the trace but not the
	// post-state tree, so `audit verify` wouldn't notice.
	//
	// New contract: snapshot failure on a MUTATING tool augments
	// meta.Error AND res.Error so the audit log + the model both see
	// the gap. The mutation itself already happened — that's not
	// undone — but the operator gets told via the trace commit's
	// `Error:` trailer, and the model sees `tool_result.Error` and
	// can adjust strategy (e.g. stop generating files). For EXEC
	// tools the snapshot is informational (used only to detect
	// changes from preTree); failure stays a slog warn and the tree
	// ref is skipped, but no error surfaces — exec tools aren't
	// supposed to mutate the audited dir anyway.
	var treeHash plumbing.Hash
	switch class {
	case tool.ClassMutating:
		if runErr == nil && res.Error == "" {
			post, err := e.Session.BuildTreeFromDir(auditDir)
			if err != nil {
				skipNote := fmt.Sprintf("audit snapshot failed (tree-ref skipped, mutation NOT in signed audit): %v", err)
				if meta.Error == "" {
					meta.Error = skipNote
				} else {
					meta.Error = meta.Error + "; " + skipNote
				}
				if res.Error == "" {
					res.Error = skipNote
				} else {
					res.Error = res.Error + "; " + skipNote
				}
				// Snapshot failure means the audit trail for this
				// mutation is incomplete — promote outcome to error
				// so telemetry, the trace commit's Summary, and the
				// OTel span status all match the surfaced res.Error.
				// Codex P2 + Copilot both caught the gap: pre-fix
				// outcome stayed "ok", meta.Summary stayed
				// "mutating [ok]", and the span reported OK even
				// though an Error: trailer was about to be written,
				// so downstream consumers keyed on Summary/span saw
				// success.
				meta.Summary = fmt.Sprintf("%s [%s]", class.String(), "error")
				span.SetAttributes(attribute.String("tool.outcome", "error"))
				span.SetStatus(codes.Error, skipNote)
				e.logBuildTreeSkip(auditDir, err)
			} else {
				treeHash = post
			}
		}
	case tool.ClassExec:
		post, err := e.Session.BuildTreeFromDir(auditDir)
		if err != nil {
			e.logBuildTreeSkip(auditDir, err)
		} else if post != preTree && !post.IsZero() {
			treeHash = post
		}
	}

	// trace ref always — now carries the audit-skip note in
	// meta.Error if the snapshot failed above for a mutating tool.
	//
	// Mutation provenance (spec STAGE 4 + 5): when a post_tool hook
	// mutated the result, emit TWO sequential trace commits — first the
	// ORIGINAL raw result (its own SHA + the original error, NO mutation
	// trailers), then the MUTATION commit (the canonical mutated SHA +
	// Original-Result-SHA + Mutated-By-Hook). The second CommitToTrace
	// auto-parents the first via commitOnRef's parent-chaining, so the
	// mutation commit links to the original with NO new ref/branch. The
	// model-facing return stays the mutated res; the mutated ResultSHA
	// stays canonical for accounting; the original is audit-only
	// provenance, now recoverable from the signed chain.
	//
	// STAGE 5 (blob-backed): BOTH provenance commits store their Content
	// as a `result` git blob in the commit tree (CommitToTraceBlob) so the
	// original AND mutated bytes are recoverable — not just their SHAs. A
	// per-commit size cap (maxTraceResultBlobBytes) falls back to SHA-only
	// (empty-tree) on overflow; we note the drop in that commit's Error
	// trailer, mirroring the snapshot-skip contract above. Normal
	// (no-mutation, no-deny) calls keep the cheap empty-tree commit below —
	// blob-backing is paid ONLY by the mutated path.
	if mutationByHook != "" {
		original := meta
		original.ResultSHA = originalResultSHA
		original.OriginalResultSHA = ""
		original.MutatedByHook = ""
		original.Error = originalError
		// The original-result commit reflects the pre-mutation outcome.
		// Re-derive its Summary so a hook that injected an error (or
		// cleared one) doesn't mislabel the untouched provenance entry.
		origOutcome := "ok"
		if originalError != "" {
			origOutcome = "error"
		}
		original.Summary = fmt.Sprintf("%s [%s]", class.String(), origOutcome)
		original.Error = appendBlobSkipNote(original.Error, "original",
			int64(len(originalContent)))
		if _, _, err := e.Session.CommitToTraceBlob(original, []byte(originalContent)); err != nil {
			return res, fmt.Errorf("commit original-result trace: %w", err)
		}
		// The mutation commit's Error trailer was already populated above
		// (runErr/res.Error/snapshot-skip). Append the blob-skip note too
		// when the MUTATED Content overflows the cap.
		meta.Error = appendBlobSkipNote(meta.Error, "mutated", int64(len(res.Content)))
		if _, _, err := e.Session.CommitToTraceBlob(meta, []byte(res.Content)); err != nil {
			return res, fmt.Errorf("commit mutation trace: %w", err)
		}
	} else if _, err := e.Session.CommitToTrace(meta); err != nil {
		return res, fmt.Errorf("commit trace: %w", err)
	}

	if !treeHash.IsZero() {
		// The tree ref records FILE STATE, not tool-result provenance. The
		// mutation-chain trailers (Original-Result-SHA + Mutated-By-Hook)
		// belong ONLY to the trace ref's two-commit chain: `audit verify`
		// walks both refs and treats any commit carrying both as a mutation
		// link whose first parent must be the original-result commit. On the
		// tree ref the first parent is the prior snapshot, so leaving the
		// trailers here makes a legitimate mutating-tool + post_tool-mutate
		// call verify as MUTATION-LINK-BROKEN (exit 1). Strip them; the
		// canonical (mutated) Result-SHA the snapshot pairs with stays.
		treeMeta := meta
		treeMeta.OriginalResultSHA = ""
		treeMeta.MutatedByHook = ""
		if _, err := e.Session.CommitToTree(treeHash, treeMeta); err != nil {
			return res, fmt.Errorf("commit tree: %w", err)
		}
	}

	return res, runErr
}

// turnIndex returns the current session turn for hook payloads, or 0 when
// there's no session (tests, headless bootstrap).
func (e *Executor) turnIndex() int {
	if e.Session == nil {
		return 0
	}
	return e.Session.Turn()
}

// logBuildTreeSkip records that an audit tree snapshot was skipped (e.g. the
// live cwd exceeded the tree-entry cap). Best-effort audit: never fatal.
func (e *Executor) logBuildTreeSkip(dir string, err error) {
	slog.Default().Warn("audit: skipped tree snapshot; tool result unaffected", "dir", dir, "err", err)
}

// appendBlobSkipNote augments a commit's Error trailer when a blob-backed
// provenance commit's Content overflows the trace-result blob cap and
// CommitToTraceBlob falls back to SHA-only. Keeps the note in the SIGNED chain
// so an auditor sees the recoverable bytes were dropped (the digest +
// provenance trailers are still present). which is "original" or "mutated".
// Returns errStr unchanged when the size is within the cap.
func appendBlobSkipNote(errStr, which string, size int64) string {
	if size <= stadogit.MaxTraceResultBlobBytes() {
		return errStr
	}
	note := fmt.Sprintf("%s result not stored as blob (%d bytes > %d cap); SHA-only fallback",
		which, size, stadogit.MaxTraceResultBlobBytes())
	if errStr == "" {
		return note
	}
	return errStr + "; " + note
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
