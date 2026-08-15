---
ep: 69
title: Agent-Owned Memory Authority
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Draft
type: Standards
created: 2026-08-15
requires: ["EP-0004", "EP-0050", "EP-0055", "EP-0059", "EP-0066", "EP-0067"]
extends: ["EP-0052", "EP-0053", "EP-0063", "EP-0064"]
see-also: ["EP-0015", "EP-0054", "EP-0058"]
history:
  - date: 2026-08-15
    status: Draft
    note: Initial draft replacing per-memory operator activation with attenuated agent authority and bounded stop/read-back/retry failure handling.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0050](./0050-broker.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md), [EP-0067](./0067-session-controller-and-application-selection.md) · **Extends:** [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md), [EP-0064](./0064-wasm-lifecycle-applications.md) · **See also:** [EP-0015](./0015-memory-system-plugin.md), [EP-0054](./0054-addressable-context-and-research-agents.md), [EP-0058](./0058-measured-adaptive-retrieval.md)

# EP-0069: Agent-Owned Memory Authority

> **Draft scope:** this EP changes who may manage memory; it does not make
> memory authoritative instructions, introduce an approval agent, or revive a
> native memory application. The current staged package remains candidate-only
> until this draft is accepted and implemented.

## Problem

The current artifact contract treats fresh memory activation as operator
authority. After native review was removed, that requires a separately trusted
presenter to reload every candidate and consume a one-use grant. This is a
sound answer to a stronger problem than Stado needs to solve, but it makes
ordinary memory maintenance slow, keeps the official memory/learn package
blocked, and still cannot prove that fluent proposed content is benign.

Stado already accepts that model inputs, project files, tool results, and
memory may be poisoned. The practical security boundary is therefore the
authority an agent was admitted with: a poisoned agent cannot read a secret or
mutate a resource that its capability set does not contain. Existing audit
records preserve attribution; adding a taint-policy matrix or a fluent steward
does not create trustworthy judgment and would make the system harder to
operate and audit.

The learning workflow also carries recovery obligations disproportionate to
the product. Provider reply loss is inherently cost-ambiguous, and a bounded
journal cannot prove an arbitrarily old partial workflow. Trying to compensate
through compound failures adds more state transitions without making the
original result knowable.

## Goals

- Make memory primarily managed by agents admitted with explicit scoped
  capabilities.
- Give root main sessions session, repository, and global memory management;
  ordinary children never receive global management.
- Let child authority stay equal or narrow at spawn and during execution, but
  never widen or mint a missing capability.
- Keep canonical storage, identity, immutable scope binding, quotas,
  idempotency, and audit broker-owned.
- Keep memory visibly untrusted and subordinate to current operator input and
  project instructions.
- Replace compound recovery with bounded stop, read-back, and explicit retry
  at one durable operation boundary.

## Non-goals

- Preventing all prompt, project, tool-result, or memory poisoning.
- Adding a memory steward, gatekeeper model, or per-write approval workflow.
- Using taint as an authorization input or building a taint index/database.
- Letting plugins, models, or children mint capabilities.
- Giving agent management rights to every generic artifact kind.
- Replacing the EP-59 WAL, git-native audit trail, or rebuildable artifact
  index.
- Recovering automatically from two independent or compounding failures.

## Design

### Ownership split

The broker owns the canonical bytes and mechanical invariants. The admitted
agent owns the semantic lifecycle of memory inside its effective authority.
These are compatible meanings of ownership:

- the broker validates exact plugin identity, kind, schema, principal,
  repository, session ancestry, scope, version, sensitivity, idempotency key,
  and quota before one append;
- the memory application chooses memory content and lifecycle operations on
  behalf of the calling agent;
- the agent's broker session decides whether that call may read or manage the
  requested resource;
- no raw WAL handle, controller token, or scope-binding field enters WASM.

Memory content is contextual data, not operator authority. Retrieval labels it
as untrusted, retains provenance, and never merges it into system, project, or
operator instructions. An active user message and project instruction continue
to override conflicting memory.

### A separate session authority set

`sandbox.Policy` remains the process-containment vocabulary. Broker sessions
gain a separate `AuthoritySet` for typed host resources. Both the immutable
ceiling and mutable effective set are recorded in `SessionHandle`; the existing
rule applies unchanged: effective authority may only narrow, and every child
ceiling must be a subset of the parent's current effective set.

The first resource grammar is exact and deliberately small:

```text
artifact:read:<qualified-kind>:session
artifact:read:<qualified-kind>:repo
artifact:read:<qualified-kind>:global
artifact:manage:<qualified-kind>:session
artifact:manage:<qualified-kind>:repo
artifact:manage:<qualified-kind>:global
```

`qualified-kind` is the stable unversioned source namespace plus local kind
from EP-63. `manage` accepts no wildcard and applies only to a kind whose exact
signed owner is admitted. Existing manifest capabilities still constrain what
the executable may call; the request must satisfy both executable capability
and session authority. A signed package cannot grant its caller a resource
right, and a session right cannot make an unadmitted import appear.

Direct-active mutation uses a separate model-facing operation:

```text
stado_artifact_manage(request_json, response_buffer)
# signed executable capability: artifact:manage:<local-kind>
```

The existing `stado_artifact_propose` and `stado_artifact_edit` calls remain
candidate-only. The manage request carries an exact lifecycle operation,
payload/version, scope, and logical idempotency key; it carries no principal,
scope binding, session authority, or activation actor. The host and broker
derive those fields and require both the signed executable capability and the
matching effective session-resource capability above.

For an agent-managed memory kind, `manage` permits an atomic direct-active
create, a new active version that supersedes the prior active version, and
retire/delete tombstones within the named scope. It does not permit descriptor
changes, migration, principal or scope-binding selection, sensitivity
downgrade, another plugin's kind, or in-place history rewrite. Other artifact
kinds keep EP-63's candidate-only behavior unless a later accepted EP assigns
them an agent-managed lifecycle.

### Admission profiles

Installing or signing the memory application does not activate it. Operator
configuration enables the exact application and selects its authority profile;
this is the guarded minting step. Runtime prompts and application prose cannot
change that profile.

The initial profile is:

| Session | Read | Manage |
|---|---|---|
| Root `main-chat` | visible session lineage, current repo, global | its own session, current repo, global |
| Ordinary child | visible ancestor lineage, current repo, global | its own session, current repo |
| Tool run or unbound application | only explicitly configured exact scopes | none by default |

"Root" means a broker-admitted `main-chat` session with no agent parent. A
manual top-level session is another root and may manage global memory. An
automatic compacted-session continuation retains the same logical-session
authority because it moves the existing scope; it is not a newly spawned
agent. A manual subagent/fork receives a projected child authority instead.

Session management is always bound to the caller's own logical session. A
child may read visible ancestor-session memory but cannot edit it. Repository
management is available to children operating in that canonical repository.
Global management is hard-dropped from every ordinary child even when the
parent holds it.

The session resource coordinate is the exact logical Git session identity
derived during main or child admission, not the transient broker handle. A
retained child can therefore recover its own session memory without gaining
another session's scope. Binding that identity does not give a subagent the
root lifecycle-application controller, application journal, or adoption
credential; those remain separate EP-64/67 authorities.

### Spawn-time attenuation

`spawn_agent` gains a bounded exact `drop_capabilities` list for the session
authority vocabulary. Omission selects the child profile above. A parent may
drop child session management, repository management, or reads. Unknown or
wildcard entries are rejected; duplicates are normalized, and dropping an
already-absent right is an idempotent no-op. The broker applies the hard child
maximum, intersects it with the parent's current effective set, then subtracts
the explicit drops.

A child cannot request an addition. Its descendants start from its already
narrowed effective set, so authority cannot reappear lower in the tree.
Existing tool, provider, filesystem, network, mode, and write-scope attenuation
remain separate and continue to apply; this EP does not encode them again in
the memory list.

### No steward and no taint authorization

There is no privileged memory-review agent. A fluent gatekeeper consumes the
same attack surface it is expected to judge, adds latency, and merely moves the
poisoning decision. Root agents may manage global memory because the operator
admitted that broad role; children cannot.

The current coarse taint marker remains audit/provenance metadata only. Stado
does not propagate taint through files or evaluate an origin/effect policy
matrix. The git-native session trace, broker decisions, artifact versions, and
provenance already record who changed what and when. Prevention comes from
scope and least privilege, not an ever-growing contamination label.

### Bounded failure contract

One broker artifact mutation remains atomic, CAS-versioned, and idempotent.
The application handles only three outcomes:

1. `committed`: use the returned immutable receipt/version.
2. `not_committed`: surface the denial/conflict; the caller may correct input
   and explicitly retry.
3. `unknown`: stop the workflow and perform one read-back by the exact logical
   idempotency key or expected artifact version. If read-back cannot establish
   a commit, preserve the ambiguity and require an explicit new attempt.

There is no automatic compensation tree. A failure while reporting, cleaning
up, or reading back is a diagnostic attached to the original outcome; it does
not erase a committed result or start another recovery path.

Provider invocation remains outside broker artifact idempotency. If a provider
may have completed before its reply was lost, the learning run stops as
`ambiguous`; it never repeats that invocation automatically. A user or root
agent may start a new run with the duplicate-cost risk visible. This is an
accepted operational limitation, not a blocker requiring exactly-once provider
billing.

Application journals are bounded workflow aids, not a second authority store.
If the projection no longer contains enough history to resume a partial learn
run, that run becomes `unrecoverable` and stops. The old journal remains
inspectable; canonical artifacts already committed remain valid. An explicit
new run starts from current broker state and does not reconstruct, compensate,
or silently replay the partial provider/proposal sequence.

### Poisoning and residual risk

An attacker may place instructions in a project file and wait for a later root
agent to write global memory. This EP does not claim to solve that general
prompt-injection problem. It limits the blast radius mechanically:

- an ordinary child cannot manage global memory;
- a session without a read capability cannot exfiltrate that memory;
- a session without a manage capability cannot persist into that scope;
- scope binding and exact plugin identity are host-derived;
- every mutation is versioned and attributable;
- operators can disable the application or narrow future sessions in config.

That residual risk is preferable to a complex policy whose false positives,
workarounds, and review burden eventually become their own failure mode.

## Migration / rollout

1. Accept this authority contract before changing artifact activation.
2. Add broker `AuthoritySet` ceiling/effective projection, narrowing, durable
   audit/status output, and child subset tests without changing sandbox policy.
3. Add exact spawn-time drops and prove recursive non-escalation.
4. Bind every agent to its exact logical Git session resource without granting
   lifecycle-controller authority.
5. Add the exact-kind/scope `stado_artifact_manage` operation and keep all
   other kinds/calls candidate-only.
6. Update the official memory/learn package to use the new operation, remove
   the presenter dependency, and implement terminal `ambiguous`/
   `unrecoverable` outcomes without recovery loops.
7. Complete the independent EP-54/58 search, measurement, signing,
   publication, install, and exact-released-bytes gates before calling the
   package shipped.

Existing staged candidates are not silently activated. After admission, an
authorized agent may explicitly adopt/edit them into active versions. Legacy
active records remain active to preserve their historical authority event.

C86 is resolved by agent admission rather than a separately trusted presenter
only after steps 2-6 ship. C87 and C88 become documented terminal outcomes,
not release blockers; their ambiguity is preserved rather than hidden.

## Failure modes

- Invalid or widening authority profile: reject session/application admission.
- Child asks for global management: hard-drop and report the denied capability.
- Spawn drop names unknown capability: reject the spawn request.
- Plugin identity/kind changes: old rights do not transfer; require explicit
  operator configuration/migration.
- Broker unavailable or scope unresolved: refuse the mutation; never fall back
  to a file writer.
- Concurrent memory edit: return a version conflict and require read/edit/retry.
- Provider reply loss: stop as `ambiguous`; no automatic second charge.
- Journal projection truncation: stop the affected run as `unrecoverable`;
  leave committed artifacts and the journal unchanged.
- Cleanup/reporting failure after commit: preserve the committed receipt and
  attach a diagnostic.

## Test strategy

- Property tests for `AuthoritySet` subset and drop-only narrowing.
- Admission matrix for root, child, grandchild, auto-compacted continuation,
  retained-child restore, other repository, and tool-run sessions.
- Recursive tests proving global management never enters a child and an
  explicitly dropped right never reappears.
- Broker/ABI tests requiring both exact executable capability and session
  resource authority.
- Scope tests proving child self-session writes, ancestor-session read-only,
  retained-session identity, lifecycle-controller separation, repository
  isolation, and root-only global writes.
- Direct-active create/edit/supersede/retire/delete CAS and idempotency tests.
- Negative tests proving non-memory artifact kinds remain candidate-only.
- Crash-boundary tests for committed, not-committed, unknown/read-back,
  ambiguous provider reply, truncated journal, and cleanup diagnostic paths.
- End-to-end tests with the signed official package before publication.

## Open questions

1. Should the first implementation expose the exact authority profile in the
   existing application configuration or in a reusable named broker profile?
2. Should `drop_capabilities` remain memory-resource-only in its first slice,
   or accept the already-existing sandbox/tool vocabulary through one typed
   union once error reporting is equally precise?
3. Should an explicit root-agent adoption action preserve a staged candidate's
   ID, or create a fresh active ID linked through provenance?

## Decision log

### D1. Agents manage memory within admitted scope

- **Decided:** admitted agent sessions may create and evolve active memory
  without a per-item operator grant.
- **Alternatives:** retain candidate-only writes; build a trusted presenter;
  appoint a steward agent.
- **Why:** memory is agent working context, and a fluent reviewer cannot prove
  benign content. Admission and scope are the useful security boundary.

### D2. Storage ownership and content ownership stay separate

- **Decided:** the broker remains the sole canonical writer while agents own
  semantic memory lifecycle through typed calls.
- **Alternatives:** plugin-owned files; direct filesystem memory; a second
  database.
- **Why:** single-writer integrity and agent autonomy solve different problems
  and do not need to be traded against each other.

### D3. Main sessions can manage global memory; children cannot

- **Decided:** root main sessions receive configured global management and
  every ordinary child hard-drops it.
- **Alternatives:** no agent global writes; all descendants inherit; steward-
  only global writes.
- **Why:** root agents need useful persistent memory, while child attenuation
  creates a simple, inspectable blast-radius boundary.

### D4. Repository and self-session memory are child-manageable

- **Decided:** ordinary children may manage current-repository and their own
  session memory unless the parent drops those rights at spawn.
- **Alternatives:** read-only children; per-write approval.
- **Why:** delegated work should be able to preserve project knowledge without
  gaining cross-project or global authority.

### D5. Taint remains provenance only

- **Decided:** no taint propagation or taint-derived capability matrix.
- **Alternatives:** origin/effect policies; file/version taint indexes;
  automatic capability loss after reads.
- **Why:** Stado consumes untrusted data to produce data. Universal taint would
  spread faster than it could be removed and turn ordinary work into policy
  deadlock.

### D6. Ambiguity stops instead of triggering compound recovery

- **Decided:** one read-back is allowed at an atomic boundary; unresolved
  ambiguity stops and requires an explicit retry/new run.
- **Alternatives:** automatic provider replay; compensation workflows; scan
  unbounded journal history to reconstruct intent.
- **Why:** the system cannot manufacture missing knowledge, and extra recovery
  transitions increase the chance of duplicate cost or state corruption.

## Related

- EP-50 Broker
- EP-52 Learn
- EP-53 Versioned Harness Artifacts
- EP-55 Retained and Resumable Sub-agents
- EP-59 Durable Broker Events
- EP-63 Plugin-Defined Harness Artifacts
- EP-64 WASM Lifecycle Applications
