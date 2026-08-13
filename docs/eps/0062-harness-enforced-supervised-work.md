---
ep: 62
title: Harness-Enforced Supervised Work
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-08-13
implemented-in: v0.80.0
requires: ["EP-0004", "EP-0007", "EP-0056", "EP-0059"]
extends: ["EP-0046", "EP-0060"]
see-also: ["EP-0009", "EP-0033", "EP-0044", "EP-0057"]
history:
  - date: 2026-08-13
    status: Accepted
    note: Accepted after the supervision brainstorm fixed authority, lifecycle, failure, and model-profile semantics.
  - date: 2026-08-13
    status: Implemented
    version: v0.80.0
    note: Shipped the durable /supervise state machine, event/live watchdogs, independent verifier, trusted wizard, task deferral, evidence tools, native detectors, and comparative eval kit.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **Extends:** [EP-0046](./0046-verify-work-phase.md), [EP-0060](./0060-native-harness-guidance.md) · **See also:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0033](./0033-responsive-supervisor-worker-lanes.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md)

# EP-0062: Harness-Enforced Supervised Work

## Problem

Model quality varies most visibly on long-running work. Some models plan and
verify naturally; others implement before requirements settle, repeat failed
tactics, forget early constraints, expand scope, accept child self-reports, or
announce completion without evidence. Prompting the worker to be disciplined is
not reliable enforcement: the same context pressure that causes the failure can
also make it forget the instruction.

Stado already has command verification, durable session state, task storage,
subagent lifecycle visibility, and native bounded guidance. It lacks one
host-owned workflow that binds these pieces into an approved contract and keeps
an independent observer able to correct or stop the worker.

## Goals

- Start high-assurance work through a trusted `/supervise` requirements wizard.
- Keep one operator-approved objective, plan, definition of done, and
  verification contract in every worker turn.
- Detect mechanical warning signs natively and let an independent watchdog
  judge ambiguous alignment without giving it mutation authority.
- Preserve operator authority over the contract and risky external effects.
- Require an independent verifier, not worker prose, to accept completion.
- Persist enough state and bounded watchdog handoff to survive process restarts.
- Make the quality/token tradeoff measurable against representative weaker
  models.

## Non-goals

- A general product-management organization, issue tracker, or scheduler.
- Nested supervision. The root worker remains accountable for all subworkers.
- Giving watchdogs arbitrary shell, filesystem mutation, network, credential,
  approval, or deployment tools.
- Treating model reasoning text as detector input or auditable authority.
- Hiding the expected watchdog/verifier token and latency cost.
- Replacing the responsive frontline `/supervisor` lane from EP-0033.
- Acting as a security control, indirect-prompt-injection defense, sandbox, or
  containment boundary for a malicious worker or repository. `/supervise` is a
  quality gate; existing broker, sandbox, plugin, hook, and repository-trust
  controls remain the security ceiling.

## Design

### Quality gate, not security boundary

Supervision separates stochastic implementation from quality judgments and
host-owned workflow transitions. This prevents a worker from self-certifying
progress, changing its approved contract, or marking its own completion. It
does not make watchdog or verifier judgments adversarially trustworthy. Both
roles read instruction-shaped repository and transcript content and can be
steered; read-only evidence tools constrain consequences but are not an
indirect-prompt-injection defense.

Human-only risk decisions and model role restrictions preserve workflow
authority. Security enforcement remains with the broker, sandbox, plugin
capability system, hooks, repository trust boundary, and operator approvals.

### Trusted setup and approved baseline

`/supervise [objective]` opens a host-rendered popup. The basic page chooses the
objective, watchdog mode (`event` or `live`), plan-pivot authority (`user` or
bounded `watchdog`), and standard/high-assurance/custom profile. Advanced fields
select watchdog and verifier provider/model, provider-native
`thinking=auto|on|off`, thinking budget, `effort=low|medium|high|xhigh|max`,
token budgets, timeouts, retry/backoff and correction limits, acceptance/DoD
hints, and required/fallback posture.

The standard profile keeps event monitoring advisory under the bounded
failure-streak policy while requiring completion verification. High assurance
makes both reviewer roles fail closed and disables advisory fallback. Scrutiny
profiles do not change pivot authority.

A fresh watchdog turns the seed into objective, constraints, non-goals,
acceptance criteria, ordered steps with evidence-based exit conditions,
definition of done, verification, and risks. No work begins until the operator
reviews the full proposal and explicitly approves it. The approved baseline and
current host state are injected on every worker turn, independently of
conversation compaction.

### Durable host state and authority

The `supervise` service folds append-only broker WAL snapshots with
compare-and-swap versions. State includes the immutable root session, currently
attached worker-session continuation, original objective seed, approved
contract digest, plan version, active/completed steps, worker sequence,
initial/current tree digests, evidence, pending pivot/completion, detector
history/cooldowns, watchdog verdict/handoff, failure counters, and lifecycle
status.

Every judgment is anchored to root session, worker sequence, plan version,
active step, and tree digest. A stale judgment cannot advance a step, approve a
pivot, or complete work. The worker can only report evidence and request pivots
or completion through host tools. It cannot mutate the supervision state
directly.

### Watchdog modes and evidence surface

`event` is the default. A deterministic detector coalesces and rate-limits
signals, then creates a fresh watchdog provider instance for that trigger.
`live` also triggers at every completed worker turn. Fresh instances avoid
accumulated context bias; a bounded durable handoff carries only open concerns,
hypotheses, interventions, missing evidence, and suggested probes.

The watchdog receives only `supervise__read`, `supervise__search`, and
`supervise__follow`. These page host-filtered state, contract, plan, mechanical
events, non-thinking transcript blocks, tool activity/results, audited session
diff, verification, budgets, root-owned child lifecycle, and immutable
repository files. Repository list/read/search is repo-relative, size/scan
bounded, and served from the anchored session tree rather than a moving live
filesystem. Pages are capped and bound to the reviewed anchor. There is no
generic mutation, shell, network, credential, approval, or arbitrary live
filesystem tool.

Continuation cursors are opaque and local to one anchored review. They retain
the exact originating read/search operation and its validated path, pattern,
kinds, and page size; following a search cannot degrade into an unfiltered read.

The native detector observes host facts for repeated failures, identical retry
thrash, edit/revert cycles, verification regression, four-turn criterion
stalls, 80% budget burn, diff/scope expansion, child failure/exhaustion, step
claims, pivot requests, risky boundaries, correction follow-up, and completion
claims. It never parses hidden reasoning. A criterion-stall window resets only
when the active step or completed-step count advances; new evidence and tree
churn remain review context but are activity, not proof of plan progress.

### Corrections, interruption, and failure

An event verdict can continue, approve an evidence-backed step transition,
inject a host-authenticated correction, or pause/stop the worker. Correction
interrupts the active stream/tool at the host boundary, clears unexecuted tool
calls, and becomes the next worker prompt. A forced follow-up review determines
whether steering recovered; repeated failed corrections pause for the operator.

Event reviews retry three times. Exhausted event-mode review failures may fail
open only as advisory absence, never as pivot/completion approval; ten
consecutive failed triggers pause the run. A successful review resets the
streak. Live mode retries indefinitely with capped exponential delays because
the reviewer is its chosen control mechanism. The operator can always
interrupt, pause/cancel, or resume.

### Single-focus work and deferred prompts

While a supervised run is active, additional ordinary prompts enter a host-held
inbox instead of steering the worker immediately. At the next safe boundary, a
fresh watchdog classifies each against the approved objective and active step.
Directly related input is explicitly labelled and added to worker context;
unrelated input, uncertainty, or reviewer failure is conservatively written as
an open item in the shared `tasks` store. The durable inbox is acknowledged
only after delivery or task creation; host-minted markers deduplicate retry and
restart replay. After verified completion, the host queries that run's marker,
hands its still-open task IDs back to the normal worker loop in request order,
and starts the oldest without claiming unrelated global backlog items. Explicit
`/steer` remains the operator override. This uses the existing task system
rather than creating a competing project tracker.

Supervision owns worker-turn scheduling while it is active. Starting
`/supervise` stops any existing `/loop`; the watchdog/host may interrupt or
stop recurring execution under the supervision lifecycle.

### Pivots and human-only boundaries

Tactical implementation changes are free. Plan replacements require a worker
request and a fresh watchdog review. By default the operator sees the exact
proposal and recommendation and decides; a configured watchdog may approve only
a plan-level pivot. Objective, criteria, constraints, permissions, budgets,
destructive operations, push/merge/release/publish/deploy, and external
commitments remain human-only. Contract pivots always require the operator.
Recognized high-risk tool calls are held in a trusted approval drawer bound to
that exact call.

### Completion

The worker requests completion with criterion-linked evidence. Configured
completion requests are rejected mechanically while any approved plan step
remains active. Verify Work commands then run through the normal audited
executor. A failed gate returns bounded correction to the worker. A pass starts
a fresh verifier provider instance, independent of all watchdog instances. Only
a current `approve` verdict with cited evidence from the verifier transitions
the durable run to completed; rejection resumes work. Required-verifier
unavailability pauses. An explicitly optional/advisory fallback invalidates the
completion request and resumes work rather than inferring success.

### Evaluation

`evals/supervise` defines paired, reproducible scenarios for premature
implementation/completion, retry thrash, scope drift, multi-stage context loss,
bad pivots, and root accountability for subagents. `stado supervise-eval`
validates scenarios and scores JSONL arms for criteria satisfaction, defects,
useful/false interventions, repetition, diff scope, escalation/completion,
role-specific tokens, latency, and transparent quality per 1,000 tokens.

## Migration / rollout

The feature is opt-in and TUI-rooted. Existing sessions, `/supervisor`, Verify
Work, tasks, providers, and ordinary turns retain their behavior. Supervision
state uses a new WAL store name; absence means no active run. Old interrupted
setup records without a durable objective seed can be inspected/cancelled but
must be restarted. No nested run is admitted for the same non-terminal root.
An automatic context-overflow recovery fork atomically advances the sequence
and tree anchor and moves the worker-session attachment while retaining the
immutable root. Reopening that child restores the run; the parent no longer
does. A normal operator-created or manually selected fork is a separate session
and does not inherit supervision implicitly.

## Failure modes

- Baseline/watchdog/verifier provider failure is visible; work never begins
  without baseline approval and never completes without verification.
- Stale model responses cannot authorize current-state transitions. Stale
  positive event verdicts are discarded; stale corrections may be delivered
  as explicitly earlier-anchor advisory steering; stale pause/stop proposals
  create a durable worker-scheduling hold and require a fresh current-anchor
  watchdog verdict before work can resume.
- Provider teardown is resource cleanup, not verdict authority: a teardown
  error does not replace a successfully parsed and anchored verdict.
- Hard token preflight includes provider-declared output headroom for native
  thinking, so a provider cannot widen a budget-derived request at dispatch.
- If a required broker-context update fails after a durable transition, the
  host durably pauses the run and carries the reason into operator resumption;
  it does not leave a nominally running run with no scheduled worker turn.
- Evidence/tool/page budgets abort the review rather than silently truncating
  authority-bearing judgments.
- The operator-selected reviewer provider receives requested evidence. A
  different provider is a data-trust choice: configured hook redaction is
  preserved, but arbitrary source/chat secrets are neither automatically
  discovered nor guaranteed to be redacted.
- Process restart restores state, detector cooldowns, handoff, and control tools;
  `/supervise resume` restarts the state-appropriate review/gate.
- Automatic compaction recovery either durably reattaches before adopting the
  child or remains on the parent; it never carries parent verdict authority
  into an unrecorded child continuation.
- Task-classification failure defers conservatively and discloses any task-store
  write failure.
- Risk detection is defense in depth. Broker/sandbox/plugin capability policy
  remains the actual execution ceiling.

## Test strategy

- State-machine unit tests cover roles, stale anchors, evidence-backed
  authority, plan-gated completion, pivots, completion,
  failure thresholds, resume, durable seed/detector restoration, recovery-fork
  attachment, and limits.
- Detector fixtures cover coalescing, cooldown restore, thrash, regressions,
  stalls, budgets, scope, child, step, correction, risk, pivot, and completion.
- Reviewer tests prove fresh provider construction, independent role profiles,
  filtered tools, anchored repository reads, exact anchoring, served-citation
  binding, strict JSON, evidence budgets, and follow-up classification.
- Provider tests assert native effort forwarding for Anthropic, OpenAI, and
  OpenAI-compatible backends.
- TUI tests cover the wizard, slash lifecycle, authority prompt, risk matcher,
  task routing, milestone and completion flows, plus PTY smoke coverage.
- Paired evals preserve the expected token increase and compare quality per
  token rather than claiming supervision is universally cheaper.

## Open questions

- Whether `live` should later retain a bounded provider conversation for a few
  turns before rotation; v1 deliberately uses fresh reviews for simpler bias and
  crash semantics.
- Whether a future non-interactive surface should expose the same workflow.
- Whether detector thresholds should become operator-tunable after comparative
  data exists; v1 keeps fixed host policy except retry/failure budgets.

## Decision log

### D1. Command and role names are `/supervise`, watchdog, and verifier

- **Decided:** `/supervisor` keeps its EP-0033 meaning; this workflow is
  `/supervise`, the observer is consistently called watchdog, and final review
  is verifier.
- **Alternatives:** overload `/supervisor`; use watchdog and supervisor
  interchangeably.
- **Why:** distinct nouns prevent two unrelated authority models from becoming
  one ambiguous interface.

### D2. Event by default; live is the high-frequency alternative

- **Decided:** `event` and `live` are the public mode names, with `event` default.
- **Alternatives:** triggered/turn, continuous/follow/stream.
- **Why:** the selected names describe activation without the spelling and
  ambiguity problems identified during design.

### D3. Host state grants authority; models only propose judgments

- **Decided:** every state transition is role checked, versioned, and anchored.
- **Alternatives:** trust system-prompt role separation or structured model JSON.
- **Why:** structured output improves parsing but is not an authorization
  boundary.

### D4. Fresh model instances with bounded handoff

- **Decided:** each event review and completion verification builds a separate
  provider instance; only typed bounded handoff persists.
- **Alternatives:** one long watchdog session with compaction; share worker
  context directly.
- **Why:** freshness reduces context bias and avoids a second unbounded
  compaction lifecycle.

### D5. Native detection, LLM judgment

- **Decided:** the host detects mechanical facts and the watchdog interprets
  ambiguous alignment.
- **Alternatives:** LLM-only monitoring; fully deterministic stopping rules.
- **Why:** models should not spend tokens rediscovering exact repeated failures,
  while semantic scope and recovery still require judgment.

### D6. One active task uses the existing task store

- **Decided:** unrelated follow-ups become shared tasks after watchdog routing;
  no second project-management database is introduced.
- **Alternatives:** inject everything, discard it, or build milestones/issues in
  a new subsystem.
- **Why:** the existing CRUD store is sufficient for safe deferral; richer
  product management can follow observed need.

### D7. Plan autonomy is independent from scrutiny

- **Decided:** profiles may increase review budget/frequency without widening
  pivot authority. Watchdogs can optionally approve plan pivots only.
- **Alternatives:** make high assurance imply watchdog autonomy; require user
  approval for every tactic.
- **Why:** quality controls and authority are separate policy axes.

### D8. Completion belongs to a fresh verifier

- **Decided:** the worker requests, deterministic gates run, and an independent
  verifier alone accepts.
- **Alternatives:** watchdog approval, worker self-attestation, or command-pass
  alone.
- **Why:** the observer correcting execution and the reviewer judging final
  evidence should not share context bias.

### D9. No nested supervision in v1

- **Decided:** subworkers remain under root-worker accountability and are
  observable as one session tree.
- **Alternatives:** recursively supervised children.
- **Why:** recursive budgets, interruptions, and authority inheritance would
  multiply complexity before the root contract is proven.

## Related

- [Supervised-work feature guide](../features/supervise.md)
- [Comparative evaluation kit](../../evals/supervise/README.md)
