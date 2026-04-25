---
ep: 13
title: Subagent Spawn Tool
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [3, 4, 6, 10, 11]
history:
  - date: 2026-04-25
    status: Partial
    note: Added the first spawn_agent implementation: synchronous read-only child sessions with explicit child audit/conversation state.
  - date: 2026-04-24
    status: Placeholder
    note: Captures the request for parallel agent work through a spawn/subagent tool.
  - date: 2026-04-24
    status: Partial
    note: Added TUI agent-selection groundwork for Do, Plan, and BTW; spawn tool remains unimplemented.
---

# EP-13: Subagent Spawn Tool

## Problem

stado currently runs one agent loop per interactive turn. Complex tasks
often contain independent investigations or implementation slices that
could progress in parallel, but the model has no first-class way to
spawn bounded sidecar agents and later integrate their results.

Ad hoc parallelism would be risky: child agents need clear ownership,
resource limits, session lineage, tool permissions, and result
boundaries so they do not silently overwrite each other or the parent.

## Goals

- Add a first-class spawn/subagent tool surface for parallel work.
- Preserve session/audit lineage for every child agent.
- Make ownership, scope, and result handoff explicit.
- Support both read-only exploration and bounded implementation work.

## Non-goals

- Unlimited background agents with no supervision.
- A generic distributed job system.
- Letting child agents bypass the parent session's sandbox, approval,
  budget, or telemetry policy.

## Design

The first runtime slice adds a native `spawn_agent` tool. It is native
instead of WASM-backed because it needs the live provider, config, and
session fork primitive rather than only the plugin host imports.

Tool request:

```json
{
  "prompt": "self-contained child task",
  "role": "explorer",
  "mode": "read_only",
  "ownership": "optional file/module scope",
  "max_turns": 6,
  "timeout_seconds": 180
}
```

Only `role=explorer` and `mode=read_only` are executable in this slice.
`max_turns` defaults to 6 and is capped at 12. `timeout_seconds`
defaults to 180 and is capped at 900; zero means the default, not
unlimited. Unsupported roles or write modes are rejected before any
child session is created.

Execution model:

- The parent tool call forks a normal child session from the parent tree
  head.
- The child conversation is seeded with the requested task plus explicit
  read-only instructions.
- The child runs synchronously inside the parent tool call. This avoids
  parent-session re-entrancy and keeps the parent waiting for one
  deterministic tool result.
- The child runs under its own timeout derived from `timeout_seconds`.
  Parent cancellation still cancels the child immediately.
- The child executor removes mutating/exec tools and also removes
  `spawn_agent`, so first-slice children cannot edit files, run shell
  commands, or recursively spawn more children.
- The child result is returned as JSON containing status, role, mode,
  child session ID, worktree path, timeout, final text, message count,
  and optional error. Child timeout returns `status: "timeout"` instead
  of making the parent tool call fail, so the parent can reason about
  partial or missing findings and decide what to do next.

Audit and persistence:

- The parent session records the `spawn_agent` tool call in its trace ref
  through the normal executor path.
- The child session records a `spawn_agent` trace marker before running.
- Any child read/search/tool calls are committed to the child trace ref.
- The child conversation log is written under the child worktree, so the
  user can attach to the child session after the parent receives the
  summary.

Earlier TUI groundwork made agent selection explicit: `Ctrl+X A` and
`/agents` open a picker for the built-in Do, Plan, and BTW agents. The
runtime `spawn_agent` tool is separate from that picker for now. When a
TUI parent receives a successful `spawn_agent` tool result, it also
renders a system block with the child status, session ID, worktree, and
attach command so the child is visible beyond the raw JSON tool result.
Future UI work can surface child sessions in the same agent/session
family.

## Migration / rollout

The first executable rollout is read-only and synchronous. Write-capable
children require a follow-up contract for ownership enforcement, conflict
detection, result adoption, and cancellation.

## Failure modes

- Child agent conflicts with parent or another child.
- Child agent runs too long or consumes too much budget.
- Parent trusts a child result without enough provenance.
- Tool-call audit becomes hard to follow across child sessions.
- Provider implementations may not be safe for true concurrent
  multi-stream use. The first slice avoids this by running children
  synchronously after the parent stream has ended.

## Test strategy

- Unit tests for spawn tool schema validation and rejected scopes.
- Runtime tests for child session creation, read-only tool filtering,
  conversation persistence, and structured timeout results.
- Future runtime tests for cancellation and write-scope rejection.
- Integration tests for parent/child transcript persistence.

## Open questions

- Write-capable children: how should write ownership be represented and
  enforced?
- How many children can run concurrently?
- How are child results summarized without losing critical details?
- Should TUI display a dedicated subagent activity view instead of only
  the parent tool result plus attachable child-session notice?

## Decision log

### D1. Capture as a Standards EP

- **Decided:** this feature requires a Standards EP before implementation.
- **Alternatives:** keep it as an informal backlog item.
- **Why:** it changes tool contracts, session topology, audit semantics,
  and TUI/runtime behavior.

## Related

- EP-3 Provider-Native Agent Interface
- EP-4 Git-Native Sessions and Audit Trail
- EP-10 Interop Surfaces
