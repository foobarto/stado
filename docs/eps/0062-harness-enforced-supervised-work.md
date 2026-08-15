---
ep: 62
title: Harness-Enforced Supervised Work
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-13
requires: ["EP-0004", "EP-0007", "EP-0056", "EP-0059"]
extends: ["EP-0046", "EP-0060"]
extended-by: ["EP-0064"]
see-also: ["EP-0009", "EP-0033", "EP-0044", "EP-0057"]
history:
  - date: 2026-08-15
    status: Accepted
    note: Official supervise v0.1.1 is offline-key signed, published, path-reproducible across distinct Go 1.26.6 GOROOT locations, and clean-installed from released bytes against the official anchor. The full plugin-owned conformance matrix against those exact published bytes and the broader accepted quality contract remain open.
  - date: 2026-08-14
    status: Accepted
    note: The official supervise application source and plugin-owned evaluator are durably checkpointed in a signed local stado-plugins commit; native workflow and evaluator surfaces were removed, while offline plugin signing, publication, and cross-repository release proof remain outstanding.
  - date: 2026-08-14
    status: Accepted
    note: EP-64 replaces this EP's native service/tool placement, marker-based shared-task implementation, and trusted approval-drawer wording with signed WASM application policy over broker-owned facts, immutable input routing, generic verification facts, and central host/broker security authority.
  - date: 2026-08-14
    status: Accepted
    note: Corrected the premature v0.80.0 implementation marker; the accepted quality contract remains, but the plugin is not released.
  - date: 2026-08-13
    status: Accepted
    note: Accepted after the supervision brainstorm fixed authority, lifecycle, failure, and model-profile semantics.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **Extends:** [EP-0046](./0046-verify-work-phase.md), [EP-0060](./0060-native-harness-guidance.md) · **Extended by:** [EP-0064](./0064-wasm-lifecycle-applications.md) · **See also:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0033](./0033-responsive-supervisor-worker-lanes.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md)

# EP-0062: Harness-Enforced Supervised Work

## Problem

Model quality varies most visibly on long-running work. Some models plan and
verify naturally; others implement before requirements settle, repeat failed
tactics, forget early constraints, expand scope, accept child self-reports, or
announce completion without evidence. Prompting the worker to be disciplined is
not reliable enforcement: the same context pressure that causes the failure can
also make it forget the instruction.

Stado already has command verification, durable session state, retained-agent
lifecycle facts, versioned artifacts, and a broker that can enforce scheduling.
It needs an opt-in quality application that binds those primitives into one
selected contract and keeps an independent observer able to correct, pause, or
stop the worker.

## Goals

- Start high-assurance work through the official `supervise` application's TUI
  wizard and one explicitly selected quality contract.
- Keep the objective, constraints, plan, definition of done, and verification
  expectations outside the worker's forgetful conversation.
- Derive deterministic warning signals from authenticated host facts while
  leaving thresholds and semantic judgment in application policy.
- Let an independent watchdog steer, pause, or stop a loop without giving it
  mutation, credential, deployment, or ambient OS authority.
- Preserve operator authority over contract changes and security-significant
  external effects.
- Require factual Verify Work results plus an independent verifier, not worker
  prose, before completion.
- Persist bounded application state and reviewer handoff across callback/module
  replay.
- Make quality/token tradeoffs measurable against representative weaker models.

## Non-goals

- A general product-management organization, issue tracker, or scheduler.
- Nested supervision. The root worker remains accountable for all subworkers.
- A non-interactive lifecycle-application host in v1. Only the TUI composes the
  complete application; other surfaces fail closed.
- Giving watchdogs arbitrary shell, filesystem mutation, network, credential,
  approval, or deployment tools.
- Treating model reasoning or structured output as control authority.
- Hiding watchdog/verifier token and latency cost. V1 budgets are token-only.
- Replacing the responsive `/supervisor` lane from EP-0033.
- Acting as a security control, prompt-injection defense, sandbox, or
  containment boundary. Supervise is a quality gate; broker, sandbox, plugin,
  hook, repository-trust, and central authorization policy remain the security
  ceiling.

## Design

### Application policy over native primitives

EP-0064 defines the implementation placement. The official
`foobarto/stado-plugins/supervise` package is a signed/installable WASM lifecycle
application. When the exact installed application is explicitly enabled, its
signed manifest dynamically owns `/supervise` and the three application-worker
tools. Native stado has no command, model tool, state machine, detector policy,
reviewer prompt, verdict parser, evaluator, or fallback implementation for this
feature.

The application may be large. It owns contract flow, cadence, deterministic
comparison policy, reviewer/verifier orchestration, retries, stale-result
handling, correction, pivot, input-routing, and completion policy. Native stado
owns only generic capabilities unavailable inside the WASM garden: canonical
identity, provider credentials, immutable session/tree facts, broker ordering,
artifacts, mailboxes, input records, audited execution, budgets, holds, and
scheduling transitions.

That division is also the security boundary. A watchdog's judgment can be wrong
or steered. The broker checks whether a requested effect is current, scoped,
capability-admitted, and CAS-valid; it does not certify that the model's semantic
judgment was wise.

### Setup and exact baseline

`/supervise [objective]` runs only through the enabled application. Basic setup
chooses objective, `event` or `live` mode, user or bounded-watchdog plan-pivot
policy, and standard/high-assurance/custom scrutiny. Advanced setup independently
selects watchdog and verifier provider/model, provider-native thinking and
reasoning effort, thinking/token budgets, timeouts, retry/backoff, correction
limits, acceptance/DoD hints, and required/advisory posture. Provider credentials
remain native.

A fresh read-only baseline architect returns a strict proposal: objective,
constraints, non-goals, acceptance criteria, ordered steps with evidence-based
exit conditions, definition of done, verification, and risks. The application
renders the exact proposal and asks for quality-workflow confirmation before
creating a session-scoped candidate artifact and one application-owned
WorkerRun.

That confirmation selects application configuration. It does not activate the
artifact as prompt authority, widen capabilities, authorize a destructive or
external effect, or prove a security-significant operator gesture.

### Durable state and model tools

Rapid workflow state is reconstructed from broker journal and projection facts,
not ambient files or guest access to stado's WAL. It includes the exact selected
artifact/version, run and WorkerRun versions, current plan anchor, active and
completed steps, criterion evidence, detector counters and cooldowns, pending
review children, bounded handoff, hold lease/version, input dispositions, and
completion state.

The signed application exposes only:

- `supervise__report_progress` for criterion-linked evidence;
- `supervise__request_pivot` for a complete replacement baseline; and
- `supervise__request_completion` for an explicit completion candidate.

The host projects these tools only to the exact WorkerRun the application owns,
under ordinary Do/Plan/BTW and global tool ceilings. Every call repeats the exact
current application plan anchor. A request can advance application policy; it
cannot grant OS, broker, or operator authority.

### Watchdog modes and stall detection

`event` is the default. Application detectors coalesce facts such as repeated
failures, retry thrash, edit/revert cycles, verification regression, budget
burn, diff/scope growth, child failure/exhaustion, milestone and pivot requests,
correction follow-up, risky quality boundaries, and unsupported completion. An
optional interval also triggers every configured N committed worker turns.

`live` additionally reviews every completed worker turn. Optional strict live
acquires a scheduling barrier before review starts, so the next worker turn
cannot outrun the verdict. Non-strict live permits forward motion subject to the
stale-result rules below.

The four-turn progress-stall detector measures approved-plan progress. It resets
only when the active step changes or completed-step count advances. More tools,
a changed tree, or new evidence are activity and useful review context; they do
not by themselves prove progress.

Each review uses a fresh bounded read-only child pinned to the exact immutable
turn source. A bounded typed handoff carries concerns and probes without keeping
a second unbounded conversation. A successfully parsed and anchored verdict is
the semantic result even if provider cleanup later fails; cleanup diagnostics
cannot erase valid work.

Event triggers receive three fresh attempts by default. Ten consecutive
exhausted triggers pause standard supervision, while a valid verdict resets the
streak. Live review retries with durable capped backoff. Three unsuccessful
correction follow-ups pause. Attempts, due times, counters, and handoff survive
callback loss and module rebind.

### Stale results

Every verdict binds the exact session generation, WorkerRun version, worker
sequence, plan version, active step, and tree source it reviewed. Delay therefore
has an explicit three-way meaning:

- stale `continue` or `approve` is discarded;
- stale `correct` is queued at the next safe worker boundary as labelled
  earlier-anchor advisory steering; it does not become a current verdict and
  does not require a second review;
- stale `pause` or `stop` first acquires a durable scheduling hold, then starts
  a fresh review against the latest anchor. Only the current result may release,
  steer, pause, or stop. Confirmation failure pauses for the operator.

This keeps ordinary steering useful without letting an obsolete approval
authorize later state or silently ignoring an obsolete but serious stop signal.
A current watchdog may stop a loop because the application requests a typed
transition and the broker enforces it; model prose itself has no such authority.

### Follow-up input and pivots

While the application owns recurrence, ordinary follow-up prompts become
immutable broker input records. The application claims one exact record for
review, acknowledges its mandatory event only after the claim is durable, and
uses a fresh anchored child to classify it. Related input is delivered unchanged
at a safe worker boundary. Unrelated, uncertain, malformed, or exhausted review
becomes deferred work. The application cannot rewrite, drop, retarget, or hide
the original, and completion cannot omit open inputs created by that run.
Explicit `/steer` remains the immediate operator override.

Tactical implementation choices are free. A pivot request contains a complete
replacement baseline. The application compares all contract fields and may
accept a watchdog recommendation only for a configured plan-only change.
Objective, criteria, constraints, and other contract changes return to quality
confirmation. Destructive actions, push/merge/release/publish/deploy, secrets,
and external commitments remain central host/broker policy; supervise neither
parses command strings into authorization nor manufactures an operator grant.

### Completion and generic Verify Work facts

Assistant prose and no-tool finality cannot start completion. The worker must
use the application tool after all criteria and ordered plan steps have
advanced. The application then holds the WorkerRun and requests the
operator-configured Verify Work suite through EP-0064's generic asynchronous
verification primitive.

The guest supplies only run/version/pending-event correlation. It cannot supply
commands, a tree anchor, or executable authority. The broker derives the exact
immutable source; the TUI resolves only `[verify].commands`, waits for the source
event acknowledgement, and uses the ordinary audited executor. Pause, stop,
completion, terminal worker state, and session-generation changes win over
verification. Hook observation/denial remains active; mutation is rejected so
evidence cannot describe a different command/result.

The terminal `stado.dev/session-verification-facts/v1` event contains strict,
bounded facts: source and source evidence, suite/command/result digests, typed
outcomes and failure fingerprints, exact signed trace/tree commit references,
and terminal broker evidence. It contains no command/output plaintext, authority
field, or native supervise verdict. `no_suite` means absence, never success.
Execution is at-least-once across the irreducible crash window after a command
returns but before the terminal WAL append, so configured commands must tolerate
retry.

Eligible command facts lead to a completion watchdog and then a separate fresh
verifier over the same contract, anchor, tree, and immutable evidence. Only a
current verifier approval lets the application call the generic broker
completion transition. Required-verifier failure pauses with the candidate and
hold intact. Advisory failure invalidates the candidate and resumes work.
Neither path infers success from absence.

### Lifecycle and supported surface

`/supervise status|resume|cancel` are application commands. `resume` retries the
exact interrupted phase and never resurrects a broker-terminal WorkerRun.
`cancel` is terminal and preserves the audit record. With no setup, or after
exact cancellation/completion cleanup, the persistent application is dormant:
it acknowledges lifecycle events without denying ordinary work. Failed child
cancellation, worker termination, hold release, or journal cleanup remains a
durable obligation rather than falsely declaring dormancy.

The TUI is the only supported lifecycle-application host in current/v1 scope.
`stado run`, headless JSON-RPC, ACP, legacy background loading, ephemeral plugin
execution, and recursively spawned agent loops fail closed before partial
composition. Automatic context recovery must atomically transfer the existing
application scope to the compacted child; manual forks inherit nothing.

### Evaluation

The evaluator and six paired scenarios live with the application under
[`foobarto/stado-plugins/supervise/evals`](https://github.com/foobarto/stado-plugins/tree/main/supervise/evals),
not in the stado binary. The module-owned `supervise-eval` validates scenario
fixtures and scores paired JSONL observations for criteria satisfaction,
defects, useful/false interventions, repetition, diff scope,
escalation/completion, role-specific tokens, latency, and transparent quality
per 1,000 tokens. Paired arms with different positive criteria totals are
rejected.

The scenarios are falsifiable test apparatus, not published evidence that a
second model universally improves work. Provider, model, parameters, tool
surface, starting tree, sandbox, and worker token budget must match between arms;
failed and paused runs remain in the data.

## Rollout status

Official `supervise/v0.1.1` is published separately from stado with
`min_stado_version: 0.80.0`; plugin and host versions are independent. Its
manifest and WASM are signed by the operator-held official plugin key, and the
release bytes reproduce across distinct Go 1.26.6 GOROOT locations. Fresh
GitHub release bytes passed signature, digest, isolated install, and
installed-package verification against the official owner anchor.

The signed package is explicitly installed and enabled; it is never a native or
default surface. EP-0062 remains Accepted rather than Implemented until the
complete plugin-owned paired/conformance matrix is repeated against those exact
published bytes and the remaining accepted quality-contract gates close.

## Failure modes

- Missing, disabled, invalid, ambiguous, unsigned, or incompatible application:
  `/supervise` is absent or fails explicitly; there is no native fallback.
- Baseline/watchdog/verifier failure is visible; work never begins without a
  selected baseline and never completes without the configured completion
  policy.
- Stale verdicts follow the three-way rule above; no old approval crosses an
  anchor and no old pause/stop is silently dropped or blindly applied.
- Provider cleanup is diagnostic and cannot replace a valid parsed verdict.
- Hard token preflight includes provider output/thinking headroom; USD does not
  participate in supervise v1 policy.
- Broker, journal, hold-renewal, or controller failure preserves the durable
  fence and pauses/fails closed rather than inventing a filesystem fallback.
- Reviewer evidence limits abort the review rather than silently truncating an
  authority-bearing judgment.
- Selected reviewer providers receive requested evidence and are a data-egress
  trust choice. Stado cannot promise to find arbitrary secrets pasted into
  source or chat.
- Verification commands may repeat after the documented crash window.
- Input-classification uncertainty or failure defers the immutable original;
  bounded continuation overflow pauses rather than dropping or relabelling it.
- The application is a quality gate below the broker/sandbox ceiling and does
  not defend against a malicious worker or source tree.

## Test strategy

- Plugin state-machine tests cover setup, exact artifacts, roles, model-tool
  anchors, detectors, retries, stale classes, evidence, pivots, completion,
  input routing, cancellation, dormancy, reply loss, and recovery.
- Host tests cover signed application command/tool ownership, one persistent
  instance, source pinning, facts schemas, event ACK ordering, broker admission,
  input records, holds, scheduling, generic verification, evidence refs, and
  generation fences.
- Strict fixture comparison keeps `session.turn_committed`, `agent.down`, and
  `session.verification_finished` producers/consumers byte-compatible.
- The official plugin check runs unit/race/vet, reproducible WASI builds, all six
  scenario validations, and example scoring.
- The real release gate installs the offline-key-signed package in isolated
  roots and exercises TUI/PTY, restart, automatic compaction transfer, update,
  and removal without native fallback.

## Decision log

### D1. `/supervise`, watchdog, and verifier are distinct roles

- **Decided:** `/supervisor` keeps its EP-0033 meaning; this workflow is
  `/supervise`, its progress observer is the watchdog, and final review belongs
  to a separate verifier.
- **Why:** distinct nouns prevent unrelated authority models from collapsing
  into one interface.

### D2. Event is default; live and event-plus-N are explicit alternatives

- **Decided:** event detectors may be supplemented by every-N-turn review; live
  reviews every turn and may enable a strict turn barrier.
- **Why:** tasks can trade latency/tokens for stronger oversight without making
  one cadence universal.

### D3. Application policy requests; the broker enforces

- **Decided:** signed WASM owns quality decisions and stado owns authenticated
  facts, capability ceilings, CAS, and scheduling effects.
- **Why:** large applications belong in plugin space; WASM cannot manufacture
  OS/runtime authority and should not receive it indirectly through prose.

### D4. Fresh models with bounded handoff

- **Decided:** each watchdog/verifier is a fresh pinned child; only typed bounded
  handoff persists.
- **Why:** freshness limits context bias and avoids a second unbounded
  conversation lifecycle.

### D5. Activity does not reset progress stalls

- **Decided:** only active/completed plan-step movement resets the four-turn
  detector.
- **Why:** a busy loop can produce evidence and tree churn without getting
  closer to an accepted criterion.

### D6. Stale outcomes are asymmetric

- **Decided:** discard approval, deliver correction as labelled advisory, and
  hold plus re-review pause/stop.
- **Why:** this avoids unsafe stale authorization and excessive barriers while
  ensuring a serious obsolete verdict receives current review.

### D7. Provider cleanup cannot erase a verdict

- **Decided:** a valid parsed anchored result wins over later teardown error.
- **Why:** resource cleanup is not semantic authority.

### D8. Completion belongs to facts plus a fresh verifier

- **Decided:** the worker requests, operator-configured commands produce generic
  facts, and an independent current verifier accepts.
- **Why:** the implementer cannot self-certify, while native stado remains free
  of supervise-shaped policy.

### D9. No nested supervision in v1

- **Decided:** root supervision observes and accounts for all subworkers.
- **Why:** recursive watchdog/verifier authority and budgets multiply complexity
  before the root contract is proven.

## Related

- [Supervised-work feature guide](../features/supervise.md)
- [Official comparative evaluation kit](https://github.com/foobarto/stado-plugins/tree/main/supervise/evals)
- [The Loop Needs a Witness](../articles/supervise-in-practice.md)
