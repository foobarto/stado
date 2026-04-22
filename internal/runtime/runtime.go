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

// OpenSession creates a new session (or resumes an existing one)
// + sidecar rooted at cwd's repo. Non-fatal callers can swallow the
// error and carry on without state.
//
// Resume semantics: when cwd is a direct child of cfg.WorktreeDir()
// (i.e. the user cd'd into an existing session's worktree), we reuse
// that session's ID + git refs instead of spawning a fresh UUID.
// This pairs with `stado session fork` / `session attach`: fork
// creates the worktree, user cd's in, next stado boot picks up where
// the session left off.
//
// Loads (or creates on first use) the agent signing key and attaches it to
// the session so every trace/tree commit carries an Ed25519 signature.
func OpenSession(cfg *config.Config, cwd string) (*stadogit.Session, error) {
	// Worktree-cwd discriminator: normal cwds go through FindRepoRoot
	// to locate the containing user repo. But when cwd IS a session
	// worktree, the parent repo lives elsewhere — we persist it per
	// worktree at .stado/user-repo so resume-on-cwd can recover it.
	userRepo := resolveUserRepo(cfg, cwd)
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

	// Resume-on-cwd path: if cwd is exactly a session worktree dir
	// under cfg.WorktreeDir(), reopen that session rather than
	// creating a fresh one. Silent no-op when cwd doesn't match or
	// the session's tree ref is unset (implying a stale / bogus
	// directory that we should not trust).
	if sess := resumeFromCWD(cfg, sc, cwd); sess != nil {
		attachSessionScaffolding(sess, cfg, userRepo)
		emitResumeSpan(cwd, sess.ID)
		return sess, nil
	}

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), uuid.New().String(), plumbing.ZeroHash)
	if err != nil {
		return nil, err
	}
	attachSessionScaffolding(sess, cfg, userRepo)
	return sess, nil
}

// userRepoFile is the relative-to-worktree file that pins which user
// repo a session belongs to. Written on first scaffold, read by
// resolveUserRepo when cwd is a session worktree.
const userRepoFile = ".stado/user-repo"

// resolveUserRepo finds the user repo root for an OpenSession call.
// For a plain cwd (repo checkout) it's FindRepoRoot(cwd). For a
// session worktree cwd it's the path recorded in .stado/user-repo —
// because the worktree itself isn't the repo and FindRepoRoot would
// otherwise fall back to cwd and generate a stale repoID.
func resolveUserRepo(cfg *config.Config, cwd string) string {
	if cwd == "" {
		return FindRepoRoot(cwd)
	}
	if isSessionWorktreeCWD(cfg, cwd) {
		if pinned := ReadUserRepoPin(cwd); pinned != "" {
			return pinned
		}
	}
	return FindRepoRoot(cwd)
}

func isSessionWorktreeCWD(cfg *config.Config, cwd string) bool {
	if cfg == nil || cwd == "" {
		return false
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	wtDir, err := filepath.Abs(cfg.WorktreeDir())
	if err != nil {
		return false
	}
	parent := filepath.Dir(abs)
	if parent != wtDir {
		return false
	}
	base := filepath.Base(abs)
	return base != "" && base != "." && base != string(filepath.Separator)
}

// attachSessionScaffolding wires the signer, slog OnCommit mirror,
// pid-file drop, and the .stado/user-repo pin onto sess. Shared by
// the fresh-session and resume-from-cwd paths so both produce
// identically-configured Session objects.
func attachSessionScaffolding(sess *stadogit.Session, cfg *config.Config, userRepo string) {
	// Persist the user-repo pointer so future resume-on-cwd boots can
	// locate the right sidecar without walking up for a .git (which
	// won't exist under a worktree subdir). Best-effort — a failure
	// here just degrades the resume path for this worktree; tool
	// execution still works.
	if userRepo != "" {
		dir := filepath.Join(sess.WorktreePath, ".stado")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(sess.WorktreePath, userRepoFile),
			[]byte(userRepo+"\n"), 0o644)
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
}

// emitResumeSpan opens a short `stado.session.resume` span parented
// by whatever trace context `.stado-span-context` carries (written
// by a prior `stado session fork`). Jaeger then renders
// fork → resume → turns as a single tree — closes out the Phase
// 9.4/9.5 cross-process span link for the reattach case specifically.
//
// Zero-op when cwd has no traceparent file (no fork ancestry) or
// when telemetry isn't configured — same graceful-degrade contract
// as WriteCurrentTraceparent.
func emitResumeSpan(cwd, sessionID string) {
	ctx, ok := telemetry.LoadParentTraceparent(context.Background(), cwd)
	if !ok {
		return // non-forked resume, nothing to link back to
	}
	_, span := otel.Tracer(telemetry.TracerName).Start(ctx, telemetry.SpanSessionResume,
		trace.WithAttributes(
			attribute.String("session.id", sessionID),
			attribute.String("session.worktree", cwd),
		),
	)
	span.End()
}

// resumeFromCWD returns an opened Session when cwd looks like an
// existing session's worktree, else nil. A worktree qualifies when
// cwd's parent is exactly cfg.WorktreeDir() (forked sessions live
// directly under it) AND the named session has a tree ref (rules
// out stale empty directories).
func resumeFromCWD(cfg *config.Config, sc *stadogit.Sidecar, cwd string) *stadogit.Session {
	if cwd == "" {
		return nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	parent := filepath.Dir(abs)
	wtDir, err := filepath.Abs(cfg.WorktreeDir())
	if err != nil {
		return nil
	}
	if parent != wtDir {
		return nil
	}
	id := filepath.Base(abs)
	if id == "" || id == "." || id == "/" {
		return nil
	}
	// Must have a tree ref to count as a real session. Fresh worktree
	// dirs (just-created fork, never-committed) are handled by the
	// fork path separately.
	if _, err := sc.ResolveRef(stadogit.TreeRef(id)); err != nil {
		return nil
	}
	sess, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), id)
	if err != nil {
		return nil
	}
	return sess
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
	return buildBundledPluginRegistry()
}

// ApplyToolFilter trims a registry per cfg.Tools. All tools are on
// by default; Enabled acts as an allowlist (keep only these);
// Disabled removes specific names from the default set. When both
// are set Enabled wins — Disabled is redundant against an explicit
// allowlist.
//
// Unknown tool names in either list log to stderr but don't abort —
// typo-tolerant so a user's config doesn't break stado across
// version upgrades that rename a tool. Mutates the registry in
// place so it's safe to chain after BuildDefaultRegistry.
func ApplyToolFilter(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Tools.Enabled) == 0 && len(cfg.Tools.Disabled) == 0 {
		return
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}

	// Warn on unknown names so typos surface.
	warnUnknown := func(list []string, label string) {
		for _, n := range list {
			if !known[n] {
				fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such bundled tool (ignored)\n", label, n)
			}
		}
	}
	warnUnknown(cfg.Tools.Enabled, "enabled")
	warnUnknown(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		for _, n := range cfg.Tools.Enabled {
			if known[n] {
				allow[n] = true
			}
		}
		if len(allow) == 0 {
			return
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
		}
		return
	}
	// Disabled-only path.
	for _, n := range cfg.Tools.Disabled {
		reg.Unregister(n)
	}
}

// BuildExecutor wires the tool registry + session + sandbox runner.
//
// Also loads any MCP servers from config and registers their tools. Failed
// MCP connections are logged to stderr, not fatal — stado should boot
// without them if the endpoint is down.
//
// Respects cfg.Tools.Enabled / Disabled — the user's allowlist /
// blocklist is applied AFTER MCP tools land so MCP-sourced names can
// also be trimmed.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) (*tools.Executor, error) {
	reg := BuildDefaultRegistry()

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			fmt.Fprintf(os.Stderr, "stado: MCP setup: %v\n", err)
		}
	}
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		return nil, err
	}
	ApplyToolFilter(reg, cfg)

	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
		ReadLog:  tools.NewReadLog(),
	}, nil
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

func allowedToolSet(defs []agent.ToolDef) map[string]struct{} {
	out := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		out[def.Name] = struct{}{}
	}
	return out
}

func toolAllowed(allowed map[string]struct{}, name string) bool {
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[name]
	return ok
}

func unavailableToolResult(name string) string {
	return fmt.Sprintf("tool %q is not available for this turn", name)
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

	// System is the optional system prompt fed to every turn in this
	// loop. The `stado run` / headless entry points populate it from
	// AGENTS.md / CLAUDE.md via internal/instructions.Load. Empty by
	// default — callers that don't want project instructions (e.g.
	// plugin-driven sub-loops) can leave it zero.
	System string

	// CostCapUSD is the optional cumulative-cost ceiling for this
	// loop. Zero disables the guard (the common case). When set, the
	// loop checks cumulative cost at every turn boundary and returns
	// ErrCostCapExceeded once the ceiling is crossed. Partial output
	// up to the moment of abort is still returned in the usual
	// (text, msgs, err) tuple so callers can persist what the model
	// already produced.
	CostCapUSD float64
}

// ErrCostCapExceeded is returned by AgentLoop when the cumulative
// provider cost for the loop has crossed opts.CostCapUSD. Callers map
// it to a non-zero exit code so CI / scripting pipelines can gate on
// cost overruns.
var ErrCostCapExceeded = errors.New("runtime: cost cap exceeded")

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
		var runner sandbox.Runner
		var rlog *tools.ReadLog
		if opts.Executor != nil {
			if opts.Executor.Session != nil {
				workdir = opts.Executor.Session.WorktreePath
			}
			rlog = opts.Executor.ReadLog
			runner = opts.Executor.Runner
		}
		opts.Host = autoApproveHost{workdir: workdir, readLog: rlog, runner: runner}
	}

	msgs := opts.Messages
	var finalText string
	var totalCostUSD float64

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
			System:   opts.System,
		}
		if opts.Executor != nil {
			req.Tools = ToolDefs(opts.Executor.Registry)
		}
		allowedTools := allowedToolSet(req.Tools)
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

		text, calls, usage, err := collectTurn(ch, opts.OnEvent)
		if err != nil {
			turnSpan.RecordError(err)
			turnSpan.SetStatus(codes.Error, err.Error())
			turnSpan.End()
			return finalText, msgs, err
		}
		totalCostUSD += usage.CostUSD
		turnSpan.SetAttributes(
			attribute.Int("turn.text_bytes", len(text)),
			attribute.Int("turn.tool_calls", len(calls)),
			attribute.Float64("turn.cost_usd", usage.CostUSD),
			attribute.Float64("loop.cumulative_cost_usd", totalCostUSD),
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

		if opts.CostCapUSD > 0 && totalCostUSD >= opts.CostCapUSD {
			turnSpan.End()
			return finalText, msgs,
				fmt.Errorf("%w: spent $%.4f of $%.2f cap", ErrCostCapExceeded, totalCostUSD, opts.CostCapUSD)
		}
		if len(calls) == 0 {
			turnSpan.End()
			return finalText, msgs, nil
		}
		needsExecutor := false
		for _, c := range calls {
			if toolAllowed(allowedTools, c.Name) {
				needsExecutor = true
				break
			}
		}
		if needsExecutor && opts.Executor == nil {
			turnSpan.End()
			return finalText, msgs, errors.New("runtime: tool calls requested but executor is nil")
		}

		// Execute tool calls, build role=tool message.
		var results []agent.Block
		for _, c := range calls {
			if !toolAllowed(allowedTools, c.Name) {
				results = append(results, agent.Block{ToolResult: &agent.ToolResultBlock{
					ToolUseID: c.ID,
					Content:   unavailableToolResult(c.Name),
					IsError:   true,
				}})
				continue
			}
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

// collectTurn drains an event stream into (assistant_text, tool_calls,
// usage, err). usage is the final EvDone.Usage on providers that
// report it; zero value if the provider emits neither EvDone nor a
// Usage payload.
func collectTurn(ch <-chan agent.Event, onEvent func(agent.Event)) (string, []agent.ToolUseBlock, agent.Usage, error) {
	var text string
	var calls []agent.ToolUseBlock
	var usage agent.Usage
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
		case agent.EvDone:
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case agent.EvError:
			return text, calls, usage, ev.Err
		}
	}
	return text, calls, usage, nil
}

type autoApproveHost struct {
	workdir string
	readLog *tools.ReadLog
	runner  sandbox.Runner
}

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h autoApproveHost) Workdir() string        { return h.workdir }
func (h autoApproveHost) Runner() sandbox.Runner { return h.runner }

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
