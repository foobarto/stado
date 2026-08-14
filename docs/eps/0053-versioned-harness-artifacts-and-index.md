---
ep: 53
title: Versioned Harness Artifacts and Rebuildable Index
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
implemented-in: v0.78.0
type: Standards
created: 2026-08-12
supersedes: ["EP-0015"]
extended-by: ["EP-0063"]
requires: ["EP-0004", "EP-0015", "EP-0059"]
see-also: ["EP-0016", "EP-0044", "EP-0052", "EP-0054", "EP-0058"]
history:
  - date: 2026-08-12
    status: Implemented
    version: v0.78.0
    note: Shipped in v0.78.0 as part of the memory, context, and continual-harness implementation.
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft.
---

> **Relationships:** **Supersedes:** [EP-0015](./0015-memory-system-plugin.md) · **Extended by:** [EP-0063](./0063-plugin-defined-harness-artifacts.md) · **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0015](./0015-memory-system-plugin.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **See also:** [EP-0016](./0016-learning-self-improvement-plugin.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0054](./0054-addressable-context-and-research-agents.md), [EP-0058](./0058-measured-adaptive-retrieval.md)

# EP-0053: Versioned Harness Artifacts and Rebuildable Index

## Problem

The EP-15 JSONL store safely persists memories and lessons, and its item already
contains tags. Retrieval, however, treats tags as unstructured words, has no
groups, and folds all knowledge into two kinds. It cannot
record how an artifact was surfaced or used, and repeatedly folding/scanning a
growing JSONL log is not a sufficient research substrate.

The solution must improve organization and search without turning an opaque
database or embedding index into the authority for future model behavior.

## Goals

- Generalize memory/lessons into typed, versioned harness artifacts first.
- Give tags and groups stable query semantics.
- Record provenance, evidence, authority, lifecycle, and usage separately.
- Retain an append-only inspectable event log as the source of truth.
- Add a rebuildable SQLite/FTS derived index for fast structured search.
- Preserve existing memories/lessons' current folded semantics and legacy bytes.

## Non-goals

- Automatically trusting generated classifications or tag aliases.
- Replacing project documentation, EPs, or signed executable skills.
- Requiring embeddings or a network service.
- Deleting unused knowledge automatically.

## Design

### Artifact shape

```json
{
  "id": "art_...",
  "version": 3,
  "kind": "memory|lesson",
  "scope": "session|repo|global",
  "scope_binding": {
    "principal":"host-populated local principal",
    "canonical_repo_id":"required for repo",
    "anchor_session_id":"required for session",
    "anchor_fork_point":"optional immutable evidence for session"
  },
  "authority": "candidate|active|legacy_active|rejected|superseded|retired|deleted",
  "summary": "bounded title",
  "content": "bounded body",
  "trigger": "required for behavioral artifacts",
  "tags": ["area:tui", "failure:bad-arguments"],
  "groups": ["stado/verification/tui"],
  "provenance": {"origins":["untrusted"], "created_by":"learn"},
  "evidence_refs": ["session:.../turn:...", "trace:..."],
  "expected_outcome": "optional observable result",
  "validation": "optional validation rule",
  "sensitivity": "normal|private|secret",
  "created_at": "...",
  "updated_at": "...",
  "expires_at": "..."
}
```

`active` replaces overloaded `approved`. Activation is an authority event,
not confidence. Confidence, when present, describes evidence quality and cannot
activate an artifact.

Valid transitions are candidate→active|rejected|deleted;
active→candidate(review)|superseded|retired|deleted; and
legacy_active→candidate|active(operator reactivation)|retired|deleted. Editing an
active artifact creates a candidate version while the prior active version stays
current. Deleted is a terminal tombstone; restoration uses a fresh ID.

Lessons require a trigger. Additional artifact kinds are deferred. A deliberately
small relation vocabulary (`related`, `supports`, `contradicts`, `supersedes`) is
included because cross-memory navigation is an explicit product requirement.
Edges are returned only when both endpoints are authorized in the query context;
hidden endpoint existence, metadata, and counts are never projected.
The host populates immutable scope binding from authenticated context; model,
plugin, and query JSON cannot supply principal/repo/session ancestry. `global`
means the current local broker principal, never all OS users or synced identities.
Session scope matches only when the query session equals `anchor_session_id` or
that anchor is in its broker-resolved ancestors. It never reaches an ancestor or
sibling. Migration maps old `session_id` to the anchor and quarantines records
whose anchor cannot be validated.

### Event log

EP-53 explicitly supersedes EP-15's plugin-owned storage decision. Canonical
artifact authority is core/broker-owned; plugins remain candidate producers and
optional search/reranking adapters. The canonical store is broker-owned under
`${XDG_STATE_HOME}/stado/artifacts/` and follows EP-59, with events:

```text
artifact.create, artifact.edit, artifact.activate, artifact.reject,
artifact.supersede, artifact.retire, artifact.delete,
tag.alias, group.define,
observation.record, review.record
```

Every event uses EP-59 single-writer CAS, chain, sequence, idempotency, sync,
snapshot, archive, and corruption recovery. Activation authority is the EP-59 WAL
under the local OS principal plus a consumed operator-origin grant. Session traces
may reference observed versions; they are not required authority roots for
CLI/global/repo activation.

### Tags and groups

Tags use Unicode NFKC then normalized lowercase tokens with optional namespaces;
length/count/reserved-namespace caps are host policy. Items may have
many tags. Alias events map variant tags to a canonical tag for queries without
rewriting historical events. Aliases are principal/scope-bound, version-checked,
acyclic, and never aggregate names/counts across sensitivity or scope boundaries.

Groups are stable hierarchical labels, not owning folders. Membership is
many-to-many. Group deletion retires the label but does not delete artifacts.
Groups carry an optional description, immutable scope binding, sensitivity, and
provenance. Hierarchy is presentation-only in the initial slice; ancestor matching
is not implied. Rename appends an alias; retirement preserves membership history.

### Usage observations

Usage is an append-only observation separate from artifact content:

```json
{
  "artifact_id":"art_...",
  "event":"considered|surfaced|opened|cited|followed|contradicted|helped|failed",
  "session_id":"...",
  "turn":12,
  "evidence_ref":"trace:..."
}
```

The host records mechanical events; evaluative events such as `helped` require
an external gate, operator action, or clearly labeled model judgment.

### Sensitivity, retention, and quotas

Sensitivity is the host-enforced lattice `normal < private < secret`. Derived
fields, indexes, tags/groups, observations, and research results inherit the
maximum source sensitivity; models cannot downgrade it. Secret/private bodies are
encrypted when retained, excluded from ordinary FTS/prompt retrieval, and never
duplicated into traces. Metadata may itself be sensitive. Known-secret scanning
is best effort, not a boundary.

Per-principal/repo/session quotas bound items, event bytes, tags/groups, and
observations. At cap automatic proposals fail closed while inspect/export/retire/
compact remain available. Retention distinguishes signed audit obligations from
model readability and supports key erasure for encrypted payloads where policy
does not mandate retention.

### Derived SQLite index

`${XDG_CACHE_HOME}/stado/memory/index-v1.sqlite` contains folded artifacts,
FTS5 content, normalized tags, groups, and usage aggregates. It
contains no authority not reconstructible from the canonical log.

EP-59 defines checkpoint/catch-up/publication. Authoritative point reads always
use a complete verified snapshot+tail. Search waits or returns explicit partial
status during rebuild. Index files/directories are private; quarantine removes
old private rows from the query path. Private/secret bodies are excluded from FTS
unless the operator explicitly enables an encrypted private index.

Embeddings may be supplied later as another rebuildable index; they are not in
the initial contract.

## Migration / rollout

- Read and verify the old `memory/memory.jsonl`, preserve exact current folded
  semantics, and retain untouched old bytes as an archive whose digest/schema/
  converter version is anchored in the new genesis event. Historical edit/actor
  equivalence is promised only where uncompacted old events still contain it.
- Preserve IDs through an alias table so CLI references remain explainable.
- Convert approved ordinary memories to `active`; approved lessons become the
  migration-only `legacy_active` state requiring operator reaffirmation. Preserve
  candidate, rejected, superseded, and deleted states exactly.
- Existing tags migrate after normalization with alias events for changed spellings.
- Migration derives immutable bindings from trusted old fields/current principal.
  Unbindable/corrupt items are quarantined, never broadened. Behavioral
  `legacy_active` items require explicit reactivation under the new review model.
- Migration is one-way, transactional, idempotent, and refuses downgrade/mixed
  old+new writers; failure leaves the old store untouched.

## Failure modes

- Index corruption: quarantine and rebuild from events.
- Concurrent edits: version conflict returned; caller reloads and retries.
- Alias cycle: reject at write and detect during rebuild.
- Store exceeds limits: reads/export/rebuild remain available while writes fail
  with an actionable compaction instruction.
- Compaction/archive mismatch: retain original log and abort replacement.

## Test strategy

- Property tests for event folding, compaction, aliases, and groups.
- Migration golden tests covering every old action and terminal tombstones.
- SQLite deletion/corruption/staleness/rebuild tests.
- Concurrent optimistic-version and crash-atomicity tests.
- Scope, sensitivity, provenance, and relation-leak security tests.
- Search parity tests between fallback folding and SQLite results.
- Fault tests for truncation, duplicate IDs, clock rollback, tag collision,
  tombstone resurrection, partial migration, disk full, and concurrent clients.

## Open questions

- Whether canonical event archives should later receive user signing is deferred;
  evidence already points into signed session refs.

## Decision log

### D1. JSONL authority, SQLite acceleration

- **Decided:** retain append-only events as authority and use SQLite only as a
  rebuildable derived index.
- **Alternatives:** SQLite as source of truth; Git refs per memory; JSONL scans only.
- **Why:** this preserves inspectability and recovery while providing FTS and
  relational query performance.

### D2. Tags and groups are distinct

- **Decided:** model descriptors and stable collections separately; typed
  relation vocabulary stays deliberately small.
- **Alternatives:** encode everything as tags or folders; ship relations now.
- **Why:** tags and group cardinality/lifecycle differ; relations need demonstrated
  queries and cross-scope rules first.

### D3. Authority is not confidence

- **Decided:** activation state and evidence confidence are separate fields.
- **Alternatives:** retain EP-15's overloaded `confidence` state machine.
- **Why:** a high-confidence model judgment still cannot authorize prompt injection.

### D4. Usage is observational

- **Decided:** record use/outcome events separately from artifact versions.
- **Alternatives:** increment mutable counters on each item.
- **Why:** append-only observations remain auditable and support later policy changes.

## Related

- EP-15 Memory System Plugin
- EP-52 Learn
- EP-58 Adaptive Retrieval
