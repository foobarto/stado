# L4 Checkpoint - 2026-04-25

This note preserves the autonomous-loop state after the EP-13 read-only
subagent slice.

## Implemented

- Added a native `spawn_agent` tool contract in `internal/subagent`.
- Wired `spawn_agent` into the default tool registry as a native tool
  because it needs live provider/config/session orchestration.
- Added `runtime.SubagentRunner`, which forks a child session and runs a
  synchronous read-only child agent loop.
- Child sessions get a seeded conversation log, a child trace marker, and
  normal audit commits for any child tool calls.
- Child executors remove mutating/exec tools and remove `spawn_agent` to
  prevent first-slice recursion.
- `spawn_agent` accepts `timeout_seconds`; child loops run under a
  wall-clock timeout and return `status: "timeout"` with child identity
  when that timeout fires.
- The TUI parses successful `spawn_agent` tool results and adds a
  visible child-session notice with status, worktree, and attach
  command.
- Headless emits `session.update` notifications with `kind:
  "subagent"` for child start/finish, including child ID, child
  worktree, role, mode, status, and timeout.
- Parent-triggered cancellation is covered at runtime and headless
  boundaries: cancelling the parent cancels the child and emits a
  finished/error subagent event.
- EP-13 now defines the future write-capable worker contract:
  `role=worker`, `mode=workspace_write`, required `write_scope`,
  child-only writes, conflict checks, and explicit adoption.
- Added `write_scope` request normalization for the future worker mode:
  repo-relative path/glob scopes are trimmed, normalized, deduplicated,
  and rejected if they use absolute paths, traversal, backslashes,
  repository-root scopes, or `.git` / `.stado` metadata segments.
- Added the host-level write-scope enforcement layer for future
  `workspace_write`: `write` and `edit` honor `tool.WritePathGuard`, and
  `subagent.ScopedWriteHost` checks normalized targets against
  `write_scope` while rejecting symlink escapes and `.git` / `.stado`
  metadata targets.
- Built the internal runtime worker path behind the public decode
  rejection: direct runtime tests can run a `role=worker`,
  `mode=workspace_write` child with read/search plus scoped `write` and
  `edit`, while `spawn_agent` tool decoding still rejects worker mode.
- Added internal worker result reporting: `changed_files` comes from the
  child tree diff against the fork point with session metadata filtered
  out, and `scope_violations` comes from deduplicated scoped-write guard
  rejections.
- Enabled spawn support in TUI, `stado run --tools`, and headless
  `session.prompt` when a live provider, config, and parent session are
  present.
- Updated EP-13 and `CHANGELOG.md` under Unreleased.

## Verification

- Focused packages passed:
  - `go test ./internal/subagent ./internal/runtime ./internal/tui ./internal/headless ./cmd/stado`
  - `go test ./internal/subagent`
  - `go test ./internal/subagent ./internal/tools/fs ./pkg/tool`
  - `go test ./internal/runtime ./internal/subagent`
  - `go test ./internal/subagent ./internal/runtime ./internal/state/git`
- Full suite passed:
  - `go test ./...`
- Whitespace check passed:
  - `git diff --check`

The local shell did not have `go`/`gofmt` on `PATH`; commands used the
repo-compatible Go toolchain at
`~/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.9.linux-amd64/bin/`.

## Current State

- Branch: `main`
- Worktree contains the EP-13 read-only spawn changes.
- No release tag has been cut for this slice.

## Next Candidates

1. Design and implement explicit child-change adoption/conflict checks
   before exposing `workspace_write`.
2. Consider a dedicated subagent activity view in the TUI if raw
   notices are not enough during real use.
3. Consider ACP/editor-facing parity for the headless subagent
   notification shape.
