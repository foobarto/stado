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

## Current state after v0.80.2

PR #257 merged as `391ca108` and shipped in signed Linux-only release v0.80.0.
Its binaries, checksum manifest, Cosign certificate/signature, SBOMs, and GitHub
provenance are published. The immutable v0.80.0 homepage check failed because
the tagged page still advertised v0.79.0; v0.80.1 corrects that metadata rather
than rewriting the published tag.

PR #258 merged as `1d77b3bd` and shipped in SSH-agent-signed Linux-only release
v0.80.1. The tagged homepage validator is green. All eleven downloaded assets
match GitHub's digests; all eight payloads match the checksum manifest; Cosign
verification succeeds; the static amd64 binary reports stado 0.80.1, Go 1.26.6,
and the exact release commit. Independent `gh attestation verify` now succeeds
and binds all eight payloads to the tag, commit, Release workflow, and
transparency log. Minisign remains deliberately unprovisioned and did not emit
an asset or embedded root.

PR #260 merged as `51bba58e` and shipped in SSH-agent-signed Linux-only release
v0.80.2. Its release race gate, GoReleaser build, Cosign signing, SBOM
generation, GitHub provenance, and tagged homepage validation are green. All
eleven clean-room-downloaded assets match GitHub's published digests, all eight
payloads match the checksum manifest, and independent Cosign and GitHub
attestation verification bind them to the exact tag, commit, Release workflow,
and GitHub-hosted runner. The static amd64 binary reports stado 0.80.2, Go
1.26.6, the exact release commit, and `modified: false`. Minisign remains
deliberately unprovisioned and emitted no asset or embedded root.

The official supervise application initially shipped as `supervise/v0.1.0`.
The terminal source/release audit found that its Go 1.26.6 WASM build retained
an absolute GOROOT and was therefore reproducible only within one toolchain
location. Corrective `supervise/v0.1.1` pins Go 1.26.6, uses trimpath and an
empty build ID, verifies the committed bundle, and reproduces byte-for-byte
across distinct GOROOT locations. Its manifest is signed by the official plugin
key; its Git commits and tag are signed through the operator's SSH agent. Fresh
GitHub release bytes passed signature, digest, isolated install, and installed
package verification with the official owner anchor.

The v0.80.1 terminal implementation-versus-EP audit is complete. It corrected
the official-plugin build defect above, removed an unused racing write in a
shared Fleet test fake, reconciled the EP/cleanup/product documentation, and
passed exact Go 1.26.6 tests, the release race suite, vet, CI-pinned lint and
govulncheck, cold-cache bundled-WASM determinism, the homepage validator, and a
full Linux GoReleaser snapshot with SBOMs. Local snapshot Cosign was deliberately
skipped because only the real GitHub Actions release has the intended OIDC
identity.

The v0.80.1 clean-download audit found one additional build-state metadata
defect. That immutable binary embeds the exact release commit but reports
`modified: true`: GoReleaser created unignored `dist/` metadata before invoking
`go build`, so Go's `-buildvcs=true` stamp saw generated output. The signed
hashes and provenance remained exact. v0.80.2 ignores `/dist/`, guards that
rule, and closes the defect with a clean downloaded binary reporting
`modified: false`; the v0.80.1 tag was not rewritten.

Immediate work, in order:

1. **Completed after v0.80.2: remove broad SSH-agent forwarding.** Stado no
   longer injects `$SSH_AUTH_SOCK`, binds arbitrary host sockets, or reserves a
   privileged git-child role. Credential masking remains; narrowly scoped SSH
   credential provisioning belongs in a separate project.
2. **Accepted after v0.80.2: EP-69's memory ownership and bounded failure
   contract.** The contract keeps canonical storage broker-owned while agents
   manage memory through
   explicit scope capabilities: root main sessions may manage global/repo/
   self-session memory; children hard-drop global management and may lose more
   rights at spawn but never mint them. It replaces compound recovery with one
   atomic mutation, one exact read-back, then a visible stop/explicit retry.
3. **Keep agent-driven auto-approval in incubation.** Do not revise or
   implement the current draft until the operator explicitly resumes it.
4. **Decide Minisign provisioning deliberately.** It remains optional until
   the CI secret and long-lived offline release root are intentionally created;
   do not imply that current releases contain either.

The final connector/adversarial review planned before v0.80.0 was bypassed when
the operator approved that merge. It remains explicit process debt; v0.80.1's
terminal source/EP audit does not retroactively claim that connector review ran.
The unrelated deferred security stash remains untouched and outside this
release-evidence follow-up.

Other staged official applications remain unsigned source candidates unless
their own Accepted EP gates close. In particular, memory/learn remains
candidate-only while accepted
[EP-0069](docs/eps/0069-agent-owned-memory-authority.md) is implemented. It replaces the
separately trusted presenter with admitted agent-managed scope capabilities and
reclassifies provider reply loss or truncated learning history as visible
terminal outcomes rather than recovery obligations. Until that contract is
implemented, C86-C88 remain current. Measured adaptive ranking and
the exact `memory__search` fast path are also not implemented. None of those
gaps may be hidden behind a native fallback or a release claim.

## V1 workstreams after the corrective release

### Linux security closure

Owning EPs: [EP-0030](docs/eps/0030-security-research-default-harness.md) and
[EP-0050](docs/eps/0050-broker.md).

Remaining gates:

- retain the signed tree/trace history as the provenance and audit substrate;
  do not add generalized taint propagation or a taint-policy matrix without a
  concrete preventive boundary that the existing audit cannot provide;
- move all canonical trace/event writes behind broker ownership;
- retain the composed real-bubblewrap regression for mount scope, namespace,
  seccomp, credential masking, environment filtering, network denial, and
  teardown; add equivalent composed Landlock and broker-admission coverage;
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

- keep the exact host-import and required-export signature matrix enforced
  against every released official plugin in cross-repository CI and keep the
  live host-import documentation guard green; finish making every host import
  capability-scoped, bounded, and behavior-tested;
- complete the remaining broader [EP-0038](docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md)
  and [EP-0039](docs/eps/0039-plugin-distribution-and-trust.md) surface and
  distribution phases without restoring name-based or compatibility authority;
- finish EP-45's separately trusted project-resource enablement path, then
  sign/publish and clean-install the explicit official skills package; project
  `allowed-tools` remains inert until that authority decision exists;
- implement EP-54's exact `memory__search` fast path and release-verify the
  isolated research package before calling that Accepted contract shipped;
- publish only the official applications whose own Accepted gates are closed;
  repeat the full plugin-owned PTY conformance matrix against the exact
  published `supervise/v0.1.1` bytes before promoting EP-62/64 to Implemented;
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
[EP-0060](docs/eps/0060-native-harness-guidance.md),
[EP-0062](docs/eps/0062-harness-enforced-supervised-work.md) through
[EP-0064](docs/eps/0064-wasm-lifecycle-applications.md), and
[EP-0069](docs/eps/0069-agent-owned-memory-authority.md).

Remaining gates:

- complete Verify Work's fresh-context semantic judge without duplicating the
  executor or confusing judgment with authority;
- complete responsive frontline lanes separately from independent watchdog
  review; shared primitives do not imply shared policy;
- implement measured retrieval ranking, shadow evaluation, and reporting before
  any adaptive policy may change prompt contents; no shadow evaluator exists in
  the current source;
- implement EP-69's separate broker session authority set, exact root/child
  memory profiles, recursive drop-only projection, and agent-managed
  exact-kind/scope artifact operations;
  provider reply loss and insufficient journal history stop visibly rather
  than triggering automatic replay or compound recovery;
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

## Incubating product proposal

Agent-driven tool-use auto-approval remains an intentionally paused idea. Do
not revise its draft or begin implementation until the operator explicitly
resumes it. If resumed, it begins with a security and authority EP, not code:
prediction of operator intent is not authority, and any mechanism remains below
the broker/plugin/sandbox ceilings without recreating per-call approval prompts
as the containment boundary.

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
