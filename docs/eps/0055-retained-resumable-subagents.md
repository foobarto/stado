---
ep: 55
title: Retained and Resumable Sub-agent Sessions
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
requires: [4, 14, 38, 50, 59]
see-also: [13, 33, 34, 36, 38, 54, 56]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft consolidating the superseded spawn/fleet designs.
---

# EP-0055: Retained and Resumable Sub-agent Sessions

## Problem

Current `agent__spawn` creates a synchronous child fork with bounded work, but
the parent cannot retain and follow up with it, spawn from an arbitrary historical
session point, select a policy-approved model/tool profile, or recover the child
relationship after restart. CLI session resume re-enters a session interactively
but is not a safe orchestration primitive.

## Goals

- Create children from the active parent or a selected historical session point.
- Never reactivate or mutate historical source identity in place.
- Support policy-controlled model, persona, tools, and capability requests.
- Retain child handles across turns, compaction, detach, and restart.
- Support synchronous, asynchronous, and follow-up work with attributable usage.
- Preserve one agent per session and capability attenuation.

## Non-goals

- Shared mutable model context or worktree between agents.
- Capability inheritance based on prompt text.
- Letting a child silently adopt its changes into a parent.
- Arbitrary cross-user or cross-repo session attachment.

## Design

### Spawn request

```json
{
  "prompt":"bounded purpose",
  "source":{"session_id":"optional", "at":"last_committed_turn|turns/N"},
  "execution":"wait|retained",
  "role":"explorer|worker",
  "model":"optional configured model",
  "persona":"optional",
  "tool_profile":"operator-defined named profile",
  "narrow_tools":[],
  "write_scope":[],
  "max_turns":8,
  "token_budget":30000,
  "timeout_seconds":300
}
```

The orchestrating session and historical source are separate. The broker first
resolves a signed immutable `ForkPoint`: source session/generation, committed turn,
conversation digest and compaction lineage, tree commit, trace commit, artifact/
session event sequence, and resolution time. Mutable selectors are never stored;
`last_committed_turn` serializes at the source turn boundary. A fork sees only
events causally visible at that sequence unless separately authorized.

The child ceiling is the intersection of global policy, orchestrator ceiling and
effective delegation allowance, named role/tool profile, optional narrowing,
source-data ACL, and write scope. Historical source contributes context only,
never authority. Model-callable spawn cannot create a new privilege root; widening
requires distinct operator admission outside this child relationship.

A requested model must be configured and allowed by operator policy. Provider
credentials remain host-side. Tool profiles expand to concrete signed tools
before ceiling projection; prompts cannot add tools.

### Durable handle registry

The broker stores EP-59 events with durable admission ID, generation, lease epoch,
runtime nonce, budget reservation, child ID,
source, purpose, status generation, ceiling digest, model, usage attribution,
and mailbox address. Handles survive compaction and restore. A tombstone removes
addressability without deleting the child transcript or audit evidence.

### Lifecycle

States are `admitted`, `starting`, `running`, `idle`, `completed`, `failed`,
`cancelled`, `expired`, `down`, `deletion_requested`, `archived`, and `deleted`.
EP-59 lease/epoch CAS prevents double launch and stale transitions. `completed`
describes the model task; `down` the runtime generation; archival the session.

Follow-ups use EP-56 messaging. A completed retained child can become active for
a new turn only through an explicit follow-up admitted under its existing
ceiling. Any wider purpose creates another child.

### Changes and usage

Workers remain isolated worktrees. Adoption is explicit. EP-59 atomically reserves
child/root aggregate budgets before admission; descendants, follow-ups, and restarts
cannot reset or escape them. Attribution is accounting layered on enforcement.

Kill/delete/GC transition broker lifecycle first. Live deletion becomes
`deletion_requested` → cancel → down → cleanup. Removing a parent-visible handle
does not make a live orphan uncontrollable, and deleting a source cannot destroy
the retained child's immutable fork evidence.

## Migration / rollout

- Extend current synchronous spawn request/result internally, then replace its
  pre-v1 public schema in one release.
- Backfill existing child lineage into read-only legacy registry entries where
  discoverable from session metadata.
- Add retained execution only after durable registry recovery tests pass.
- Add historical-source spawning after source/fork authorization is centralized
  in the broker.

## Failure modes

- Source session changes while admission occurs: resolve immutable turn/tree SHA.
- Parent restarts during child launch: generation-tagged registry reconciliation
  adopts the live child or records terminal failure; it never double-spawns.
- Requested model/tool unavailable: fail admission rather than substitute silently.
- Child asks to widen scope: deny and require a new projected child.
- Orphan consumes resources: broker-enforced budgets and leases terminate it.
- Attribution arrives twice: event IDs make folding idempotent.

## Test strategy

- Active-tip and historical-turn fork tests with source immutability assertions.
- Model/persona/tool/ceiling policy matrix and attenuation tests.
- Crash points around admission, registry append, process start, and restore.
- Retained follow-up, cancellation, tombstone, and usage-attribution tests.
- Adoption isolation and scope-violation tests.
- Daemon/TUI/headless end-to-end recovery tests.

## Open questions

- The initial custom tool-profile grammar may be limited to named operator-defined
  profiles to avoid reproducing arbitrary capability config in model arguments.

## Decision log

### D1. Resume means fork into a new identity

- **Decided:** historical continuation always creates a new child session.
- **Alternatives:** reactivate and mutate the old session in place.
- **Why:** source evidence and prior ceilings remain immutable and auditable.

### D2. Orchestrator and source are separate

- **Decided:** record both who requested work and which context seeded it.
- **Alternatives:** make the source session implicitly own the child.
- **Why:** research and recovery often use historical context on behalf of a
  different active parent.

### D3. No silent model fallback

- **Decided:** unavailable requested settings fail admission.
- **Alternatives:** inherit or choose a nearby model silently.
- **Why:** model choice affects cost, capability, and reproducibility.

## Related

- EP-4 Git-Native Sessions
- EP-38 ABI v2 Runtime
- EP-50 Broker
