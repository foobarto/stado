# Platform cleanup ledger — 2026-08-14

This is the durable working index for the architecture cleanup discovered while
finishing PR #257. It is not itself an EP and cannot override an accepted EP.
Normative decisions link to their owning EP; open interpretations and
inconsistencies are labelled as such so a future session does not mistake an
assistant's working conclusion for an operator decision.

The historical reason for treating drift seriously is recorded in
[the 2026-05-05 architectural reset](./2026-05-05-architectural-reset.md). A
long AI-assisted implementation and review cycle produced native
implementations behind plugin-shaped wrappers. EP-37 and EP-38 were the
correction, not merely a preference for one implementation language.

## Accepted decisions in this cleanup

- This is a strong pre-v1 corrective migration. Drive directly toward the
  accepted architecture rather than preserving drift-shaped interfaces. Remove
  obsolete imports, capabilities, wrappers, config, native application paths,
  and fallback writers instead of aliasing or deprecating them indefinitely.
  Preserve authoritative user data with explicit, auditable, one-way migration;
  that is data integrity, not API backward compatibility.

- [EP-63](../0063-plugin-defined-harness-artifacts.md) extends EP-53 with
  plugin-defined, schema-validated artifact `.data`, stable canonical kind
  namespaces, archived descriptors, and authenticated `stado_artifact_*`
  imports. It replaces the ambient `stado_memory_*` bridge.
- [EP-64](../0064-wasm-lifecycle-applications.md) extends EP-51 and EP-62 with
  persistent serialized WASM applications, lifecycle/event subscriptions,
  typed broker bridges, retained-agent/mailbox integration, scheduling holds,
  and the supervise migration.
- [EP-66](../0066-canonical-plugin-authority-and-application-placement.md)
  makes canonical runtime identity authoritative across secrets, state,
  artifacts, lifecycle, broker bindings, and audit attribution; it also marks
  every stado-owned native model application—including EP-60 guidance—as
  migration debt rather than a permanent exception.
- [EP-67](../0067-session-controller-and-application-selection.md) separates
  session-controller possession, exact application-local quality configuration,
  and security-significant artifact activation. A controller token or
  in-process UI callback is not operator-origin proof.
- Supervise is a quality gate, not a security boundary. A watchdog may still
  pause or stop the loop through host-enforced scheduling transitions.
- V1 budget enforcement uses token ceilings only across Stado and supervise.
  Provider-reported currency remains observational telemetry, not authority.
- Stale verdicts are asymmetric: discard stale approval, deliver stale
  correction as anchored advisory steering, and hold plus re-review a stale
  pause/stop before applying authority to current work.
- The existing `stado_ui_choose` (`ui:choice`), `stado_ui_print` (`ui:print`),
  and `stado_ui_render` (`ui:render`) imports are the generic operator UI bridge
  for WASM applications. The plugin owns wording and workflow; the host owns
  transport, sanitization, availability, and UI delivery. Durable authority
  requires broker policy or proof from a separately trusted presenter.
- Linux is the only supported platform target for the current project and for
  v1. macOS and Windows are not roadmap goals. Existing conditional code may be
  removed as part of cleanup and must not constrain the Linux architecture;
  its mere presence is not a compatibility promise.

## EP-2 stress test — working interpretation

**Question:** is “all tools as WASM plugins” too strict for a feature as large
and loop-integrated as supervise?

**Working conclusion:** do not weaken EP-2 for supervise. Clarify its scope and
pair it with EP-64 instead. EP-2 directly governs every model-facing tool; it
does not say that every line of native Go is forbidden. EP-37 and EP-38 supply
the broader split: stado provides generic capabilities and trusted execution
seams, while plugin authors own application policy. EP-64 deliberately applies
that split to lifecycle applications.

The reasons for the boundary, in order, are:

1. WASM gives plugins a deliberately sandboxed execution environment whose
   access to the host is limited to explicit capability-gated imports.
2. Keeping applications in plugin space keeps stado's core small and coherent,
   while allowing experimentation and innovation to proceed without repeatedly
   expanding the native product kernel.

Uniform dispatch, replacement, attribution, and auditing follow from those two
reasons. They are useful consequences, not substitutes for the original
rationale.

The strongest argument for a native supervise implementation is engineering
cohesion. It touches provider turns, tool dispatch, session identity, durable
ordering, child agents, budgets, and operator control. Native code avoids ABI
round trips, serialization, plugin crash recovery, and a larger broker surface.
It can be easier to debug and may be faster.

That is not enough to make supervise policy native. The same convenience
argument can exempt every sufficiently ambitious plugin. A native supervise
application would leave the plugin sandbox, gain implicit access to authority
and storage, expand and clutter the core with fast-moving application logic,
become harder to replace, introduce a second application model, and tempt
reviewers to move policy across the boundary one helper at a time. The
2026-05-05 reset is direct evidence that this failure mode is not hypothetical.

Supervise being a quality gate rather than a security feature does not change
where its implementation runs. The quality/security statement limits what
supervise claims about worker behavior. The WASM sandbox limits what supervise
plugin code itself can do to the host. Those are independent boundaries.

Use this review test:

1. If the code observes a broker-verified or kernel-observed fact, executes an OS/runtime action,
   serializes an authoritative transition, or enforces a ceiling that WASM
   cannot enforce on itself, it belongs in the host or broker.
2. If two plugins could reasonably make different choices from the same facts,
   it is application policy and belongs in the plugin.
3. A new native import should describe a generic capability independently of
   supervise. If its name or semantics encode a watchdog workflow, detector,
   verdict, or supervise state machine, the boundary is probably wrong.
4. The host may provide a protected input and validate a narrow output without
   owning the decision between them. Lifecycle callbacks are this shape.
5. Complexity and a fast-moving design space are reasons to make the sandboxed
   plugin application runtime adequate, not automatic exceptions from it.

Applied to supervise, native stado owns event ordering, authenticated anchors,
broker writes, scope checks, budgets, agent execution, mailbox delivery,
scheduling holds, pause/stop/cancel enforcement, and operator UI transport. The
WASM application owns contract wording, setup flow, detector and cadence policy,
evidence selection, prompts and schemas, reviewer/verifier orchestration, stale
verdict interpretation, correction policy, and model-facing tools.

An exception can still be proposed, but it should require a new accepted EP
that names the non-generic native behavior, explains why a generic primitive is
insufficient, and preserves a replaceable plugin surface. Implementation
convenience alone is not that case.

## Inconsistency register

| ID | Inconsistency | Current disposition |
|---|---|---|
| C01 | EP-62 claimed Implemented before PR #257 or v0.80.0 existed. | Corrected to Accepted; EP-64 extends it. |
| C02 | EP-30 claimed Implemented while stated harness-security work remains incomplete. | Corrected to Partial. |
| C03 | EP-33 claimed Implemented while responsive lane work remains incomplete. | Corrected to Partial. |
| C04 | EP-53 and the runtime hardcode memory/lesson fields and `stado_memory_*`. | EP-63 accepted; generic artifact implementation in progress. |
| C05 | Plugin runner and TUI open the legacy memory JSONL directly. | Must be removed; broker-only, fail-closed artifact bridge in progress. |
| C06 | PR #257 implements six model-facing supervise tools in native Go. | Must move to the official signed WASM application under EP-2/38/64/66 before merge. |
| C07 | PR #257 opens the broker WAL directly from the TUI/supervise service. | Must move behind authenticated broker IPC under EP-50/59/64. |
| C08 | Lifecycle hooks are usable from Lua but not signed WASM applications. | EP-64 manifest/dispatcher implementation in progress. |
| C09 | `agent:fleet` was bundled-only and granted the whole agent surface. | Corrected: exact `agent:{spawn,list,read,send,cancel}` capabilities now gate each operation, the aggregate grants nothing, and retained work pins and verifies the admitted source before launch. |
| C10 | EP-38 says the no-native-tools migration is Implemented while documented Steps 8–10 and native carve-outs remain. | Corrected EP-2/38 to Partial; EP-66 and the shrinking native-surface guard own closure. |
| C11 | EP-39 D7 retains global active-symlink wording reversed by its later D12. | Corrected with a history entry and inline amendment that makes D12's canonical identity layout and project/per-user selection authoritative without erasing D7. |
| C12 | EP-44 retains stale claims about project MCP configuration in test/phase prose. | Corrected: the history, phase prose, and test strategy now agree that `mcp.servers` is always stripped and no project TOFU store shipped. |
| C13 | EP-45 refers to an EP-44 project TOFU gate that did not ship. | Corrected: project-skill `allowed-tools` is unconditionally inert; any future authority path needs a new accepted decision. |
| C14 | No accepted EP previously owned all of `stado_ui_choose`, `stado_ui_print`, and `stado_ui_render`; code comments point to temporary F9/F10 specs. | Corrected: EP-64 and the host-import reference own the UI bridge; production runtime comments cite them and an architecture guard rejects temporary slice labels. |
| C15 | DESIGN/PLAN and several older EP passages still describe macOS and Windows runners as product targets. | Corrected: EP-65 owns Linux-only current/v1 scope and explicitly extends the surviving EP-28/37/38 decisions; current docs, release metadata, installer, self-update, bundled fetches, and platform files are Linux-only. Older body text remains labelled decision history, not a live support promise. |
| C16 | Display alias `Manifest.Name` still namespaces secrets/state and several production paths synthesize local identity. | Corrected for executable paths: loader-supplied `RuntimeIdentity` now scopes secrets/state/audit across installed, bundled, override, TUI, headless, background, and CLI execution. Source-bound local identity is explicitly unstable. Test-only compatibility helpers and the fabricated native learn-kind owner are frozen shrinking debt; the latter closes with C04/native-learn migration. |
| C17 | EP-28 was both Partial and wholesale superseded by EP-38 despite surviving HOME/XDG decisions. | Removed the over-broad reciprocal supersession; EP-38 remains a scoped historical amendment. |
| C18 | EP-37 permissive defaults conflict with later broker/sandbox-first EP-50. | Added reciprocal scoped extension: EP-50 replaces default containment posture, not the plugin/application boundary. |
| C19 | Application/artifact bind, native event publication, and schedule projection accepted a raw session ID from any same-UID broker client. | Corrected with a hash-retained broker session-controller capability across bind/event/schedule/terminate/taint/child operations and exclusive lifecycle admission. It proves session control, not operator intent. |
| C20 | EP-50 language and new lifecycle prose risked calling a hostile-orchestrator-supplied context or TUI approval “unforgeable.” | Broker admission bindings authenticate broker-verified facts and ceilings to a guest, but an in-process TUI cannot prove a fresh operator gesture against a compromised orchestrator. Security authority needs predeclared policy or a separately trusted presenter; ordinary supervise setup is explicitly a quality workflow. |
| C21 | Durable application events initially exposed internal routing fields and allowed acknowledgement outside the binding's pending subscribed set. | RPC results now project only the public event shape and acknowledgements advance only over broker-verified pending subscriptions, while preserving idempotent retry. |
| C22 | EP-64 initially coupled supervise startup to an active artifact and therefore invited a false in-process “operator grant.” | EP-67 makes supervise contract choice exact application-local candidate selection with no authority transition; real artifact activation remains withheld pending broker policy or a separately trusted presenter. |
| C23 | Lifecycle applications bind at TUI boot but are not atomically rebound after session switch, fork recovery, or `/reload`. | Corrected: the TUI owns one root controller and an exact per-session peer, stages application composition transactionally, rolls back failed transitions, rotates peers on session changes, and reloads without duplicating the session binding. |
| C24 | The application API has pause/stop/holds but no successful-completion handoff. | Corrected: `session:complete` records an idempotent broker-owned completion, projects it into scheduler precedence, ends the worker loop successfully, and lets the application release its exact hold. |
| C25 | No production host publishes an exact-parent `agent.down` event. | Corrected: child terminal facts are durably published through a leased event context only to the authenticated parent session/generation, including bounded scope/change and C29 terminal metadata; switching sessions cannot relabel or drop an admitted child's event. This replaces the unsafe native PR #257 forwarding path. |
| C26 | ACP has no fleet, headless can compose legacy and lifecycle paths twice, and non-TUI surfaces do not uniformly route application commands. | Corrected for v0.80/v1: TUI is the sole supported lifecycle-application surface and owns exactly one persistent instance per canonical identity/session/generation. Run, headless, ACP, ephemeral plugin execution, and legacy background loading fail closed before partial/duplicate composition and name the configured application in an actionable diagnostic. |
| C27 | Native `/supervise` owns worker recurrence, but generic application commands cannot claim/start/finish a loop. | Corrected: signed applications can request/cancel a bounded durable `WorkerRun`; only a successful declared command may hand its exact run ID to the TUI for native CAS activation. One application owns recurrence, explicit policy may replace only an operator loop, stale ticks are generation-fenced, and completion/pause/stop/cancel terminalize durably below normal enforcement. |
| C28 | EP-62 followup routing and session continuation remained native-only. | Corrected: the broker now captures immutable 48 KiB-bounded input against the exact active application run, delivers a targeted mandatory-action event, accepts only `deliver`/`defer` through `session:input:route`, and derives one canonical deferred-task projection (`open` → `pending_continuation` → `continued`) without legacy task writes or marker scans. Completion validates the exact ordered deferred set and digest-checked idempotent receiver delivery commits before recurrence. Oversize/backpressure fail visibly without unsupervised fallthrough; startup, logical-session rebind, worker handoff, and cancellation retain a draft-preserving fail-closed ownership fence; source-specific failures survive reload and cannot clear one another. Terminal recovery preserves all unresolved queued/reviewing/ready originals, while native read-only open-task summaries keep cancelled/orphaned deferred work operator-visible. Capture, routing, and recovery provenance remains quality/audit data rather than security proof. Cross-generation automatic transfer remains explicitly separate C35. |
| C29 | Agent terminal reads omitted host-measured token usage and did not distinguish a valid result from a later cleanup diagnostic. | Corrected: terminal spawn/read results carry token-only host counters with an explicit completeness bit; cleanup is a separate bounded fingerprint and cannot erase valid final text. |
| C30 | A delayed reviewer spawn pinned the then-current parent head rather than the authenticated turn/event source. | Corrected: `source.at` accepts the exact authenticated application `turn_ref`; the host derives/authorizes its session and synchronously pins that immutable tree before fleet admission. Exact reviewer spawns start with fresh prompt context. |
| C31 | Supervise review and verification can outlive a leased hold. | Corrected in the migration application: it renews and durably schedules the exact hold at half-TTL, replays renewal idempotently after restart, and requests a hold-independent fail-closed pause if renewal fails. Official persistence/signing remains C34. |
| C32 | Native code marked every no-tool assistant turn as a completion candidate. | Corrected: `session.turn_committed` contains assistant/tool facts only. Supervise completion begins from the plugin-owned explicit completion tool and durable plugin state. |
| C33 | Native supervise's `gh`/shell risk parser is both incomplete and application policy. | Delete it rather than chasing command spellings. For the initial cutover, the signed tool identity, host registry mutation class, and outcome are the generic typed effect facts; the plugin may conservatively review every `exec` call. It must not infer security authority by parsing command text. A finer host-import effect taxonomy needs its own application-independent case and is not required merely to preserve the old classifier. Security approval remains central tool/broker policy. |
| C34 | Official supervise remains temporary, unsigned, and absent from the installed/default lifecycle path. | Persist source to `foobarto/stado-plugins`, dev-install only under isolated ephemeral trust for integration, and use the real offline signing workflow before release. |
| C35 | An application-owned worker run and its journal/input state were scoped to one exact logical session, while automatic context recovery switched to a compacted child without transferring that authoritative scope. | Corrected with a native-only, two-phase logical-subject handoff for the whole broker application scope. The source controller reserves an exact direct child at an authenticated turn; the native client pre-stages the same no-follow 0600 recovery bearer under the child; and a broker-side canonical-lineage verifier gates the WAL commit. Commit preserves SessionID, generation, and every application WAL key while changing only the logical subject, rotating the controller/version, and invalidating bindings. Crash before commit leaves the source authoritative; exact prior-controller/bearer replay closes the lost-reply window across broker restart; unresolved outcomes fence ordinary input and never revive the source. Manual forks remain independent. The documented same-UID bearer-theft limitation is unchanged. |
| C36 | The first migration plugin accepted only `status` or `start <complete-contract-json>`, while the accepted feature requires an application-owned setup wizard, fresh baseline proposal/quality confirmation, and durable `resume`/`cancel`. | Corrected in the migration application. `/supervise [start] [objective]` durably sequences the bounded wizard, spawns an idempotent fresh read-only baseline architect, strictly validates and renders constraints/non-goals/criteria/ordered plan/DoD/verification/risks, and proposes a candidate plus WorkerRun only after quality confirmation. `status`, exact interrupted-run `resume`, and CAS `cancel` reconstruct from broker projection and never revive stopped/cancelled/completed work. Command/UI provenance grants no security authority; there is no native wizard fallback. Official persistence/signing remains C34. |
| C37 | A full Stado process restart created a new broker session/controller, leaving the prior logical session's durable application binding, worker run, holds, events, cursors, and pending input unreachable even though their WAL records survived. | Corrected: the broker now mints an expiring, parent-controller-bound reservation before first durable session creation; the native layer saves its opaque recovery bearer in a no-follow 0600 credential file before commit. Restart adoption preserves the exact SessionID/generation/application WAL scope, rotates the live controller and bindings, and rejects live, cross-repo, cross-parent, replayed, terminated, or malformed claims. The stable recovery bearer avoids a one-response rotation crash window. This is local-user bearer protection, not authentication against a malicious same-UID process. |
| C38 | Application commands inherit the lifecycle callback's 60-second maximum even when a signed command intentionally invokes the generic interactive UI bridge, forcing a real setup wizard into one opaque JSON field or a sequence of fragile re-invocations. | Corrected: signed `commands[].timeout_ms` defaults to the lifecycle timeout and is independently capped at 15 minutes. It changes only wall-clock cancellation for the serialized command callback; it grants no capability, makes no surface interactive, and leaves hook/event/tick limits unchanged. The supervise plugin can sequence ordinary bounded `stado_ui_choose` calls instead of restoring native widgets or inventing a bespoke form import. |
| C39 | C27 initially called `worker.get`, `worker.activate`, and host cancellation through the ordinary application binding and relied on the absence of corresponding WASM imports to call them “native-only.” A hostile orchestrator holding that bearer could still select them, so the broker did not enforce the claimed boundary. | Corrected: all three operations use explicit session-controller-authenticated RPCs. The controller authorizes the native action and the exact lifecycle binding selects the broker-derived plugin namespace/generation; the guest operation map rejects them, and an AST guard prevents hidden-import reasoning from becoming an authority boundary again. |
| C40 | A long-running signed application command could still race an ordinary prompt into the provider before the command finished its durable worker-run handoff. | Corrected: the fence remains active across the signed callback and the asynchronous native lookup/CAS activation or cancellation. Ordinary non-slash submission remains visibly in the editor and cannot start a provider turn; another application command, reload, or session transition also waits. Explicit slash controls remain available. |
| C41 | A replayed successful application command could observe an already-active broker WorkerRun but fail to restore local recurrence, while worker lookup/activation errors were transient notices that released the prompt fence. | Corrected: exact broker-active state reconciles once when no local owner exists; lookup/activation errors and non-active replies retain a distinct retryable ownership fence. A different local owner is never overwritten or used to cancel the broker-active run implicitly, and `/loop` cannot start a new competing owner while recovery is unresolved. Generation-fenced replay, draft preservation, and independent C28 failure sources are regression-tested. |
| C42 | Conversation persistence claimed a partial trailing JSONL write was recoverable, but the next append could concatenate directly onto that fragment and the decoder stopped before every later valid record. C28 receiver-delivery commit would then be able to outlive an invisible appended message. | Corrected: in-process conversation appends serialize, preserve and newline-frame a crash-shortened tail before the next complete record, and decoding skips that invalid fragment while continuing to later valid frames. Matching delivery evidence rejects unknown fields, trailing JSON, duplicate IDs, invalid role/batch shape, and byte/schema/digest mismatches; partial-tail → append → reopen → idempotent replay is covered. |
| C43 | The development supervise plugin derived a setup/run ID from an in-memory lifecycle callback sequence that resets on module rebind, so an identical later setup could reuse a terminal broker WorkerRun identity. | Corrected: every new workflow uses 128 bits from WASI's cryptographic random source. Callback sequence remains non-authoritative instance-local ordering, and regressions prove an identical post-rebind setup cannot reuse a terminal run identity. |
| C44 | Several C36 setup/baseline/worker transitions mutated in-memory plugin state before the journal append that made the transition durable, without consistently rolling back or marking the unjournalled broker result for retry. | Corrected across setup, rejection/cancel, baseline terminal/ready, artifact selection, worker request/resume/cancel, review, and completion boundaries. The plugin validates exact journal acknowledgements; an ambiguous append invalidates memory and forces projection refold; pre-append state is restored where retry is local. Stable effect keys make committed broker results replay rather than duplicate, and restart/rebind crash regressions cover both sides of each boundary. |
| C45 | `stado_agent_spawn` could admit an asynchronous child and lose the reply when a lifecycle callback was cancelled or rebound before the plugin journaled the returned child ID. Retrying setup or review could then create an indistinguishable duplicate watchdog. | Corrected: a bounded guest idempotency key is scoped to the authenticated plugin/session/generation and bound to a digest of the exact normalized spawn request. Concurrent or post-rebind replay in the same live host returns the same Fleet child; reuse with different input fails closed, generation changes isolate it, and failed pre-admission attempts remain retryable. The map is intentionally process-local because Fleet children terminate with the process. The supervise plugin journals deterministic intent keys, reuses them across callback/module rebind, and can adopt only the exact authenticated ownership when an `agent.down` event wins the reply-journal race. |
| C46 | The migration plugin had the core watchdog/verifier flow but not the full EP-62 failure policy: bounded reviewer retry/backoff, the ten-consecutive-failure pause, live-review retry, correction follow-up, and bounded worker/watcher handoff were incomplete. | Corrected in the migration application: event reviews use three fresh attempts and pause after ten consecutively exhausted triggers; success resets the streak; live review retries indefinitely with a durable 500 ms exponential delay capped at eight seconds; every correction forces a next-turn review and pauses after three failed corrections; and a typed 16-item-per-field handoff reaches each fresh reviewer. Attempt counters, retry deadlines, timers, child ownership, handoff, and correction state are journal-replayable with attempt-scoped idempotency. Valid verdicts remain independent of cleanup diagnostics. Official persistence/signing remains C34. |
| C47 | Generic immutable operator-input routing is implemented, but the migration supervise application does not yet subscribe to, asynchronously classify, route/defer, or order continuation inputs. | Integrate C28/C50 entirely in the signed application: durably claim before acknowledging when fresh child review is needed; invalidate any pre-capture conclusion, route only from the matching terminal result, settle the exact quality hold, then acknowledge that result. Preserve original text/order and provide the exact deferred ID order on a fresh completion decision. Crash/rebind tests must prove no input is dropped, replaced, duplicated, or stranded behind its own event cursor. |
| C48 | The accepted setup can choose reviewer/verifier provider and reasoning controls, but generic `agent.spawn` exposed `model` without a separately bounded provider, thinking mode/budget, or effort contract. | Corrected: optional provider/model, `auto|on|off` thinking with token-only budget, and bounded reasoning effort require both exact `agent:spawn` and non-standalone `agent:spawn:configure`. Provider construction/credentials stay native; exact resolution, token bounds, unsupported controls, forwarding, and capability attenuation are regression-tested. The signed supervise source declares both caps, restores separate watchdog/verifier advanced choices, bounds provider/model/budget/effort, and maps every spawn field byte-exactly across rebind. |
| C49 | Broker pause consumption terminalized an application WorkerRun as `interrupted`, but no generic transition could continue that accepted workflow even when its application state and holds were recoverable. | Corrected: `session:worker:resume` CAS-records an exact `resume_requested` transition without changing run identity, prompt, journal, input, deferred ownership, or WAL order. A distinct successful command result triggers controller-authenticated lookup and resume activation; stale CAS, later stop/completion, racing pause, conflicting owners, and any unexpired aggregate session hold fail closed without changing the request. Hold release/expiry permits exact retry. Cancelled, stopped, and completed runs never resume. Core replay/restart, RPC/import, native capability, TUI handoff/recovery/conflict, and own/cross-plugin/expired/raced hold regressions cover the contract. |
| C50 | C28 initially required an `operator.input.queued` event to be routed before its mandatory cursor could advance. EP-62 requires a fresh asynchronous watchdog to classify that input, but the watchdog's later `agent.down` shares the blocked cursor, so a correct plugin would deadlock or be tempted to replace the accepted policy with a lexical classifier. | Corrected: `stado_session_input_claim` uses the existing `session:input:route` capability to CAS an exact queued input/run/version into durable `reviewing` with a bounded untrusted review correlation ID. Only that transition permits the queued event ACK; rebind projects the same claim for exact idempotent child-result replay, and only the same binding/version/review ID may route it to `deliver|defer`. Reviewing remains pending for queue bounds, blocks completion, is an aggregate provider/tool scheduling fence, and joins queued/ready terminal recovery without changing original text or order. There is no native review timeout or classification policy. Core WAL/restart/idempotency/cross-scope/completion/capture-race/recovery tests, capability/RPC projections, native schedule precedence, and TUI turn-boundary delivery regressions cover the primitive; fresh-watchdog invalidation, hold settlement, and classification remain signed supervise-application policy. |

This table is intentionally a queue, not proof that every proposed correction
is right. Each item must be checked against code, history, and the owning EP
before changing behavior or metadata.

## Ordered implementation queue

[The root PLAN](../../../PLAN.md#current-corrective-release-pr-257-and-v0800) is the sole
authoritative work and release sequence. This ledger is a decision and
inconsistency index only; it deliberately does not duplicate an order that can
stale. The repository-wide implementation-versus-EP audit remains PLAN item 10
and the final work item before merge/release is unlocked.

## External plugin source and signing checkpoint

`github.com/foobarto/stado-plugins` is the source and release home for official
application plugins, including supervise. EP-42's existing in-tree bundled
source rule continues to govern the small first-party embedded primitive/tool
set; it does not move official application policy back into stado core. The
official repository has been staged in an isolated temporary checkout for
source/API work, but it does not contain the real signing setup. Never
search for, copy, display, or bypass its private signing key. External
publication must use the proper checkout and signing workflow; lack of it does
not justify an unsigned or duplicate compatibility path in stado.

For this cleanup session only, the operator permits an ephemeral test signing
key generated under `/tmp`. It may be used to exercise local build, signature,
install, and runtime paths. It must never be committed, copied into either
repository, treated as an official-plugin identity, or used to publish a
release. Final plugin publication and the next stado release remain gated on
the real external checkout and signing workflow.
