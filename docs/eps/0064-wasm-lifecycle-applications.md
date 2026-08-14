---
ep: 64
title: WASM Lifecycle Applications
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-14
requires: ["EP-0002", "EP-0038", "EP-0050", "EP-0051", "EP-0055", "EP-0056", "EP-0057", "EP-0059", "EP-0063"]
extends: ["EP-0051", "EP-0062"]
extended-by: ["EP-0066", "EP-0067"]
see-also: ["EP-0017", "EP-0033", "EP-0036", "EP-0046", "EP-0060"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Attenuated child provider/model/thinking/effort selection behind agent:spawn:configure in addition to the operation-scoped spawn grant; provider credentials remain native.
  - date: 2026-08-14
    status: Accepted
    note: Defined automatic compacted-child recovery as a crash-safe broker handoff of the whole existing application scope; manual forks never copy or inherit it.
  - date: 2026-08-14
    status: Accepted
    note: Required native worker-run lookup, activation, and operator cancellation to authenticate the session controller at the broker instead of relying on operations hidden from the WASM import table.
  - date: 2026-08-14
    status: Accepted
    note: Added independently bounded signed command timeouts so generic interactive application setup can use ordinary UI imports without weakening hook, event, or tick deadlines.
  - date: 2026-08-14
    status: Accepted
    note: Clarified the EP-62 extension: contract choice and UI callbacks are quality-workflow input rather than security authority; durable operator-input routing replaces marker-based shared-task dual authority; advisory risk consumes generic tool identity/class/outcome rather than command-text parsing.
  - date: 2026-08-14
    status: Accepted
    note: Fixed the v1 supported surface to the interactive TUI; partial run, headless, ACP, legacy-background, and recursive AgentLoop composition fail closed.
  - date: 2026-08-14
    status: Accepted
    note: EP-0067 refines contract activation into exact application-local candidate selection because supervise is a quality workflow, not an artifact authority grant.
  - date: 2026-08-14
    status: Accepted
    note: Clarified that supervise is an official signed plugin sourced and released from foobarto/stado-plugins, not in-tree bundled application source.
  - date: 2026-08-14
    status: Accepted
    note: Accepted after re-evaluating supervise against the complete EP architecture.
  - date: 2026-08-14
    status: Draft
    note: Initial draft defining persistent WASM applications and the supervise migration.
---

> **Relationships:** **Requires:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0050](./0050-broker.md), [EP-0051](./0051-lua-lifecycle-hook-contract.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md) · **Extends:** [EP-0051](./0051-lua-lifecycle-hook-contract.md), [EP-0062](./0062-harness-enforced-supervised-work.md) · **Extended by:** [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md), [EP-0067](./0067-session-controller-and-application-selection.md) · **See also:** [EP-0017](./0017-tool-surface-policy-and-plugin-approval-ui.md), [EP-0033](./0033-responsive-supervisor-worker-lanes.md), [EP-0036](./0036-loop-monitor-schedule.md), [EP-0046](./0046-verify-work-phase.md), [EP-0060](./0060-native-harness-guidance.md)

# EP-0064: WASM Lifecycle Applications

## Problem

Stado's plugin ABI can expose tools, poll session fields/events, invoke a model,
spawn agents, and run a once-per-turn background tick. Its lifecycle hooks,
durable artifact services, broker control plane, and trusted session identity
are not available as one coherent WASM application surface.

PR #257 consequently implemented `/supervise` as a large native TUI/service
application with thin model-facing boundaries. That preserves the intended
quality semantics but reverses EP-2, EP-37, and EP-38: native stado should expose
capabilities that WASM cannot obtain directly from the operating system or
runtime, while plugins may be large applications that own product policy.

The missing primitive is not a native supervision framework. It is a persistent,
authenticated WASM lifecycle application that can participate in hooks, use the
broker, communicate with retained agents, and ask the host to apply narrow
scheduling decisions.

This preserves both reasons for the plugin boundary. Supervise application code
runs inside the same explicit capability sandbox as other plugins, and its
fast-moving workflow remains out of the native core so alternative supervision
strategies can evolve in plugin space. Supervise being a quality gate rather
than a security feature describes what it can establish about worker output; it
does not make the supervise implementation exempt from plugin containment.

This EP replaces only EP-62's implementation-placement clauses: its native
workflow/service/tool prescription is superseded by the WASM application and
generic host primitives below. EP-62's product contract, quality semantics,
watchdog stop authority, and evidence rules remain normative.

## Goals

- Let signed WASM plugins declare lifecycle subscriptions in their manifests.
- Reuse one serialized per-session plugin instance across tools, hooks, broker
  events, and ticks.
- Expose authenticated, typed broker primitives for artifacts, session journal,
  mailboxes, retained agents, budgets, and scheduling holds.
- Reuse the existing capability-gated operator UI imports for application-owned
  setup, choices, progress text, and structured reports.
- Keep native code responsible for policy ceilings, execution, durable ordering,
  authority grants, and deterministic facts, not application workflows.
- Keep the native core small while allowing supervision applications to evolve,
  compete, and compose inside the ordinary WASM capability sandbox.
- Move `/supervise` contract, detector policy, reviewer prompts, verdict policy,
  handoff, and workflow state into an official signed WASM application.
- Preserve EP-62's quality-gate semantics, including a watchdog's ability to
  pause or stop the worker and the asymmetric handling of stale verdicts.

## Non-goals

- Making supervision, lifecycle plugins, watchdogs, or verifiers a security
  boundary. Broker, sandbox, capabilities, repo trust, and operator grants remain
  the security ceiling.
- Letting a plugin forge lifecycle facts, session identity, authority, message
  routing, child status, or an operator decision.
- Giving every lifecycle callback an unlimited synchronous timeout.
- Replacing Lua hooks. Lua remains the compact operator-authored policy surface;
  WASM adds distributable applications under the same point semantics.
- Treating mailbox messages as control authority or allowing mailbox exhaustion
  to block cancel/down processing.

## Design

### Persistent application instance

A manifest opts into application mode with lifecycle declarations:

```json
{
  "lifecycle": {
    "points": ["pre_llm", "post_llm", "pre_tool", "post_tool", "post_turn"],
    "events": ["session.turn_committed", "agent.progress", "agent.down",
               "mailbox.available", "budget.threshold", "timer.due"],
    "failure": "open|closed",
    "timeout_ms": 5000
  }
}
```

The host creates one application instance for each effective
`(plugin identity, session identity, session generation)`. A single mutex and
bounded queue serialize tool exports, lifecycle callbacks, durable event
callbacks, ticks, and close. WASM is never re-entered concurrently. Instance
memory is an optimization only; authoritative state belongs in broker services
and recovery must work after a fresh instance starts.

The instance exports the normal allocator/free ABI plus:

```text
stado_plugin_lifecycle(input_json, result_buffer)
stado_plugin_event(input_json, result_buffer)
stado_plugin_tick() -> i32
```

The payload names its schema version, point/event, authenticated session anchor,
sequence, immutable evidence references, and point-specific bounded data. The
host selects the point; guest input cannot invoke a callback or change identity.
Unknown response fields and unsupported decisions fail validation.

`stado_plugin_tick` remains a yield mechanism for bounded background work. It is
not the delivery guarantee. Declared durable events are scheduled by the host
from broker sequence and acknowledged only after the application callback
returns a valid result or the configured failure policy records an outcome.

### Lifecycle hook composition

WASM callbacks implement the same five point semantics and narrow mutable fields
as EP-51. Lua and WASM participants share one deterministic operator-visible
order. The initial order is configured Lua hooks followed by enabled WASM
applications sorted by canonical identity, unless operator config explicitly
orders named participants. The first deny short-circuits; validated mutations
thread forward. Every decision names the participant identity and is audited.

Build/load failure is visible and follows admission policy. Runtime timeout,
trap, malformed output, or unavailable broker follows the manifest declaration
intersected with stricter operator policy. A plugin cannot choose fail-open when
the operator requires fail-closed. Post-action denial still cannot undo an
effect.

Lifecycle participation is a trusted plugin capability, not an ambient feature:

```text
lifecycle:observe:<point-or-event>
lifecycle:decide:<point>
```

Project-local config cannot enable or widen it under EP-44. Installed plugins
require the ordinary trust/pin/opt-in flow; an official package is not
automatically enabled for every session.

### Operator UI bridge

Lifecycle applications use the existing generic operator UI imports rather
than application-specific native widgets:

```text
stado_ui_choose   # capability: ui:choice
stado_ui_print    # capability: ui:print
stado_ui_render   # capability: ui:render
```

`stado_ui_choose` is the ABI function name; `ui:choice` is the manifest
capability. It returns an operator choice or a structured unavailable/cancelled
result. `stado_ui_print` emits bounded plain text, while `stado_ui_render` emits
bounded structured panels. Print and render remain non-blocking transport;
choose is the explicit interactive operation. Their wire contracts remain in
[the host-import reference](../plugins/host-imports.md#stado_ui_choose).

These imports are host-native because WASM cannot draw on or receive input from
stado's operator surface directly. The wizard, wording, sections, defaults,
validation rules, and interpretation of a choice remain plugin application
logic. For supervise, that means the plugin owns contract setup and the
operator-facing narrative while the host only transports sanitized UI data and
binds callbacks to the exact application/session workflow. The callback does
not prove a fresh operator gesture and cannot grant security authority.

EP-17 standardized `ui:approval` and records the later `ui:choice` demo, but no
accepted EP previously specified `stado_ui_choose`, `stado_ui_print`, and
`stado_ui_render` together as the generic UI bridge available to WASM
applications. This EP records that existing runtime contract; it does not add a
supervise-specific UI primitive. A render or choice is user interaction, not a
security or quality decision by itself.

### Broker bridge

Application imports are narrow typed requests over one authenticated bridge,
not file handles or raw WAL access. In addition to EP-63 artifacts, the surface
provides:

```text
stado_session_journal_append
stado_session_projection_read
stado_session_hold_acquire
stado_session_hold_release
stado_session_request_pause
stado_session_request_stop
stado_session_complete
stado_session_input_claim
stado_session_input_route
stado_session_worker_request
stado_session_worker_resume
stado_session_worker_cancel
stado_agent_spawn / list / read_messages / send_message / cancel
stado_agent_monitor / demonitor
stado_mailbox_send / receive / ack
stado_timer_schedule / cancel
```

Existing agent imports may retain names while their single bundled-only
`agent:fleet` gate is split into operation-scoped capabilities. User plugins may
receive those capabilities through policy; official identity is not itself an
authorization rule. All child admission still goes through EP-55 attenuation,
budget reservation, immutable fork resolution, and the normal executor.
Plain `agent:spawn` inherits the parent provider/reasoning profile. Any signed
provider, model, thinking mode/token budget, or reasoning-effort override also
requires the non-standalone `agent:spawn:configure` attenuation; it exposes no
credential or operation by itself, and native resolution rejects unsupported
forced controls or silent model substitution.

The broker independently verifies the installed package, signature, lock
identity, declared capabilities, active session/generation, ceiling, and policy
before minting an opaque application binding. The WASM guest never sees or
chooses it. The host bridge transports guest payloads under that binding; the
broker resolves plugin identity, manifest/schema digests, principal,
repository, ancestry, ceiling, and idempotency namespace from broker state and
ignores authority-shaped guest fields. A binding is scoped permission, not an
attestation that a potentially compromised orchestrator authored particular
prose. The bridge returns typed conflicts, backpressure, policy denials, and
retry hints and never falls back to `wal.OpenShared` or `cfg:state_dir`.

Session journal entries are namespaced plugin application data and cite broker
facts; they cannot manufacture host event kinds. Projections expose bounded
EP-57/59 chronology and current facts. Durable application data that should
outlive one run or be reusable belongs in EP-63 artifacts instead.

### Scheduling authority

A lifecycle application may request a scheduling hold only with a scoped
`session:schedule` capability and only for the session to which its instance is
attached. The broker persists the hold before acknowledging it. A hold blocks
new provider turns and tool dispatch at the shared agent-loop/executor seams;
cancel, down, operator control, and broker maintenance remain deliverable.

Holds are leased, reason-coded, owned by plugin identity and run ID, visible to
the operator, bounded by policy, and recoverable after restart. Release uses
owner/run/version CAS. The operator may always override or cancel. Pause and stop
requests are proposals evaluated by the host against current attachment and
policy; an authorized, current request transitions the session control plane.
They do not arrive as mailbox data or prompt text.

This makes a watchdog able to stop a loop without giving its model arbitrary
process or filesystem control. The authority comes from the operator-enabled
supervise application and broker transition rules, not from watchdog prose.

### Application-owned worker recurrence

A signed application may request a bounded worker recurrence through the exact
`session:worker:request` capability. The durable request is scoped by session,
generation, canonical plugin identity, and application run ID and contains
only an objective, recurrence prompt, and the generic operator-loop conflict
rule. It carries no authority fields. A request is not active merely because
the guest wrote it.

After a successful manifest-declared command callback names the request's run
ID, the native TUI presents both its session-controller capability and that
callback application's opaque binding to a dedicated broker RPC. The
controller authorizes native scheduling; the binding only selects the exact
broker-admitted plugin namespace. The ordinary application-bearer operation
map cannot fetch, activate, or operator-cancel a run. The host starts it once.
At most one application-owned run may be active in a session generation; no
application may replace another application's run. It may replace an operator
`/loop` only when the durable request explicitly selected
`replace_operator_loop`.

The application may CAS-cancel its own requested, resume-requested, or active run through
`session:worker:cancel`. Successful `session:complete` derives the run's
`completed` projection and ends recurrence normally. Before the native
scheduler returns pause or stop, it durably terminalizes the active run as
`interrupted` or `stopped`, including the reason and control WAL sequence. A
restart or rebind therefore cannot mistake a consumed control result for an
active recurrence. The native operator host can always controller-authenticate
a CAS cancellation of a run it activated; this cannot be disabled by omitting
the guest's self-cancel capability. Hiding an operation from the guest import
table is never treated as authority. Worker authority remains below scheduling
holds, token budget, context, sandbox, and normal provider/tool dispatch gates.

An interrupted run may be continued without minting a replacement identity.
With `session:worker:resume`, the application CAS-records an exact
`resume_requested` transition for the same session, generation, canonical
plugin namespace, run ID, objective, and prompt. It then returns the distinct
`resume_worker_run_id` field from a successful signed command. The native
command host independently looks up that broker projection and performs a
controller-authenticated resume activation CAS; no callback field supplies
scope or version. A replay returns the same transition, a stale version fails
closed, and a different local or broker recurrence owner is never overwritten.
Cancelled, stopped, and completed runs cannot resume. A later stop is terminal,
a pause racing after the resume request blocks activation until a fresh exact
request. Any unexpired aggregate hold in the exact session generation blocks
resume activation with a retryable conflict; releasing or settling it permits
an exact activation retry, while an expired hold is ignored. Journal,
operator-input, deferred-task, and application state keep their original run
ownership and WAL order.

### Automatic compacted-child continuation

Automatic context recovery is not an ordinary fork while an application owns
the worker run. Copying worker, hold, journal, cursor, or operator-input records
would create two apparent authorities and lose their single WAL order. Instead,
the source session controller reserves the exact child at an exact source
anchor. The native session layer materializes that child and stages the existing
opaque recovery credential in the child's strict credential store before asking
the broker to commit.

The broker verifies the reserved child and its ancestry, then atomically changes
the logical-session subject of the existing application scope. The broker
session ID, generation, recovery bearer, application WAL keys, and ordered state
remain the same; the live controller and every opaque application/artifact
binding rotate. The source subject is fenced and cannot be adopted again. A
crash before broker commit leaves the source authoritative. A crash after commit
finds the already-staged child credential and can adopt the child without a
record-copy or duplicate-owner window.

Manual forks and fork-from-point remain separate sessions with new broker
scopes. They never inherit lifecycle applications, worker ownership, pending
input, holds, or cursors merely because their git tree descends from another
session. Automatic recovery fails closed when the authenticated handoff is
unavailable or its commit outcome cannot be resolved; it never resumes the
possibly fenced source session.

### Worker-watcher communication

EP-56 mailboxes carry watchdog requests, evidence requests, progress, replies,
and steering payloads between retained sessions. Host monitor events carry
child lifecycle. Pause, stop, cancel, down, holds, and grants stay on the broker
control plane. Message payloads never transfer capabilities or approval.

The supervisor application spawns each watchdog/verifier through EP-55 with a
fresh context, bounded role/tool profile, token budget, and immutable worker
anchor. It sends bounded handoff and evidence references through the mailbox and
correlates the reply to that anchor. Generic read/search/follow evidence access
uses session/research primitives; native stado need not register
`supervise__read`, `supervise__search`, or `supervise__follow` tools.

### Supervise as an official application

The official signed `supervise` plugin owns:

- setup UI and baseline schema;
- its EP-63 `supervision-contract` artifact kind;
- baseline/reviewer/verifier prompts and strict response schemas;
- detector thresholds, coalescing, cooldowns, and review cadence;
- plan and completion policy within the exactly selected contract candidate;
- watchdog handoff, retries, correction tracking, follow-up classification, and
  broker-owned input/deferred-task composition;
- event/live modes and optional event-mode `review_every_turns` cadence;
- the narrative rendered to the worker and operator.

Native stado owns only generic facts and authority primitives:

- broker-stamped session/turn/tree/trace anchors and bounded non-thinking transcript;
- tool/audit/diff/budget/verification/child lifecycle projections;
- broker artifact, journal, mailbox, retained-agent, timer, and hold services;
- broker-owned application worker-run request, CAS, projection, and scheduling
  consumption, with recurrence composition in the TUI;
- broker-owned immutable operator-input capture, ordered deferred-task
  projection, and digest-checked exact-session receiver delivery; the guest
  may durably claim an exact input while an asynchronous reviewer runs, but
  owns only the eventual `deliver`/`defer` quality decision. A reviewing input
  remains an aggregate scheduling and completion fence. The host derives task
  status as `open`, `pending_continuation`, or `continued` from the same input,
  completion, and delivery records rather than writing a second task state;
- provider construction and execution through normal policy ceilings;
- sanitized operator UI transport, without treating an in-process callback as
  fresh operator-origin security proof;
- shared agent-loop/executor enforcement of pause, stop, cancel, and holds.

The selected contract is one exact session-scoped candidate EP-63 artifact.
Selection is application-local configuration: it does not activate the
artifact, grant authority, or prove operator origin. Changing objective,
constraints, criteria, definition of done, permissions, or quality-workflow
confirmation boundaries creates a new candidate version. Rapid run state such
as current step, evidence observations, triggers, retries, pending reviews,
holds, and inbox cursors stays in the session journal rather than creating
artifact versions.

EP-64 replaces two EP-62 implementation assumptions while preserving its
product behavior:

- ordinary input captured during an application-owned worker run is an
  immutable broker record targeted to that exact run. The application chooses
  `deliver` or `defer`; it may first make an exact idempotent `reviewing` claim
  so the mandatory input event can be acknowledged before the correlated
  asynchronous child result arrives. That application-authored review ID is
  crash-safe correlation, never agent identity or authority. Queued,
  reviewing, and ready inputs all block completion and remain recoverable;
  reviewing also fences aggregate provider/tool scheduling until exact route
  or terminal recovery. Deferral creates the broker projection of the same
  record, not a second `tasks.json` authority or marker-text deduplication path.
  Successful completion names the exact ordered deferred set for continuation.
  Native recovery also projects open deferred-task count and bounded titles so
  cancellation or a temporarily unadmittable owner cannot make durable work
  operator-invisible; this projection is read-only and grants no task authority.
- the application may apply conservative quality policy to host-published
  signed tool identity, mutation class, and outcome. It does not recognize
  security approvals by parsing shell or `gh` text. Security-significant tool
  authorization remains the generic host/broker tool policy; UI confirmation
  inside the application is only a workflow decision.

The application exports operator commands and model-facing tools declared in
its signed manifest. Stado's registry and operator-surface projection remain
generic; no native `ToolDef` or Go dispatcher may implement supervise policy.

### Review cadence and stale verdicts

`event` remains asynchronous by default and may add a configured periodic
review every N completed worker turns. `live` may be configured as a strict turn
barrier for tasks that prefer current review over throughput: the application
acquires a hold at the turn boundary and releases it only after a current-anchor
verdict. This is a quality/latency choice, not a stronger security sandbox.

Asynchronous stale responses have three classes:

- stale `continue`/`approve`: discard and schedule another review only when
  current policy still calls for one;
- stale `correct`: deliver bounded, explicitly earlier-anchor advisory steering
  without treating it as current authority or pausing first;
- stale `pause`/`stop`: acquire a durable scheduling hold and run a fresh review
  at the latest anchor. Only that current verdict may release, correct, pause,
  or stop.

This keeps harmless steering responsive while preventing an obsolete stop from
controlling a different worker state and preventing the worker from outrunning a
potentially still-valid intervention.

Provider cleanup happens after a successfully parsed verdict and cannot replace
its authority result. Token caps include provider-declared output/thinking
headroom. Supervise v1 exposes token budgets only; cost metadata may be observed
but is not an enforcement input.

Generic agent terminal results expose host-collected input/output/cache token
counters plus an explicit completeness bit. Provider cleanup is a separate,
fingerprinted diagnostic; raw cleanup text is not application input. A child
spawn can consume the exact broker-stamped `turn_ref` emitted in
`session.turn_committed`. The host derives and ancestry-checks its logical
session, pins the immutable tree before asynchronous admission returns, and
starts the independent reviewer with a fresh conversation. It may not silently
substitute `turns/N` or a later current tip.

Operator commands default to the lifecycle callback timeout. A signed
`commands[].timeout_ms` may extend one serialized command call to at most 15
minutes when a bounded interactive workflow needs several generic UI bridge
round trips. The override changes only cancellation time: it grants no UI,
artifact, scheduling, or model capability, and lifecycle points, durable event
callbacks, and ticks retain their separate 60-second maximum.

### Supported host surface for v1

The interactive TUI is the only lifecycle-application host for v0.80 and v1.
It owns one persistent instance for the exact canonical
`(plugin, session, generation)` identity and routes that instance's commands,
model tools, lifecycle hooks, durable events, ticks, close, and required generic
bridges together.

`stado run`, `stado run --headless`, and `stado acp` reject a configured
lifecycle application before provider or session work. The diagnostic names the
canonical application identity and directs the operator to the TUI. A persona's
additive plugin declarations participate in the same check. The generic
`AgentLoop` never discovers or instantiates lifecycle applications itself; in
particular, retained reviewer/verifier children cannot recursively load their
parent's application.

Legacy `BackgroundPlugin` loading skips lifecycle manifests, and ephemeral
`plugin.run` rejects them. Merely being able to instantiate WASM or call
`AgentLoop` does not make a surface application-capable. A future surface must
first own the complete persistent composition contract above; partial command,
tool, hook, or event support remains a hard error rather than degraded
execution.

## Migration / rollout

1. Add canonical plugin runtime identity and the persistent application
   dispatcher with lifecycle declarations and serialization.
2. Route WASM lifecycle participants through the EP-51 runner on all supported
   agent-loop/executor surfaces; make missing surface wiring explicit in tests.
3. Add authenticated broker IPC for EP-63 artifacts, session projections,
   journal, mailboxes, retained-agent monitoring, timers, and scheduling holds.
4. Split the bundled-only `agent:fleet` capability into granular policy grants
   and port the bundled agent plugin.
5. Build and release the official supervise WASM plugin from
   `github.com/foobarto/stado-plugins`, then migrate PR #257 state into one
   activated contract artifact plus session journal events.
6. Remove native supervise model tools, native workflow dispatch, and all direct
   TUI/plugin WAL opens. Native fact collectors may remain after their output is
   exposed generically.
7. Keep EP-62 eval scenarios as behavior conformance tests and add strict-live,
   periodic-event, stale-steer, stale-stop-confirmation, restart, and plugin-trap
   cases.

Until this migration is complete, EP-62 remains Accepted rather than
Implemented and v0.80.0 must not be published.

## Failure modes

- Application load/manifest failure: surface before supervised work starts;
  ordinary sessions remain available unless operator policy requires the app.
- Callback timeout/trap: apply bounded failure posture, record it, and avoid
  re-entering the instance until recovery/restart policy decides.
- Broker unavailable: authority-bearing operations fail closed; no file/WAL
  fallback. Existing holds remain effective until operator override or lease
  policy resolves them.
- Durable event callback crashes before ack: redeliver with the same event and
  idempotency namespace; application requests must tolerate repeats.
- Application queue backpressure: ordinary observation may coalesce only where
  the event contract permits; cancel/down/hold/control events use the independent
  broker plane and are never dropped behind data.
- Stale instance generation: every mutation is rejected by authenticated
  generation/sequence checks.
- Plugin removed while it owns a hold: operator-visible lease/recovery policy
  releases or transfers it; silent indefinite wedging is forbidden.
- Watchdog/verifier failure: follow EP-62 required/advisory posture; never infer
  approval or completion from absence.

## Test strategy

- ABI and manifest tests for lifecycle declarations, canonical identity,
  capabilities, payload bounds, and response validation.
- Deterministic ordering tests across Lua and multiple WASM participants.
- Concurrency/race tests proving one instance is never re-entered across tool,
  hook, event, tick, and close.
- Crash/redelivery/idempotency tests at every event and broker transition.
- Cross-session/plugin generation, scope, capability, and authority adversarial
  tests.
- Hold tests across provider turns, tool dispatch, operator override, cancel,
  plugin crash/removal, process restart, and loop stop.
- Surface contract tests proving exact-one TUI composition and fail-closed
  rejection by run, headless, ACP, legacy background loading, ephemeral
  `plugin.run`, and recursively spawned children.
- Supervise paired evals and state-machine tests run against the official WASM
  application, with a guard that no native supervise tool is model-visible.

## Decision log

### D1. Supervise is a WASM application, not a native application

- **Decided:** native stado supplies missing primitives; official signed WASM owns the
  supervision workflow and policy.
- **Alternatives:** native state machine with WASM façades; split detector/policy
  arbitrarily between Go and WASM.
- **Why:** plugins may be whole applications. Their WASM sandbox makes host
  access explicit, and keeping fast-moving product workflows in plugin space
  keeps the core coherent while allowing innovation. Native code is justified
  where the WASM garden lacks OS/runtime authority, not merely because logic is
  complex.

### D2. Lifecycle callbacks and tools share one serialized instance

- **Decided:** one per-session application instance serves every entry point.
- **Alternatives:** fresh module per tool/callback; concurrent module entry; one
  process-global instance.
- **Why:** applications need coherent bounded local state, WASM is not reentrant,
  and session/generation isolation must remain explicit.

### D3. Broker operations are authenticated typed imports

- **Decided:** plugins submit requests through capability-gated bridges to the
  broker single writer.
- **Alternatives:** raw WAL access; state-dir files; TUI-owned services.
- **Why:** only the host can bind principal, session, ancestry, identity,
  generation, and authority without trusting guest JSON.

### D4. Mailbox data and control authority stay separate

- **Decided:** EP-56 carries work communication; holds, pause, stop, cancel, and
  down remain broker control events.
- **Alternatives:** encode control in messages; share an in-memory channel.
- **Why:** messages are untrusted content and may backpressure; control must be
  broker-authoritative, capability-checked, durable, and independently
  deliverable.

### D5. Stale verdicts are asymmetric

- **Decided:** discard stale positives, deliver stale steering as advisory, and
  confirm stale pause/stop behind a durable hold.
- **Alternatives:** discard all stale output; apply all stale output; pause for
  every stale response.
- **Why:** the three outcomes carry different risk. The split minimizes pauses
  without letting obsolete intervention authority act on new state.

### D6. The v1 lifecycle-application surface is TUI-only

- **Decided:** only the interactive TUI hosts applications in v0.80/v1. Run,
  headless, and ACP reject configured applications until they own the whole
  persistent composition contract.
- **Alternatives:** let `AgentLoop` auto-load applications; allow commands or
  tools without the remaining callbacks and bridges; reuse legacy background
  instances.
- **Why:** all entry points must share one admitted, serialized instance.
  Partial support silently forks workflow state and recursively loads the
  supervisor into its own reviewers, which is worse than an explicit
  unsupported-surface error.

## Related

- [Lifecycle hooks](../features/lifecycle-hooks.md)
- [Supervised work](../features/supervise.md)
- [The Loop Needs a Witness](../articles/supervise-in-practice.md)
