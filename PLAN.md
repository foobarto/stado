# stado — Plan

[DESIGN.md](DESIGN.md) is the architectural destination distilled from accepted
EPs. This file is the forward ledger: what remains incomplete, in what order,
and what evidence closes each item. Release history belongs in
[CHANGELOG.md](CHANGELOG.md), not here.

## v1 outcome

V1 is a Linux-only, broker-mediated, git-native agent runtime and signed WASM
application platform with these properties:

- the default execution path is mechanically contained by Linux and WASM
  capabilities;
- operator authority, model content, plugin policy, and broker control are
  separate concepts in code and storage;
- every model-facing tool and larger lifecycle application is WASM-backed;
- canonical state is single-writer, typed, scoped, replayable, and auditable;
- sessions, retained agents, mailboxes, budgets, jobs, holds, and forks survive
  process boundaries without inventing duplicate authority;
- adaptive context, learning, research, verification, and supervision improve
  work quality without pretending to be security proofs;
- TUI, run, headless, ACP, and MCP share generic runtime boundaries, while
  lifecycle-application support is explicit per surface; v0.80/v1 hosts
  applications only in the TUI and the other surfaces fail closed;
- current documentation and EP status are checked as part of the build.

macOS and Windows are not current or v1 targets
([EP-0065](docs/eps/0065-linux-only-platform-scope.md)). Their old roadmap
items are removed rather than deferred.

## Current corrective release: PR #257 and v0.80.0

PR #257 began as a native implementation of supervised work. Review found both
valuable defects and a deeper architecture drift: large native application
policy, model-visible native tools, direct TUI/WASM-side WAL access, hardcoded
artifact concepts, and plugin capabilities too weak to host the application the
EP architecture actually calls for.

The correction is deliberately sweeping and pre-v1. Preserve authoritative
data through explicit one-way migration, but do not preserve obsolete imports,
wrappers, aliases, config, fallback writers, native application paths, or
platform shims merely for compatibility.

The release sequence is:

1. **Architecture compass and guardrails.** Rewrite DESIGN/PLAN, accept
   [EP-0063](docs/eps/0063-plugin-defined-harness-artifacts.md),
   [EP-0064](docs/eps/0064-wasm-lifecycle-applications.md),
   [EP-0065](docs/eps/0065-linux-only-platform-scope.md),
   [EP-0066](docs/eps/0066-canonical-plugin-authority-and-application-placement.md),
   and
   [EP-0067](docs/eps/0067-session-controller-and-application-selection.md),
   correct stale EP
   relationships/status, and add executable drift/documentation checks.
2. **Canonical plugin authority and generic artifacts.** Remove display-name
   identity from secrets, instance state, artifacts, lifecycle instances, and
   audit attribution. Require loader-supplied canonical lock/bundled identity
   on every production path, make local identity source-bound and explicitly
   unstable. Mint an unguessable broker session-controller capability, retain
   only its hash, require it for native session-bound broker operations, and
   make lifecycle admission exclusive per exact session/generation/plugin
   namespace. This authenticates session control but must not be described as
   proof of a fresh operator gesture. Then finish plugin-defined
   schemas/projections, archived descriptors, dynamic `data`, and idempotent
   memory/lesson migration.
3. **Broker-owned artifact API.** Add authenticated kind registration and typed
   propose/query/edit/observe RPC. Derive principal, repository, session,
   ancestry, generation, capability ceiling, idempotency, and actor from a
   broker-minted admission binding independently verified against installed
   package, signature, lock identity, session, and policy. The guest never sees
   that binding. Keep authority-grant issuance private to the broker. A
   separate exact activation coordinator may accept a candidate reference, but
   security-significant fresh operator intent requires a predeclared policy or
   presenter outside the hostile orchestrator boundary; an in-process TUI
   confirmation is only a quality/user-experience step. Remove
   `stado_memory_*`, direct legacy JSONL, and every non-broker canonical writer.
4. **WASM lifecycle applications.** Implement one persistent serialized
   per-session instance across tools, EP-51 hooks, durable broker events, ticks,
   UI, and close. Make reentrancy, ordering, timeout, failure posture,
   acknowledgement, and restart behavior explicit.
5. **Generic orchestration primitives.** Operation-scoped
   `agent:{spawn,list,read,send,cancel}` capabilities and exact retained-source
   pinning are implemented; the obsolete `agent:fleet` aggregate grants
   nothing. Finish mailboxes, journal projections, timers, budgets, jobs,
   leases, scheduling holds, exact-parent terminal events, successful
   completion, followup/continuation delivery, and lifecycle composition
   recovery. That includes broker-owned logical-session admission/adoption
   across full process restart and an ancestry-checked transfer for automatic
   compacted-child recovery. The latter reassigns the whole existing broker
   application scope to the broker-minted child and fences the source; it must
   not copy worker/input/hold records into a second authority. Surviving WAL
   bytes are not recovery while their authoritative scope is unreachable. Keep mailbox data separate from
   pause/stop/cancel/down authority.
6. **Supervise application migration.** Move contract flow, tools, detectors,
   cadence, evidence policy, prompts, reviewer/verifier orchestration, stale
   verdict handling, correction, pivot, and completion policy into the official
   sandboxed WASM application. Keep only generic facts and enforcement native.
7. **Delete drift.** Remove every stado-owned native model tool and workflow:
   supervise, registry meta-tools, skills loading, research dispatchers, tasks,
   and the carve-outs in `docs/features/no-internal-tools.md`. Keep external MCP
   tools explicitly classified as typed external implementations rather than
   stado-owned native applications. Remove TUI direct WAL opens, artifact
   compatibility aliases, currency-based budget caps, native fallback writers, and
   obsolete platform/release targets. Add architecture tests that make each
   removal permanent.
8. **Conformance and documentation.** Run EP-62 eval scenarios plus strict-live,
   periodic-event, stale-steer, stale-stop-confirmation, restart, trap, lease,
   and surface-parity cases. Update feature references, ABI/host imports, threat
   model, README, and the narrative articles with the final implementation.
9. **Pre-release review and validation.** Treat Copilot/Codex findings as hypotheses; validate
   them against EPs and agreed tradeoffs. Obtain clean raw reviews and green
   focused/race/full/static/reproducibility/PTY gates. Prepare the real official
   signing workflow, but do not merge or publish yet. An ephemeral `/tmp` key is
   permitted only for local path testing.
10. **Repository-wide EP drift audit.** After every preceding item, compare the
    complete implementation and current documentation against all accepted EPs
    and decision records. Reproduce suspected mismatches, correct confirmed
    drift, and require operator review for any changed tradeoff. This remains
    the final item of the corrective sequence.

The release is blocked until all ten items are complete. A clean item-10 audit
then unlocks merge, real-key build/sign/publish, independent fetch, and v0.80.0
verification. Green CI on the old native architecture is not sufficient.

## V1 workstreams after the corrective release

### Linux security closure

Owning EPs: [EP-0030](docs/eps/0030-security-research-default-harness.md),
[EP-0050](docs/eps/0050-broker.md),
[EP-0059](docs/eps/0059-durable-event-and-budget-substrate.md), and
[EP-0065](docs/eps/0065-linux-only-platform-scope.md).

Remaining gates:

- complete provenance assignment at every ingestion point and feed taint into
  broker policy without content classification;
- move all canonical trace/event writes behind broker ownership;
- complete narrow ssh-agent/git-child construction, fetch-oriented dispatch,
  scoped egress, deterministic teardown, and removal of broad main-session
  socket forwarding;
- remove unsupported OS runners, release jobs, docs, and portability-only
  abstractions that weaken Linux clarity;
- exercise mount, namespace, Landlock, seccomp, credential-mask, and network
  invariants in real Linux integration tests;
- finish the adversarial default harness and make its declared status honest.

### Plugin application platform closure

Owning EPs: [EP-0002](docs/eps/0002-all-tools-as-plugins.md),
[EP-0028](docs/eps/0028-plugin-run-tool-host.md),
[EP-0037](docs/eps/0037-tool-dispatch-and-operator-surface.md),
[EP-0038](docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md),
[EP-0039](docs/eps/0039-plugin-distribution-and-trust.md),
[EP-0042](docs/eps/0042-binaries-out-of-source-tree.md),
[EP-0045](docs/eps/0045-model-invocable-skills.md),
[EP-0063](docs/eps/0063-plugin-defined-harness-artifacts.md),
[EP-0064](docs/eps/0064-wasm-lifecycle-applications.md),
[EP-0066](docs/eps/0066-canonical-plugin-authority-and-application-placement.md),
and
[EP-0067](docs/eps/0067-session-controller-and-application-selection.md).

Remaining gates:

- eliminate every model-visible native tool and wrapper-shaped implementation;
- finish exact remote identity/resolved-commit propagation into runtime and
  artifacts;
- make lifecycle-only applications loadable without a synthetic model tool;
- make every host import documented, capability-scoped, bounded, and covered by
  ABI tests;
- finish plugin-run/tool-host and model-invocable skill surfaces where their EPs
  remain Partial;
- publish official application source and reproducible unsigned build output
  from `foobarto/stado-plugins`, then sign release manifests only with the
  operator-held offline key;
- test install, trust, update, rollback, removal, archived-schema rendering, and
  deterministic bundled builds end to end.

### Quality and long-running work

Owning EPs: [EP-0033](docs/eps/0033-responsive-supervisor-worker-lanes.md),
[EP-0046](docs/eps/0046-verify-work-phase.md),
[EP-0052](docs/eps/0052-learn-trajectory-refinement.md) through
[EP-0060](docs/eps/0060-native-harness-guidance.md), and
[EP-0062](docs/eps/0062-harness-enforced-supervised-work.md) through
[EP-0064](docs/eps/0064-wasm-lifecycle-applications.md).

Remaining gates:

- complete Verify Work's fresh-context semantic judge without duplicating the
  executor or confusing judgment with authority;
- complete responsive frontline lanes separately from independent watchdog
  review; shared primitives do not imply shared policy;
- graduate adaptive retrieval beyond shadow mode only with measured evidence;
- validate retained-agent recovery, mailboxes, recursive budgets, and lifecycle
  holds under crash, cancellation, stale generation, and backpressure;
- ship supervise with token-only reviewer ceilings, durable stop authority, and
  the accepted asymmetric stale-verdict behavior;
- keep native bounded facts generic as application wording and policy move to
  WASM, including migration of EP-60's learn/research/coordination guidance
  policy into a lifecycle application.

### Shared surfaces and operator experience

Owning EPs: [EP-0008](docs/eps/0008-repo-local-instructions-and-skills.md),
[EP-0009](docs/eps/0009-session-guardrails-and-hooks.md),
[EP-0010](docs/eps/0010-interop-surfaces-mcp-acp-headless.md),
[EP-0014](docs/eps/0014-multi-session-tui.md),
[EP-0018](docs/eps/0018-configurable-system-prompt-template.md) through
[EP-0027](docs/eps/0027-repo-root-discovery.md),
[EP-0032](docs/eps/0032-acp-client-wrap-external-agents.md),
[EP-0035](docs/eps/0035-project-local-stado-dir.md),
[EP-0036](docs/eps/0036-loop-monitor-schedule.md),
[EP-0041](docs/eps/0041-shell-pty-tool-naming.md),
[EP-0043](docs/eps/0043-shell-pty-ux-rethink.md), and
[EP-0051](docs/eps/0051-lua-lifecycle-hook-contract.md).

Remaining gates:

- verify lifecycle/application participation on every supported surface or
  report the surface explicitly unavailable;
- keep repository configuration untrusted and user opt-ins authoritative;
- ensure UI rendering never starts work or becomes durable authority;
- preserve provider-native thinking and ordered blocks regardless of display;
- keep recurring work on durable broker scheduling rather than TUI timers;
- maintain TUI/PTY behavior through real terminal tests on Linux.

## Architecture and documentation maintenance

The following checks are v1 release requirements, not optional housekeeping:

- EP frontmatter, rendered relationships, reciprocal links, and catalogue status
  agree;
- every live Standards EP is integrated into DESIGN or PLAN;
- every Accepted/Partial Standards EP remains in PLAN until completed;
- host imports and capability grammar match the ABI/reference docs;
- critical packages and subtle boundary functions cite owning EPs;
- direct canonical-WAL and native model-tool allowlists only shrink;
- current product docs agree on Linux-only support;
- README, DESIGN, PLAN, threat model, feature references, examples, generated
  docs, and release metadata are validated together;
- a status changes to Implemented only with code-backed tests, release evidence,
  and a history entry.

## Explicit non-goals through v1

- macOS or Windows builds, runtime support, packaging, parity, or roadmaps;
- preserving pre-v1 API/ABI/config/platform compatibility when it conflicts with
  the accepted architecture;
- native product applications hidden behind WASM façades;
- raw WAL, trust-root, daemon-socket, or host filesystem access as a plugin API;
- model/content classifiers as security authority;
- per-tool approval prompts as the containment boundary;
- supervision as a security proof;
- currency-based caps across runtime and supervise; v1 budget enforcement is
  token-only while provider cost remains observational telemetry;
- a distributed multi-host broker or general policy language;
- automatic activation of generated knowledge or project-authored authority;
- treating draft [EP-0040](docs/eps/0040-bundled-local-inference.md) or
  [EP-0047](docs/eps/0047-structured-loop-result-and-output.md) as committed work.

## Next product proposal

After the corrective sequence, the next intended feature is agent-driven
tool-use auto-approval. It begins with a security and authority EP, not code.
The design must distinguish prediction of operator intent from actual authority,
must stay under the broker/plugin/sandbox ceilings, must be auditable and easy
to revoke, and must not recreate approval prompts as the containment boundary.

## Definition of v1 complete

V1 is complete only when:

- all Partial/Accepted Standards EPs claimed for v1 are implemented or
  explicitly moved out by a new decision;
- Linux default execution and every supported surface pass the real containment
  and authority tests;
- no model-visible native application/tool or non-broker canonical writer
  remains;
- durable state survives restart and rejects stale identity/generation/scope;
- release artifacts reproduce and verify from a clean checkout;
- current docs describe the behavior the tested binary actually has;
- the final repository-wide implementation-versus-EP audit is clean.
