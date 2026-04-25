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
    note: Added an explicit runtime adoption apply helper for non-conflicting child changes.
  - date: 2026-04-25
    status: Partial
    note: Added a dry-run child adoption planner that reports parent/child conflicts before any apply step.
  - date: 2026-04-25
    status: Partial
    note: Added worker changed-file reporting and scope-violation collection to the internal workspace_write path.
  - date: 2026-04-25
    status: Partial
    note: Added an internal worker-mode execution path with scoped write/edit tools, while keeping public decode rejection in place.
  - date: 2026-04-25
    status: Partial
    note: Added the scoped write host guard that future worker-mode write/edit tools must use.
  - date: 2026-04-25
    status: Partial
    note: Added write-scope request validation helpers for the future workspace_write rollout.
  - date: 2026-04-25
    status: Partial
    note: Documented the write-capable worker contract: explicit ownership scopes, child-only writes, conflict checks, and no auto-adoption.
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

Surface notifications:

- The runtime emits best-effort child lifecycle events when a child
  starts and finishes.
- Headless maps those to `session.update` notifications:

```json
{
  "kind": "subagent",
  "phase": "started",
  "status": "running",
  "child": "<child-session-id>",
  "childWorktree": "<path>",
  "parentSession": "<git-session-id>",
  "role": "explorer",
  "mode": "read_only",
  "timeout_seconds": 180
}
```

The `finished` notification keeps the same identity fields and reports
`status` as `completed`, `timeout`, or `error`; `error` is included only
when present. These notifications are visibility only. The authoritative
record remains the parent and child trace refs.

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

Next write-capable worker contract:

- Request shape extends the existing tool instead of adding a new tool:

```json
{
  "prompt": "bounded implementation task",
  "role": "worker",
  "mode": "workspace_write",
  "ownership": "human-readable responsibility statement",
  "write_scope": ["internal/foo/**", "docs/foo.md"],
  "max_turns": 6,
  "timeout_seconds": 180
}
```

- `role=worker` and `mode=workspace_write` are valid only when
  `write_scope` is non-empty. Free-form `ownership` remains required
  for the model-facing task summary, but enforcement uses
  `write_scope`, not prose.
- `write_scope` entries are repo-relative path or glob patterns. They
  must stay inside the child worktree and must not target `.git`,
  `.stado`, the sidecar repository, or parent/session metadata outside
  the declared scope.
- Request decoding already normalizes future `write_scope` entries
  before any `workspace_write` mode is exposed: it trims entries,
  normalizes slash-separated paths, deduplicates in request order, and
  rejects empty entries, absolute paths, `..` traversal, backslash paths,
  repository-root scopes, and `.git` / `.stado` metadata segments.
- The file mutating tools already honor an optional host-level
  `WritePathGuard`. The future worker host wraps the normal tool host
  with `ScopedWriteHost`, which resolves write targets through the
  worktree boundary check, rejects symlink escapes and `.git` / `.stado`
  metadata targets, then matches the normalized target against
  `write_scope`. This guard exists before `workspace_write` is exposed so
  write enforcement is below prompt text and tool descriptions.
- The child still forks from the parent tree head. Parent state is never
  modified while the child runs.
- The first write-capable implementation should expose read/search tools
  plus scoped `write` / `edit` / structured code-mod tools. Shell/exec
  should remain unavailable until there is a separate scoped exec policy;
  shell commands are too broad to enforce reliably through path checks
  alone.
- The runtime now has an internal worker execution branch for this tool
  set: read/search, LSP lookup, `write`, and `edit` are kept; shell,
  `ast_grep`, network fetch, and recursive `spawn_agent` are removed.
  This branch is reachable only by direct runtime calls while
  `DecodeRequest` continues to reject `role=worker` /
  `mode=workspace_write` from the public tool surface.
- Runtime enforcement must happen below the model prompt: mutating tools
  must reject writes outside `write_scope`, even if the child prompt
  asks for them.
- Recursive `spawn_agent` remains disabled for write-capable children in
  the first implementation.

Conflict and adoption contract:

- A write-capable child returns a structured result with `status`,
  `child_session`, `worktree`, `summary`, `changed_files`, and
  `scope_violations` if any were attempted.
- The internal worker path now populates `changed_files` from the child
  tree diff against the fork point, filtering session metadata such as
  `.stado/`, `.stado-pid`, and trace context files. Scope guard
  rejections are deduplicated and returned as `scope_violations`.
- The parent receives only the result. Child edits remain in the child
  session until a separate user-visible adoption step.
- There is no automatic merge into the parent session. Adoption should
  be an explicit future command/tool that computes a diff from the
  fork point and applies it only after conflict checks.
- Adoption conflict check: if the parent tree changed a path touched by
  the child since the fork point, adoption blocks and reports the path
  list. The user can inspect the child session, fork again, or manually
  land/rebase.
- The runtime now has a dry-run `PlanSubagentAdoption` helper that
  compares parent and child tree changes against an explicit fork tree,
  returns the child changed files, parent changed files, conflict list,
  and `can_adopt`, and does not mutate either session. This is the
  required gate before an apply/adopt command or tool is exposed.
- The runtime also has `AdoptSubagentChanges`, which re-runs the plan,
  refuses conflicts with a typed error, copies only the child changed
  files into the parent worktree, supports child-side deletions, and
  records `subagent_adopt` trace and tree commits on the parent. No
  public command or tool invokes it yet.
- If a child attempts writes outside `write_scope`, the offending tool
  call is rejected, recorded in the child trace, and reflected in
  `scope_violations`. The child session itself remains valid.

Review flow:

- TUI: show the child notice with changed-file count and attach command;
  do not switch sessions automatically.
- Headless: include `changed_files` and `scope_violations` in the
  finished `subagent` notification when available.
- CLI/run: print the structured tool result; users inspect or land the
  child through normal session commands.

## Failure modes

- Child agent conflicts with parent or another child.
- Write-capable worker writes outside its declared scope.
- Parent adopts child changes over newer parent edits.
- Child agent runs too long or consumes too much budget.
- Parent trusts a child result without enough provenance.
- Tool-call audit becomes hard to follow across child sessions.
- Provider implementations may not be safe for true concurrent
  multi-stream use. The first slice avoids this by running children
  synchronously after the parent stream has ended.

## Test strategy

- Unit tests for spawn tool schema validation and rejected scopes.
- Runtime tests for child session creation, read-only tool filtering,
  conversation persistence, structured timeout results, and
  parent-triggered cancellation.
- Headless tests for `session.cancel` while a child is running, including
  the finished/error subagent notification.
- Future runtime tests for write-scope rejection.
- Integration tests for parent/child transcript persistence.
- Future adoption tests that simulate parent/child edits to the same
  path and assert adoption blocks with a conflict list.

## Open questions

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
