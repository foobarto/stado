# CHANGELOG

Notable changes to stado, reverse-chronological. Pre-1.0; breaking
changes still allowed between tags. Sections: UX / CLI / TUI /
Plugins / Infra / Fixes.

## Unreleased

### Plugins

- **Added the first memory host API slice.** Plugins can now declare
  `memory:propose`, `memory:read`, and `memory:write` to use a
  capability-gated local append-only memory store for candidate capture,
  approved-memory retrieval, and explicit write mutations.

### Infra

- **Pinned CI and release tool versions.** GitHub workflows now opt into
  Node 24 action execution and pin GoReleaser / golangci-lint versions
  instead of relying on `latest`.

### Docs

- **Accepted EP-15 memory-system design.** The memory plugin standard
  now defines item schema, scopes, host APIs, retrieval, review controls,
  storage, and prompt-injection defenses.
- **Accepted EP-16 learning-plugin design.** The learning standard now
  defines lesson candidates, approval, retrieval, invalidation, and its
  relationship to the EP-15 memory substrate.

## v0.13.1 — 2026-04-24

### Fixes

- **Restored session compaction auditability.** TUI compaction now keeps
  `.stado/conversation.jsonl` append-only, records raw-log digests on
  compaction markers, and creates real turn-boundary refs for pure chat
  and no-file-change turns. `stado run --session` also attaches to the
  persisted session and records a turn boundary when tools are disabled;
  headless and ACP git-backed prompts persist their transcripts before
  later compaction; headless compaction now writes the same raw-log
  audit marker when a git-backed session is attached.

## v0.13.0 — 2026-04-24

### Prompt

- **Aligned the default system prompt with cairn.** New first-run
  `system-prompt.md` templates now include cairn's governing
  principles, six-phase workflow, session artefact discipline, and
  autonomous-work safety rules while keeping stado identity fixed.
  Untouched generated default templates from prior releases are updated
  automatically; customized templates are left alone.

## v0.12.0 — 2026-04-24

### TUI

- **Added thinking display modes.** `Ctrl+X H` and `/thinking` now cycle
  provider-native thinking output between full, tail-only, and hidden
  display without changing what is persisted.
- **Improved model and slash workflows.** Model selection now persists
  the chosen model/provider as the new default, `Ctrl+X M` opens the
  model picker, `/` opens inline fuzzy command suggestions, and `Ctrl+P`
  remains the full command palette.
- **Clarified manual approval demo use.** The `approval_demo` tool spec
  now warns that it is a human-triggered manual test tool only.
- **Added mode-coloured input rails.** Do, Plan, and BTW now use distinct
  left-rail colours in the chat input.

## v0.11.0 — 2026-04-24

### TUI

- **Expanded multi-session management.** The session overlay now supports
  switch/resume, new, rename, fork, and confirmed delete actions without
  leaving the TUI.

## v0.10.0 — 2026-04-24

### TUI

- **Improved footer density.** The chat status row now keeps compact cwd,
  git branch, and version context on the left while preserving usage and
  command hints on the right.

## v0.9.0 — 2026-04-24

### TUI

- **Clarified LSP readiness.** The status modal now explains that LSP
  tools activate when supported files are read and lists detected
  language-server binaries.

## v0.8.0 — 2026-04-24

### TUI

- **Added a status modal.** `/status` and `Ctrl+X S` now show a compact
  provider, model, tool, plugin, MCP, OTel, sandbox, and context summary.

## v0.7.0 — 2026-04-24

### TUI

- **Added a bundled theme picker.** `/theme` and `Ctrl+X T` now open a
  picker for `stado-dark`, `stado-light`, and `stado-contrast`; picking
  one updates the running TUI and persists it to `theme.toml`.

## v0.6.0 — 2026-04-24

### TUI

- **Started unified `@` completion.** The inline `@` picker now shows
  Do, Plan, and BTW agents before repo files; accepting an agent switches
  the active agent and consumes the mention.
- **Added assistant turn footers.** Completed assistant responses now
  show compact metadata for the agent, model/provider, elapsed time,
  tool count, token delta, and cost delta.

## v0.5.0 — 2026-04-24

### TUI

- **Added a first-class agent picker.** `ctrl+x a` and `/agents` now
  open a modal for switching between Do, Plan, and BTW while preserving
  the existing `Tab` Do/Plan toggle and `ctrl+x ctrl+b` BTW shortcut.
- **Made the active agent visible in the sidebar.** The Agent section now
  shows the current Do, Plan, or BTW agent without restoring the old
  noisy mode suffix in the session header.
- **Added the first in-TUI multi-session workflow.** `ctrl+x l` opens a
  searchable session switcher and `ctrl+x n` creates/switches to a
  fresh session in the same TUI process. Switching is blocked while a
  draft, queued prompt, approval, compaction, stream, or tool is active.
- **Improved model picker continuity.** The picker now marks the
  current model and remembers recent model/provider selections under
  stado state so frequently used choices surface first.
- **Added model picker favorites.** Press `ctrl+f` in the model picker
  to favorite or unfavorite the highlighted model; favorites persist in
  stado state and appear before recents.
- **Calmed the default sidebar.** Debug-only diagnostics such as info
  logs, unknown context limits, unbounded budgets, and normal sandbox
  status now stay hidden unless `/debug` enables sidebar diagnostics
  or warnings/errors need attention.
- **Auto-title fresh sessions from the first prompt.** The TUI now writes
  a compact session description from the first user message when no
  manual `/describe` label exists, improving future session lists and
  switchers without overwriting user labels.

### Infra

- **Refreshed the real-PTY TUI UAT harness for the landing view.** The
  tmux harness now isolates config/state, avoids live-provider
  nondeterminism, expects the startup landing view to be sidebar-free,
  and checks the current rail-card message treatment.

## v0.4.2 — 2026-04-24

### Fixes

- **Fixed main CI failures from the `v0.4.1` push.** Cleaned up new
  lint findings and removed the remaining `-race` hazards in TUI trace
  logging and Linux sandbox cleanup.

## v0.4.1 — 2026-04-24

### Docs

- **Documented release versioning policy.** Minor releases now mean new
  features or meaningful behavior adjustments; patch releases mean
  smaller fixes, docs/process updates, dependency bumps, or contained
  internal changes.

## v0.4.0 — 2026-04-24

### TUI

- **Added the first-run landing view.** A new opencode-style startup
  screen centers the stado ANSI logo, model/provider status, command
  hints, and the editable prompt before the first message.
- **Made the chat input taller by default.** The editor now reserves
  three extra visible rows so multi-line prompts do not collapse the
  interaction area immediately.
- **Fixed the first-message freeze path.** TUI trace logging and
  renderer/log-tail fixes keep input responsive while thinking and
  response blocks stream into the chat history.
- **First-turn provider startup is now async instead of blocking the
  UI.** When no default provider is pinned, the TUI now probes local
  runners at startup, queues the first prompt behind that probe if it is
  still in flight, and replays the prompt automatically when the probe
  resolves.
- **Added focused TUI trace logging for startup / first-turn issues.**
  `STADO_TUI_TRACE=1 stado` now emits timestamped trace lines for the
  provider probe, first-submit queueing, provider resolution, and stream
  start into the sidebar log tail.

### CLI / Infra

- **Added a configurable default system prompt template.** First-run
  config creation now writes `system-prompt.md` under the stado config
  directory, and the TUI, run, ACP, headless, MCP, and session-resume
  surfaces all compose prompts from the same template.
- **OpenTelemetry is now actually bootstrapped by the runtime-facing
  command surfaces.** `stado` (TUI), `stado session resume`, `stado run`,
  `stado headless`, `stado acp`, `stado mcp-server`, and session
  fork/revert flows now start the configured OTel runtime and flush it on
  shutdown instead of leaving the shipped spans as no-ops.
- **Release builds now stamp both CLI and TUI version strings.**
  Goreleaser sets the root command version and the sidebar/bundled
  plugin version value from the same tag.

### Docs

- **Added standalone command guides for every shipped top-level
  command.** The docs index now links `agents`, `audit`, `stats`,
  `headless`, `acp`, `mcp-server`, `verify`, `self-update`, and the
  small generated/informational commands.
- **Moved planned work into EP placeholders.** `BUGS.md` now stays
  focused on active bugs, while planned subagents, multi-session TUI,
  memory, learning, tool approval policy, and system-prompt work are
  covered by EPs.
- **Refreshed stale design/config/context docs.** The docs now reflect
  plugin approval cards, current context accounting, bundled
  auto-compact behavior, and the actual `config` command surface.

## v0.3.0 — 2026-04-24

### CLI / Infra

- **Shipped first-install `install.sh`.** Linux/macOS installs can now
  follow a signed-manifest path on day one: the script verifies
  `checksums.txt` with `cosign`, verifies the matching archive against
  that manifest, and installs `stado` to `~/.local/bin` by default.
- **Direct command coverage now includes `agents` and `audit`.**
  `stado agents list/attach/kill` and `stado audit verify/export/pubkey`
  now have dedicated command-level tests instead of depending only on
  lower-level helper coverage.

### TUI

- **Custom template overlays are now live in the shipped app.** Files
  under `$XDG_CONFIG_HOME/stado/templates/*.tmpl` now override the
  bundled renderer templates at boot, matching the long-documented
  `render.NewWithOverlay` contract.

## v0.2.2 — 2026-04-24

### CLI / Infra

- **Provider credential lookup is now centralized.** The direct
  provider constructors, TUI provider builder, and `stado doctor` now
  share one source of truth for provider-name-to-env-var resolution
  under `internal/config` instead of carrying separate maps.
- **Bundled hosted-provider overrides keep their API-key env lookup.**
  If you override a bundled hosted provider name like `groq`,
  `openrouter`, or `deepseek` via `[inference.presets.<name>]`, the TUI
  now still injects the conventional API key for that provider instead
  of silently dropping it.

## v0.2.1 — 2026-04-24

### Security / Hardening

- **Linux `net:<host>` subprocess policies are now real proxy-only
  network sandboxes.** Instead of sharing the host netns and relying on
  proxy env vars alone, the Linux runner now wraps `bwrap` in
  `pasta --splice-only` and forwards only the local proxy port into the
  private netns.

## v0.2.0 — 2026-04-23

+ Plugin-driven context-management release: shipped bundled
auto-compaction by default, promoted session-aware plugin flows on the
CLI/headless surfaces, reorganized the shipped/example plugin catalog,
and tightened several local authority boundaries.

### TUI / Plugins

- **Bundled auto-compaction is now on by default.** Stado ships the
  `auto-compact` plugin source at `plugins/default/auto-compact/`,
  bundles it into the binary as a default background plugin, and loads
  it automatically in the TUI/headless server.
- **Hard-threshold TUI recovery now forks and replays automatically.**
  When the TUI hits the hard context threshold, it emits a
  `context_overflow` event to background plugins; the bundled
  auto-compact plugin responds by forking a compacted child session and
  the blocked prompt is replayed there.
- **Plugin layout is now product-facing.** The repo now uses
  `plugins/default/` for shipped bundled plugin sources and
  `plugins/examples/` for opt-in samples; the old internal
  `builtinplugins` package was renamed to `bundledplugins`.

### CLI / Headless

- **`[hooks].post_turn` now has cross-surface parity.** The same
  notification-only shell hook now fires on completed turns in the TUI,
  `stado run`, and headless `session.prompt`, with the same JSON payload
  shape and the same bash-disable guard when `bash` is removed from the
  active tool set.
- **`stado plugin run` can now attach to persisted sessions.** Pass
  `--session <id>` to give a plugin real `session:read`,
  `session:fork`, and `llm:invoke` access on the CLI path instead of the
  old "zeroed session" fallback.
- **Plugin-created forked sessions now persist their seed summary.**
  Session-aware plugins that fork a child session now write the seeded
  summary into the child's `.stado/conversation.jsonl`, so resuming the
  child picks up with that summary already loaded.
- **`plugin install` no-op output is now explicit.** Reinstalling the
  same plugin version prints a stdout `skipped:` line instead of only a
  stderr advisory, so scripts and users can distinguish "already
  installed" from silent failure.

### Security / Hardening

- **Session/agent/plugin path traversal holes were closed.** Session
  and agent worktree lookups now validate local IDs before joining
  paths, installed plugin IDs are checked before resolving under the
  plugin state dir, and `session:fork` no longer accepts foreign-session
  refs or raw commit hashes.
- **Writable install/update paths now propagate final flush errors.**
  Plugin install and self-update now treat `Sync` / `Close` failures as
  real errors instead of reporting success after a partial write.
- **Timeouts were added to external HTTP control paths.** Self-update,
  CRL/Rekor, and local-provider probe calls now use explicit HTTP
  clients instead of the process-wide default client with no timeout.
- **Capability-less stdio MCP servers are now refused.** Local MCP
  subprocesses must declare `capabilities` in config instead of falling
  back to caller privileges.
- **Sandbox defaults are narrower.** The built-in `bash` tool now uses
  deny-all networking on the sandboxed subprocess path by default, and
  the docs now describe the remaining Linux `net:<host>` limitation
  honestly as proxy-mediated rather than a raw-socket firewall.
- **Several crash/corruption edges were removed.** The LSP client no
  longer panics on bad pending entries, plugin FS reads fail on overflow
  instead of silently truncating, and TUI aggregate usage accounting now
  stays on the Bubble Tea event loop instead of being mutated from the
  stream goroutine.

## v0.1.3 — 2026-04-23

+ Sandbox follow-up release: Linux subprocess host-allowlist policies
now route through the local CONNECT proxy as originally designed, and
the README now distinguishes Linux, macOS, Windows, and WASM tool
sandbox behavior more precisely.

### Infra / Security

- **Linux `net:<host>` subprocess policies now wire through the local
  CONNECT-allowlist proxy.** `BwrapRunner` starts the loopback proxy for
  `NetAllowHosts`, injects `HTTP_PROXY` / `HTTPS_PROXY` into the child,
  and clears `NO_PROXY` so HTTPS-aware subprocesses and MCP stdio
  servers actually honor the configured host allowlist instead of
  bypassing it.
- **Runner env propagation is now handled at the runner boundary.**
  The sandbox runner interface accepts the candidate child environment
  directly so Linux `bwrap`, macOS `sandbox-exec`, Windows passthrough,
  and the fallback runner all perform filtering from the same source of
  truth.

### Docs

- **README sandbox wording now matches the implementation.** The docs
  now call out that Linux has the strongest shipped path, macOS has
  real subprocess sandboxing but not Linux-style whole-process
  narrowing, Windows v1 is still warning-only, and WASM tools are
  sandboxed by `wazero` host-import gates rather than the OS subprocess
  runner.

## v0.1.2 — 2026-04-23

+ Docs + CLI parity release: ships the documented `doctor` automation
surface, refreshes the top-level/docs catalog for the actual shipped
runtime, and lands a large internal source split to make the codebase
easier to read and maintain.

### CLI

- **`stado doctor` now exposes the documented machine/CI flags.**
  `--json` emits newline-delimited JSON (`check`, `status`, `value`,
  `detail`) and `--no-local` skips local-runner probes for faster or
  offline CI preflight. Blocking doctor failures now exit 1, matching
  the command guide.

### Docs

- **README refresh for the current release and CLI surface.** The
  install section now documents the signed `checksums.txt` verification
  flow that releases actually publish, the quick-start plugin commands
  include the missing sign/trust steps, and the configuration / docs /
  roadmap sections now point at shipped guides instead of stale
  placeholder wording.
- **Retroactive EP backfill for the major shipped design decisions.**
  `docs/eps/` now includes accepted records for the provider seam,
  git-native session model, sandboxing, plugin runtime, conversation
  state, repo-local prompt inputs, guardrails, and interop surfaces,
  and the docs indexes now link that catalog directly.
- **Roadmap + command docs now call out the actual remaining product
  gaps.** `PLAN.md`, `README.md`, and the relevant `docs/` guides now
  explicitly describe the unfinished user-visible surfaces: Windows
  sandbox v2, the advisory-only CLI `session compact` shim, the pending
  `install.sh` first-install path, and template-overlay support that
  exists in the renderer but is not yet wired into the TUI entry point.

### Infra

- **Large source files were split by concern without changing package
  boundaries or exported surfaces.** The TUI model, session/plugin CLI,
  plugin host runtime, headless server, runtime loop, and git commit
  internals are now spread across smaller focused files, making the
  codebase easier to review and maintain without changing the shipped
  behavior.

## v0.1.1 — 2026-04-23

+ Release follow-up: fixes the bundle-fetch path that broke the `v0.1.0`
release workflow, and is the first successful 0.1 release build.

### Infra / Release

- **Bundled-tool release fetches no longer depend on GitHub REST asset
  digests.** `hack/fetch-binaries.go` now reads ripgrep checksums from
  upstream `.sha256` sidecars, reads ast-grep checksums from GitHub's
  public `expanded_assets` fragment, and aborts immediately on any
  supported-target fetch failure instead of silently skipping a bundle
  and letting the compiler fail later.

## v0.1.0 — 2026-04-23

+ Built-in tools now ship through the same signed WASM runtime as
third-party plugins, macOS sandboxing is shipped alongside Linux, and
the public plugin workflow is documented end-to-end. Pre-1.0: breaking
changes still allowed between tags.

### Plugins / Tool runtime

- **Built-in tools now load through the plugin runtime.** The default
  `read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch` /
  `ripgrep` / `ast_grep` / `read_with_context` / LSP tools are now
  embedded signed WASM modules instantiated through the same wazero host
  surface used for third-party plugins. That removes a large native-vs-
  plugin split, makes override behavior consistent, and keeps the
  bundled tool surface auditable.
- **Approval wrappers moved into plugins.** The old in-process
  "approval tool" path is gone. Approval behavior now lives in explicit
  example plugins (`approval-bash-go`, `approval-write-go`,
  `approval-edit-go`, `approval-ast-grep-go`) plus a bundled
  `approval_demo` module that exercises the `ui:approval` import.

### Docs

- **README refresh for the 0.1.0 surface.** The install section now
  documents release assets and the Homebrew tap that already exists, the
  plugin command examples no longer mention the removed GitHub bot
  workflow generator, and the shipped-status sections now reflect the
  macOS sandbox + WASM plugin runtime that are already live.
- **`stado plugin` now has a command guide.** `docs/README.md` links a
  new `docs/commands/plugin.md` guide covering scaffold → sign → trust
  → verify → install → run, plus the distinction between trusted
  signers (`plugin list`) and installed plugin IDs (`plugin installed`).

### Infra / Security

- **Removed the `stado github` bot workflow generator.** The GitHub
  comment-triggered bot path added a high-risk hosted-runner execution
  surface that was not core to stado's runtime model. The CLI command,
  its workflow template, and related docs references are gone.
- **Plugin FS sandbox now resolves symlinks before capability checks.**
  `stado_fs_read` / `stado_fs_write` used to call `os.ReadFile` / `os.WriteFile`
  directly, which follows symlinks. A plugin with `fs:read:/allowed` could
  create a symlink in `/allowed` pointing outside the tree and read arbitrary
  files. The new `realPath()` helper resolves symlinks before the
  `allowRead` / `allowWrite` check, so escape is caught.
- **Plugin install path traversal prevention.** Manifest `Name` and `Version`
  are validated with `filepath.IsLocal()` plus explicit rejection of `/` and
  `\` characters so a malicious manifest can't write outside the plugins
  directory with fields like `Name: "../../../etc"`.
- **Headless session ID no longer reuses numbers after deletion.**
  `sessionNew` used `len(s.sessions)+1`, so deleting `h-1` and creating a new
  session would overwrite `h-2`. Now uses a monotonic `nextID` counter.
- **Headless session operations are mutex-protected.** `sessionPrompt`,
  `sessionCancel`, and `sessionCompact` all raced on `sess.messages`,
  `sess.cancel`, and `sess.gitSess`. Each `hSession` now carries its own
  `sync.Mutex`.
- **ACP session operations are mutex-protected.** Same race pattern as headless
  — `session/prompt` and `session/cancel` dispatched on separate goroutines
  could corrupt message history or cancel the wrong turn. Fixed with per-
  `acpSession` mutex and monotonic ID counter.
- **MCP client leak on reconnect fixed.** `attachMCP` called from every
  `BuildExecutor` reconnected each configured MCP server without closing the
  previous `client.Client`. After several tool-enabled prompts the process
  leaked stdio MCP subprocesses. `Connect` now closes the old client inside
  the lock after a successful replacement.
- **`run --session --tools` now opens the correct worktree.** When `--session`
  was set, tools still opened a session from the caller's cwd instead of the
  resumed session's worktree. Running from a different directory would
  execute mutating tools against the wrong repo.
- **Host-safe SDK split for bundled WASM modules.** `internal/bundledplugins/sdk`
  now keeps the real pointer-based implementation behind `//go:build wasip1`
  and provides a host-only stub for tests and lint. That stops host-side
  tooling from treating wasm32 offsets as native pointers while preserving
  the ABI used by the embedded modules.

### UX


- **Async tool execution — TUI no longer blocks on long-running tools.**
  `bash sleep 30` (or any slow tool call) used to freeze the entire
  interface until the command returned. Tool calls now run on a
  goroutine and ferry their result back via `toolResultMsg`, so the
  user can keep typing, scroll, or cancel while the tool is in-flight.
  Same pattern already used for `/plugin:...` invocations.
- **Queued prompts get visual feedback.** When you hit Enter while a
  turn is still streaming, the follow-up message is appended to the
  chat immediately with a muted "queued — runs when the current turn
  finishes" pill. Previously the only signal was a tiny status-bar
  label that was easy to miss. Ctrl+C on a queued prompt now also
  removes the block, not just the internal buffer.
- **Render caching eliminates glamour-induced keypress lag.** Long
  conversations used to stutter during streaming because every frame
  re-ran glamour/markdown on every historical block. Two changes fix
  this: (1) `Renderer` now memoises `glamour.TermRenderer` instances
  per width (creating one costs 5–10 ms), and (2) each conversation
  block caches its last rendered output, invalidating only when its
  body, width, expand state, or tool result changes. The live
  assistant block still re-renders every tick (it is growing), but
  everything else is near-free.
- **`[approvals]` allowlist is actually wired to the TUI.** The config
  parser has supported `mode = "allowlist"` and `allowlist = ["read",
  "grep"]` for a while, but `Run()` never called `SetApprovals()`, so
  the allowlist was silently ignored. Now tools named in the allowlist
  auto-execute without the `⚠ y/n` prompt.
- **Gated `?` and `/` keybindings.** Typing a literal `?` or `/` inside
  a non-empty prompt no longer pops the help overlay or command
  palette — the characters insert as text instead. Both shortcuts
  still work when the input box is empty.
- **Tool cancellation actually works.** Approved tool calls run on a
  goroutine so the UI stays responsive. Previously there was no way
  to cancel a long-running tool after approving it. Now Ctrl+C
  propagates cancellation into the tool's context; the goroutine
  exits with "cancelled by user" instead of running to completion.
- **Live "tool running" indicator with elapsed counter.** Tool blocks
  now show `running 3.2s` while the command is active, refreshed
  every 250 ms via `toolTickMsg`. No more silent 30-second waits
  where the user can't tell if stado is working or frozen.
- **Narrow-terminal startup hint.** Terminals narrower than 90 columns
  no longer show a blank empty-state — they get `"Send a message to
  get started — /help for commands"` so first-time users aren't
  staring at empty whitespace.
- **`Ctrl+C` during streaming confirms cancellation.** Cancelling a turn
  now drops a one-time "turn cancelled" system block into the chat,
  so users know the keystroke registered instead of wondering if
  the model finished coincidentally.
- **Collapsed tool cards show an expand affordance.** When a tool
  call has completed but the card is collapsed, a muted
  `shift+tab` hint is appended to the header row so the user
  knows they can expand it.
- **Sidebar placeholder when no model is set.** If `model` is empty in
  config, the sidebar Model field now reads `"no model set — /model"`
  instead of a completely blank line.

### Infra

- **Test coverage: 5 new UAT scenarios** for previously uncovered
  slash commands: `/split`, `/todo`, `/provider` (uninitialised),
  `/tools` (populated + empty paths).

## v0.0.1 — 2026-04-21
+ ACP + plugin ABI + MCP client + MCP server surfaces are all
feature-complete relative to the ranked research list (AGENTS.md
auto-load, `[budget]` cost gate, `.stado/skills/`, `[hooks]`
post_turn, and `stado mcp-server`). Pre-1.0: breaking changes still
allowed between tags.

### Iteration-cycle additions (post-initial-sweep)

- **`stado mcp-server` — expose stado's tools as an MCP server.**
  Every bundled stado tool (read, grep, ripgrep, ast-grep, bash,
  webfetch, file ops, LSP-find) is registered with an MCP v1
  server over stdio. Other MCP-aware agents (Claude Desktop,
  Cursor, etc.) can call stado as a tool backend. Scope is
  tools-only in this release — no resources, no prompts, no
  sampling. `[tools].enabled` / `[tools].disabled` trim the
  exposed surface same as the TUI and `run` paths, so an MCP
  client only sees what stado is currently configured to offer.
  Auto-approve host rooted at process cwd — the MCP client is
  assumed to be the authorization boundary. Closes the last item
  in the ranked research list.
- **`/context` is a one-stop session-state view.** Used to show
  only token + threshold info. Now also renders: session id,
  cost, budget caps (when set), loaded instructions file, skill
  names, configured post_turn hook. Answers "what does this
  session look like to the model?" without bouncing across
  /budget, /skill, sidebar.
- **`/session` slash command** — prints the current session id,
  worktree path, and description label. Copy-paste target for
  `stado session fork`, `session tree`, `session attach` in
  other shells. Explains itself when invoked outside a live
  session instead of silently failing.
- **TUI sidebar surfaces loaded skills.** A new "Skills: N — /skill"
  row renders when skills are loaded from `.stado/skills/`. Users
  no longer have to know the slash command in advance to discover
  the feature — a repo with a skills directory advertises itself.
  The row stays hidden when no skills are loaded so empty repos
  don't see a misleading "0 skills" row.
- **`stado headless --help` documents `plugin.list`/`plugin.run`.**
  Both RPC methods landed months ago but the help text never
  listed them, so CI integrators had to read the server code to
  learn they existed. Added the shape summary for both plus the
  full set of `session.update` notification kinds.
- **`stado run --skill <name>`** — skills are now CLI-usable, not
  just a TUI feature. Resolves `.stado/skills/<name>.md` from cwd
  and uses the body as the prompt. Combines with `--prompt` (skill
  body prepended) so the reusable skill plus a per-invocation ask
  compose naturally. Unknown skill lists the available names in the
  error message so a typo doesn't force a filesystem grep. Useful
  in CI: a repo can ship `.stado/skills/ci-review.md` and pipelines
  invoke `stado run --skill ci-review` instead of inlining the
  full prompt text.
- **`/retry` slash command.** Regenerates the last assistant turn
  from the same user prompt — equivalent to the "regenerate" button
  in ChatGPT/Claude web UIs. Truncates the conversation back to the
  last user message (dropping assistant + tool-role messages) and
  re-streams. No-ops with a hint when there's nothing to retry, the
  last message is already a user prompt, or a stream is already
  running (avoids doubling cost and racing the goroutine).
- **`agents list` hides stale/empty rows by default.** Same problem
  `session list` had pre-dogfood: every aborted run leaves a PID
  file + empty worktree in the agents listing, so the output was
  30+ stale rows with dashes everywhere. Now hidden; `--all`
  restores the full view. A row is worth showing when the process
  is alive OR there's committed content on the tree/trace refs.
- **`stado doctor` now surfaces opt-in feature config** — Budget
  caps, Lifecycle hooks, Tools filter. All render as ✓ regardless
  of whether they're set; the point is to make the features
  discoverable and let users verify that their config.toml took
  effect. "Did I configure the budget cap?" is now a `doctor`-
  answerable question instead of a config-file-read task.
- **Lifecycle hooks — `[hooks]` section (MVP, notification-only).**
  Users can wire a shell command to the `post_turn` event; stado
  runs `/bin/sh -c <cmd>` with a JSON payload on stdin carrying
  turn index, input/output tokens, cost, duration, and a ≤200-char
  excerpt of the assistant text. 5-second wall-clock cap per hook.
  stdout/stderr go to stado's own stderr with a `stado[hook:<event>]`
  prefix so they're distinguishable in shared terminals. Failures
  are logged, never propagated — a broken hook can't poison the
  next turn. MVP scope is deliberate; a richer "approve tool call
  via external policy" form can grow on top.
- **Help overlay (`?`) now lists slash commands.** Users had to
  open the palette separately to learn that `/budget`, `/skill`,
  `/model`, etc. existed. The overlay now appends the palette's
  Commands table below the keybindings section, grouped the same
  way (Quick / Session / View).
- **Skills: `.stado/skills/*.md` auto-loader.** Drop a markdown file
  with frontmatter `name:` / `description:` in a `.stado/skills/`
  directory and stado exposes it as `/skill:<name>` in the TUI.
  Invocation injects the body as a user message so the next turn
  acts on it. `/skill` alone lists what's loaded. Resolution walks
  from cwd upward — nearest-wins for module-level overrides in a
  monorepo. Bodies without frontmatter use the filename stem as
  the name. Matches the emerging cross-vendor convention for
  reusable prompt fragments.
- **`stats --json` now emits a valid empty shape when there are no
  sessions.** Previously stdout was empty and `(no sessions in
  window)` leaked to stderr, which broke `stado stats --json | jq`
  in a fresh repo. Matches the already-valid empty case for
  "sessions exist but no tool calls in window."
- **`config init` template now covers `[budget]` + an AGENTS.md
  pointer.** The generated template was the only docs users saw
  for many knobs; adding budget + pointing at AGENTS.md closes the
  gap between config knobs users can see and features actually
  available.
- **`session list` hides empty rows by default.** Zero-turn +
  zero-message + zero-compaction sessions were cluttering the
  default output — `session list` on a long-lived repo was showing
  50 empties per 3 real rows. Now hidden; `--all` restores the
  full listing. A stderr footer reports how many were hidden with
  a copy-pasteable `session gc --apply` pointer.
- **`stado doctor` stops failing on missing optional tools.** gopls
  is only needed by the `lsp-find` tool; stado works fine without
  it. Now rendered as ✓ with a "not found — optional" detail
  instead of ✗, and the exit code no longer flips to 2 when the
  only missing dep is optional. New `checkOptionalBin` helper
  separates "must-have" from "nice-to-have" checks.
- **`stado config show` now prints `[budget]` and `[tools]`.** Both
  sections were silently absent — users could set them in
  config.toml but couldn't confirm they took effect without
  reading the loader. Budget always renders (with "(unset)"
  labels) so the knob doubles as documentation.
- **`[budget]` cost guardrail.** Two opt-in thresholds:
  `warn_usd` paints a yellow status-bar pill `budget $X/$cap` and
  appends a one-time system block once the cumulative session cost
  crosses it. `hard_usd` blocks further turns with an actionable
  hint; `/budget ack` unblocks for the rest of the session; `/budget
  reset` re-arms the gate. `stado run` maps the hard cap onto
  `AgentLoopOptions.CostCapUSD` and exits 2 with a pointer at the
  config knob. Defaults are 0 (disabled) so local-runner users with
  no cost concerns see no guardrail UI. Misconfigured pairs
  (`hard_usd ≤ warn_usd`) drop the hard cap with a stderr warning.
- **Project-level instructions auto-loader.** Stado now walks up
  from cwd looking for `AGENTS.md` (preferred, cross-vendor
  convention) or `CLAUDE.md` (fallback) and injects the file body
  into every turn as a system prompt. `stado run` prints the
  resolved path to stderr; the TUI sidebar gains an `Instructions`
  row showing the file's basename. Missing file is a silent no-op;
  a broken file becomes a stderr warning — the TUI never fails to
  boot because of an instructions file. Wired into TUI,
  `stado run`, ACP server, and the headless JSON-RPC session.prompt.
  Compaction retains its purpose-specific summarisation prompt —
  only user-facing turns pick up `AGENTS.md`.
- `[tools]` config section lets users trim the bundled tool set.
  `enabled = [...]` acts as an explicit allowlist; `disabled =
  [...]` removes specific tools from the default. When both are
  set, `enabled` wins. Unknown names log a stderr warning and are
  ignored, so configs survive tool renames.
  Applies everywhere the executor is built: TUI, `run`, `headless`,
  and the headless `tools.list` RPC.
- `stado plugin init <name>` — scaffold a new plugin project. Go
  wasip1 template with the full ABI surface (stado_alloc,
  stado_free, stado_tool_*, stado_log import) plus a working demo
  tool. `--dir` relocates; `--force` overwrites. Pairs with
  SECURITY.md's publish cookbook — zero → signed plugin in
  minutes.
- `stado session logs <id> -f` — follow mode live-tails the trace
  ref. Useful for multi-terminal workflows: agent runs in pane 1,
  logs watches in pane 2. `--interval` tunes poll frequency
  (default 500ms).

#### Earlier iterations

Continued polish after the first round of dogfood fixes. Each item
landed independently so the history tells the shape of the
feature set.

- `stado run --session <id>` — continue a long-running session
  from the CLI. Loads the prior conversation, appends the new
  prompt, persists the exchange so the TUI resume picks it up.
  Useful for scripted follow-ups: `stado run --session react
  "what was that hook we extracted?"`. Same id/prefix/description
  resolver as `session resume`.
- `stado session logs <id>` — render the session's trace ref as
  a scannable one-line-per-tool-call feed. Fills the gap between
  `session show` (summary) and `audit export` (JSONL). Shows
  time, tool(arg), summary, tokens, cost, duration, and marks
  errors with ✗. `--limit N` to cap; accepts the same lookup
  resolver.
- `stado config show` — print the resolved effective config
  (TOML + env + defaults merged). Human table by default, `--json`
  for jq. Answers "why is stado using X?" without reading the
  loader. Highlights when `config.toml` doesn't exist yet.
- `stado stats --json` — structured output for dashboards, CI
  gating, jq piping. Shape:
  `{window_days, total{calls,tokens_in,tokens_out,cost_usd},
  total_duration_ms, by_model, by_tool}`. Empty-window case emits
  a valid empty shape so scripts don't special-case.
- Shell-style aliases on frequent subcommands: `session ls` →
  `list`, `session rm` → `delete`, `session cat` → `export`.
- `session list` status column is now colourised — live green,
  idle grey, detached dim. Respects `NO_COLOR` / `FORCE_COLOR` /
  isatty so piped output stays plain.

### UX sweep — dogfood-driven findings (pre-release polish)

**Session management.**

- `stado session describe <id> [text]` — attach a human-readable
  label to a session. Stored in `<worktree>/.stado/description`.
  `--clear` removes; no-text mode prints the current label.
  Surfaces in `session list` (new DESCRIPTION column), `session
  show` (label: line), and the TUI `/sessions` overview.
- `stado session resume <id>` now accepts UUID prefixes (≥8
  chars) and case-insensitive description substrings.
  Ambiguous matches list candidates so you can narrow:
  `stado session resume react` → resolves via description.
- `stado session search <query>` — grep across every session's
  persisted conversation. Case-insensitive substring by default;
  `--regex` switches to RE2. Flags: `--session <id>` to scope,
  `--max N` to cap hits. Output is `session:<id> msg:<n>
  role:<role>  <excerpt>` for easy piping.
- `stado session export <id>` — render the conversation as
  markdown (default) or raw JSONL (`--format jsonl`). `-o
  session.md` writes to a file; otherwise stdout. Markdown has
  per-role headers, fenced tool-call JSON, fenced tool-result
  bodies, thinking blocks as blockquotes (signature stripped).
- `stado session gc [--apply]` — sweep zero-turn, zero-message,
  zero-compaction sessions older than `--older-than` (default
  24h). Dry-run by default; `--apply` to actually delete. Live
  sessions are always skipped.
- `stado session show` now renders a `usage` line summarising
  tool calls, token counts, cost, and wall time for the session.
- `session list` gains a DESCRIPTION column; `Status` values
  refined to `live` / `idle` / `detached` (was `attached`),
  reflecting whether a process actually holds the worktree.

**TUI additions.**

- `@`-file fuzzy autocomplete in the input. Typing `@` opens an
  inline picker of repo files; Up/Down navigate; Tab/Enter
  accepts, replacing the `@query` fragment with the path plus a
  trailing space. Esc closes without changing the buffer. Email-
  style `user@x` deliberately does NOT trigger — the `@` has to
  be at start of input or follow whitespace.
- **Message queuing.** Enter during streaming no longer silently
  drops your message. The user block appears in the chat right
  away; the LLM-facing `msgs` add is deferred to drain so the
  current turn's context isn't mutated. Ctrl+C/Esc with a queue
  pending clears the queue (doesn't also cancel the stream —
  that's the second press).
- Status row surfaces: cumulative cost, cache-hit ratio (when
  non-zero), and a `queued: <excerpt>` pill while something is
  queued.
- `/describe` slash command — mirrors the CLI subcommand:
  `/describe <text>`, `/describe --clear`, or `/describe` alone
  to read back the current label. Sidebar now renders the
  session label under the stado title.
- `/sessions` overview lists sessions with descriptions when set.

**Shell tab-completion** for session IDs on every session
subcommand that takes one: `show`, `attach`, `resume`, `delete`,
`fork`, `describe`, `revert`, `land`, `tree`, `export`.
Descriptions attach as completion hints — `<TAB>` in bash/zsh/fish
shows "id    description" alongside.

### Opencode / Pi gap features

Three features added after researching the top coding-agent CLIs.

- **`stado stats`** — cost + usage dashboard aggregated from the
  git-native audit log (trace-ref trailers). Works offline /
  airgap — no OTel collector required. Flags: `--days` (default
  7), `--session`, `--model`, `--tools` to include a per-tool
  breakdown. Sorted by cost descending.
- **`stado github install`** — writes a
  `.github/workflows/stado-bot.yml` that fires on issue/PR
  comments starting with `@stado`. Runs `stado run --prompt`
  inside the user's Actions runner and posts the reply back via
  `gh api`. `--force` overwrites; `install` / `uninstall` pair is
  idempotent.
- Status-row cache-hit pill. Renders `cache NN%` when the
  provider reports non-zero prompt-cache reads (ratio is
  `CacheReadTokens / (CacheReadTokens + InputTokens)`).

### Plugins + headless

- **Headless plugin surface.** `plugin.list` and `plugin.run`
  JSON-RPC methods; plugin-driven forks emit
  `session.update { kind: "plugin_fork", plugin, reason, child,
  at_turn_ref, childWorktree }`. Background plugins load on
  `Serve()` entry and tick on `session.prompt` completion.
  Closes the deferred K2 line item.
- **Shutdown ordering** in headless. `Conn.WaitPendingExceptCaller`
  lets the `shutdown` handler drain earlier in-flight requests
  before replying, so `plugin.run` responses can't arrive
  *after* the shutdown ACK.
- **`providers.list.current`** now reports the resolved provider
  (previously parroted `cfg.Defaults.Provider` which is blank on
  the local-fallback path).
- **Persistent plugin lifecycle.** Plugins that export
  `stado_plugin_tick` load once per TUI boot via
  `[plugins].background = [...]` and fire on every turn
  boundary.
- Second validating plugin: `plugins/examples/session-recorder/`
  — `session:read` + `fs:read` + `fs:write` + `stado_plugin_tick`.
  Appends JSONL per turn. Proves the ABI generalises beyond
  auto-compaction.
- `stado plugin installed` — list installed plugin IDs (was
  conflated with the trust-store list before).

### Terminal hygiene

- **OSC color-query responses** no longer leak into the
  textarea. Root cause: bubbletea v1.3 has no OSC parser, and
  slow terminals answer `\x1b]11;?` after stado has acquired
  stdin, so the payload lands as Alt-prefixed rune bursts.
  Two-layer fix: byte-level `oscStripReader` that state-machines
  through the response across Read boundaries + `tea.WithFilter`
  backstop for the Alt-wrapped shape. Both removed once
  bubbletea v2 (native OSC parser) lands.
- **Raw-mode regression** from the OSC wrapper — fixed. The
  earlier stripper was a plain `io.Reader`, which made bubbletea's
  `initInput` type assertion (`p.input.(term.File)`) fail: no raw
  mode, no epoll cancel path, so keystrokes echoed to the
  terminal cursor instead of reaching the TUI. New
  `oscStripFile` embeds `*os.File` so `Fd()`/`Write()`/`Close()`/
  `Name()` forward to stdin and bubbletea can still call
  `term.MakeRaw(fd)`. cancelreader's epoll reads via
  `file.Read()` which routes through the filter.
- **Sidebar no longer latches closed** on the first render. View()
  used to flip `sidebarOpen = false` when width was below the
  min threshold — but the very first View() call runs before
  any `WindowSizeMsg` arrives, at width=0, permanently closing
  the sidebar. Now the flag is preserved; only the current-frame
  render is skipped.
- `hack/tmux-uat.sh` — real-PTY harness. Spawns `./stado` in a
  detached tmux session, asserts against the captured pane.
  Orthogonal to teatest: it catches regressions in the termios +
  cancelreader path (the exact path the two fixes above sit on).

### CLI polish

- `stado` in a non-TTY context now exits 1 with an actionable
  pointer to `run --prompt` / `headless` (was exit 0 with a raw
  `/dev/tty: no such device` leak).
- `stado version` and `stado verify` agree — both read the
  shared `collectBuildInfo()`.
- `stado doctor` uses correct pluralisation ("1 check failed"
  vs "2 checks failed").
- `session attach <unknown>` / `session delete <missing>` print
  concise errors (previously wrapped OS stat errors).
- `plugin trust` error explains both unlock paths: out-of-band
  pubkey trust or `plugin verify . --signer <pubkey>` TOFU.
- Provider-fallback message no longer says "no
  ANTHROPIC_API_KEY" — now "no provider configured — using local
  <runner>". Stale from an earlier anthropic-first era.
- Config init template bumped `claude-sonnet-4-5` →
  `claude-sonnet-4-6`.
- Top-level description: "Sandboxed, git-native coding-agent
  runtime" (was "AI CLI harness and editor" — stado is not an
  editor).

### Testing

- 30 UAT scenario tests covering the enumerated user-facing
  flows in `.learnings/UAT_SCENARIOS.md`. 3 in
  `uat_direct_test.go` + 16 in `uat_scenarios_test.go` + 11 in
  `uat_scenarios_extended_test.go`. All direct-Update —
  teatest's virtual terminal fights stado's sidebar+viewport
  layout for reliable snapshot assertions.
- Phase 11.5 PTY harness for interactive `session tree` —
  teatest-backed end-to-end test that navigates + presses `f` to
  fork. Simpler layout, reliable under teatest.

### Infra

- `hack/otel-compose/` — Jaeger-all-in-one compose fixture for
  eyeballing OTel traces during development. Closes Phase 6
  verify.
- Plugin-publish cookbook in SECURITY.md — nine-step guide from
  `gen-key` through rotation.

### Fixes

- Session list's "attached" status now reflects whether a pid is
  actually alive (reads `.stado-pid` + `signal(0)` probe). Was
  "worktree exists on disk" regardless of whether anyone was
  using it.
- Removed the dead `short()` helper from `cmd/stado/session.go`
  that lint caught after an adjacent-file edit triggered a re-
  lint.

---

## Earlier

See `git log --oneline` for pre-changelog history. PLAN.md has the
phase-by-phase roadmap; most ✅ rows there landed before this
changelog started.
