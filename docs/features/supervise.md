# Supervised work (`/supervise`)

`/supervise` is the accepted opt-in quality-gate workflow for long-running or
multi-stage work. An installed official signed WASM lifecycle application keeps
an exactly selected contract in worker turns, evaluates host-observed progress
with an independent read-only watchdog, and accepts completion only after
deterministic gates and a fresh verifier.

> **Implementation status:** EP-0064's persistent WASM dispatcher and the
> migration of supervise policy out of native code are in flight. This page
> describes the accepted target behavior. Existing native workflow code is
> cleanup debt, not a second supported architecture.
>
> The interactive TUI is the only lifecycle-application host for v0.80/v1.
> `stado run`, headless JSON-RPC, and ACP reject configured applications before
> provider/session work rather than running an incomplete copy of the workflow.

It is a quality gate, not a security feature. It is designed to catch stalls,
scope or contract drift, weak evidence, and premature completion in ordinary
agent work. It does not make hostile repository content safe, contain a
malicious worker, protect secrets, or turn model agreement into an
authentication decision. Broker, sandbox, plugin, hook, and repository-trust
controls remain the security boundary.

This is different from `/supervisor`: the latter is EP-0033's responsive
off-band answer lane. It does not plan, monitor, interrupt, or verify work.

## Start a run

```text
/supervise
/supervise Implement resumable imports without breaking existing callers
/supervise start Implement resumable imports without breaking existing callers
```

The application's setup UI asks for the objective, watchdog mode (`event` by
default or `live`), plan-pivot approval (`user` by default or `watchdog`), and
standard/high-assurance/custom scrutiny profile. `Ctrl+A` opens advanced
settings for requirements/acceptance/verification hints, independent watchdog
and verifier provider/model, provider-native thinking, thinking budget, effort,
token budgets, timeouts, retry/backoff and correction limits, and
required/fallback posture.

The standard profile treats event monitoring as advisory with the bounded
ten-failure pause streak, while completion verification is required. High
assurance makes watchdog and verifier fail closed and disables advisory
fallback; changing the profile never widens plan-pivot authority.

Thinking and effort are separate: `thinking=auto|on|off` controls whether a
native reasoning budget is requested, while
`effort=low|medium|high|xhigh|max` is forwarded through the provider's effort
control when supported.

Reviewer spend ceilings are token-only in v1. Token caps are preflighted and
enforced on every reviewer turn; timeouts remain a separate execution bound.

A fresh watchdog proposes the full baseline. The application renders it through
the generic WASM UI bridge. After quality-workflow confirmation, it proposes a
session-scoped candidate, journals the exact artifact ID and version, and
refuses to start or recover against any other version. This is application-local
selection: it grants no authority and does not make the contract active prompt
content. A TUI command or UI callback is not operator-origin security proof.

## Modes

| Mode | Reviews | Failure behavior |
|------|---------|------------------|
| `event` | Coalesced application detector events, plus optional review every configured N worker turns | Each review follows the application's bounded retry/escalation policy. Failure never approves a pivot or completion. |
| `live` | Every completed worker turn, plus detector events; optional strict turn barrier | A strict profile holds the next turn until a current-anchor verdict. The operator can always interrupt, pause, or cancel. |

Both modes build a fresh watchdog provider instance per review. Bounded durable
handoff carries open concerns and suggested probes without keeping a second
unbounded conversation. A separate fresh provider instance performs final
verification.

All verdicts remain bound to the exact worker anchor they reviewed. A stale
`continue`/`approve` is discarded. A stale `correct` is delivered at the next
worker boundary as explicitly earlier-anchor advisory steering, without
pausing or becoming a current verdict. A stale `pause`/`stop` creates a durable
worker-scheduling hold and forces a fresh review of the latest anchor; only
that current verdict can release, steer, pause, or stop the run. Failure of this
confirming review pauses for the operator rather than falling back to advisory
execution.

## Policy and enforcement ownership

The supervise application owns workflow policy. The host and broker own bounded
session-anchored observations, durable ordering, capability ceilings, and the
effects of scheduling transitions. Artifact activation is a separate broker
authority operation and is not part of supervise startup.

- The application keeps exactly one approved plan step active. Step-completion
  claims need evidence under its policy; final verification cannot start until
  every approved step has advanced.
- The application owns the supervision schedule. Starting `/supervise` requests
  replacement of an existing `/loop`; the broker enforces authorized holds,
  pause, stop, cancel, and turn admission.
- Ordinary follow-up prompts are held until a safe boundary. The application
  asks a fresh watchdog to classify input, routes directly related input to the
  worker, and defers unrelated or uncertain work as a broker-owned task tied to
  the immutable input record. The application cannot rewrite or drop the
  original input, and it cannot acknowledge the event before choosing a
  disposition. After verified completion, the broker hands the exact ordered
  set of still-open inputs created by that run back to the normal worker loop;
  unrelated global backlog items are not claimed. Use
  `/steer <message>` when you
  explicitly want to override that default and affect the current turn.
- The worker may record evidence, request a plan/contract pivot, and request
  completion through tools exported by the supervise plugin. These are
  application requests, not authority grants.
- Plan pivots default to your approval. If configured, the application may
  accept a watchdog recommendation only for plan-level pivots.
  Objective/criteria/constraint changes always return to you.
- A contract may require quality-workflow confirmation before typed
  high-consequence effects such as destructive changes, push/merge, release,
  publish, deploy, or external commitments. The application can hold scheduling
  and request confirmation through the generic UI bridge, but this does not
  authenticate the operator or create a security approval. Security-significant
  authorization remains central host/broker policy; supervise does not infer it
  by parsing shell or `gh` command text.
- The application requests configured Verify Work commands through the audited
  executor. It accepts only an anchored approval with cited evidence from the
  independent verifier; the broker records completion against current contract,
  run, and tree versions. A verifier failure never silently becomes completion.
- Nested supervision is rejected. The root worker remains responsible for all
  children, while the watchdog can inspect their lifecycle, results, diff, and
  scope violations.

Deterministic detectors inside the supervise application cover repeated errors,
identical retry thrash, edit/revert cycles, verification regression, stalled
criteria, budget burn, growing or
out-of-scope diffs, child failure/exhaustion, step and pivot requests, risky
boundaries, correction follow-up, and unsupported completion. Detectors inspect
bounded host observations tied to the session/tree anchor, never hidden model
reasoning. Their thresholds and
coalescing are application policy, not native stado semantics.

## Watchdog access

The application spawns each watchdog/verifier with an attenuated role and tool
profile. A bounded handoff and immutable evidence references travel through
broker mailboxes. Generic session/research reads page host-filtered state,
non-thinking transcript, tool results, audited diff, verification, budgets,
children, and the anchored repository tree. Evidence stays tied to the root
session sequence, contract/plan versions, active step, and tree digest.

The accepted EP-0064 design does not add native
`supervise__read/search/follow` evidence tools. Any remaining bespoke native
surface is migration debt; generic session, mailbox, artifact, and research
primitives are sufficient. The reviewer receives no shell, mutation, ambient
network, credential, approval, or deployment capability.

The selected watchdog/verifier provider receives the evidence it asks for.
Choose those providers as a data-trust decision, especially when they differ
from the worker provider; stado preserves any hook-redacted tool result but
does not promise to discover arbitrary secrets pasted into source or chat.

This is a quality gate, not a security boundary or a replacement for the
broker, sandbox, plugin capabilities, repository trust policy, or audit log.
The supervise application runs below that execution ceiling.

## Lifecycle commands

```text
/supervise status
/supervise resume
/supervise cancel
```

The exactly selected contract candidate is a versioned session-scoped artifact. Rapid run state,
detector history, watchdog handoff, and event cursors live in broker journal and
mailbox records. A fresh WASM instance can reconstruct that state after an
exact-session rebind; authoritative recovery never depends on instance memory.
Full-process logical-session adoption remains a cutover blocker and must fail
closed until the broker can reopen the durable generation and mint fresh opaque
bindings without creating a second owner. `resume` restarts the
appropriate interrupted phase: baseline review, approval, pivot review,
verification, or an operator-paused worker. `cancel` is terminal and never
deletes the audit record.

If hard context recovery automatically forks the worker, the application asks
the broker to advance the durable sequence/tree anchor and attach the same
immutable-root run to the compacted child. Reopening the child resumes
supervision; reopening the parent does not. Manual forks and ordinary session
switches never inherit a run implicitly. That atomic ancestry-checked transfer
is also still a cutover blocker; current code must stop rather than silently
lose or copy application state.

## Evaluation

The paired scenarios under [`evals/supervise`](../../evals/supervise/README.md)
exercise common local/Ollama Cloud quirks. Validate and score artifacts with:

```sh
stado supervise-eval scenario evals/supervise/scenarios/retry-thrash.json
stado supervise-eval score --input observations.jsonl
```

The scorer keeps worker, watchdog, and verifier tokens separate and reports the
quality-per-token tradeoff rather than pretending supervision is free.

For a narrative walkthrough of the failure modes behind the design, see
[The Loop Needs a Witness](../articles/supervise-in-practice.md).

See [EP-0062](../eps/0062-harness-enforced-supervised-work.md) for the quality
semantics and [EP-0064](../eps/0064-wasm-lifecycle-applications.md) for the
application/host placement and lifecycle contract.
