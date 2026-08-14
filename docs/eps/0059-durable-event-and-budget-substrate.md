---
ep: 59
title: Durable Broker Event and Budget Substrate
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
implemented-in: v0.78.0
type: Standards
created: 2026-08-12
requires: ["EP-0004", "EP-0011", "EP-0050"]
extended-by: ["EP-0067"]
see-also: ["EP-0053", "EP-0055", "EP-0056", "EP-0057"]
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
    note: Added after adversarial review found no safe durability, ordering, or aggregate-budget base for the adaptive-context EPs.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0011](./0011-observability-and-telemetry.md), [EP-0050](./0050-broker.md) · **Extended by:** [EP-0067](./0067-session-controller-and-application-selection.md) · **See also:** [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md)

# EP-0059: Durable Broker Event and Budget Substrate

## Problem

The memory store, session projections, fleet registry, and broker lifecycle have
independent persistence and concurrency assumptions. The current fleet and
broker registries are process-local. Appending JSONL from several CLI/runtime
processes cannot provide compare-and-swap updates, crash-safe compaction, durable
admission, leases, or aggregate recursive budgets.

EP-53 and EP-55–57 require one authoritative serialization point before they can
safely promise versioned artifacts, retained children, mailboxes, or consistent
state projections.

## Goals

- Make the per-user broker the single authoritative writer for adaptive-context
  events and lifecycle transitions.
- Define crash-safe, checksum/hash-chained append, snapshot, and recovery.
- Give each session and store a monotonic sequence and immutable `as_of` snapshot.
- Provide idempotent jobs, admissions, leases, epochs, and fencing.
- Enforce recursive/concurrent budgets through atomic reservation accounting.
- Preserve inspectable export and recovery without making SQLite authoritative.

## Non-goals

- Replacing signed session tree/trace refs.
- A general distributed consensus service or multi-host broker cluster.
- Letting model-facing plugins append authoritative events directly.
- Claiming durability beyond the configured filesystem's guarantees.

## Design

### Ownership

Only the authenticated broker service appends canonical events. CLI, TUI,
headless, ACP, plugins, and child runtimes submit typed requests over versioned
IPC. Offline read/export verifies snapshots and logs; offline writes are refused
while the broker/store is unavailable.

Before opening canonical state, the broker acquires and holds an exclusive
OS-backed lock on a canonical, non-symlinked private store root. Concurrent
startup fails closed. Under the lock it durably increments/publishes the broker
epoch before IPC. Recovery/rotation require the lock; loss is fatal and stops
admission/mutation. Stale sockets or PIDs are never singleton proof.

### Operator-origin authority

Authority-changing operations use a broker primitive unavailable inside model,
plugin, child, or session sandboxes. Authority comes from a fresh operator gesture
on a trusted presentation channel or predeclared operator policy. The one-use grant
binds canonical action digest, final text or capabilities/corpus, immutable scope,
version, actor, nonce, and expiry. Authenticated orchestrator requests and CLI
commands executed from an agent shell are not operator intent. Headless requires
predeclared policy or fails closed.

The broker-owned WAL is authoritative for local global/repo activation under the
OS principal and operator-origin grant. Session traces may reference observed
versions but are not required as an undefined activation anchor.

### Event envelope

```json
{
  "store":"artifact|session|mailbox|lifecycle|budget",
  "sequence":42,
  "event_id":"evt_...",
  "idempotency_key":"...",
  "principal":"local-user-id",
  "timestamp":"broker time",
  "actor":{},
  "causation_id":"optional",
  "previous_digest":"sha256:...",
  "payload_digest":"sha256:...",
  "payload":{}
}
```

The broker serializes validate/fold/CAS/append. Records are newline-delimited
canonical JSON with per-record digest and previous-digest chain. Append success
requires file sync. Creation, rotation, snapshot replacement, and archive
publication sync the parent directory. Stable event IDs/idempotency keys make
retries fold once.

### Recovery and snapshots

An invalid final partial/checksum-failing record is copied to quarantine and
truncated to the last verified boundary; an invalid interior record fails the
store closed. Snapshots record last sequence, chain digest, schema/converter
version, and source archive digest. Publication is temp-write → fsync → rename →
directory fsync while the single writer fences appends. Archived segments remain
digest-linked from the snapshot manifest and are retained under policy.

Derived SQLite projections capture `(snapshot_digest,last_event_sequence)`, scan
through that point, apply tails until caught up, and publish atomically. They never
acknowledge canonical writes. Authoritative point reads use a verified snapshot
plus complete tail; search may wait or return explicit `partial/index_rebuilding`.

### Jobs, admissions, and leases

Long operations use durable jobs with `queued`, `leased`, `running`, `completed`,
`failed`, and `cancelled` events. Admission is appended before process/provider
work. A lease contains generation, broker epoch, runtime nonce, expiry, and owner.
All mutations carry the epoch; stale owners are fenced. PID alone never proves
identity. Recovery resumes/re-admits only according to the job-type policy and
idempotency key.

### Budget ledger

The broker owns reserve/commit/release entries for input/output tokens, monetary
cost, turns, provider calls, tool calls, context bytes opened, wall time, and
child count. Each dimension is marked hard or advisory by the requesting policy.

Child and descendant reservations are bounded by their own quota and the remaining
root/ancestor aggregate account. Concurrent admission reserves atomically.
Provider usage received after cancellation is committed exactly once. Follow-up
and restart do not reset budgets; they consume the existing reservation or require
a new authorized reservation.

### Atomic WAL, chronology, and projections

One canonical broker WAL commits transactions containing all typed events affected
by an operation. Per-store logs and SQLite tables are projections, not co-authorities.
Readers expose committed transactions only. Session sequence allocation occurs in
the WAL commit; recovery replays projections, so a crash between projection writes
cannot publish mixed state.

Each session has one monotonic session-event sequence. State, journal, decisions,
signals, child registry, and message presentation are typed projections over that
chronology, even when materialized into different files/index tables. Multi-part
events commit in one WAL transaction and readers expose `as_of_sequence`.

## Migration / rollout

1. Add read-only verification/export for the new envelope.
2. Route new artifact writes through the broker single writer.
3. Add snapshot/rotation/recovery and derived-index checkpointing.
4. Add durable job/admission/lease machinery.
5. Add budget reservations before retained or concurrent children use it.
6. Migrate other projections only as their dependent EP ships.

## Failure modes

- Broker crashes after append before reply: retry idempotency returns the recorded result.
- Disk full/fsync failure: operation is not acknowledged; store remains at last verified event.
- Interior corruption/hash mismatch: writes and prompt retrieval fail closed; export/quarantine remains available.
- Stale runtime writes after restart: epoch fencing rejects them.
- Budget reservation leaks: expired job reconciliation releases only uncommitted reservation; late usage still commits once.
- Index leaks stale private rows: stale/corrupt index is unpublished/quarantined under private permissions before rebuild.

## Test strategy

- State-machine/property tests for append/CAS/idempotency/snapshot/recovery.
- Fault injection at write, fsync, rename, dir-fsync, reply, and rotation boundaries.
- Concurrent-client serialization and stale-epoch fencing tests.
- Concurrent-daemon, stale-socket, epoch, lock-loss, and split-brain tests.
- Crash injection between every projection write and WAL commit/publication.
- Admission double-launch and runtime-nonce/PID-reuse tests.
- Recursive concurrent budget reservation/late-usage tests.
- Index catch-up, private permissions, quarantine, and parity tests.

## Open questions

- Projection layout is an implementation choice; the canonical WAL/commit boundary is fixed.

## Decision log

### D1. Broker is the single writer

- **Decided:** all authoritative adaptive-context mutations serialize at the broker.
- **Alternatives:** advisory locks shared by every process; SQLite authority.
- **Why:** stado already centralizes session construction there, and fencing/admission
  semantics require an owner that outlives clients.

### D2. One session chronology, several projections

- **Decided:** distinct semantic artifacts share an atomic session event sequence.
- **Alternatives:** unrelated logs with best-effort cross-references.
- **Why:** crash recovery must not expose impossible mixed state.

### D3. Budgets are reservations, not attribution

- **Decided:** atomically reserve aggregate resources before concurrent work.
- **Alternatives:** sum usage after completion.
- **Why:** accounting after the fact cannot enforce a hard recursive ceiling.

## Related

- EP-50 Broker
- EP-53 Harness Artifacts
- EP-55 Retained Sub-agents
