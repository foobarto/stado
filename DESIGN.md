# stado — Design

This document is the compact architectural synthesis of stado's accepted
[Enhancement Proposals](docs/eps/README.md). It describes the system stado is
building and the invariants implementation must preserve. [PLAN.md](PLAN.md)
owns the remaining distance between that architecture and the repository.

An EP is the authoritative record for the precise decision and its rationale.
If this document and an accepted EP disagree, the EP wins and this document is
stale. If code and this document disagree, PLAN must name the unfinished
migration or the code is drift. Historical and superseded decisions remain in
the EP catalogue; they are not silently blended into the current design.

## What stado is

Stado is a Linux-native runtime for sustained work by coding agents. It began
with a security question—how can a model use powerful tools without inheriting
the operator's whole machine?—but the answer grew into a broader system:

- a provider-native agent loop that preserves each model's real protocol;
- a capability sandbox and privileged broker that bound effects mechanically;
- git-native, forkable sessions and tamper-evident evidence of work;
- a signed WASM application platform in which tools and larger agent
  applications can evolve outside the core;
- durable orchestration for retained agents, mailboxes, budgets, jobs, and
  scheduling control;
- adaptive context, evidence-backed learning, research, verification, and
  supervision for work that outlives one model window.

Security remains foundational, but it is not the only product goal. Stado is
also an execution substrate, an application platform, a durable memory and
coordination system, and a set of quality gates around fallible agent work.

Linux is the only supported platform now and through v1. Existing Darwin or
Windows remnants are not compatibility promises and do not constrain the Linux
architecture ([EP-0065](docs/eps/0065-linux-only-platform-scope.md)).

## The architecture in one view

```text
 operator
   │  trusted gestures, user configuration, grants
   ▼
 surfaces ─────────────── provider adapters
 TUI · run · headless     Anthropic · OpenAI · Google · OAI-compatible · ACP
 ACP · MCP server              │
   │                           ▼
   └──────────────► orchestrator / agent loop
                         │
             ┌───────────┴───────────┐
             ▼                       ▼
      signed WASM tools       WASM lifecycle applications
      model-facing calls      hooks · events · ticks · UI · tools
             │                       │
             └───────────┬───────────┘
                         ▼
                capability-gated host primitives
                         │ typed, authenticated requests
                         ▼
                  privileged broker
          policy · sessions · WAL · grants · budgets · holds
              │                         │
              ▼                         ▼
       Linux containment          durable state planes
   namespaces · bwrap · Landlock  git tree/trace · broker events
   seccomp · egress proxy         artifacts · projections · indexes
```

The broker and Linux kernel own authority. The orchestrator owns conversation
and execution flow. WASM applications own product policy. Providers own their
native protocol. Durable stores own history. User surfaces present these facts;
they do not manufacture them.

## Core invariants

### Authority is not text

Model output, tool output, repository files, web content, plugin output,
sub-agent messages, and reviewer verdict prose are content. None of them is an
operator gesture, a capability grant, a session identity, or a broker control
transition merely because it says so.

The host derives identity, scope, ancestry, generation, sequence, and provenance
from runtime facts. Operator authority comes from a fresh trusted presentation
channel or predeclared user policy. Authority-bearing actions are bound to the
exact action, payload, scope, version, actor, nonce, and expiry.

### The fence, not the prompt, contains

Capabilities, the broker ceiling, the effective policy, the WASM import gate,
and Linux enforcement define what code can do. Human approval can be useful as
an audit anchor or deliberate product interaction, but it is never substituted
for containment. The policy intersection may narrow; it may never widen by
accident.

### Native stado provides primitives; plugins provide applications

Every stado-owned tool the model sees is implemented by a WASM plugin. Tools
implemented by an external MCP server are typed integration endpoints under
EP-10, not stado-native application implementations; their adapter does not
grant stado authority. Native Go may expose
capabilities unavailable inside the WASM garden—filesystem operations, process
execution, provider calls, session facts, broker requests, UI transport—but it
must not hide a model-facing application behind a plugin-shaped wrapper.

Plugins may be large applications. Complexity is a reason to make the plugin
runtime adequate, not an exemption from it. If two plugins could reasonably
make different choices from the same facts, the choice is application policy
and belongs in WASM. Native imports must be generic enough to make sense without
naming one application.

### One authoritative writer, many derived views

Only the authenticated broker appends canonical adaptive-context and
orchestration events. CLI, TUI, runtime, plugins, and children submit typed
requests. They do not open the canonical WAL for mutation. Snapshots, SQLite
indexes, UI caches, and prompt projections are rebuildable views, never parallel
authority.

### Sessions fork; history is not silently rewritten

Conversation and executable history are append-only. Capability widening,
automatic context recovery, alternative work, and isolated review create child
sessions. Compaction may be explicitly accepted or represented by a fork; it
does not erase the original evidence. Adoption appends to a parent rather than
rewriting it.

### Facts and judgments stay separate

The harness can know which session emitted an event, which tree was current,
whether a command passed, which bytes were read, and where content originated.
It cannot turn a model's semantic judgment into a security fact. Deterministic
facts may enforce ceilings. Model judgments may guide quality policy within
those ceilings.

### Failure is explicit

Missing brokers, unavailable imports, stale generations, malformed schemas,
exhausted budgets, lost leases, and unsupported surfaces return bounded errors
or apply a declared fail posture. They do not fall back to ambient files,
unscoped native implementations, direct WAL access, or silent unsandboxed work.

## Trust and execution layers

### Operator layer

User configuration, trust roots, plugin pins, global broker policy, and fresh UI
gestures belong to the operator domain. Repository configuration is untrusted.
A project may suggest instructions, skills, or plugin needs, but cannot activate
operator-domain hooks, MCP servers, background applications, trust roots,
credentials, sandbox changes, or authority grants.

### Broker layer

The broker is the small privileged process boundary. It:

- validates typed requests against global policy;
- creates sessions and projects immutable capability ceilings;
- owns canonical event ordering and write handles;
- issues and consumes exact operator-origin grants;
- manages budgets, admissions, jobs, leases, holds, pause, resume, stop, and
  cancellation;
- keeps control events independently deliverable from ordinary mailbox data;
- records its decisions.

The broker does not run an LLM, execute plugin policy, parse repository prose,
or accept raw syscalls and arbitrary writes over IPC. Its API is enumerated,
strict about unknown fields, bounded, versioned, and authenticated to the local
operator/session context.

### Orchestrator layer

The orchestrator owns the agent loop, provider selection, typed message history,
tool dispatch, lifecycle callback ordering, prompt assembly, and user-surface
coordination. It is exposed to untrusted content and therefore is not the
security root. It can request authority only through broker primitives.

### WASM application layer

Signed plugins execute in wazero with no ambient OS access. A canonical runtime
identity—not a display alias—binds the exact source, version or commit, manifest
digest, capabilities, tools, lifecycle subscriptions, and artifact schemas.
Host imports check both the signed declaration and the effective runtime/broker
policy.

There are two application shapes over the same runtime:

- tools are model-invoked, stateless-per-call entry points unless they use an
  explicit instance or broker service;
- lifecycle applications are serialized per-session instances shared across
  their tools, synchronous hooks, durable events, background ticks, and close.

The second shape permits whole applications such as supervision without moving
their workflow into native Go.

### Provider layer

`pkg/agent` is deliberately small. Providers implement one streaming turn and
report capabilities. Messages preserve ordered text, thinking, image, tool-use,
and tool-result blocks. Opaque native payloads preserve reasoning signatures and
provider-specific state across round trips.

Provider capabilities—not provider-name switches—control thinking, structured
output, cache behavior, context limits, vision, parallelism, and output
headroom. Cleanup after a successfully parsed result cannot replace that valid
result with a cleanup error.

## Linux containment

The supported sandbox composes several independent controls:

- the broker projects a ceiling from operator policy and declared purpose;
- bubblewrap and Linux namespaces construct mount, process, IPC, and network
  boundaries;
- Landlock irreversibly narrows filesystem access where used;
- seccomp reduces the syscall surface;
- a private network namespace is the egress floor, with a typed allowlist proxy
  as a refinement;
- the WASM runtime gates every host import by capability;
- secret-bearing paths and trust roots are absent or read-only;
- an explicit `--no-sandbox` run is visibly outside the v1 containment claim.

The default policy grants only what ordinary work needs, normally the launch
repository/worktree and bounded scratch space for writes. Credential material is
not made readable for convenience. A socket or service that can exercise a
credential is itself a high-leverage capability and belongs in a narrow,
broker-created session.

The security atom is a session:

- its ceiling is immutable;
- its effective set is drop-only;
- each tool call intersects global policy, ceiling, effective set, plugin
  declaration, and per-call narrowing;
- widening creates a new session;
- child capability never exceeds the broker-approved projection.

## Agent loop and lifecycle

A turn is an ordered state transition:

1. establish the broker-issued session-controller binding and current
   session/tree anchor, keeping operator-input provenance distinct from
   security authority;
2. build a deterministic prompt prefix and bounded dynamic context;
3. run ordered `pre_llm` lifecycle callbacks;
4. stream a provider-native response into typed blocks;
5. run `post_llm` callbacks and validate tool calls against the current surface;
6. for each tool, run `pre_tool`, dispatch the WASM implementation through its
   capability-gated primitives, record the result, then run `post_tool`;
7. continue provider/tool iterations until the turn is terminal;
8. append conversation, tree, trace, usage, and broker events in their owning
   stores;
9. run `post_turn`, publish lifecycle events, and yield to scheduling control.

Lifecycle applications are never re-entered. One session instance has a single
serialized queue for tools, hooks, events, and ticks. Synchronous points have a
bounded deadline and declared `open` or `closed` failure posture. Durable events
are acknowledged only after callback completion and redeliver with the same
identity/idempotency namespace after a crash. Application-generated events do
not recursively invoke the same callback unless an explicit contract allows it.

`stado_plugin_tick` remains useful for asynchronous work; it is not the sole
delivery path for turn-critical or authority-bearing events.

## Tool and plugin platform

### Model-visible tools

The model-visible name, description, JSON schema, and application behavior come
from a WASM plugin. A small native discovery kernel may locate, describe,
activate, and deactivate plugin tools, but it must not implement the work behind
ordinary model tools. Transitional native exceptions are architecture debt and
may not grow.

The host provides primitives such as:

- bounded filesystem, process, PTY, network, DNS, HTTP, LSP, crypto, JSON, and
  compression operations;
- session observation, fork, retained-agent, mailbox, budget, timer, journal,
  artifact, and scheduling requests;
- provider invocation under explicit token budgets;
- trusted transport for approval, choice, print, and structured render UI;
- nested tool dispatch through the same executor, hook, sandbox, and audit path.

The host-import reference is a contract. An exported import must be documented,
capability-scoped where it conveys authority, bounded, and linked to its owning
EP. Removed imports are removed from both runtime and reference—there are no
indefinite pre-v1 aliases.

### Distribution and identity

Remote plugins use canonical, versioned source identities, signed manifests,
content hashes, trust anchors, lock files, rollback protection, and resolved
commits. The small set of first-party plugins already embedded under EP-42 is
reproducibly built from in-tree source. Local development identities are
explicitly unstable and cannot impersonate bundled or installed identities.

`github.com/foobarto/stado-plugins` is the source and release home for official
application plugins, including supervise. Their anchor key remains offline:
CI may build reproducibly, but only the operator-held key signs a publishable
manifest. Stado ships their generic host/broker primitives and installs them
through the ordinary EP-39 trust path; it does not duplicate application policy
as native Go. Ephemeral keys may exercise local paths only.

## Durable state

Stado has distinct state planes because they answer different questions.

### Git session plane

Each user repository has a sidecar bare repository. Every session has:

- a `tree` ref for executable state and turn/compaction boundaries;
- a `trace` ref for tool calls and auditable execution evidence;
- turn refs for addressable fork points;
- a materialized worktree for isolated child work.

Commits carry machine-readable metadata and signatures. A child may seed its
tree from a parent's exact point but owns its conversation and trace. Adoption
into a parent is explicit and append-only.

The broker or a broker-owned writer holds authoritative writable trace/state
handles. A compromised agent must not be able to rewrite the record of its own
actions.

### Broker event plane

One hash-chained WAL stores atomic typed events for artifacts, sessions,
mailboxes, lifecycle, budgets, jobs, leases, and control. It provides:

- broker epoch and exclusive-writer ownership;
- optimistic versions and compare-and-swap;
- idempotency keys and deterministic replay;
- snapshots and corruption recovery;
- atomic cross-store transactions;
- budget reserve/commit/release accounting;
- admissions, leases, restart generations, and scheduling holds.

Clients use typed IPC. Offline tools may verify/export; offline mutation is
refused while the broker is unavailable.

### Artifact plane

Artifacts are versioned, scoped, authority-bearing harness records. The broker
owns their common envelope: identity, kind schema, scope binding, authority,
sensitivity, provenance, evidence, observations, timestamps, and version.
Relations are broker-owned side records with endpoint authorization rather than
inline plugin-controlled envelope data. A signed plugin manifest owns the
schema of the dynamic `data` object.

Kinds are qualified by the plugin's stable canonical source namespace. The
exact plugin identity, resolved commit, manifest digest, schema digest, and
declarative index projections are archived before a write. Historical artifacts
remain interpretable after upgrade or uninstall.

Plugins may propose and edit candidate versions, query explicitly authorized
kinds, and record observations through `stado_artifact_*`. They cannot forge
scope, ancestry, identity, authority, or another plugin's local kind. Activation
and other authority transitions require trusted host/operator paths.

Memory, lessons, supervision contracts, research results, and future records are
artifact kinds, not reasons to add more top-level Go fields or parallel stores.

### Derived projections and local preferences

SQLite/search indexes, prompt sections, dashboards, and cached summaries are
rebuildable from canonical state and archived descriptors. Stale or incomplete
indexes are detected; they do not silently become truth.

Themes, picker state, recent selections, and similar per-machine ergonomics may
live in local UI state. They cannot hold broker authority or durable session
chronology.

## Sessions, retained agents, and communication

One agent runs in one session. Forking creates explicit lineage, isolated
history, and a broker-projected ceiling. Retained children add durable identity,
immutable source fork points, recursive root budgets, leases, restart policy,
and new generations rather than pretending a dead process resumed unchanged.

Mailboxes carry authenticated, ordered data between agent generations. Delivery
is at least once; receiver-input commit and acknowledgement are atomic so a
crash cannot silently lose or duplicate a logical input. Messages remain
untrusted content.

Control travels separately. Pause, stop, cancel, down, holds, and operator
override are broker events, not magic mailbox phrases. Control remains
deliverable when a data queue is full. A plugin can request a transition only
through a capability and the broker applies the current generation, scope,
budget, and policy checks.

Recurring work, timers, and monitors use durable jobs and leases. UI timers may
present status but are not the scheduler. A watchdog may stop a loop because
the broker owns the loop's scheduling transition; the watchdog's prose itself
does not.

## Context, learning, research, and verification

### Context discipline

Prompt-cache stability, context limits, compaction, and tool-output curation are
separate concerns:

- the static prefix is deterministic and tools are serialized by name;
- dynamic context is bounded and visibly truncated;
- provider usage is authoritative where available and unknown limits are not
  presented as precise;
- original turns are append-only;
- automatic recovery forks rather than silently rewriting a parent;
- user-confirmed in-place compaction remains explicit and auditable.

### Provenance and taint

Origin labels are harness-side facts, never forgeable text markers. Operator
input is trusted origin; repository, tool, web, plugin, provider, and child
content is untrusted origin. Taint conservatively over-approximates influence
within a turn and may only narrow consequential policy. A content classifier is
not in the trust-critical path.

### Adaptive context and learning

Fast retrieval selects a few active, visible artifacts under hard limits.
Research agents explore larger authorized corpora through opaque handles,
bounded search/open operations, and exact citations. Knowing an ID is not
authorization. The host can prove which authorized bytes were read, not that a
model's conclusion follows from them.

Session projections record bounded objectives, working state, decisions,
journal entries, and deterministic signals. Learning reviews consume immutable
boundaries, typed signals, and evidence. They may create candidates but cannot
activate them. Usage observations distinguish exposure, opening, citation, and
evaluated outcome; correlation is not causation and mandatory guidance cannot
be automatically demoted.

### Verification

Deterministic Verify Work gates run before semantic judgment. A fresh verifier
may assess evidence in a separate session after command gates, but its result is
a quality decision scoped to an exact anchor. It does not widen capabilities or
replace operator authority.

## Supervision

Supervise is a WASM lifecycle application over generic stado primitives. It is
a quality gate for long-running work, not a security feature. The distinction
matters in both directions:

- supervision does not claim to contain a malicious worker or prove semantic
  correctness;
- the supervise application still runs inside the WASM capability sandbox and
  does not receive ambient core authority.

Its exactly selected quality contract is one version of a session-scoped
candidate artifact; selecting it does not activate artifact authority.
Operational state—worker
attachment, plan cursor, detector cooldowns, review jobs, evidence, retries,
mailbox cursors, verdicts, and holds—lives in broker session chronology rather
than a second application database.

The host supplies session/tree anchors, bounded deterministic observations,
broker services, provider execution, and UI transport. The plugin owns contract wording, setup,
detectors, cadence, evidence selection, reviewer/verifier prompts, verdict
policy, corrections, pivots, and model-facing tools.

Watchdog results bind the exact session generation, worker sequence, plan
version, turn/tree/trace anchor, and contract version. Staleness is asymmetric:

- stale approval/continue is discarded;
- stale steering is delivered as bounded advisory tied to its earlier anchor;
- stale pause/stop creates a durable hold and triggers a fresh current-anchor
  review; only the fresh verdict may release, correct, pause, or stop.

This avoids unnecessary pauses without letting obsolete authority act on newer
work. A valid parsed verdict survives provider cleanup errors. Ordinary activity
does not by itself prove progress against criteria. Supervise v1 enforces token
ceilings only; USD caps are outside the contract.

## User surfaces

TUI, `stado run`, headless JSON-RPC, ACP, and MCP-server modes reuse the same
generic provider, runtime, plugin, broker, sandbox, audit, and context
boundaries. Lifecycle-application support is a stricter whole-surface contract:
the surface must own one persistent instance and route its commands, tools,
hooks, events, ticks, close, and required bridges together. For v0.80 and v1
only the TUI does so. Configuring an application on run, headless, or ACP fails
before provider/session work rather than creating a partial second instance.
Unsupported surface participation is explicit.

Native slash commands may route an operator interaction and present UI. They do
not prove operator-origin security authority, and they do not become a loophole
for model-facing application logic. Plugins can use the
capability-gated UI bridge:

- `stado_ui_approve` for a quality/workflow yes/no interaction;
- `stado_ui_choose` for bounded choice/input;
- `stado_ui_print` for operator-facing text;
- `stado_ui_render` for structured presentation.

The plugin owns wording and workflow. The host owns transport, sanitization,
availability, and UI delivery. Durable authority requires a broker-owned
predeclared policy or proof from a separately trusted presenter; an in-process
callback, command origin flag, or session-controller token is not that proof.

## Audit, telemetry, and release integrity

Audit has two complementary records:

- signed session tree/trace history explains what a session observed and did;
- the broker chronology and decision log explain authoritative admissions,
  grants, denials, budgets, and control transitions.

Telemetry is emitted from shared state transitions rather than duplicated by
surfaces. Metric and span names are contracts. Sensitive content is not smuggled
into labels. Provider-native usage, cache, and thinking data are preserved.

Bundled WASM and the stado binary are reproducibly built. Releases publish
Linux artifacts, checksums, signatures, SBOMs, and provenance. A release is not
complete until the published artifacts have been independently fetched and
verified. Test signing keys are never release identities.

## Architecture and documentation guardrails

Architecture drift is a correctness bug. The repository enforces the following:

1. Accepted EPs are append-only; reversals get a new EP with reciprocal
   relationship metadata. Where live EPs disagree, a later explicit
   extends/supersedes relationship controls only the scope it names.
2. The EP catalogue status must match each file's frontmatter.
3. Every live Standards EP must appear in DESIGN or PLAN. Every Accepted or
   Partial Standards EP must appear in PLAN until implementation is complete.
4. Critical packages and non-obvious boundary functions cite the EPs whose
   invariants they implement.
5. Exported WASM host imports must appear in the ABI/reference documentation;
   removed imports must disappear from both.
6. Direct canonical-WAL opens outside broker ownership are an explicit shrinking
   debt allowlist; new call sites fail CI.
7. Native model-facing tool/application exceptions are an explicit shrinking
   debt allowlist; new entries fail CI.
8. Current product documents must not reintroduce unsupported macOS or Windows
   claims.
9. Review suggestions are hypotheses. A review cannot override an EP, product
   decision, or documented tradeoff without an explicit new decision.
10. `make check` validates generated docs, references, examples, deterministic
    WASM, tests, static analysis, and architecture guards before release.

Dates labeled “last reviewed” are not a substitute for these checks. A document
stays current because its structural claims are compared to executable facts
and because incomplete EPs remain visible in PLAN.

## Package ownership

| Area | Owns | Must not own |
|---|---|---|
| `pkg/agent`, `internal/providers/*` | typed provider protocol and capabilities | application policy or sandbox authority |
| `internal/runtime` | agent loop, prompt/tool/lifecycle orchestration | authoritative broker writes or hidden native tools |
| `internal/plugins`, `internal/plugins/runtime` | manifests, identity, WASM execution, host primitives | ambient OS access or operator authority |
| `internal/broker`, `internal/daemon` | admission, IPC, sessions, authority, ordering, budgets, control | LLM prompts or plugin workflows |
| `internal/sandbox` | Linux policy enforcement | product approval logic |
| `internal/state/git` | forkable executable and trace history | adaptive-context authority |
| `internal/artifacts`, session/mailbox/budget services | typed broker-domain validation and folds | independent writers or UI policy |
| `internal/tui`, headless, ACP, MCP | presentation and surface adapters | alternate runtime semantics |
| `plugins/*`, official plugin repositories | tools and application policy | native authority implementation |

## EP integration map

This map is intentionally explicit and machine-checked. It prevents a new
accepted decision from living only in an isolated EP while the top-level design
continues to tell an older story.

| Theme | Current EPs |
|---|---|
| Governance and release | [EP-0001](docs/eps/0001-ep-purpose-and-guidelines.md), [EP-0011](docs/eps/0011-observability-and-telemetry.md), [EP-0012](docs/eps/0012-release-integrity-and-distribution.md), [EP-0061](docs/eps/0061-linked-ep-relationship-metadata.md), [EP-0065](docs/eps/0065-linux-only-platform-scope.md) |
| Provider and session foundations | [EP-0003](docs/eps/0003-provider-native-agent-interface.md), [EP-0004](docs/eps/0004-git-native-sessions-and-audit.md), [EP-0007](docs/eps/0007-conversation-state-and-compaction.md), [EP-0014](docs/eps/0014-multi-session-tui.md), [EP-0032](docs/eps/0032-acp-client-wrap-external-agents.md) |
| Plugin and capability platform | [EP-0002](docs/eps/0002-all-tools-as-plugins.md), [EP-0006](docs/eps/0006-signed-wasm-plugin-runtime.md), [EP-0017](docs/eps/0017-tool-surface-policy-and-plugin-approval-ui.md), [EP-0028](docs/eps/0028-plugin-run-tool-host.md), [EP-0029](docs/eps/0029-config-introspection-host-imports.md), [EP-0031](docs/eps/0031-fs-cap-path-templates.md), [EP-0037](docs/eps/0037-tool-dispatch-and-operator-surface.md), [EP-0038](docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0039](docs/eps/0039-plugin-distribution-and-trust.md), [EP-0042](docs/eps/0042-binaries-out-of-source-tree.md), [EP-0044](docs/eps/0044-repo-config-trust-boundary.md), [EP-0045](docs/eps/0045-model-invocable-skills.md), [EP-0063](docs/eps/0063-plugin-defined-harness-artifacts.md), [EP-0064](docs/eps/0064-wasm-lifecycle-applications.md), [EP-0066](docs/eps/0066-canonical-plugin-authority-and-application-placement.md), [EP-0067](docs/eps/0067-session-controller-and-application-selection.md) |
| Security and broker | [EP-0030](docs/eps/0030-security-research-default-harness.md), [EP-0050](docs/eps/0050-broker.md), [EP-0059](docs/eps/0059-durable-event-and-budget-substrate.md), [EP-0065](docs/eps/0065-linux-only-platform-scope.md), [EP-0067](docs/eps/0067-session-controller-and-application-selection.md) |
| Instructions, hooks, and surfaces | [EP-0008](docs/eps/0008-repo-local-instructions-and-skills.md), [EP-0009](docs/eps/0009-session-guardrails-and-hooks.md), [EP-0010](docs/eps/0010-interop-surfaces-mcp-acp-headless.md), [EP-0018](docs/eps/0018-configurable-system-prompt-template.md), [EP-0027](docs/eps/0027-repo-root-discovery.md), [EP-0033](docs/eps/0033-responsive-supervisor-worker-lanes.md), [EP-0035](docs/eps/0035-project-local-stado-dir.md), [EP-0036](docs/eps/0036-loop-monitor-schedule.md), [EP-0051](docs/eps/0051-lua-lifecycle-hook-contract.md), [EP-0064](docs/eps/0064-wasm-lifecycle-applications.md) |
| TUI contracts | [EP-0019](docs/eps/0019-model-provider-picker-ux.md), [EP-0020](docs/eps/0020-inline-context-completion.md), [EP-0021](docs/eps/0021-assistant-turn-metadata.md), [EP-0022](docs/eps/0022-theme-catalog-and-picker.md), [EP-0023](docs/eps/0023-status-modal.md), [EP-0024](docs/eps/0024-footer-density.md), [EP-0025](docs/eps/0025-thinking-display-modes.md), [EP-0026](docs/eps/0026-command-input-ergonomics.md), [EP-0041](docs/eps/0041-shell-pty-tool-naming.md), [EP-0043](docs/eps/0043-shell-pty-ux-rethink.md) |
| Context, learning, and orchestration | [EP-0046](docs/eps/0046-verify-work-phase.md), [EP-0052](docs/eps/0052-learn-trajectory-refinement.md), [EP-0053](docs/eps/0053-versioned-harness-artifacts-and-index.md), [EP-0054](docs/eps/0054-addressable-context-and-research-agents.md), [EP-0055](docs/eps/0055-retained-resumable-subagents.md), [EP-0056](docs/eps/0056-agent-mailboxes-and-supervision.md), [EP-0057](docs/eps/0057-session-state-journal-decisions-and-signals.md), [EP-0058](docs/eps/0058-measured-adaptive-retrieval.md), [EP-0059](docs/eps/0059-durable-event-and-budget-substrate.md), [EP-0060](docs/eps/0060-native-harness-guidance.md), [EP-0062](docs/eps/0062-harness-enforced-supervised-work.md), [EP-0063](docs/eps/0063-plugin-defined-harness-artifacts.md), [EP-0064](docs/eps/0064-wasm-lifecycle-applications.md) |

Superseded EPs remain useful history but do not define current architecture.
Draft [EP-0040](docs/eps/0040-bundled-local-inference.md) and
[EP-0047](docs/eps/0047-structured-loop-result-and-output.md) are not commitments;
PLAN may mention them only as explicitly unaccepted possibilities.
