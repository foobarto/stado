# L4 Checkpoint - 2026-04-25

This note preserves the autonomous-loop state after the EP-13 subagent
spawn/adoption slices and the local worker dogfood pass.

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
- EP-13 now defines the write-capable worker contract:
  `role=worker`, `mode=workspace_write`, required `write_scope`,
  child-only writes, conflict checks, and explicit adoption.
- Added `write_scope` request normalization for worker mode:
  repo-relative path/glob scopes are trimmed, normalized, deduplicated,
  and rejected if they use absolute paths, traversal, backslashes,
  repository-root scopes, or `.git` / `.stado` metadata segments.
- Added the host-level write-scope enforcement layer for
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
- Worker `spawn_agent` tool results now include `adoption_command` when
  changed files are present, so the parent model receives the exact
  `stado session adopt ... --apply` command instead of deriving it from
  child IDs.
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
- Inline `@` completion now also groups top-level Python `class`, `def`,
  and `async def` declarations as symbol rows that insert `path:line`
  references.
- Inline `@` completion now also groups top-level JavaScript and
  TypeScript class, function, and variable declarations as symbol rows.
- The JavaScript/TypeScript symbol scanner skips indented nested
  declarations so the `@` picker keeps its top-level-symbol contract.
- Inline `@` completion now also groups top-level `.sh`/`.bash`
  functions as symbol rows.
- Landing view now samples the large ANSI/plain banner down to a compact
  fixed-height mark, keeping the prompt primary on wide terminals and
  falling back to the plain wordmark in cramped terminals.
- Theme switching now supports direct `/theme light`, `/theme dark`, and
  `/theme toggle` shortcuts in addition to the picker and explicit
  bundled theme IDs.
- Status modal rows now show next-step hints for focused commands or
  config files, including provider/model, tools, plugins, MCP, OTel,
  budget, and context rows.
- `/status` now reports active-provider credential env var health as
  missing, set, or not required by a local preset.
- `/status` now summarizes configured MCP server names without probing
  or starting MCP clients.
- `/status` now also uses cached snapshots for background-plugin
  lifecycle issues and MCP attach health, including connected/tool
  counts and the latest attach error, without probing during render.
- Approved-memory retrieval can now be disabled for the current
  session/worktree with `/memory off` or `stado memory session off`;
  the shared prompt context honors the marker across TUI, `stado run`,
  headless, and ACP.
- `stado learning` now owns the lesson review path directly with
  `edit|approve|reject|delete|supersede`, including lesson-specific
  trigger, rationale, evidence, tags, scope, and expiry edits.
- `stado learning document` now implements the explicit
  `.learnings/` handoff and rejects the documented lesson from prompt
  retrieval.
- Assistant turn footers now have expandable details behind `Shift+Tab`,
  covering token deltas, cache read/write deltas, requested tools, and
  a session trace command hint when available.
- `/subagents` now renders a dedicated recent-child overview with full
  child IDs, worktrees, status, changed-file counts, scope violations,
  and adoption commands.
- `/providers` now shows runner-specific remediation when a reachable
  local backend has no models loaded, including LM Studio's `lms load`
  path.
- `/provider <name>` now prints provider setup/remediation guidance
  directly, mirroring the model picker's `Ctrl+A` path.
- `/providers` now includes active-provider credential env var health in
  addition to local-runner load/start hints.
- Assistant markdown rendering now picks Glamour's light or dark style
  from the active theme background luminance and clears the markdown
  renderer cache on theme switch.
- Inline slash suggestions now show compact command group labels,
  matching the modal command palette while staying anchored to the
  input.
- `/status` now shows the current OTel trace id when the TUI root
  context carries a valid span context.
- `/theme` now appends the active custom `theme.toml` theme as the
  current row when it does not match a bundled catalog entry; selecting
  that row closes the picker without rewriting the override.
- Custom `theme.toml` files can now choose markdown renderer style via
  `[markdown].style = "auto" | "light" | "dark"`; `auto` preserves the
  background-luminance behavior.
- The bundled theme catalog now includes `stado-rose`, a dark neutral
  theme with rose and cyan accents.
- The compact TUI footer now shows repo-relative cwd segments inside
  git worktrees and appends `*` to the branch/detached SHA when a
  cached git status check sees uncommitted worktree changes.
- `/thinking` and `Ctrl+X H` now persist the display-only thinking
  viewport mode to `[tui].thinking_display` and restore it on TUI
  startup.
- EP-26 now records that inline slash rows show command IDs and
  secondary keyboard shortcuts together, with regression coverage.
- Assistant turn metadata now annotates requested tool counts with
  failed/rejected result counts after tool execution finishes.
- Resumed sessions now reconstruct persisted provider-native thinking as
  separate TUI thinking blocks so display modes still apply after
  restart.
- Bundled theme selections now persist as `[tui].theme` config keys;
  custom `theme.toml` remains the fallback path when no bundled theme is
  pinned.
- TUI and slash-command docs now describe `[tui].theme`,
  `[tui].thinking_display`, and custom `theme.toml` fallback behavior
  consistently.
- EP-14 now records the multi-session TUI policy and shape as
  implemented: active-session-only execution, confirmed full delete, and
  command-palette/session-overview management.
- EP-13 now records the synchronous worker spawn/adoption contract as
  implemented; higher child concurrency is future scheduler work outside
  that contract.
- Local-runner detection now distinguishes LM Studio installed models
  from loaded/runnable models, so fallback and picker rows avoid
  unloaded models while doctor and `/providers` show remediation.
- EP-20 now records inline context completion as implemented for the
  scoped surface: agents, sessions, skills, docs, files, and
  repo-shaped symbol scanners.
- EP-22 now records the scoped theme catalog and picker work as
  implemented; future theme additions should be usage-led.
- EP-23 now keeps status rows read-only with inline action hints and
  cached plugin/MCP health snapshots; focusable row actions remain
  future workflow-led work.
- EP-21 now records assistant turn metadata as display-only while
  `conversation.jsonl` remains the provider-message transcript.
- EP-13 now records the current concurrency policy as part of the
  implemented synchronous contract: one active child per parent
  session/tool queue, with higher concurrency left for future scheduler
  work.
- EP-19 now records the scoped model/provider picker work as
  implemented; true connect/OAuth remains separate provider-specific
  product work.
- The EP README status table now matches implemented EP-13, EP-14, and
  EP-24 frontmatter.
- Headless/ACP command docs and CLI help now document the `subagent`
  lifecycle payload, worker update fields, and explicit
  `stado session adopt` review flow.
- The opencode TUI UAT follow-up report has been refreshed against the
  current L4 slices so its backlog no longer lists already-shipped
  subagent, docs/symbol completion, landing, status, turn metadata, and
  theme work as missing.
- `/adopt [child] [--apply]` now lets the TUI dry-run the latest
  adoptable worker child by default and apply non-conflicting child
  changes only when `--apply` is explicit.
- `/sessions` now states that inactive sessions are parked and names the
  active-work blockers that must clear before switching.
- Enabled spawn support in TUI, `stado run --tools`, and headless
  `session.prompt` when a live provider, config, and parent session are
  present.
- Updated EP-13, EP-14, EP-20, and `CHANGELOG.md` under Unreleased.
- EP-13 and the EP README now mark the synchronous subagent spawn and
  adoption contract as implemented after live local-provider dogfood.

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
  - `go test ./internal/runtime ./internal/subagent ./internal/headless ./internal/acp`
  - `go test ./internal/runtime ./internal/tui -run 'Test(AttachMCP|JoinErrors|Status)'`
- Full suite passed:
  - `go test ./...`
- Whitespace check passed:
  - `git diff --check`
- Live local-provider smoke passed after loading
  `qwen/qwen3.6-35b-a3b` in LM Studio:
  - `stado doctor` detected LM Studio as running with one loaded model.
  - `stado run --prompt 'Reply with exactly: stado-dogfood-ok'`
    returned the expected text.
  - `stado run --tools --json` successfully spawned a
    `workspace_write` worker child, isolated the child write in the
    child worktree, and reported the child session and changed file.
  - `stado session adopt <parent> <child> --json` dry-ran the worker
    adoption with `can_adopt: true`.
  - `stado session adopt <parent> <child> --apply` applied
    `docs/reports/live-worker-dogfood.md` into the parent session and
    recorded `subagent_adopt` trace metadata.
- Live TUI worker smoke passed with the same loaded LM Studio model:
  - The TUI accepted a prompt that used `spawn_agent` with
    `role=worker`, `mode=workspace_write`, and a single-file
    `write_scope`.
  - The sidebar showed the child moving from running to completed and
    then marked adoption as ready.
  - The parent conversation recorded the worker tool result with
    `changed_files` and `adoption_command`.
  - `/subagents` listed the child, worktree, changed-file count, and
    adoption command.
  - `/adopt` dry-ran the latest adoptable worker child and printed
    `dry_run: true` plus the apply command.
  - `/adopt eb1e9de5 --apply` applied
    `docs/reports/live-tui-worker-dogfood.md` into the parent session
    and recorded a `subagent_adopt` trace commit.

The local shell did not have `go`/`gofmt` on `PATH`; commands used the
repo-compatible Go toolchain at
`~/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.9.linux-amd64/bin/`.

## Current State

- Branch: `main`
- Latest committed slices include the EP-13 scoped worker
  spawn/adoption flow, EP-14 provider/session policy fixes, EP-20
  docs/Go/Python/JS/TS/shell symbol completion, landing-logo refinement,
  status-modal/provider/MCP details, expandable assistant turn details,
  `/subagents`, `/adopt`, local-runner no-model remediation, grouped
  inline slash suggestions, theme-aware markdown rendering, the custom
  `theme.toml` picker row, `stado-rose`, repo-relative footer dirty
  state, persisted thinking display mode, resumed thinking blocks,
  failed/rejected tool-result metadata, config-backed bundled theme
  selection, and EP-26 shortcut-hint coverage. EP-13's synchronous
  subagent spawn/adoption contract, EP-14's multi-session TUI policy
  docs, EP-19 model/provider picker, EP-20 inline context completion,
  EP-21 assistant turn metadata, and EP-22 theme catalog/picker are also
  closed, as is EP-23's read-only status modal scope. LM Studio
  installed-vs-loaded model detection is also fixed.
- Live worker dogfood completed against LM Studio
  `qwen/qwen3.6-35b-a3b`. The worker spawn, child isolation, CLI dry-run,
  and CLI apply path worked end-to-end. The run also exposed that the
  model-facing tool result needed the exact adoption command; that is now
  included as `adoption_command`.
- Live TUI worker dogfood also completed against the loaded LM Studio
  model. The sidebar, `/subagents`, `/adopt` dry-run, and `/adopt
  --apply` path all worked against an actual spawned worker child.
- No release tag has been cut for this slice.

## Next Candidates

1. Add richer adoption affordances to headless/ACP/editor clients once a
   client consumes the subagent notification payload.
2. If true concurrent child execution becomes a priority, write a
   separate scheduler EP instead of extending EP-13 retroactively.
3. Pick the next accepted-but-unimplemented Standards EP only when there
   is a concrete user workflow to drive it; avoid speculative plugin or
   provider product work.
