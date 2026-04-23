---
ep: 7
title: Conversation State and Compaction
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [3, 4, 9, 11]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the context-management, compaction, and fork-from-point work that landed across the late v0.0.1 cycle.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Context thresholds, explicit compaction, and read/output budgeting are shipped behaviors.
---

# EP-7: Conversation State and Compaction

## Problem

Long-running coding-agent sessions exhaust context windows, repeat large
reads, and accumulate tool output faster than users can manually keep
the prompt lean. A useful recovery path must preserve the important
facts of a session without silently rewriting history behind the user's
back.

stado also needs conversation controls that work across the TUI,
session CLI, and headless flows without hiding what happened to the
underlying git state.

## Goals

- Surface context pressure before the provider hard-fails.
- Make compaction explicit, reviewable, and auditable.
- Keep historical recovery non-destructive through fork-from-point.
- Reduce repeat prompt bloat from unchanged reads and oversized tool
  output.

## Non-goals

- Automatic background summarization.
- Silent sliding-window eviction of old turns.
- Vector-store memory or semantic importance scoring in the core loop.

## Design

Context usage is tracked against the provider's max context window using
client-side token estimates corrected by provider-reported usage when
available. The user-facing contract is two thresholds:

- a soft threshold for warnings and `/compact` advice
- a hard threshold that blocks fresh turns until the user acts

Compaction is explicit. `/compact` and other live-session compaction
surfaces ask the active provider for a summary, show the proposed
result to the user, allow inline edits, and only rewrite conversation
state after confirmation.
Accepted compactions record dual-ref compaction markers so the compacted
conversation on `tree` and the replaced raw turns on `trace` remain
discoverable.

Historical recovery uses child sessions instead of in-place rewrites.
`session fork --at` and `session tree` expose the same primitive: pick a
turn boundary or commit, materialize a new child session there, and
leave the parent untouched.

Tool output is curated rather than dumped wholesale. Bundled tools apply
per-tool output budgets with explicit truncation markers so the model
can request narrower follow-up reads. The `read` tool supports ranged
reads and in-process deduplication of unchanged content through a
process-local read log keyed by file path and requested range.

## Decision log

### D1. Prefer explicit, user-controlled recovery

- **Decided:** compaction and other history-shaping actions require an
  explicit user action and confirmation.
- **Alternatives:** automatic summarization or silent window eviction.
- **Why:** invisible prompt surgery makes it too hard to reason about
  what the model still knows and what the audit trail should say.

### D2. Recover by forking, not by rewriting the parent

- **Decided:** historical rollback produces a new child session.
- **Alternatives:** mutate the current session back in place.
- **Why:** child sessions preserve provenance and make experimentation
  reversible without destroying the parent record.

### D3. Budget tool output per tool, not with one global blunt limit

- **Decided:** each bundled tool has a sensible default output budget
  and explicit truncation marker.
- **Alternatives:** one global cap for all tools or no truncation until
  the provider rejects the turn.
- **Why:** different tools have very different useful output shapes, and
  the model needs to know when a narrower follow-up request is possible.

### D4. Deduplicate repeated reads in-process

- **Decided:** repeated reads of the same unchanged region return a
  reference response instead of the full bytes again.
- **Alternatives:** no deduplication or a durable memory store.
- **Why:** most value comes from collapsing repeated file reads inside
  one active process; a persistent memory layer would add complexity
  well beyond the core need.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md)
- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [DESIGN.md](../../DESIGN.md#context-management)
- [docs/features/context.md](../features/context.md)
