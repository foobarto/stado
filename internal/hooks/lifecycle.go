package hooks

// Lifecycle hooks: deterministic, scriptable interception points around
// the tool-dispatch and agent-loop seams. This is the F1 foundation that
// LSP (post-edit diagnostics) and hashline (CommitMeta anchors) will also
// attach to, so the seam carries BOTH a deny decision and a mutated
// payload cleanly.
//
// DECISION (operator, 2026-06-11): v1 hooks have FULL power — they can
// deny (veto the action) AND mutate (rewrite tool args, LLM requests, or
// outputs). NOT observe-only. The design-prep stub's "observe-only v1"
// recommendation was overridden.
//
// Semantics:
//   - Hooks run SERIALLY in config order at each Point.
//   - Each hook returns a HookResult: Continue | Deny(reason) | Mutate(payload).
//   - At a PRE point, Deny short-circuits the remaining hooks and the
//     action itself (the caller skips the tool / LLM call and surfaces the
//     reason). Mutate rewrites the payload that the action — and any
//     subsequent hooks at this point — operate on.
//   - At a POST point, Deny is treated as a request to replace the
//     result with the deny reason (the action already happened; there is
//     nothing left to veto), and Mutate rewrites the result the caller
//     hands back.
//   - Execution is FAIL-OPEN: a hook that errors (script fault, timeout,
//     panic) is logged and treated as Continue. A broken policy hook must
//     not wedge the agent loop. (Fail-closed is a future config knob.)
//   - Each hook has a 5-second wall-clock timeout.

import (
	"context"
	"time"
)

// Point identifies a lifecycle interception site. The string values are
// the stable `event` field carried in every payload and matched against
// hook config, so they must not change without a migration.
type Point string

const (
	PointPreTool  Point = "pre_tool"
	PointPostTool Point = "post_tool"
	PointPreLLM   Point = "pre_llm"
	PointPostLLM  Point = "post_llm"
	PointPostTurn Point = "post_turn"
)

// Decision is the verdict a single hook returns for one Point.
type Decision int

const (
	// DecisionContinue: proceed unchanged. The zero value, so a hook that
	// returns the zero HookResult is a safe no-op.
	DecisionContinue Decision = iota
	// DecisionDeny: veto the action (PRE) or replace the result with the
	// reason (POST). Carries a human-readable Reason.
	DecisionDeny
	// DecisionMutate: rewrite the payload (PRE) or result (POST). Carries
	// the replacement Payload.
	DecisionMutate
)

// HookResult is the outcome of running one hook at one Point. The zero
// value ({DecisionContinue, "", "", nil}) means "no opinion, proceed".
type HookResult struct {
	Decision Decision
	// Reason is the operator/model-facing explanation for a deny. Empty
	// for Continue/Mutate.
	Reason string
	// HookName attributes the WINNING decision to the deciding hook in a
	// multi-hook chain — set by LifecycleRunner.Fire at the Deny
	// short-circuit and the final Mutate return to the Name() of the
	// hook whose verdict survived (spec: hooks-audit-mutation-provenance,
	// STAGE 2). The per-hook Deny()/Mutate() constructors leave it empty;
	// only the aggregate result the runner returns carries it. Surfaces
	// as the audit `Mutated-By-Hook` / `Denied-By-Hook` trailer.
	HookName string
	// Payload is the rewritten payload for a mutate. Must be the same
	// concrete payload type the Point operates on (e.g. *PreToolPayload
	// for PointPreTool). Nil for Continue/Deny.
	Payload Payload
}

// Continue is the no-op result.
func Continue() HookResult { return HookResult{Decision: DecisionContinue} }

// Deny vetoes the action with a reason.
func Deny(reason string) HookResult { return HookResult{Decision: DecisionDeny, Reason: reason} }

// Mutate rewrites the payload/result.
func Mutate(p Payload) HookResult { return HookResult{Decision: DecisionMutate, Payload: p} }

// Payload is the common interface implemented by every lifecycle payload
// struct. Point reports which interception site the payload belongs to;
// the runner uses it to reject a hook that mutates to a payload of the
// wrong Point (a script returning a PreLLM payload at a pre_tool site).
type Payload interface {
	// HookPoint returns the Point this payload type belongs to.
	HookPoint() Point
}

// Common carries the fields every lifecycle payload shares. Embedded
// (not interface-promoted) so the Lua/JSON projection sees them as
// top-level keys on each payload.
type Common struct {
	Event     Point `json:"event"`
	Timestamp int64 `json:"timestamp"` // unix millis at the interception point
	TurnIndex int   `json:"turn_index"`
}

// PreToolPayload is handed to pre_tool hooks before a tool runs. A
// mutate rewrites Args (the JSON the tool receives). Class is the tool's
// mutation class string ("non-mutating" / "state-mutating" / "mutating"
// / "exec") so policy hooks can gate on side-effect risk.
type PreToolPayload struct {
	Common
	Tool  string `json:"tool"`
	Class string `json:"class"`
	Args  string `json:"args"` // raw JSON args, as a string for Lua-friendliness
}

func (p *PreToolPayload) HookPoint() Point { return PointPreTool }

// PostToolPayload is handed to post_tool hooks after a tool runs. A
// mutate rewrites Result / Error (what the model sees as the tool
// result). Args is the (possibly already pre-mutated) args the tool ran
// with.
type PostToolPayload struct {
	Common
	Tool   string `json:"tool"`
	Class  string `json:"class"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

func (p *PostToolPayload) HookPoint() Point { return PointPostTool }

// PreLLMPayload is handed to pre_llm hooks before a turn request is sent
// to the provider. A mutate rewrites System (the system prompt) and/or
// Model. The message history itself is NOT exposed for mutation in the F1
// foundation — rewriting append-only history would void the prompt-cache
// invariant; scoped to system+model knobs for now.
type PreLLMPayload struct {
	Common
	Model    string `json:"model"`
	System   string `json:"system"`
	NumMsgs  int    `json:"num_msgs"`
	NumTools int    `json:"num_tools"`
}

func (p *PreLLMPayload) HookPoint() Point { return PointPreLLM }

// PostLLMPayload is handed to post_llm hooks after a turn streams back. A
// mutate rewrites Text (the assistant text). Tool calls are reported for
// inspection but not mutated in the F1 foundation (rewriting tool-call
// IDs/args mid-stream is its own can of worms; the pre_tool point covers
// arg rewriting per-call).
type PostLLMPayload struct {
	Common
	Text       string  `json:"text"`
	NumToolUse int     `json:"num_tool_use"`
	TokensIn   int     `json:"tokens_in"`
	TokensOut  int     `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
}

func (p *PostLLMPayload) HookPoint() Point { return PointPostLLM }

// PostTurnLifecyclePayload is the scriptable-hook view of a completed
// turn. Distinct from the existing PostTurnPayload (the shell-hook JSON
// shape) so the legacy notification hook is untouched. Deny/mutate at
// post_turn is informational in the F1 foundation (the turn is over);
// the point exists so a single script can observe every boundary.
type PostTurnLifecyclePayload struct {
	Common
	Text       string  `json:"text"`
	TokensIn   int     `json:"tokens_in"`
	TokensOut  int     `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
	DurationMS int64   `json:"duration_ms"`
}

func (p *PostTurnLifecyclePayload) HookPoint() Point { return PointPostTurn }

// newCommon stamps the shared header for a payload built at a Point.
func newCommon(point Point, turnIndex int) Common {
	return Common{
		Event:     point,
		Timestamp: time.Now().UnixMilli(),
		TurnIndex: turnIndex,
	}
}

// PreTool builds a PreToolPayload with a freshly stamped Common.
func PreTool(turnIndex int, toolName, class, args string) *PreToolPayload {
	return &PreToolPayload{Common: newCommon(PointPreTool, turnIndex), Tool: toolName, Class: class, Args: args}
}

// PostTool builds a PostToolPayload with a freshly stamped Common.
func PostTool(turnIndex int, toolName, class, args, result, errStr string) *PostToolPayload {
	return &PostToolPayload{Common: newCommon(PointPostTool, turnIndex), Tool: toolName, Class: class, Args: args, Result: result, Error: errStr}
}

// PreLLM builds a PreLLMPayload with a freshly stamped Common.
func PreLLM(turnIndex int, model, system string, numMsgs, numTools int) *PreLLMPayload {
	return &PreLLMPayload{Common: newCommon(PointPreLLM, turnIndex), Model: model, System: system, NumMsgs: numMsgs, NumTools: numTools}
}

// PostLLM builds a PostLLMPayload with a freshly stamped Common.
func PostLLM(turnIndex int, text string, numToolUse, tokensIn, tokensOut int, costUSD float64) *PostLLMPayload {
	return &PostLLMPayload{Common: newCommon(PointPostLLM, turnIndex), Text: text, NumToolUse: numToolUse, TokensIn: tokensIn, TokensOut: tokensOut, CostUSD: costUSD}
}

// PostTurnLifecycle builds a PostTurnLifecyclePayload with a freshly
// stamped Common.
func PostTurnLifecycle(turnIndex int, text string, tokensIn, tokensOut int, costUSD float64, dur time.Duration) *PostTurnLifecyclePayload {
	return &PostTurnLifecyclePayload{
		Common:     newCommon(PointPostTurn, turnIndex),
		Text:       text,
		TokensIn:   tokensIn,
		TokensOut:  tokensOut,
		CostUSD:    costUSD,
		DurationMS: dur.Milliseconds(),
	}
}

// HookScript is one scriptable hook. Implementations evaluate the hook
// body (Lua, Go-native builtin, …) against the payload and return a
// HookResult. Run MUST be safe for serial reuse across many calls and
// MUST respect ctx cancellation/deadline (the runner imposes a per-hook
// 5s timeout via ctx). A Run that panics is recovered by the runner and
// treated as fail-open Continue.
type HookScript interface {
	// Points reports which lifecycle Points this hook subscribes to. The
	// runner skips a hook whose Points don't include the firing Point, so
	// a pre_tool-only policy isn't invoked on every post_llm.
	Points() []Point
	// Name is a short identifier for logging.
	Name() string
	// Run evaluates the hook at point against payload and returns the
	// result. point always matches payload.HookPoint() and is one the
	// hook subscribed to.
	Run(ctx context.Context, point Point, payload Payload) (HookResult, error)
}

const defaultHookTimeout = 5 * time.Second
