# Supervised work (`/supervise`)

`/supervise` is an opt-in, high-assurance workflow for long-running or
multi-stage work. Stado keeps an operator-approved contract in every worker
turn, monitors host-observed progress with an independent read-only watchdog,
and accepts completion only after deterministic gates and a fresh verifier.

This is different from `/supervisor`: the latter is EP-0033's responsive
off-band answer lane. It does not plan, monitor, interrupt, or verify work.

## Start a run

```text
/supervise
/supervise Implement resumable imports without breaking existing callers
/supervise start Implement resumable imports without breaking existing callers
```

The popup's basic page asks for the objective, watchdog mode (`event` by
default or `live`), plan-pivot approval (`user` by default or `watchdog`), and
standard/high-assurance/custom scrutiny profile. `Ctrl+A` opens advanced
settings for requirements/acceptance/verification hints, independent watchdog
and verifier provider/model, provider-native thinking, thinking budget, effort,
token/cost/time budgets, retry/backoff and correction limits, and
required/fallback posture.

The standard profile treats event monitoring as advisory with the bounded
ten-failure pause streak, while completion verification is required. High
assurance makes watchdog and verifier fail closed and disables advisory
fallback; changing the profile never widens plan-pivot authority.

Thinking and effort are separate: `thinking=auto|on|off` controls whether a
native reasoning budget is requested, while
`effort=low|medium|high|xhigh|max` is forwarded through the provider's effort
control when supported.

A fresh watchdog proposes the full baseline in scrollback. Worker execution
does not begin until you review it and choose Allow in the trusted approval
drawer.

## Modes

| Mode | Reviews | Failure behavior |
|------|---------|------------------|
| `event` | Only coalesced native detector events | Each review retries three times. Advisory work may continue after a failed review, but failure never approves a pivot/completion; ten consecutive failed triggers pause. |
| `live` | Every completed worker turn, plus native events | Retries indefinitely with capped exponential delay because live review is the selected control mechanism. The operator can interrupt, pause, or cancel the run. |

Both modes build a fresh watchdog provider instance per review. Bounded durable
handoff carries open concerns and suggested probes without keeping a second
unbounded conversation. A separate fresh provider instance performs final
verification.

## What stado enforces

- Exactly one approved plan step is active. Step-completion claims need evidence
  and watchdog approval; final verification cannot start until every approved
  step has advanced.
- Ordinary follow-up prompts are held until a safe boundary. A fresh watchdog
  routes directly related input to the worker and persists unrelated or
  uncertain work in the shared task store. The inbox is durable and acknowledged
  only after worker delivery or task creation, so restart/error recovery is
  at-least-once and marker-deduplicated. After verified completion, still-open
  task IDs created by that run are handed back to the normal worker loop in
  request order; unrelated global backlog items are not claimed. Use
  `/steer <message>` when you
  explicitly want to override that default and affect the current turn.
- The worker may record evidence, request a plan/contract pivot, and request
  completion through `supervise__*` control tools. These are requests, not
  authority grants.
- Plan pivots default to your approval. A configured watchdog may approve only
  plan-level pivots. Objective/criteria/constraint changes always return to you.
- Destructive actions, permission or budget changes, push/merge, release,
  publish, deploy, and external commitments are human-only. Recognized tool
  calls stop in a trusted approval drawer bound to that exact call.
- Completion runs configured Verify Work commands first. Only an anchored
  approval with cited evidence from the independent verifier marks the run
  complete. An optional-verifier fallback returns the run to work; it never
  converts verifier failure into completion.
- Nested supervision is rejected. The root worker remains responsible for all
  children, while the watchdog can inspect their lifecycle, results, diff, and
  scope violations.

Native detectors cover repeated errors, identical retry thrash, edit/revert
cycles, verification regression, stalled criteria, budget burn, growing or
out-of-scope diffs, child failure/exhaustion, step and pivot requests, risky
boundaries, correction follow-up, and unsupported completion. Detectors inspect
host facts, never hidden model reasoning.

## Watchdog access

The watchdog/verifier sees only `supervise__read`, `supervise__search`, and
`supervise__follow`. They page host-filtered state, contract, plan, events,
non-thinking transcript, tool activity/results, audited session diff,
verification, budgets, children, and an immutable repository snapshot. A
repository read lists or opens a bounded repo-relative file; search is a
bounded fixed-string scan with an optional path prefix. Pages and total
evidence are tied to the root session sequence, plan version, active step, and
tree digest. The reviewer gets no shell, mutation, network, credential,
generic live-filesystem, approval, or deployment tool.

The selected watchdog/verifier provider receives the evidence it asks for.
Choose those providers as a data-trust decision, especially when they differ
from the worker provider; stado preserves any hook-redacted tool result but
does not promise to discover arbitrary secrets pasted into source or chat.

This is a workflow boundary, not a replacement for the broker, sandbox, plugin
capabilities, or audit log. Those remain the actual execution ceiling.

## Lifecycle commands

```text
/supervise status
/supervise resume
/supervise cancel
```

State is persisted in the broker WAL. Restarting stado restores the latest run,
detector cooldown/history, watchdog handoff, and control tools for the root
session. `resume` restarts the appropriate interrupted phase: baseline review,
approval, pivot review, verification, or an operator-paused worker. `cancel` is
terminal and never deletes the audit record.

If hard context recovery automatically forks the worker, stado first advances
the durable sequence/tree anchor and attaches the same immutable-root run to the
compacted child. Reopening the child resumes supervision; reopening the parent
does not. Manual forks and ordinary session switches never inherit a run
implicitly.

## Evaluation

The paired scenarios under [`evals/supervise`](../../evals/supervise/README.md)
exercise common local/Ollama Cloud quirks. Validate and score artifacts with:

```sh
stado supervise-eval scenario evals/supervise/scenarios/retry-thrash.json
stado supervise-eval score --input observations.jsonl
```

The scorer keeps worker, watchdog, and verifier tokens separate and reports the
quality-per-token tradeoff rather than pretending supervision is free.

See [EP-0062](../eps/0062-harness-enforced-supervised-work.md) for the state
machine, authority model, detector policy, and decisions.
