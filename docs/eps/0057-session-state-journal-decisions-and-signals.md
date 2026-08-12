---
ep: 57
title: Session State, Journal, Decisions, and Signals
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
implemented-in: v0.78.0
type: Standards
created: 2026-08-12
requires: ["EP-0004", "EP-0011", "EP-0021", "EP-0059"]
see-also: ["EP-0007", "EP-0009", "EP-0047", "EP-0052", "EP-0053"]
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
    note: Initial draft internalizing the project's state and journal discipline.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0011](./0011-observability-and-telemetry.md), [EP-0021](./0021-assistant-turn-metadata.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **See also:** [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0047](./0047-structured-loop-result-and-output.md), [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md)

# EP-0057: Session State, Journal, Decisions, and Signals

## Problem

Conversation, audit commits, and file history preserve what happened but do not
provide a clean current operational state, human-readable chronology, durable
decision record, or normalized observations suitable for learning. Project-local
`.agent/STATE.md`, journals, and ADRs demonstrate the value, but their semantics
depend on prompt discipline and cannot drive reliable lifecycle behavior.

## Goals

- Separate current operational state, chronology, decisions, and detected signals.
- Generate mechanical facts host-side where possible.
- Make each stream durable, queryable, fork-aware, and compaction-safe.
- Give `/learn` structured evidence without treating interpretations as facts.
- Keep prompt injection bounded and selective.

## Non-goals

- Replacing conversation or security trace logs.
- Capturing hidden chain-of-thought.
- Automatically converting every event into a memory.
- Requiring repositories to commit `.agent/`.
- Letting model-authored state grant authority.

## Design

### Four artifacts

1. **State** — versioned current projection: objective, mode, work in progress,
   blockers, next actions, pending decisions, validation, active children.
2. **Journal** — append-only chronology of material actions, observations, and
   transitions, with host events plus optional agent/operator annotations.
3. **Decisions** — versioned records of context, choice, alternatives,
   consequences, evidence, status, and supersession.
4. **Signals** — normalized observations that may form a repeated pattern but are
   not yet lessons or conclusions.

They are typed projections over one EP-59 per-session chronology and are
digest-linked from signed turn/trace metadata. Forks capture an immutable base
sequence/digests and append local events.

### State contract

Initial model-writable state is limited to current task, blockers, and next step,
with hard caps. Host goal/session/capabilities/cost/children/verification fields
are immutable to the model. Operator/host commitments and model hypotheses are
separate. Model state is untrusted quoted working context, expires unless accepted,
and never gains instruction priority.

State updates use optimistic version checks and typed patches. Host-owned fields
(session, capabilities, token/cost totals, child status, verification results)
cannot be model-written. Model fields are labeled assertions. A compact bounded
state view may enter each turn; full history is queried explicitly.

### Journal contract

The journal is a human-readable projection of canonical trace/broker/artifact
events plus explicit annotations, not a second event authority. Host events include
turn boundaries, tool outcomes, approach switches when
explicitly signaled, validation, adoption, compaction, learning reviews, message
events, and state/decision changes. The journal references conversation and trace
content rather than duplicating large payloads.

### Decision contract

```json
{
  "id":"dec_...",
  "status":"proposed|accepted|superseded|rejected",
  "context":"...",
  "decision":"...",
  "alternatives":[],
  "consequences":[],
  "evidence_refs":[],
  "scope":"session|repo",
  "created_by":"operator|agent",
  "version":1
}
```

Agent decisions are model hypotheses unless operator accepted. The first slice
keeps them session-local and non-injected; repo artifact promotion is deferred.

### Signal contract

Mechanical signal types initially include:

```text
tool_failure, repeated_call, argument_changed_after_failure,
command_succeeded_after_retry, verification_failed/passed,
permission_denied, scope_violation, file_reverted,
memory_considered/opened/cited, user_correction_candidate,
assumption_disproved_candidate, child_disagreement, goal_stalled
```

Each signal records origin events, deterministic detector version, confidence
class (`fact` or `inference`), and bounded attributes. Model interpretation is
always `inference`. Signals expire or aggregate; they never enter prompts raw by
default.

### Shapes

The first slice has five host-versioned deterministic shapes: repeated tool
failure; argument changed then success; verification fail then pass; recurring
permission/scope denial; and explicit operator correction marker. General detector
DSL/model-discovered shapes are deferred. Signals have per-type TTL and sample/
occurrence/store caps; aggregation keeps evidence references, not duplicated prose.

## Migration / rollout

- Build projections from new events; do not attempt to hallucinate complete
  historical state for old sessions.
- Old sessions expose conversation/trace-derived read-only legacy state and begin
  native streams on their next turn.
- Add journal/state query commands before injecting bounded state into prompts.
- Add deterministic signals incrementally with detector-version fixtures.

## Failure modes

- State patch race: version conflict, reload required.
- Journal duplication after crash: stable source-event IDs make append idempotent.
- Detector false positive: inference/fact labels and evidence remain visible; only
  `/learn` interpretation can propose an artifact.
- State bloat: schema caps lists and archives completed items by reference.
- Sensitive payload duplication: streams store references and redacted attributes.
- Fork ambiguity: every event identifies base session/turn and local generation.

## Test strategy

- Projection and fork inheritance property tests.
- Crash/idempotency/version-conflict tests.
- Detector golden fixtures and adversarial near-misses.
- Provenance, secret-redaction, and host-field immutability tests.
- Compaction/resume tests proving active state and evidence survive.
- Prompt-budget assertions for bounded state views.

## Open questions

- Which project `.agent/` files should later have explicit import/export adapters
  is deferred; runtime semantics must stabilize first.

## Decision log

### D1. Four distinct persistence semantics

- **Decided:** state, journal, decisions, and signals are separate typed streams.
- **Alternatives:** one universal event log rendered differently; Markdown files only.
- **Why:** authority, mutability, prompt use, and lifecycle differ even if storage
  shares an event substrate.

### D2. Facts and inferences are explicit

- **Decided:** detectors label mechanical facts separately from interpretations.
- **Alternatives:** present all detected patterns as equivalent observations.
- **Why:** learning and security decisions need to know what the host observed
  versus what a model inferred.

### D3. No chain-of-thought capture

- **Decided:** persist actions, outcomes, concise annotations, and decisions only.
- **Alternatives:** store private reasoning traces as journal state.
- **Why:** they are unnecessary, provider-dependent, sensitive, and unreliable as facts.

## Related

- EP-4 Git-Native Sessions
- EP-52 Learn
- EP-53 Harness Artifacts
