# CHANGELOG

Notable changes to stado, reverse-chronological. Pre-1.0; breaking
changes still allowed between tags. Sections: UX / CLI / TUI /
Plugins / Infra / Fixes.

## Unreleased

### Iteration-cycle additions (post-initial-sweep)

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
