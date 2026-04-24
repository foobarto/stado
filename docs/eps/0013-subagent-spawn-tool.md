---
ep: 13
title: Subagent Spawn Tool
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [3, 4, 6, 10, 11]
history:
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

The full spawn tool design still needs to define the tool schema, child
session model, lifecycle, result format, cancellation, and merge
semantics.

The first shipped slice makes agent selection explicit inside the TUI:
`Ctrl+X A` and `/agents` open a picker for the built-in Do, Plan, and
BTW agents. This does not create child agents yet; it establishes the
user-facing agent-selection surface that future subagents can extend.

The likely shape is a parent-visible tool that creates a child session
rooted in the same repo/worktree context, with a prompt, ownership
description, allowed tool class, and optional write scope. The child
streams or records progress separately and returns a compact result to
the parent.

## Migration / rollout

Start behind an explicit feature flag or disabled tool entry. Land read-only
children before write-capable children.

## Failure modes

- Child agent conflicts with parent or another child.
- Child agent runs too long or consumes too much budget.
- Parent trusts a child result without enough provenance.
- Tool-call audit becomes hard to follow across child sessions.

## Test strategy

- Unit tests for spawn tool schema validation and rejected scopes.
- Runtime tests for child session creation, cancellation, and lineage.
- Integration tests for parent/child transcript persistence.

## Open questions

- Is a child agent a normal session fork, a special session kind, or a
  nested turn inside the parent trace?
- How should write ownership be represented and enforced?
- How many children can run concurrently?
- How are child results summarized without losing critical details?

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
