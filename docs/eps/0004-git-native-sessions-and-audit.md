---
ep: 4
title: Git-Native Sessions and Audit Trail
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [3, 7, 10, 11, 12]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the git-native session design that shipped before v0.0.1 and was later extended through the session CLI work.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Sidecar storage, dual refs, and audit-signing are documented as the current contract.
---

# EP-4: Git-Native Sessions and Audit Trail

## Problem

An agent session needs durable history, isolation from the user's main
repository, and an audit trail of what tools actually did. A plain
database or ad hoc temp directory can store messages, but it does not
compose naturally with diffs, forks, reverts, or git-native review
flows.

stado also needs a strong invariant that user repositories stay
pristine unless the user explicitly lands session output onto a branch.

## Goals

- Keep session state in git-native storage.
- Preserve separate invariants for executable history and audit history.
- Make fork and revert create auditable child sessions.
- Keep the user's repo untouched until an explicit land step.

## Non-goals

- Storing session state in the user's main branch history.
- A database-backed session index as the source of truth.
- Sharing a single audit log across all child sessions.

## Design

The shipped storage model is a sidecar bare repository plus alternates.
For each user repository, stado stores session history in
`$XDG_DATA_HOME/stado/sessions/<repo-id>.git` and points the sidecar at
the user repo object store via git alternates. Session worktrees are
materialized in stado state, not in the user's branch checkout.

`tree` and `trace` are distinct invariants:

- `refs/sessions/<id>/tree` records executable history
- `refs/sessions/<id>/trace` records every tool call as the audit log
- `refs/sessions/<id>/turns/<n>` marks turn boundaries

The commit policy intentionally differs across those refs. Mutating
tools write both `tree` and `trace`. Read-only calls write only
`trace`. Exec calls write `trace` always and `tree` only when the
materialized tree changed. Turn boundaries and accepted compactions may
also write no-file-change commits on `tree` so fork points and
history-shaping events are durable even in pure chat sessions.

Fork and revert use child-session semantics. `session fork`, `session
fork --at`, `session tree`, and `session revert` resolve an existing
tree point, create a new child session rooted there, and materialize a
matching worktree. The parent session is never rewritten in place.

Audit metadata lives in structured trailers and the audit chain is
signed. Tool name, argument hash, result hash, token counts, cache
telemetry, cost, duration, model, agent surface, and turn metadata are
encoded as machine-parseable trailers so `stado audit verify`, export
flows, and session introspection all consume the same signed history.

## Decision log

### D1. Store sessions in a sidecar git repo

- **Decided:** session history lives in a sidecar bare repo with
  alternates into the user repository object store.
- **Alternatives:** write directly into the user's repo or keep session
  state in a database plus temp files.
- **Why:** the sidecar gives stado native diffs, refs, and history
  tooling while keeping the user's repo clean until an explicit land.

### D2. Split executable history from audit history

- **Decided:** `tree` and `trace` remain separate invariants with
  different commit policies.
- **Alternatives:** one ref for everything or one database row per tool
  call.
- **Why:** executable history should reflect actual file state, while the
  audit log must record every call, including failed and read-only ones.

### D3. Make forks and reverts create child sessions

- **Decided:** fork and revert always create child sessions.
- **Alternatives:** rewrite the current session in place or mutate the
  parent worktree to an earlier point.
- **Why:** child sessions preserve provenance, avoid destructive
  rollbacks, and make experimentation easy to audit.

### D4. Use signed, machine-parseable commit trailers

- **Decided:** audit commits carry structured trailers and signatures.
- **Alternatives:** free-form commit bodies, JSON sidecars, or unsigned
  notes.
- **Why:** trailers are easy to inspect in git and easy for tooling to
  parse, while signatures make the audit chain tamper-evident.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md)
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [EP-12: Release Integrity and Distribution](./0012-release-integrity-and-distribution.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [DESIGN.md](../../DESIGN.md#git-native-state-internalstategit)
- [docs/commands/session.md](../commands/session.md)
