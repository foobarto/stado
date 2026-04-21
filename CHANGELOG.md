# CHANGELOG

Notable changes to stado, reverse-chronological. Pre-1.0; breaking
changes still allowed between tags. Sections: UX / CLI / TUI /
Plugins / Infra / Fixes.

## Unreleased

### Iteration-cycle additions (post-initial-sweep)

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
- Second validating plugin: `examples/plugins/session-recorder/`
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
