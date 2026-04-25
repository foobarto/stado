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
- Added a dry-run adoption planner: `PlanSubagentAdoption` compares
  parent and child changed files against the fork tree, reports
  conflicts, and does not mutate either session.
- Added internal adoption apply: `AdoptSubagentChanges` re-runs the
  planner, refuses conflicts, copies only child changed files into the
  parent worktree, supports child-side deletions, and commits
  `subagent_adopt` trace/tree metadata.
- Added the explicit `stado session adopt <parent-id> <child-id>`
  command. It dry-runs by default, accepts `--fork-tree` from the worker
  result, supports `--json`, and requires `--apply` before mutating the
  parent.
- Exposed `role=worker`, `mode=workspace_write` in `spawn_agent` with
  required `ownership` and normalized `write_scope`. Worker child writes
  remain isolated in the child session until `stado session adopt`
  applies them.
- TUI worker notices now show changed-file and scope-violation counts plus
  an adoption command. Headless finished subagent notifications include
  `forkTree`, `changedFiles`, and `scopeViolations` when present.
- ACP `session/update` notifications now mirror the subagent lifecycle
  payload, including worker `forkTree`, `changedFiles`, and
  `scopeViolations`.
- Headless and ACP finished-worker notifications now include
  `adoptionCommand` when changed files are present.
- TUI subagent lifecycle events now populate a sidebar `Subagents`
  activity section with child status, changed-file counts, scope
  violations, and adoption readiness.
- TUI session switching now preserves per-session provider/model
  selection and resets provider probes when a restored session uses a
  different provider.
- TUI session switching now blocks while background plugin ticks are
  running or queued, keeping in-process inactive sessions execution-free.
- Inline `@` completion now groups root Markdown docs and `docs/**/*.md`
  before ordinary file matches.
- Inline `@` completion now groups top-level Go symbols before ordinary
  file matches and inserts `path:line` references.
- Landing view now samples the large ANSI/plain banner down to a compact
  fixed-height mark, keeping the prompt primary on wide terminals and
  falling back to the plain wordmark in cramped terminals.
- Theme switching now supports direct `/theme light`, `/theme dark`, and
  `/theme toggle` shortcuts in addition to the picker and explicit
  bundled theme IDs.
- Status modal rows now show next-step hints for focused commands or
  config files, including provider/model, tools, plugins, MCP, OTel,
  budget, and context rows.
- Assistant turn footers now have expandable details behind `Shift+Tab`,
  covering token deltas, cache read/write deltas, requested tools, and
  a session trace command hint when available.
- `/subagents` now renders a dedicated recent-child overview with full
  child IDs, worktrees, status, changed-file counts, scope violations,
  and adoption commands.
- `/providers` now shows runner-specific remediation when a reachable
  local backend has no models loaded, including LM Studio's `lms load`
  path.
- Assistant markdown rendering now picks Glamour's light or dark style
  from the active theme background luminance and clears the markdown
  renderer cache on theme switch.
- Headless/ACP command docs and CLI help now document the `subagent`
  lifecycle payload, worker update fields, and explicit
  `stado session adopt` review flow.
- Enabled spawn support in TUI, `stado run --tools`, and headless
  `session.prompt` when a live provider, config, and parent session are
  present.
- Updated EP-13, EP-14, EP-20, and `CHANGELOG.md` under Unreleased.

## Verification

- Focused packages passed:
  - `go test ./internal/subagent ./internal/runtime ./internal/tui ./internal/headless ./cmd/stado`
  - `go test ./internal/subagent`
  - `go test ./internal/subagent ./internal/tools/fs ./pkg/tool`
  - `go test ./internal/runtime ./internal/subagent`
  - `go test ./internal/subagent ./internal/runtime ./internal/state/git`
  - `go test ./internal/runtime`
  - `go test ./cmd/stado ./internal/runtime`
  - `go test ./internal/subagent ./internal/runtime ./internal/tui ./internal/headless`
  - `go test ./internal/acp`
  - `go test ./cmd/stado`
  - `go test ./internal/runtime ./internal/headless ./internal/acp`
  - `go test ./internal/tui ./internal/runtime ./internal/subagent`
  - `go test ./internal/tui -run 'TestSwitchToSession'`
  - `go test ./internal/tui`
  - `go test ./internal/tui/filepicker ./internal/tui`
- Full suite passed:
  - `go test ./...`
- Whitespace check passed:
  - `git diff --check`

The local shell did not have `go`/`gofmt` on `PATH`; commands used the
repo-compatible Go toolchain at
`~/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.9.linux-amd64/bin/`.

## Current State

- Branch: `main`
- Latest committed slices include the EP-13 scoped worker
  spawn/adoption flow, EP-14 provider/session policy fixes, EP-20
  docs/symbol completion, landing-logo refinement, and direct
  light/dark theme shortcuts, status-modal action hints, and expandable
  assistant turn details, the `/subagents` overview, and local-runner
  no-model remediation, and theme-aware markdown rendering.
- Live worker dogfood was attempted with local LM Studio auto-detect,
  but the provider returned `No models loaded`; rerun after loading a
  local model.
- No release tag has been cut for this slice.

## Next Candidates

1. Exercise the exposed worker flow in a manual dogfood run and refine
   TUI/headless adoption ergonomics based on the transcript.
2. Add richer adoption affordances to editor/ACP clients once a client
   consumes the new subagent notification payload.
