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

	"github.com/foobarto/stado/internal/sandbox"
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
	Model    string
	Messages []agent.Message

	MaxTurns int // default 20

	// OnEvent receives every provider event before any accumulation happens.
	// Useful for stdout streaming in `stado run`.
	OnEvent func(agent.Event)

	// OnTurnComplete fires after a turn streams (text + tool-calls) and
	// before the assistant/tool messages are appended to history. Callers can
	// inspect the turn with the same pre-append turn index the TUI hook sees.
	OnTurnComplete func(turnIndex int, text string, toolCalls []agent.ToolUseBlock, usage agent.Usage, duration time.Duration)

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
		turnStart := time.Now()

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
			opts.OnTurnComplete(len(msgs), text, calls, usage, time.Since(turnStart))
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
