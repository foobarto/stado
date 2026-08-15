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
EP architecture actually calls for. The corrective cutover removed native
supervise policy/tools/evaluator and preserved the complete official
application source in the checkpoint identified below.

The correction is deliberately sweeping and pre-v1. Preserve authoritative
data through explicit one-way migration, but do not preserve obsolete imports,
wrappers, aliases, config, fallback writers, native application paths, or
platform shims merely for compatibility.

The source cutover and generic host work are complete. The authoritative
official-source checkpoint is signed Git commit
`987906479e9675b2aa9f972802b5ab3716d4af48`; the complete-history recovery
bundle is `.agent/stado-plugins-official-20260815-completion.bundle` (SHA-256
`7eb946fe4c35b3630086f68eadab69d1bd3e5e6a3d702852648c2a1bd88c19ea`).
Neither the checkpoint nor the bundle is a signed plugin release.

Two broad implementation-versus-EP passes corrected the source and forward
ledger. Their full test, race, vet, architecture/document, EP relationship, and
Accepted/Partial-to-PLAN checks are green. They are not the terminal audit:
subsequent conformance and adversarial-boundary work found C92-C103 and changed
the source. The original release order therefore still requires one final
repository-wide drift audit
after conformance and live review freeze the candidate. Any source correction
after that audit invalidates it and requires a repeat.

The supervise conformance proof is complete against an ephemeral signing root.
It covers all eight source packages, source-keyed installation, explicit
lifecycle admission, dynamic command ownership, absence of native fallback,
setup/cancellation, exact review and pivot policy, immutable operator-input
routing, Verify Work and independent completion, transactional reload, cold
session resume, and automatic compacted-child transfer of the existing broker
scope. The compacted-child PTY binds the authenticated canonical auto-compact
identity, does not replay the application prompt as ordinary input, reconciles
the exact WorkerRun once in the child, advances it only from a child-anchored
review, and terminalizes it cleanly. Plugin-owned scenarios and the focused,
race, reproducibility, and PTY gates are green. Real-key repetition belongs to
the release ceremony, not the development proof.

Only these corrective-release gates remain, in order:

1. **Resolve the live PR review.** Authenticate to the forge, fetch the current
   PR #257 review/thread state, inspect the complete raw Copilot review including
   suppressed findings, and obtain the repository-required fresh final-head
   Codex connector/adversarial review. Map every finding to the final generic
   regression or a new correction, and require green CI. The native shell/GH
   parser and unsafe cross-session child forwarding were deleted; review
   resolution must cite the replacement generic tests rather than revive them.
2. **Repeat the terminal implementation-versus-EP audit.** Audit the frozen
   root and official-plugin source against every Accepted decision, later
   counter-requirement, cleanup-ledger item, ABI/reference claim, and current
   product document. Re-run the full tests, selected race matrix, vet,
   architecture/document/EP guards, reproducible builds, and final diff review.
   This is the last source-changing gate; a finding reopens the audit.
   "Terminal" here means the final source-changing gate of this corrective
   release, not completion or removal of the v1 workstreams and next product
   proposal recorded below.
3. **Perform the real release ceremony.** Freeze the official supervise package
   at immutable `0.1.0`, build it reproducibly from the checkpointed source,
   sign only with the operator-held offline plugin key, publish
   `supervise/v0.1.0`, and independently fetch/install/verify it in a clean trust
   root against the v0.80.0 candidate. Repeat the complete conformance matrix
   against those exact signed bytes. Development keys and unsigned manifests
   must not cross this boundary.
4. **Merge and publish v0.80.0.** Merge only the audited, reviewed source,
   produce the Linux-only signed release artifacts through the real workflow,
   verify install/update/rollback and the official application from clean
   state, then record the released hashes and evidence in the changelog.

The completed development proof included the following progression:

1. The isolated ephemeral-key
   proof already covers all eight source packages, source-keyed install,
   explicit lifecycle admission, dynamic command ownership, tasks across
   reload and cold restart, absence of native fallback, supervise setup
   cancellation, the default fresh baseline child, and operator rejection of
   the baseline proposal. It also confirms that proposal, activates the exact
   WorkerRun, crosses the anchorless first iteration and exact anchored progress
   claim, admits a fresh pinned read-only watchdog, advances the plan only from
   its current-anchor approval, proves the review barrier blocks parent-provider
   follow-up, and cancels the terminal workflow cleanly. A further PTY path
   captures ordinary input during a live worker stream, claims and classifies
   it through a fresh pinned reviewer, proves it cannot steer that in-flight
   provider request, and delivers the immutable original only after the turn
   boundary to the next recurrence. The pivot path now also proves exact
   pinned review, recommendation-only handling, explicit user confirmation,
   artifact CAS, plan re-anchor, revised recurrence, and cancellation. The
   proof now also crosses operator-owned Verify Work, exact source-content
   anchoring, watchdog review, a fresh independent verifier, durable successful
   completion, exact hold release, transactional `/reload`, and an explicit
   cold `session resume` that re-adopts the same broker scope without duplicate
   recurrence. The final automatic-compaction path transfers the same durable
   scope to the authenticated direct child and reconciles its exact WorkerRun
   instead of replaying it as ordinary input.

Other staged official applications remain unsigned source candidates unless
their own Accepted EP gates close. In particular, memory/learn remains
candidate-only until a separately trusted presenter exists; provider reply loss
is still cost-ambiguous; truncated learning-journal recovery fails closed;
measured adaptive ranking and the exact `memory__search` fast path are not
implemented. None of those gaps may be hidden behind a native fallback or a
release claim.

## V1 workstreams after the corrective release

### Linux security closure

Owning EPs: [EP-0030](docs/eps/0030-security-research-default-harness.md) and
[EP-0050](docs/eps/0050-broker.md).

Remaining gates:

- complete provenance assignment at every ingestion point and feed taint into
  broker policy without content classification;
- move all canonical trace/event writes behind broker ownership;
- complete narrow ssh-agent/git-child construction, fetch-oriented dispatch,
  scoped egress, deterministic teardown, and removal of broad main-session
  socket forwarding;
- exercise mount, namespace, Landlock, seccomp, credential-mask, and network
  invariants in real Linux integration tests;
- finish the adversarial default harness and make its declared status honest.

### Plugin application platform closure

Owning EPs: [EP-0038](docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md),
[EP-0039](docs/eps/0039-plugin-distribution-and-trust.md),
[EP-0045](docs/eps/0045-model-invocable-skills.md),
[EP-0054](docs/eps/0054-addressable-context-and-research-agents.md),
[EP-0063](docs/eps/0063-plugin-defined-harness-artifacts.md),
[EP-0064](docs/eps/0064-wasm-lifecycle-applications.md),
[EP-0066](docs/eps/0066-canonical-plugin-authority-and-application-placement.md),
[EP-0067](docs/eps/0067-session-controller-and-application-selection.md), and
draft [EP-0068](docs/eps/0068-signed-plugin-cli-commands.md).

Remaining gates:

- make every host import documented, capability-scoped, bounded, and covered by
  ABI tests;
- complete the remaining broader [EP-0038](docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md)
  and [EP-0039](docs/eps/0039-plugin-distribution-and-trust.md) surface and
  distribution phases without restoring name-based or compatibility authority;
- finish EP-45's separately trusted project-resource enablement path, then
  sign/publish and clean-install the explicit official skills package; project
  `allowed-tools` remains inert until that authority decision exists;
- implement EP-54's exact `memory__search` fast path and release-verify the
  isolated research package before calling that Accepted contract shipped;
- publish only the official applications whose own Accepted gates are closed;
  sign `supervise/v0.1.0` only with the operator-held offline key;
- test install, trust, update, rollback, removal, archived-schema rendering, and
  deterministic bundled builds end to end.
- evaluate EP-68's explicit signed one-shot CLI command profile after v0.80;
  current memory/learn application commands deliberately remain TUI-only, and
  no native compatibility handler or inferred lifecycle-command promotion is
  permitted.

### Quality and long-running work

Owning EPs: [EP-0033](docs/eps/0033-responsive-supervisor-worker-lanes.md),
[EP-0046](docs/eps/0046-verify-work-phase.md),
[EP-0052](docs/eps/0052-learn-trajectory-refinement.md),
[EP-0058](docs/eps/0058-measured-adaptive-retrieval.md),
[EP-0060](docs/eps/0060-native-harness-guidance.md), and
[EP-0062](docs/eps/0062-harness-enforced-supervised-work.md) through
[EP-0064](docs/eps/0064-wasm-lifecycle-applications.md).

Remaining gates:

- complete Verify Work's fresh-context semantic judge without duplicating the
  executor or confusing judgment with authority;
- complete responsive frontline lanes separately from independent watchdog
  review; shared primitives do not imply shared policy;
- implement measured retrieval ranking, shadow evaluation, and reporting before
  any adaptive policy may change prompt contents; no shadow evaluator exists in
  the current source;
- implement C86's separately trusted artifact-activation presenter before the
  official memory/learn package can leave candidate-only status. The presenter
  must reload the broker's exact candidate and commit one actor-bound,
  version/digest/scope-exact, reply-loss-idempotent grant issue/consume/
  activation transaction; a guest import, controller token, ordinary TUI
  callback, or application-rendered prose is not operator authority;
- validate retained-agent recovery, mailboxes, recursive budgets, and lifecycle
  holds under crash, cancellation, stale generation, and backpressure;
- release-verify supervise's token-only reviewer ceilings, durable stop
  authority, and accepted asymmetric stale-verdict behavior;
- prove, offline-sign, publish, install, and pin the official TUI-only EP-60
  `guidance` lifecycle application; its native facts, append-only contribution
  seam, source migration, and native composition deletion are complete.

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

Owning decision: [EP-0001](docs/eps/0001-ep-purpose-and-guidelines.md).

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
