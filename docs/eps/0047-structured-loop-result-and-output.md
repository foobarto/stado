---
ep: 47
title: Structured Agent-Loop Result Contract and Structured Output Mode
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Draft
type: Standards
created: 2026-06-17
requires: [3, 10]
see-also: [4, 7, 36, 38, 46]
history:
  - date: 2026-06-17
    status: Draft
    note: Initial draft. Borrows the Claude Agent SDK's ResultMessage
      termination taxonomy and structured-output-with-retry. Gives
      stado's AgentLoop a machine-readable result envelope across all
      programmatic surfaces.
---

# EP-47: Structured Agent-Loop Result Contract and Structured Output Mode

## Problem

stado's `AgentLoop` (`internal/runtime/agentloop.go`) ends a session by
returning to its caller, but **what it returns is ad-hoc**: a Go error
on the failure paths, an accumulated value on success, with no uniform,
machine-readable statement of *why* the loop ended or *what* it produced.
A programmatic consumer — headless (`stado run`), ACP, MCP, the fleet
registry, or a parent reading a subagent's return — cannot reliably tell
these apart:

- the task finished normally,
- the loop hit `MaxTurns`,
- it hit the cost/token cap,
- the provider stopped on `max_tokens` mid-thought,
- the model **refused**,
- an API error aborted execution.

The Claude Agent SDK solves this with a `ResultMessage` carrying a
`subtype` termination taxonomy (`success`, `error_max_turns`,
`error_max_budget_usd`, `error_during_execution`,
`error_max_structured_output_retries`) plus a `stop_reason`
(`end_turn` / `max_tokens` / `refusal`) and uniform `usage`,
`total_cost_usd`, `num_turns`, `session_id`. Every consumer reads the
same envelope.

stado also has **no structured-output mode**. There is no way to tell a
loop (or a subagent) "return JSON matching this schema," validate it,
and retry on failure. That is exactly what the SDK's
`error_max_structured_output_retries` subtype exists for, and it is what
makes scripted callers, subagent/fleet returns, and
data-extraction tasks reliable instead of "parse the model's prose and
hope." (The in-tree Workflow tooling already leans on forced structured
output via a `StructuredOutput` schema — the agent loop itself has no
equivalent.)

These two gaps are one concern: **the loop's output contract.** A
structured result envelope is where a structured-output payload and its
`max_structured_output_retries` terminator naturally live.

## Goals

- Define a single `LoopResult` envelope returned by `AgentLoop` and
  surfaced **uniformly** across headless, ACP, MCP, fleet, and subagent
  returns: termination `subtype`, `stop_reason`, `usage`, `cost`,
  `num_turns`, `session_id`, and (when configured) a validated
  structured `output`.
- Derive the termination taxonomy from the loop's **existing** exit
  paths plus the provider's finish reason (EP-3) — no new control flow,
  just a named, observable result.
- Add a **structured-output mode**: a caller supplies a JSON Schema; the
  loop forces schema-valid output, validates it, retries a bounded number
  of times, and terminates with a distinct reason when retries are
  exhausted.
- Subsume EP-46's `verify_exhausted` reason into the same taxonomy so
  there is one result vocabulary, not several.

## Non-goals

- **Not** a change to the TUI's human-facing rendering. This is the
  machine contract for programmatic consumers; the interactive surface
  keeps rendering blocks as it does (it can *also* read the envelope, but
  that's not the point of this EP).
- **Not** a streaming-protocol redesign. stado already streams turn/tool
  events to consumers; this EP defines the **terminal** result, not the
  event stream. (Aligning the streamed event taxonomy with the SDK's
  `SystemMessage`/`AssistantMessage`/… types is a separate possible EP.)
- **Not** structured-output-on-by-default. With no schema supplied, the
  loop returns text exactly as today.
- **Not** a new budget/cap mechanism — it *names* the outcomes of the
  caps EP-7/EP-36 already enforce.

## Design

### The `LoopResult` envelope

`AgentLoop` returns a `LoopResult` (new type in the runtime/agent
package):

```go
type LoopResult struct {
    Subtype    ResultSubtype   // termination taxonomy, below
    StopReason string          // provider finish reason: end_turn | max_tokens | refusal | ...
    Text       string          // final assistant text (present on Success)
    Output     json.RawMessage // validated structured payload, when output-schema set
    Usage      TokenUsage      // input/output/total tokens
    CostUSD    float64
    NumTurns   int
    SessionID  string
    Err        error           // non-nil on the error_* subtypes, for the Go caller
}
```

Termination taxonomy (`ResultSubtype`), mapped from existing exit points:

| Subtype | Mapped from |
|---|---|
| `success` | model returned no tool calls (and structured output, if required, validated) |
| `error_max_turns` | `turn == MaxTurns` reached (EP-36) |
| `error_max_budget` | `CostCapUSD` crossed (EP-7/36) |
| `error_max_tokens` | `TokenCap`/`OutputTokenCap` crossed, or provider `stop_reason == max_tokens` on the final turn |
| `error_during_execution` | provider/API error, cancelled context |
| `refusal` | provider `stop_reason == refusal` |
| `verify_exhausted` | EP-46 `max_verify_rounds` hit with a failing verdict |
| `error_max_structured_output_retries` | structured-output validation failed `max_output_retries` times |

`stop_reason` is surfaced straight from the provider (EP-3's
`StreamTurn` already receives a finish reason); `refusal` and
`error_max_tokens` are the two that also drive a `Subtype`. The envelope
is populated on **every** path — the error subtypes still carry
`Usage`/`CostUSD`/`NumTurns`/`SessionID` so a caller can account for and
resume a run that ended badly.

### Surfacing across programmatic surfaces (EP-10)

One envelope, rendered per surface:

- **Headless (`stado run`)**: `--json` emits the `LoopResult` as a final
  JSON object; exit code derives from `Subtype` (0 for `success`,
  nonzero per error class). A `--output-schema FILE` flag turns on
  structured-output mode and puts the validated payload in `Output`.
- **ACP / MCP**: the result message carries `subtype`, `stop_reason`,
  usage/cost, and `output` so a wrapping agent (EP-32) or MCP client
  branches on the outcome instead of scraping text.
- **Fleet**: `FleetEntry` records the terminal `Subtype` and `output`
  (today it stores `Status` + `Result` text — this refines "completed"
  into the taxonomy).
- **Subagent returns (EP-38)**: a parent's `agent.spawn` tool result
  includes the child's `Subtype` and, when the spawn requested a schema,
  the validated `Output` — so a parent can rely on a child returning
  structured data, not a prose summary it must re-parse.

### Structured-output mode

When a caller supplies a JSON Schema (per-run `--output-schema`, an ACP/
MCP param, or an `agent.spawn` `output_schema` argument):

1. The loop injects a synthetic `StructuredOutput` tool whose input
   schema **is** the caller's schema, and instructs the model to call it
   to deliver the final answer.
2. When the model calls it, the payload is validated against the schema.
   On success it becomes `LoopResult.Output` and the loop ends
   `success`.
3. On validation failure, the validation error is fed back as the tool
   result (the normal tool-feedback path), and the model retries — up to
   `max_output_retries` (config, default e.g. 3). Exhausting retries ends
   the loop `error_max_structured_output_retries`, with the last invalid
   payload retained for debugging.

Using a synthetic tool-call (rather than only a provider-native
`response_format`) keeps this **provider-portable** — it works on any
EP-3 provider that supports tool calls, reuses the existing
tool-result feedback loop for the retry, and composes with normal tool
use (the model can read files, then emit structured output). Providers
with native structured-output support can back the same surface without
changing the contract (Open questions).

## Migration / rollout

- `AgentLoop`'s return type changes from its current ad-hoc form to
  `LoopResult`. This is an **internal API break**: all in-repo callers
  (headless, TUI, ACP, MCP, fleet, subagent runner) are updated in the
  same pass and the change is recorded in `CHANGELOG.md` (no-kid-gloves
  pre-1.0). No user-visible default changes — text sessions still get
  their text.
- The headless `--json` / `--output-schema` flags and the ACP/MCP
  `output` fields are **additive**; existing callers that ignore them are
  unaffected.
- Land in two slices: (1) the `LoopResult` envelope + taxonomy + surface
  wiring; (2) structured-output mode + its subtype. EP-46's
  `verify_exhausted` folds into (1) when both are in flight.

## Failure modes

- **Provider doesn't report a finish reason** ⇒ `stop_reason` is empty
  and `Subtype` falls back to `success`/`error_during_execution` from the
  control-flow path; never block on a missing reason.
- **Schema the model can't satisfy** ⇒ bounded by `max_output_retries`,
  ends `error_max_structured_output_retries` with the last payload, so
  the caller sees the near-miss rather than an infinite retry.
- **Retries inflate cost** ⇒ each retry is a normal turn under the
  existing cost/token caps; a cap crossed mid-retry ends on the cap
  subtype, not the retry subtype (cap precedence, asserted in tests).
- **Caller reads `Text`/`Output` on an error subtype** ⇒ documented that
  `Text` is meaningful only on `success` and `Output` only when a schema
  was set and validated (mirrors the SDK's "check subtype before reading
  result").

## Test strategy

- **Taxonomy mapping** (`internal/runtime`): each exit path produces the
  right `Subtype`; `MaxTurns`, cost cap, token cap, context-cancel,
  provider `refusal`, and provider `max_tokens` each assert their subtype
  and a populated `Usage`/`Cost`/`NumTurns`/`SessionID`.
- **Structured output**: valid first-try ⇒ `success` + `Output`;
  invalid-then-valid ⇒ retried, `success`; always-invalid ⇒
  `error_max_structured_output_retries` with last payload; cap crossed
  mid-retry ⇒ cap subtype wins.
- **Surface parity** (EP-10): headless `--json` exit codes per subtype;
  ACP/MCP result carries subtype + output; `agent.spawn` with
  `output_schema` returns a validated child `Output`; fleet entry records
  the terminal subtype.
- **EP-46 interop**: `verify_exhausted` is emitted through the same
  envelope.

## Open questions

- **Q1.** Provider-native structured output (`response_format`/
  tool-choice forcing) where available vs. the portable synthetic-tool
  path everywhere. Leaning: synthetic tool as the universal contract,
  provider-native as an optional optimization behind the same `Output`
  surface. Decide per provider in EP-3.
- **Q2.** Should the streamed *event* taxonomy (turn/tool/system events
  to ACP/MCP consumers) also be aligned to the SDK's
  `SystemMessage`/`AssistantMessage`/`UserMessage`/`ResultMessage` names?
  Out of scope here (this EP is the terminal result); worth its own EP if
  consumers want it.
- **Q3.** Exit-code mapping for headless: one nonzero code for all
  `error_*`, or distinct codes per subtype? Distinct is more scriptable
  but a wider contract to keep stable. Lean distinct, documented.
- **Q4.** Does `agent.spawn`'s `output_schema` belong here or in an
  EP-38 follow-up? It depends on this envelope, so specify the field
  here and implement alongside slice (2).

## Decision log

### D1. One result envelope across every programmatic surface

- **Decided:** `AgentLoop` returns a single `LoopResult`; headless, ACP,
  MCP, fleet, and subagent returns all render the same envelope.
- **Alternatives:** per-surface ad-hoc results (today's state — each
  consumer scrapes what it can).
- **Why:** the same run means the same outcome everywhere stado runs
  (the EP-8 D4 principle, applied to results, not inputs); one taxonomy
  to learn and test, not five.

### D2. Derive the taxonomy from existing exits + provider finish reason

- **Decided:** subtypes map onto the loop's current exit paths and the
  EP-3 provider `stop_reason`; no new control flow is introduced.
- **Alternatives:** a richer state machine with new stop conditions.
- **Why:** the gap is observability, not behavior — the loop already
  stops for these reasons, it just doesn't *name* them. Naming is cheap
  and low-risk; new control flow is neither.

### D3. Structured output via a validated synthetic tool-call with bounded retry

- **Decided:** a `StructuredOutput` tool carrying the caller's JSON
  Schema; validate the call payload; feed validation errors back and
  retry up to `max_output_retries`; exhaustion ⇒
  `error_max_structured_output_retries`.
- **Alternatives:** provider-native `response_format` only (not portable
  across EP-3 providers, and can't interleave with tool use); accept any
  text and post-parse (the unreliable status quo).
- **Why:** the synthetic tool is provider-portable, reuses the existing
  tool-feedback retry path, and lets the model gather context with normal
  tools before emitting the final structured answer.

### D4. Clean internal break on the return type; additive on the wire

- **Decided:** change `AgentLoop`'s Go return to `LoopResult`, fix all
  in-repo callers in one pass, note it in `CHANGELOG.md`; headless/ACP/
  MCP output fields are additive.
- **Alternatives:** a parallel "result v2" alongside the old return (a
  compat shim).
- **Why:** pre-1.0 stado takes clean breaks over shims
  (`feedback_no_kid_gloves_pre_1_0`); the wire additions don't break
  existing consumers, so only the internal callers move.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md) — supplies the per-turn finish reason `stop_reason` is read from.
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md) — the programmatic surfaces the envelope is rendered to.
- [EP-38: ABI v2, bundled wasm tools, and runtime surface](./0038-abi-v2-bundled-wasm-and-runtime.md) — subagent returns that carry the subtype and validated `output`.
- [EP-7](./0007-conversation-state-and-compaction.md) / [EP-36](./0036-loop-monitor-schedule.md) — the caps whose outcomes become `error_max_turns`/`error_max_budget`/`error_max_tokens`.
- [EP-46: Verify-Work Phase](./0046-verify-work-phase.md) — contributes the `verify_exhausted` subtype to this taxonomy.
- [Claude Agent SDK — How the agent loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop) — `ResultMessage` subtypes, `stop_reason`, and structured-output retries are the borrow target.
