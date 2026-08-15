---
ep: 46
title: Verify-Work Phase — command-gate and LLM-judge verification
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Partial
type: Standards
created: 2026-06-17
requires: ["EP-0009", "EP-0038"]
extended-by: ["EP-0062"]
see-also: ["EP-0007", "EP-0036", "EP-0037", "EP-0044", "EP-0045"]
history:
  - date: 2026-07-10
    status: Partial
    version: v0.77.0
    note: >
      Phase 1 command gates shipped. Ordered commands run through the shared
      sandboxed/audited shell executor at natural loop exit, failed output is
      fed back into bounded model retries, and exhaustion returns the typed
      verify_exhausted error. TUI and programmatic surfaces emit phase status;
      run adds repeatable --verify and --no-verify controls. Project [verify]
      is stripped by EP-44. The independent LLM judge and EP-47 structured
      result envelope remain deferred, so this EP stays Partial.
  - date: 2026-06-17
    status: Draft
    note: Initial draft. Borrows the agentic loop's "verify work" phase
      (Claude Agent SDK) — an automated post-action verification gate the
      stado loop lacks today. Builds on EP-9 hooks, EP-38 subagents, and
      the EP-44 trust boundary.
---

> **Relationships:** **Requires:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md) · **Extended by:** [EP-0062](./0062-harness-enforced-supervised-work.md) · **See also:** [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0036](./0036-loop-monitor-schedule.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0045](./0045-model-invocable-skills.md)

# EP-46: Verify-Work Phase — command-gate and LLM-judge verification

> **Implementation status:** Ordered sandboxed command gates, bounded retry,
> evidence, TUI/run controls, and the generic lifecycle verification-facts
> bridge are implemented. The independent fresh-context semantic judge remains
> deferred, so this EP is Partial. All enforcement is turn/token based; cost is
> observational telemetry.

## Problem

The agentic loop has three phases: **gather context → take action →
verify work → repeat**. stado's `AgentLoop`
(`internal/runtime/agentloop.go`) implements the first, second, and
fourth richly — append-only history, deferred-tool search (EP-37),
pre/post LLM and tool hooks (EP-9), fork-based subagents (EP-38),
compaction and token/turn caps (EP-7, EP-36). It has **no verify-work
phase at all**.

Today, when the model produces a response with no tool calls, the loop
treats that as "done" and returns to the operator immediately. Whether
the work is actually correct — tests pass, the build is green, the
change matches the task — is left entirely to (a) the operator reading
the output, or (b) the model having chosen, unprompted, to run a check
mid-turn. There is no automated step between "the model says it's
finished" and "control returns to the operator."

This is exactly the gap the operator's own working discipline targets:
*validate before claiming done; passing tests are necessary, not
sufficient; argue against the work before declaring it done.* The model
is told to do this in the system prompt, but nothing enforces it — a
model that declares victory with a red test suite ends the loop with a
red test suite. EP-9 hooks can *deny* a tool or *mutate* a result, but
`post_turn` is informational only (the turn is already over), so there
is no seam that says "the model thinks it's done — check, and if it
isn't, send it back."

## Goals

- Add an opt-in **verify-work phase** that runs at the loop's natural
  exit point (model returned no tool calls), evaluates whether the work
  meets a defined bar, and on failure **feeds the verdict back as a new
  turn** instead of returning to the operator.
- Support two verifier kinds, cheapest-first:
  **command-gate** (deterministic: lint/test/build/typecheck/custom
  script, pass-by-exit-code) and **LLM-judge** (a secondary model pass
  against a rubric, returning a structured verdict).
- Bound the phase: a `max_verify_rounds` cap and the existing turn/token caps,
  with a clear termination reason when a cap is hit, so verification can never
  wedge or exhaust the loop. Currency cost remains observational telemetry.
- Reuse existing stado machinery — the sandboxed exec path for command
  gates, a subagent (EP-38) for the judge, the EP-44 trust boundary for
  project-supplied check commands — rather than a parallel mechanism.
- Make verify rounds visible in the TUI and reflected in the headless/
  ACP/MCP result.

## Non-goals

- **Not on by default.** Verification is opt-in per session/persona/spec.
  An existing session's behavior is unchanged until verification is
  configured. (No-kid-gloves still applies: any internal signature change
  to `AgentLoop`'s return is a clean break documented in `CHANGELOG.md`,
  but there is no *user-visible* default change.)
- **Not** a replacement for operator review or for the pre-release gates
  (`feedback_security_sweep_before_release`); it tightens the inner loop,
  it doesn't certify a release.
- **Not** visual/screenshot or "does the UI look right" verification —
  the page mentions visual feedback, but that is surface-specific and
  deferred (Open questions).
- **Not** a general assertion DSL. A verifier is a command exit code or a
  judge verdict, nothing more expressive.
- **Not** a per-tool gate. Verification runs at loop-exit by default
  (and optionally at turn boundaries, Phase 3), not after every tool —
  per-tool gating is what EP-9 `post_tool` hooks already do.

## Design

### The seam

The verify phase hooks the loop's terminal condition. In
`agentloop.go`, when `StreamTurn` returns an assistant message with no
tool calls, the loop currently returns. With verification configured,
that exit becomes conditional:

```
model returns no tool calls          # candidate "done"
  -> run verifiers (command gates first, then judge)
       all pass        -> loop exits normally (success)
       any fail        -> synthesize a feedback message from the
                          failing verdict(s), append it as a new
                          role=user (or role=tool) turn, and continue
                          the loop  (round += 1)
  -> round == max_verify_rounds -> exit with termination reason
                                    `verify_exhausted`, carrying the
                                    last verdict back to the operator
```

Each verify round that sends the model back counts as a normal turn for
`MaxTurns`, and every provider invocation counts against the cumulative token
and per-direction token caps. Verification lives inside the existing budget;
it does not get a private one. Provider cost remains observational telemetry,
never an enforcement cap.

This is a new loop phase, not a new EP-9 hook decision; but it composes
with hooks (a `pre_tool` deny still fires inside a verify-triggered
round, etc.). Phase 3 optionally exposes a plugin-authored `verify` hook
point so third-party verifiers can register (Open questions).

### Verifier kinds

**Command-gate (Phase 1).** A deterministic check run through the same
sandboxed exec path as `shell__bash` (`buildSandboxedCmd` /
`DefaultSandboxPolicy`). Exit `0` = pass; nonzero = fail, with stdout/
stderr (budget-truncated, like any tool output) becoming the feedback
message. Configured as an ordered list so the cheapest check gates
first (matching CLAUDE.md's validate-with-the-cheapest-tool-first
ladder):

```toml
[verify]
max_rounds = 3
commands = [
  "go vet ./...",
  "go test ./internal/... -count=1",
]
```

**LLM-judge (Phase 2).** A secondary model pass that answers "does this
work satisfy the task?" It runs as a **subagent** (EP-38
`SubagentRunner`), so it starts from a fresh context — it sees the task,
the diff/result, and a rubric, not the full main transcript — and only
its verdict returns. This keeps the judge cheap and independent
(adversarial-verify in spirit; the judge is told to default to *fail*
when unsure). The verdict is forced **structured output**:

```json
{ "pass": false,
  "critique": "auth_test.go:42 still fails: token expiry off by 1h",
  "checked": ["ran the failing test", "re-read the task"] }
```

Config picks the judge model/persona (default: a cheaper model than the
main session) and the rubric:

```toml
[verify]
judge = { model = "claude-haiku-4-5", rubric = ".stado/verify-rubric.md" }
```

On `pass: false`, `critique` is the feedback message fed back to the
main loop.

### Ordering and short-circuit

Within a round, command gates run first (deterministic, cheap, no model
spend); if any command gate fails, the round fails immediately and the
judge is **not** run (no point paying for a judgment when the build is
already red). The judge runs only when all command gates pass, as the
"looks green, but is it actually right?" second opinion.

### Configuration sources and precedence

Verification is configured, in increasing specificity:

1. Global/user config (`~/.config/stado/config.toml` `[verify]`) —
   operator-authored, trusted.
2. Project config (`.stado/config.toml` `[verify]`) — **attacker-
   controlled under EP-44**; see Trust below.
3. Persona frontmatter (`verify:` block) — scopes verification to a
   persona, layered like per-persona skills/tools (decision
   `2026-06-13-per-persona-skills-plugins`).
4. Per-run flag (`stado run --verify <spec>` / `--no-verify`) and TUI
   `/verify` toggle — operator's explicit, highest-precedence choice.

### Trust boundary

A `[verify] commands` entry is *a command that runs automatically every
time the model finishes*. From a project config that is **RCE on loop
exit** for any repo you open — precisely the EP-44 threat. Therefore:

- `[verify] commands` and a project-supplied `judge.rubric`/`judge.model`
  from **project** config (`.stado/config.toml`) take effect only after
  the repo clears the EP-44 trust gate (TOFU). Until trusted, project
  verify config is **stripped** exactly like the other powerful project
  keys EP-44 already strips, and a one-line notice says verification is
  disabled pending trust.
- Command gates always run under the session sandbox policy, never the
  raw operator shell — a verify command gets no more reach than a
  model-issued `bash` call would.
- Global/user and per-run verify config is operator-authored and
  trusted by origin.

This is the same treatment EP-45 gives project-skill `allowed-tools`
and shell injection — one trust story, not a second.

### Result and observability

- **Termination reasons.** The loop gains a `verify_exhausted` terminal
  state (max rounds hit with a still-failing verdict), distinct from
  `success` and from `error_max_turns`. (This is a small step toward the
  structured result envelope sketched as the sibling borrow; this EP
  introduces only the one reason it needs and does not depend on that
  larger contract landing.)
- **TUI.** A verify round renders as a distinct block — `verifying…`
  then the gate output or judge critique — so the operator sees *why*
  the model was sent back, not a silent extra turn.
- **Headless/ACP/MCP.** The final result carries the verify outcome
  (passed / exhausted + last critique) so a scripted caller can branch
  on it.
- **Audit.** Verify commands and judge calls are tool-like actions and
  get the same EP-4 trace metadata (args/result SHA, duration, cost) as
  any tool, so a session's audit trail shows what was checked.

## Migration / rollout

- Additive and opt-in: with no `[verify]` config and no `--verify`, the
  loop behaves exactly as today.
- Phase 1 (command gates) ships first; Phase 2 (judge) layers on.
- Any change to `AgentLoop`'s return type to carry the verify
  outcome/`verify_exhausted` reason is an internal break, fixed across
  in-repo callers in the same pass and noted in `CHANGELOG.md`
  (no-kid-gloves pre-1.0).
- Docs to update on implementation: `docs/features/` (new verify guide),
  `docs/commands/run.md` (`--verify`/`--no-verify`), `docs/commands/tui.md`
  (`/verify`), and the config reference.

## Failure modes

- **Verifier infrastructure error** (the verification shell/executor cannot
  launch, or a judge API fails) — distinguished from a check *failing*. A
  command inside a launched shell returning 126 or 127 is a failed check, not
  infrastructure. Default: surface loudly
  and **fail-open** (exit the loop with a warning) so a broken verifier
  can't wedge the session; a `[verify] strict = true` flips this to
  fail-closed for operators who want verification to be load-bearing.
  Open question Q1.
- **Unsatisfiable gate** (model can't make the check pass) — bounded by
  `max_verify_rounds`; the loop returns `verify_exhausted` with the last
  critique so the operator takes over, rather than burning turns
  forever.
- **Token blow-up** from judge rounds — every round counts against the existing
  turn and token ceilings; a verify round that would cross a token cap exits on
  the cap rather than running. Reported currency cost remains observational.
- **Over-eager judge** (false negatives sending good work back) —
  mitigated by opt-in, a tunable rubric, judge-only-after-gates-pass
  ordering, and `max_verify_rounds` as the floor on damage.
- **Untrusted-repo RCE** via `[verify] commands` — defeated by the EP-44
  trust gate (project verify config inert until trusted).

## Test strategy

- **Loop seam** (`internal/runtime`): no `[verify]` ⇒ byte-identical
  loop behavior; a failing command gate appends a feedback turn and
  re-enters the loop; all gates pass ⇒ normal exit; `max_verify_rounds`
  reached ⇒ `verify_exhausted` with the last verdict.
- **Command-gate**: pass/fail by exit code; output truncation; runs under
  the sandbox policy (assert it cannot exceed a model `bash` call's
  reach); judge skipped when a gate fails.
- **Judge**: structured-output verdict validated and retried on malformed
  output; fresh-context subagent (judge does not see the full
  transcript); `pass:false` critique becomes the feedback message.
- **Trust**: untrusted project `[verify] commands` are stripped and do
  not run; trusted project / user / per-run config runs. Pin against
  EP-44's existing trust tests.
- **Budget**: verify rounds count toward `MaxTurns` and token caps; cost is
  observational telemetry only. A token cap crossed mid-verify exits on the
  cap.
- **TUI E2E** (pty-bridge, per CLAUDE.md, inside `distrobox enter kali`):
  a verify round renders its own block; `/verify` toggles the phase;
  `verify_exhausted` surfaces a distinct end state.

## Open questions

- **Q1.** Fail-open vs fail-closed on verifier *infrastructure* error.
  Leaning fail-open-by-default with `strict = true` opt-in, but a
  security-minded operator may want the inverse default. Decide at
  Phase 1.
- **Q2.** Default trigger granularity: loop-exit only (Phase 1), or also
  an opt-in turn-boundary trigger (verify after each mutating turn, not
  just at the end)? Turn-boundary verification catches drift earlier but
  is far noisier/costlier; defer to Phase 3 behind an explicit flag.
- **Q3.** Plugin-authored verifiers via a `verify` hook point (EP-9
  style) — lets a wasm plugin register a verdict function. Attractive for
  domain checks (e.g. a security-research verifier), but adds an ABI
  surface; defer until the built-in kinds prove out.
- **Q4.** Should `verify_exhausted` be folded into the larger structured
  result-envelope EP (the sibling agent-loop borrow) or stand alone?
  This EP ships the minimal reason it needs; the envelope can subsume it
  later without a re-break.
- **Q5.** Visual/secondary verification (render a page, screenshot-diff)
  — out of scope here; revisit once the browser/chrome surface and a
  golden-image story exist.

## Decision log

### D1. Verify at the loop's exit point, feed failures back as a turn

- **Decided:** the verify phase runs when the model returns no tool calls
  (its "done" signal); on failure it injects the verdict as a new turn
  and continues the loop, rather than returning to the operator.
- **Alternatives:** verify after every tool/turn (noisy, expensive — and
  already covered by EP-9 `post_tool` for per-action policy); or verify
  only as a post-hoc report with no feedback loop (doesn't close the
  loop, the model never gets to fix it).
- **Why:** "verify work" is specifically the check *before* declaring
  done; the value is the model getting sent back to fix what failed, not
  a passive report the operator has to act on.

### D2. Two verifier kinds, cheapest-first, judge only after gates pass

- **Decided:** command gates (deterministic, exit-code) run first; the
  LLM-judge runs only if all gates pass.
- **Alternatives:** judge-only (pays a model for what a linter settles for
  free, and a judge can be fooled by a green-looking diff that doesn't
  build); gates-only (misses "compiles but wrong").
- **Why:** mirrors the operator's validate-with-the-cheapest-tool-first
  ladder; don't pay a model to judge a red build, and don't trust a green
  build to be *correct*.

### D3. Run the judge as a fresh-context subagent with a structured verdict

- **Decided:** the LLM-judge is an EP-38 subagent that sees the task +
  result + rubric (not the main transcript) and returns a forced
  `{pass, critique}` structured output; it is told to default to fail
  when unsure.
- **Alternatives:** judge in the main context (biased by the same
  reasoning that produced the work, and pays full-transcript tokens); a
  free-text verdict (unparseable, can't branch on it).
- **Why:** independence is the point of a second opinion — a fresh
  context is harder to fool with its own prior rationalizations, and the
  structured verdict makes pass/fail machine-checkable. Reuses the
  subagent machinery instead of inventing a judge runtime.

### D4. Bind project verify config to the EP-44 trust boundary

- **Decided:** `[verify] commands` and project-supplied judge config run
  only after the repo clears the EP-44 trust gate; untrusted, they are
  stripped like other powerful project keys, and gates always run under
  the session sandbox.
- **Alternatives:** run project verify config implicitly (RCE on opening
  any repo); a separate verify-specific trust prompt (a second trust
  story to reason about).
- **Why:** an auto-running on-exit command from a checked-in file is the
  EP-44 threat exactly; reuse that gate, don't fork it.

### D5. Opt-in, bounded, inside the existing budget

- **Decided:** verification is off until configured; rounds are capped by
  `max_verify_rounds` and counted against the existing `MaxTurns` and token
  caps; exhaustion yields a distinct `verify_exhausted` reason. Cost remains
  observational telemetry.
- **Alternatives:** on-by-default (surprises every existing session and
  can loop/cost unexpectedly); a private verify budget (two budgets to
  reason about, easier to blow real spend).
- **Why:** a verification loop that can wedge or exhaust the session is worse
  than none; bounding it inside the token/turn budget already in place keeps
  one enforcement story and a predictable exit.

## Related

- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md) — the lifecycle-hook seam this phase composes with (and the informational `post_turn` cousin it upgrades).
- [EP-38: ABI v2, bundled wasm tools, and runtime surface](./0038-abi-v2-bundled-wasm-and-runtime.md) — the subagent runner the LLM-judge runs on.
- [EP-44: Repo-config trust boundary](./0044-repo-config-trust-boundary.md) — the gate project-supplied verify commands bind to.
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md) and [EP-36: Loop, monitor, and schedule](./0036-loop-monitor-schedule.md) — the turn/token caps verification lives inside; provider cost is observational telemetry.
- [EP-37: Tool dispatch and operator surface](./0037-tool-dispatch-and-operator-surface.md) — command gates run through the sandboxed exec path.
- [EP-45: Model-Invocable Skills](./0045-model-invocable-skills.md) — the sibling "feature to borrow," sharing the EP-44 trust treatment.
- [Claude Agent SDK — How the agent loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop) and the gather/act/**verify**/repeat agentic loop — the borrow target.
