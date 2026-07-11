package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/lspfind"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// AgentLoopOptions parameterises a headless agent loop. Callers typically
// pre-build Executor (which owns the registry + session) and feed initial
// messages; the loop streams turn → tool calls → tool exec → next turn until
// no tool calls remain, or MaxTurns is hit.
type AgentLoopOptions struct {
	Provider agent.Provider
	Executor *tools.Executor
	Config   *config.Config
	Metrics  telemetry.Metrics
	Broker   BrokerController
	// InitialTaint is the provenance state of the seed messages. Empty means a
	// trusted top-level operator prompt (clean); model-derived child prompts set
	// this to ContextTainted.
	InitialTaint ContextTaint
	Model        string
	Messages     []agent.Message

	// Hooks is the lifecycle-hook runner for the LLM-side points:
	// pre_llm (deny -> abort the turn; mutate -> rewrite system prompt /
	// model before the provider call), post_llm (mutate -> rewrite the
	// assistant text before it lands in history), and post_turn (the
	// scriptable post-turn boundary, distinct from OnTurnComplete's shell
	// hook). Nil / empty is a no-op. The pre/post-tool points live on
	// Executor.Hooks (the per-tool dispatch seam); callers wiring hooks
	// set BOTH this and Executor.Hooks to the same runner. F1 seam.
	Hooks *hooks.LifecycleRunner

	MaxTurns int // default 20

	// OnEvent receives every provider event before any accumulation happens.
	// Useful for stdout streaming in `stado run`.
	OnEvent func(agent.Event)

	// OnTurnComplete fires after a turn streams (text + tool-calls) and
	// before the assistant/tool messages are appended to history. Callers can
	// inspect the turn with the same pre-append turn index the TUI hook sees.
	OnTurnComplete func(turnIndex int, text string, toolCalls []agent.ToolUseBlock, usage agent.Usage, duration time.Duration)

	// OnSubagentEvent fires when spawn_agent creates or finishes a child
	// session. It is best-effort user/client visibility; audit remains in
	// the parent and child trace refs.
	OnSubagentEvent func(SubagentEvent)

	// Verify runs deterministic command gates when the model reaches its
	// natural no-tool exit. Failures feed a user-role critique back into the
	// same bounded loop; OnVerifyEvent exposes the phase to outer surfaces.
	Verify        VerifyConfig
	DisableVerify bool
	OnVerifyEvent func(VerifyEvent)

	// Host implements tool.Host during tool execution. Defaults to an
	// auto-approve host using Workdir below (or Session.WorktreePath
	// when Workdir is empty).
	Host tool.Host

	// DefaultSandboxPolicy, when non-nil, is returned by the auto-created
	// host's tool.SandboxPolicyProvider so bash/exec tool calls run confined
	// by it. Top-level sandboxed surfaces set it from
	// ExecutorSandbox.DefaultSandboxPolicy; an explicit --no-sandbox decision
	// leaves it nil.
	// Model A (decision 2026-06-13). Ignored when Host is supplied — that host
	// owns its own SandboxPolicyProvider (e.g. acp / mcp-server).
	DefaultSandboxPolicy any

	// Workdir overrides the cwd that tools see during this loop. When
	// empty, the loop falls back to Executor.Session.WorktreePath
	// (per-session scratch). v1 `stado run` always sets this to
	// os.Getwd() — the launch cwd is the canonical writable boundary
	// in both sandboxed (default) and --no-sandbox modes.
	Workdir string

	// Thinking controls extended-thinking injection. Values mirror
	// cfg.Agent.Thinking: "auto" / "on" / "off" / "" (same as auto).
	// Auto respects Capabilities.SupportsThinking on the active
	// provider.
	Thinking string
	// ThinkingBudgetTokens is threaded through to the provider when
	// Thinking resolves to on. 0 means "use a sensible default."
	ThinkingBudgetTokens int

	// System is the optional AGENTS.md / CLAUDE.md project-instructions
	// body fed into SystemTemplate as ProjectInstructions. Empty by
	// default — callers that don't want project instructions (e.g.
	// plugin-driven sub-loops) can leave it zero.
	System string
	// SystemTemplate is the editable stado system prompt template loaded
	// from ~/.config/stado/system-prompt.md by config.Load. Empty falls
	// back to instructions.DefaultSystemPromptTemplate.
	SystemTemplate string
	// MemoryContext is optional approved-memory prompt context. Callers
	// own retrieval/scoping so this loop stays provider/session generic.
	MemoryContext string

	// Persona, when non-nil, supplies the agent's operating manual
	// (system prompt body). Replaces the default stado-shipped
	// system prompt template entirely; project AGENTS.md / CLAUDE.md
	// (passed as System) and MemoryContext still append. Nil falls
	// back to instructions.ComposeSystemPrompt for legacy callers.
	Persona *personas.Persona

	// Skills is the effective skill catalog for this loop (cwd ∪ persona).
	// Drives the model-facing listing in the system prompt and skills__load.
	Skills []skills.Skill

	// InboxFn is the optional pull-source for operator- or peer-
	// injected messages addressed to this agent. The loop calls it
	// at every turn boundary; non-empty returns are prepended to the
	// next turn request as user-role inputs. Used by FleetBridge
	// AgentSendMessage to deliver messages mid-loop without rewriting
	// the existing transcript. Empty / nil result is a no-op.
	InboxFn func() []string

	// QuietRegistryDiagnostics propagates a live TUI's terminal ownership into
	// nested subagents so their background registry builds remain silent.
	QuietRegistryDiagnostics bool

	// CostCapUSD is the optional cumulative-cost ceiling for this
	// loop. Zero disables the guard (the common case). When set, the
	// loop checks cumulative cost at every turn boundary and returns
	// ErrCostCapExceeded once the ceiling is crossed. Partial output
	// up to the moment of abort is still returned in the usual
	// (text, msgs, err) tuple so callers can persist what the model
	// already produced.
	CostCapUSD float64

	// TokenCap is the optional cumulative-token ceiling for this loop
	// (sum of InputTokens + OutputTokens across all turns). Zero
	// disables. Useful for local-runner setups where CostUSD is always
	// zero — there the meaningful budget is throughput, not dollars.
	TokenCap int

	// InputTokenCap and OutputTokenCap are per-direction caps. Power
	// users may want to bound output length without restricting input
	// context (output tokens are ~3–5× more expensive on most paid
	// providers), or cap context-window growth without limiting
	// generation. Both default to 0 = disabled. Whichever cap fires
	// first aborts the loop; the returned error names the specific cap
	// crossed so callers can distinguish.
	InputTokenCap  int
	OutputTokenCap int

	// Sampling overrides. Nil/zero = use provider default. These map
	// to TurnRequest.Temperature/TopP/TopK on every turn. EP-0036.
	Temperature *float64
	TopP        *float64
	TopK        *int
}

// ErrCostCapExceeded is returned by AgentLoop when the cumulative
// provider cost for the loop has crossed opts.CostCapUSD. Callers map
// it to a non-zero exit code so CI / scripting pipelines can gate on
// cost overruns.
var ErrCostCapExceeded = errors.New("runtime: cost cap exceeded")

// ErrTokenCapExceeded is returned by AgentLoop when the cumulative
// input+output token count has crossed opts.TokenCap. Mirror of
// ErrCostCapExceeded for the dollars-zero local-runner case.
var ErrTokenCapExceeded = errors.New("runtime: token cap exceeded")

// ErrMaxTurnsExceeded is returned when the provider keeps requesting tool
// continuations through the configured turn limit.
var ErrMaxTurnsExceeded = errors.New("runtime: max turns exceeded")

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
	if opts.Broker != nil {
		initialTaint := opts.InitialTaint
		if initialTaint == "" {
			initialTaint = ContextClean
		}
		if err := opts.Broker.SetTaint(ctx, initialTaint); err != nil {
			return "", opts.Messages, fmt.Errorf("runtime: set initial broker taint: %w", err)
		}
	}
	if opts.Metrics.ToolLatency == nil && opts.Metrics.TokensTotal == nil &&
		opts.Metrics.CacheHitRatio == nil && opts.Executor != nil {
		opts.Metrics = opts.Executor.Metrics
	}
	if opts.DisableVerify {
		opts.Verify = VerifyConfig{}
	} else if !opts.Verify.Enabled() {
		opts.Verify = VerifyConfigFrom(opts.Config)
	}
	if opts.Host == nil {
		workdir := opts.Workdir
		var runner sandbox.Runner
		var rlog *tools.ReadLog
		if opts.Executor != nil {
			if workdir == "" && opts.Executor.Session != nil {
				workdir = opts.Executor.Session.WorktreePath
			}
			rlog = opts.Executor.ReadLog
			runner = opts.Executor.Runner
		}
		loopRunner := SubagentRunner{
			Config:                   opts.Config,
			Parent:                   sessionFromExecutor(opts.Executor),
			Provider:                 opts.Provider,
			Model:                    opts.Model,
			Thinking:                 opts.Thinking,
			ThinkingBudgetTokens:     opts.ThinkingBudgetTokens,
			System:                   opts.System,
			SystemTemplate:           opts.SystemTemplate,
			AgentName:                "stado-subagent",
			OnEvent:                  opts.OnSubagentEvent,
			QuietRegistryDiagnostics: opts.QuietRegistryDiagnostics,
			Metrics:                  opts.Metrics,
			Broker:                   opts.Broker,
		}
		spawnFn := buildLoopSubagentSpawner(loopRunner)
		var fb *FleetBridgeAdapter
		if spawnFn != nil {
			fleet := NewFleet()
			fb = &FleetBridgeAdapter{
				Fleet:   fleet,
				Spawner: loopRunner,
				RootCtx: ctx,
			}
		}
		ptyMgr := pty.NewManager()
		// Reap any PTYs the loop opened on return so subprocesses don't
		// outlive the loop. Idempotent.
		defer ptyMgr.CloseAll()
		// Reap any LSP servers (gopls / rust-analyzer / pyright) the
		// lsp.* tools spawned during this one-shot loop. They're spawned
		// host-side, outside the sandbox, via the process-default LSP
		// client manager — same one-shot lifetime as the PTYs above.
		// Interactive hosts (TUI / daemon / mcp) supply opts.Host and own
		// the manager's lifetime across turns themselves; we only reap on
		// the self-contained headless path.
		defer lspfind.CloseAll()
		opts.Host = autoApproveHost{
			workdir:              workdir,
			readLog:              rlog,
			runner:               runner,
			spawn:                spawnFn,
			fleetBridge:          fb,
			pty:                  ptyMgr,
			defaultSandboxPolicy: opts.DefaultSandboxPolicy,
		}
	}

	msgs := opts.Messages
	var finalText string
	var totalCostUSD float64
	var totalTokens, totalInputTokens, totalOutputTokens int
	verifyRounds := 0

	// Append-only guardrail (DESIGN §"Context management" → "Append-only
	// history"). Prior messages are the cached prefix; any in-place mutation
	// invalidates every downstream cache entry. We record a hash at each
	// turn boundary and verify it survived the tool-execution interlude.
	// Mismatch panics under `go test` (fail loudly in CI); in release it
	// logs slog.Warn and aborts the loop because continuing would make the
	// prompt-cache/audit trail silently diverge from the append-only model.
	var priorHash string
	var priorLen int

	caps := opts.Provider.Capabilities()
	tracer := otel.Tracer(telemetry.TracerName)

	// EP-0037: track tools activated by tools.describe across turns.
	activatedNames := map[string]bool{}

	for turn := 0; turn < opts.MaxTurns; turn++ {
		finalTextBeforeTurn := len(finalText)
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
				return finalText, msgs, errors.New(violationMsg)
			}
		}

		// Inbox drain — operator or peer-agent messages queued via
		// FleetBridge AgentSendMessage land here at turn boundaries.
		// Append before the model sees the next request so they
		// appear as user-role inputs the model can react to.
		if opts.InboxFn != nil {
			if pending := opts.InboxFn(); len(pending) > 0 {
				for _, body := range pending {
					msgs = append(msgs, agent.Text(agent.RoleUser, body))
				}
				// priorLen / priorHash are intentionally NOT updated
				// here. The end-of-iteration update at the bottom of
				// the loop refreshes them once the tool-result block
				// has been appended; an inbox flush only grows the
				// suffix, so the prefix-of-priorLen check still
				// matches priorHash on next iteration.
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
		turnCtx = WithSkillCatalog(turnCtx, opts.Skills)
		turnStart := time.Now()
		if opts.Verify.Enabled() {
			emitVerify(opts.OnVerifyEvent, VerifyEvent{
				Status: VerifyPending, Round: verifyRounds + 1,
				Output: "provider output is buffered until verification accepts the candidate",
			})
		}
		closeVerifyPending := func(err error) {
			if opts.Verify.Enabled() {
				emitVerifyGenerationEnd(opts.OnVerifyEvent, verifyRounds+1, err)
			}
		}

		req := agent.TurnRequest{
			Model:    opts.Model,
			Messages: msgs,
			System:   buildTurnSystem(opts),
			// EP-0036: sampling overrides from config or --temperature / --top-p / --top-k.
			Temperature: opts.Temperature,
			TopP:        opts.TopP,
			TopK:        opts.TopK,
		}
		if opts.Executor != nil {
			// EP-0037: send only autoloaded tools + session-activated tools each turn.
			// Per-persona tool promotion (2026-06-13): the active persona's
			// EffectiveTools() merge ADDITIVELY into the autoload surface for
			// this run — headless/run honors persona `tools:`/`recommended_tools:`
			// the same way the TUI's per-turn path does. nil persona = no extra.
			extra := opts.Persona.EffectiveTools()
			// EP-0045: surface skills__load on the per-turn tool slate only
			// when the session has a model-invocable skill (and the tool
			// isn't denied). Keeps it off the slate — and out of the model's
			// sight — when there's nothing to load.
			if SkillModelInvocationEnabled(opts.Executor.Registry, opts.Skills) {
				extra = append(extra, "skills__load")
			}
			autoloaded := AutoloadedToolsWithExtra(opts.Executor.Registry, opts.Config, extra)
			surface := dedupeTools(append(autoloaded, activatedSlice(opts.Executor.Registry, activatedNames)...))
			req.Tools = ToolDefsFromSlice(surface)
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

		// pre_llm hook seam (F1). Fires before the provider call:
		//   - Deny  → abort the loop; the reason surfaces as the error so
		//     the operator/client sees why the turn was blocked.
		//   - Mutate → rewrite the system prompt and/or model the provider
		//     receives this turn. Message history is intentionally NOT
		//     mutable here (append-only / prompt-cache invariant).
		if opts.Hooks.HasPoint(hooks.PointPreLLM) {
			pre := hooks.PreLLM(turn, req.Model, req.System, len(req.Messages), len(req.Tools))
			decision, out := opts.Hooks.Fire(turnCtx, hooks.PointPreLLM, pre)
			switch decision.Decision {
			case hooks.DecisionDeny:
				err := fmt.Errorf("runtime: turn denied by pre_llm hook: %s", decision.Reason)
				closeVerifyPending(err)
				turnSpan.SetStatus(codes.Error, decision.Reason)
				turnSpan.End()
				return finalText, msgs, err
			case hooks.DecisionMutate:
				if mp, ok := out.(*hooks.PreLLMPayload); ok {
					req.System = mp.System
					req.Model = mp.Model
				}
			}
		}

		ch, err := opts.Provider.StreamTurn(turnCtx, req)
		if err != nil {
			closeVerifyPending(err)
			turnSpan.RecordError(err)
			turnSpan.SetStatus(codes.Error, err.Error())
			turnSpan.End()
			return finalText, msgs, fmt.Errorf("stream: %w", err)
		}

		turnEvents := make([]agent.Event, 0, 8)
		turnEventBytes := 0
		var turnEventErr error
		onEvent := opts.OnEvent
		if opts.Verify.Enabled() && opts.OnEvent != nil {
			onEvent = func(event agent.Event) {
				if turnEventErr == nil {
					turnEventErr = bufferVerifyEvent(&turnEvents, &turnEventBytes, event)
				}
			}
		}
		text, calls, usage, err := collectTurn(ch, onEvent)
		if err != nil {
			closeVerifyPending(err)
			turnSpan.RecordError(err)
			turnSpan.SetStatus(codes.Error, err.Error())
			turnSpan.End()
			return finalText, msgs, err
		}
		if turnEventErr != nil {
			turnEvents = coalesceVerifyEvents(text, calls, usage)
		}
		totalCostUSD += usage.CostUSD
		totalTokens += usage.InputTokens + usage.OutputTokens
		totalInputTokens += usage.InputTokens
		totalOutputTokens += usage.OutputTokens
		opts.Metrics.RecordTurnUsage(turnCtx, opts.Provider.Name(), req.Model,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens)
		turnSpan.SetAttributes(
			attribute.Int("turn.text_bytes", len(text)),
			attribute.Int("turn.tool_calls", len(calls)),
			attribute.Float64("turn.cost_usd", usage.CostUSD),
			attribute.Float64("loop.cumulative_cost_usd", totalCostUSD),
			attribute.Int("turn.tokens_total", usage.InputTokens+usage.OutputTokens),
			attribute.Int("turn.tokens_in", usage.InputTokens),
			attribute.Int("turn.tokens_out", usage.OutputTokens),
			attribute.Int("loop.cumulative_tokens", totalTokens),
			attribute.Int("loop.cumulative_tokens_in", totalInputTokens),
			attribute.Int("loop.cumulative_tokens_out", totalOutputTokens),
		)
		providerText := text
		// post_llm hook seam (F1). Fires after the turn streams back,
		// before the assistant text is flushed into history:
		//   - Mutate → rewrite the assistant text the model history
		//     (and finalText) records. Tool calls are reported for
		//     inspection but not mutated here — pre_tool covers per-call
		//     arg rewriting.
		//   - Deny   → the generation already happened; treat as a
		//     request to replace the assistant text with the reason.
		if opts.Hooks.HasPoint(hooks.PointPostLLM) {
			post := hooks.PostLLM(turn, text, len(calls), usage.InputTokens, usage.OutputTokens, usage.CostUSD)
			decision, out := opts.Hooks.Fire(turnCtx, hooks.PointPostLLM, post)
			switch decision.Decision {
			case hooks.DecisionDeny:
				text = fmt.Sprintf("[post_llm hook: %s]", decision.Reason)
			case hooks.DecisionMutate:
				if mp, ok := out.(*hooks.PostLLMPayload); ok {
					text = mp.Text
				}
			}
		}
		if opts.Verify.Enabled() && opts.OnEvent != nil && text != providerText {
			turnEvents = coalesceVerifyEvents(text, calls, usage)
		}

		if opts.OnTurnComplete != nil {
			opts.OnTurnComplete(len(msgs), text, calls, usage, time.Since(turnStart))
		}

		// post_turn lifecycle hook seam (F1). Fires on every completed
		// turn boundary, mirroring the existing shell post_turn (which
		// runs via OnTurnComplete). Informational in F1 — the turn is
		// over, so deny/mutate have no downstream effect here; the point
		// exists so one script can observe every boundary.
		if opts.Hooks.HasPoint(hooks.PointPostTurn) {
			pt := hooks.PostTurnLifecycle(turn, text, usage.InputTokens, usage.OutputTokens, usage.CostUSD, time.Since(turnStart))
			opts.Hooks.Fire(turnCtx, hooks.PointPostTurn, pt)
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
			err := fmt.Errorf("%w: spent $%.4f of $%.2f cap", ErrCostCapExceeded, totalCostUSD, opts.CostCapUSD)
			if opts.Verify.Enabled() {
				finalText = finalText[:finalTextBeforeTurn]
				emitVerifyGenerationEnd(opts.OnVerifyEvent, verifyRounds+1, err)
			}
			turnSpan.End()
			return finalText, msgs, err
		}
		if opts.TokenCap > 0 && totalTokens >= opts.TokenCap {
			err := fmt.Errorf("%w: used %d of %d cap", ErrTokenCapExceeded, totalTokens, opts.TokenCap)
			if opts.Verify.Enabled() {
				finalText = finalText[:finalTextBeforeTurn]
				emitVerifyGenerationEnd(opts.OnVerifyEvent, verifyRounds+1, err)
			}
			turnSpan.End()
			return finalText, msgs, err
		}
		if opts.InputTokenCap > 0 && totalInputTokens >= opts.InputTokenCap {
			err := fmt.Errorf("%w (input): used %d of %d cap", ErrTokenCapExceeded, totalInputTokens, opts.InputTokenCap)
			if opts.Verify.Enabled() {
				finalText = finalText[:finalTextBeforeTurn]
				emitVerifyGenerationEnd(opts.OnVerifyEvent, verifyRounds+1, err)
			}
			turnSpan.End()
			return finalText, msgs, err
		}
		if opts.OutputTokenCap > 0 && totalOutputTokens >= opts.OutputTokenCap {
			err := fmt.Errorf("%w (output): used %d of %d cap", ErrTokenCapExceeded, totalOutputTokens, opts.OutputTokenCap)
			if opts.Verify.Enabled() {
				finalText = finalText[:finalTextBeforeTurn]
				emitVerifyGenerationEnd(opts.OnVerifyEvent, verifyRounds+1, err)
			}
			turnSpan.End()
			return finalText, msgs, err
		}
		if len(calls) == 0 {
			var passedEvents []VerifyEvent
			if opts.Verify.Enabled() {
				verifyRounds++
				onVerifyEvent := func(event VerifyEvent) {
					if event.Status == VerifyPassed {
						passedEvents = append(passedEvents, event)
						return
					}
					for _, passed := range passedEvents {
						emitVerify(opts.OnVerifyEvent, passed)
					}
					passedEvents = nil
					emitVerify(opts.OnVerifyEvent, event)
				}
				outcome := RunVerificationRound(turnCtx, opts.Executor, opts.Host,
					opts.Verify, verifyRounds, onVerifyEvent)
				switch outcome.Status {
				case VerifyCancelled:
					finalText = finalText[:finalTextBeforeTurn]
					if opts.Executor != nil && opts.Executor.Session != nil {
						if err := opts.Executor.Session.NextTurn(); err != nil {
							turnSpan.End()
							return finalText, msgs, fmt.Errorf("turn boundary: %w", err)
						}
					}
					turnSpan.End()
					if outcome.Err != nil {
						return finalText, msgs, outcome.Err
					}
					return finalText, msgs, context.Canceled
				case VerifyInfrastructure:
					if opts.Verify.Strict {
						finalText = finalText[:finalTextBeforeTurn]
						if opts.Executor != nil && opts.Executor.Session != nil {
							if err := opts.Executor.Session.NextTurn(); err != nil {
								turnSpan.End()
								return finalText, msgs, fmt.Errorf("turn boundary: %w", err)
							}
						}
						turnSpan.End()
						if outcome.Err != nil {
							return finalText, msgs, fmt.Errorf("runtime: verification infrastructure: %w", outcome.Err)
						}
						return finalText, msgs, fmt.Errorf("runtime: verification infrastructure: %s", outcome.Output)
					}
					// Default fail-open: the warning event is visible to the
					// caller, and the candidate completion is accepted.
				case VerifyFailed:
					// This completion candidate was rejected. Keep it in message
					// history so the verifier feedback has context, but never expose
					// it as accepted output to programmatic callers.
					finalText = finalText[:finalTextBeforeTurn]
					if opts.Broker != nil {
						if err := opts.Broker.SetTaint(turnCtx, ContextTainted); err != nil {
							turnSpan.End()
							return finalText, msgs, fmt.Errorf("runtime: mark verification feedback tainted: %w", err)
						}
					}
					msgs = append(msgs, agent.Text(agent.RoleUser, outcome.Feedback))
					if verifyRounds >= opts.Verify.MaxRounds {
						emitVerify(opts.OnVerifyEvent, VerifyEvent{
							Status: VerifyExhausted, Round: verifyRounds,
							Command: outcome.Command, Output: outcome.Feedback,
						})
						if opts.Executor != nil && opts.Executor.Session != nil {
							if err := opts.Executor.Session.NextTurn(); err != nil {
								turnSpan.End()
								return finalText, msgs, fmt.Errorf("turn boundary: %w", err)
							}
						}
						turnSpan.End()
						return finalText, msgs, &VerifyExhaustedError{
							Round: verifyRounds, Command: outcome.Command, Feedback: outcome.Feedback,
						}
					}
					priorLen = len(msgs)
					priorHash = hashMessagesPrefix(msgs, priorLen)
					turnSpan.End()
					continue
				}
			}
			emitAgentEvents(opts.OnEvent, turnEvents)
			for _, passed := range passedEvents {
				emitVerify(opts.OnVerifyEvent, passed)
			}
			if opts.Executor != nil && opts.Executor.Session != nil {
				if err := opts.Executor.Session.NextTurn(); err != nil {
					turnSpan.RecordError(err)
					turnSpan.SetStatus(codes.Error, err.Error())
					turnSpan.End()
					return finalText, msgs, fmt.Errorf("turn boundary: %w", err)
				}
			}
			turnSpan.End()
			return finalText, msgs, nil
		}
		if opts.Verify.Enabled() {
			emitVerify(opts.OnVerifyEvent, VerifyEvent{
				Status: VerifyDeferred, Round: verifyRounds + 1,
				Output: "turn requested tools; candidate verification deferred",
			})
		}
		emitAgentEvents(opts.OnEvent, turnEvents)
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
		var skillInjections []string
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
			// EP-0037: tools.describe activates tool schemas into the session surface.
			if c.Name == "tools__describe" && !isErr {
				extractActivated(content, activatedNames)
			}
			// EP-0045: skills__load injects the body as a user message and
			// promotes persona-scoped allowed-tools into the session surface.
			if c.Name == "skills__load" && !isErr {
				for _, name := range SkillLoadAllowedTools(content) {
					activatedNames[name] = true
				}
				if body, trimmed := AbsorbSkillLoad(content); body != "" {
					skillInjections = append(skillInjections, body)
					content = trimmed
				}
			}
			results = append(results, agent.Block{ToolResult: &agent.ToolResultBlock{
				ToolUseID: c.ID,
				Content:   content,
				IsError:   isErr,
			}})
		}
		msgs = append(msgs, agent.Message{Role: agent.RoleTool, Content: results})
		if opts.Broker != nil {
			if err := opts.Broker.SetTaint(turnCtx, ContextTainted); err != nil {
				turnSpan.RecordError(err)
				turnSpan.SetStatus(codes.Error, err.Error())
				turnSpan.End()
				return finalText, msgs, fmt.Errorf("runtime: mark broker tainted: %w", err)
			}
		}
		for _, body := range skillInjections {
			msgs = append(msgs, agent.Text(agent.RoleUser, body))
		}

		priorLen = len(msgs)
		priorHash = hashMessagesPrefix(msgs, priorLen)
		turnSpan.End()
	}
	return finalText, msgs, fmt.Errorf("%w: limit %d", ErrMaxTurnsExceeded, opts.MaxTurns)
}

func emitAgentEvents(fn func(agent.Event), events []agent.Event) {
	if fn == nil {
		return
	}
	for _, event := range events {
		fn(event)
	}
}

func coalesceVerifyEvents(text string, calls []agent.ToolUseBlock, usage agent.Usage) []agent.Event {
	events := make([]agent.Event, 0, len(calls)+2)
	if text != "" {
		events = append(events, agent.Event{Kind: agent.EvTextDelta, Text: text})
	}
	for i := range calls {
		call := calls[i]
		events = append(events, agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &call})
	}
	events = append(events, agent.Event{Kind: agent.EvDone, Usage: &usage})
	return events
}

// buildTurnSystem assembles the system prompt for a turn. When a
// persona is active it replaces the stado-shipped template entirely
// (project AGENTS.md + memory still append). Without a persona,
// falls back to instructions.ComposeSystemPrompt for legacy behavior.
func buildTurnSystem(opts AgentLoopOptions) string {
	var sys string
	if opts.Persona != nil {
		sys = personas.AssembleSystem(opts.Persona, opts.System, opts.MemoryContext, "")
	} else {
		sys = instructions.ComposeSystemPrompt(opts.SystemTemplate, opts.System, instructions.RuntimeContext{
			Provider: opts.Provider.Name(),
			Model:    opts.Model,
			Memory:   opts.MemoryContext,
		})
	}
	// Gate the listing on the same condition as the skills__load autoload:
	// no listing when there's no model-invocable skill or the load tool is
	// denied, so we never advertise a capability the model can't use.
	var reg *tools.Registry
	if opts.Executor != nil {
		reg = opts.Executor.Registry
	}
	if SkillModelInvocationEnabled(reg, opts.Skills) {
		if listing := skills.FormatModelListing(opts.Skills); listing != "" {
			if sys != "" {
				sys += "\n\n"
			}
			sys += listing
		}
	}
	return sys
}
