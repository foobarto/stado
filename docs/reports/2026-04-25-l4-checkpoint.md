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
- Enabled spawn support in TUI, `stado run --tools`, and headless
  `session.prompt` when a live provider, config, and parent session are
  present.
- Updated EP-13 and `CHANGELOG.md` under Unreleased.

## Verification

- Focused packages passed:
  - `go test ./internal/subagent ./internal/runtime ./internal/tui ./internal/headless ./cmd/stado`
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

1. Add explicit cancellation tests for parent-triggered cancellation
   across TUI/headless/run.
2. Define the write-capable worker contract: ownership scopes, conflict
   checks, merge/adoption surface, and review flow.
3. Consider a dedicated subagent activity view in the TUI if raw
   notices are not enough during real use.
4. Consider ACP/editor-facing parity for the headless subagent
   notification shape.
