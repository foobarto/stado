# `stado` (TUI)

The primary surface. Launching `stado` with no subcommand opens the
TUI inside the current repo.

## What it does

Boot sequence:

1. Load config (koanf: user `config.toml` → project `.stado/config.toml`
   with security strip → `STADO_*` env → defaults). Stripped project
   keys print a one-time stderr warning — see [config.md](config.md).
2. Resolve the provider — explicit `[defaults].provider` if set,
   otherwise start an async probe for bundled local runners
   (ollama / llamacpp / vllm / lmstudio) + user presets, picking the
   first reachable one. If the probe is still running when you hit
   `Enter` on the first prompt, the prompt queues and auto-replays when
   the probe resolves instead of freezing the UI on a duplicate probe.
3. Open or create a session for this cwd (sidecar bare repo at
   `$XDG_DATA_HOME/stado/sessions/<repo-id>.git`).
4. Walk cwd upward for `AGENTS.md` / `CLAUDE.md` → injected as the
   system prompt on every turn.
5. Load `.stado/skills/` (flat `*.md` and `<name>/SKILL.md` bundles) →
   available as `/skill:<name>` for the operator **and** to the model
   via `skills__load` (EP-0045).
6. Load the bundled `auto-compact` background plugin, then any extra
   installed plugins from `[plugins].background` in **user** config.
   Project-local plugins under `.stado/plugins/` autoload only when
   `[plugins] allow_project_plugins = true` in user config (default
   false).
7. Start the bubbletea event loop.

Shutdown: `Ctrl+D` or `/exit`.

## Why it exists

Interactive coding sessions benefit from:

- Visible conversation history with distinct user / assistant /
  thinking / tool blocks.
- Live-streaming assistant text with an elapsed counter during
  long thinking.
- Explicit plugin approval cards for plugins that declare
  `ui:approval`.
- Mid-stream slash commands (`/clear`, `/compact`, `/retry`).
- Context-window visibility (soft/hard thresholds, cost, cache ratio).
- Automatic hard-threshold recovery through the bundled `auto-compact`
  background plugin.
- In-process session switching and fresh-session creation.
- A sidebar pinning session label, cwd, model, instructions, skills.

Everything the TUI does is backed by `internal/runtime`'s core agent
loop, so `stado run` / `stado acp` / `stado run --headless` produce
bit-identical conversations from the same input.

## Launching

```sh
stado                     # TUI in current dir
stado session resume abc  # TUI rooted in a past session's worktree
```

`stado session resume` changes into the session's worktree before
booting so replay of `.stado/conversation.jsonl` picks up where you
left off.

### Tracing startup / first-turn issues

For focused TUI diagnostics:

```sh
STADO_TUI_TRACE=1 stado
```

This enables a narrow trace log for the startup probe and first-turn
path. The sidebar `Logs` section will show events like provider probe
start/finish, prompt queueing behind the probe, provider resolution,
and stream start/first-event timing.

If `[otel].enabled = true`, the TUI also exports real OTel spans for the
session boot path, including the TUI run span and the startup local-
provider probe span. The sidebar trace mode is additive; it is for
human debugging, not a replacement for OTLP export.

## Theme and templates

- Bundled themes: `stado-dark`, `stado-light`, `stado-contrast`, and
  `stado-rose`.
- `/theme` or `Ctrl+X T` opens the theme picker. `/theme <id>` switches
  directly. `/theme light`, `/theme dark`, and `/theme toggle` are
  shortcuts for quick mode changes.
- Bundled theme selections persist as `[tui].theme` in
  `$XDG_CONFIG_HOME/stado/config.toml`. Custom theme overrides still
  live at `$XDG_CONFIG_HOME/stado/theme.toml` and are loaded when
  `[tui].theme` is unset. If the current override is a custom theme, the
  picker shows it as the current custom row. Custom themes can set
  `[markdown].style` to `auto`, `light`, or `dark`; `auto` follows
  background luminance.
- Template overrides live at `$XDG_CONFIG_HOME/stado/templates/*.tmpl`.
- Missing template files fall back to the bundled defaults, so you can
  override a single widget without copying the entire template set.

## Layout

```
┌────────────────────────────────────┬──────────────────┐
│ (chat viewport)                    │ stado            │
│                                    │ <session label>  │
│   user / assistant / thinking /    │                  │
│   tool / system blocks             │ Context          │
│                                    │ 12.3K tokens (24%)│
│                                    │ $0.15 spent      │
│                                    │                  │
│                                    │ Cwd              │
│                                    │ /path            │
├────────────────────────────────────┤                  │
│ ╭──────────────────────────────╮   │ Instructions     │
│ │ Type a message...            │   │ AGENTS.md        │
│ │ Do · claude-sonnet-4-6       │   │                  │
│ ╰──────────────────────────────╯   │ Skills           │
│ ● thinking 12s  12.3K · $0.15 ctrl+p commands  Todo / Model / Version │
└────────────────────────────────────┴──────────────────┘
```

- **Chat viewport** — all conversation blocks. Scroll with PageUp
  / PageDown. GotoBottom on every new block.
- **Input box** — bubbles textarea. Grows with newlines (Shift+Enter).
- **Status bar** — compact cwd, branch, version, streaming state,
  elapsed-during-stream pill, tokens / context %, cost, keybind hint.
- **Sidebar** — pinned metadata. Toggle with `Ctrl+T` or `/sidebar`.
  Debug diagnostics and the info log tail are hidden by default; use
  `/debug` to expand them when investigating runtime/provider issues.
- **Subagents** — while `spawn_agent` runs, the sidebar shows recent
  child session status, changed-file counts, scope violations, and
  whether adoption is ready. `/adopt` dry-runs the latest adoptable
  worker child; `/adopt <child> --apply` applies non-conflicting child
  changes into the current parent session.

### Split view

`/split` divides the chat viewport into two panes:

- **Top** — activity tail: `tool` + `system` blocks.
- **Bottom** — conversation: `user` + `assistant` + `thinking`.

Useful in heavy tool-use sessions where operational noise was
drowning the conversation.

## Keybinds

Full list lives in the `?` help overlay. The ones most worth
memorising:

| Key | Action |
|-----|--------|
| `Enter` | Submit the current input (while a turn is streaming, steers: injects into the current turn) |
| `Shift+Enter` | Newline in input (multi-line prompt) |
| `Alt+Enter` | Queue message for the next turn |
| `Ctrl+Enter` | Interrupt: cancel the turn and run now |
| `@` | Open inline agent/session/skill/file completion |
| `Tab` | Toggle Plan / Do mode |
| `Ctrl+P` | Open command palette |
| `/` | Open inline slash-command suggestions above the input |
| `Ctrl+X A` | Open agent picker |
| `Ctrl+X M` | Open model picker (`Ctrl+A` setup, `Ctrl+F` favorite) |
| `Ctrl+X L` | Open session manager |
| `Ctrl+X N` | Create and switch to a fresh session |
| `Ctrl+X T` | Open theme picker |
| `Ctrl+X S` | Open status modal |
| `Ctrl+X H` | Cycle thinking display: preview, auto, collapsed, expanded |
| `Ctrl+X O` | Cycle tool-output display: preview, auto, collapsed, expanded |
| `Ctrl+X K` | Open shared task manager |
| `Shift+Tab` | Expand the focused (or latest) tool call / assistant turn details |
| `Alt+Up` / `Alt+Down` | Move focus to older / newer expandable block (then `Shift+Tab` toggles it) |
| Mouse left-click | Click any tool block to focus + expand it (hold `Shift` while click-dragging if you need terminal-native selection — or set `[tui].mouse_capture = false` to disable app mouse capture entirely) |
| `Ctrl+T` | Toggle sidebar |
| `Ctrl+X Ctrl+B` | Toggle BTW mode |
| `Esc` / `Ctrl+G` | Interrupt model: clear queued prompt / cancel stream / cancel running tool |
| `Ctrl+C` | Cancel stream / clear pending queue (converges on the same cancel semantic as `Esc` / `Ctrl+G`) |
| `Ctrl+D` | Exit stado |
| `?` | Help overlay (keybinds + slash commands) |
| `Up` / `Down` | Prev/next in input history |
| `PageUp` / `PageDown` | Scroll chat viewport |
| `Home` | Scroll to top |
| `Ctrl+Alt+G` / `End` | Scroll to bottom |

Keybindings are configurable (since v0.65.0). Set the base layout with
`[keymap].schema = "emacs"` (default), `"vscode"`, or `"vim"`, and override
individual actions under `[keymap.bindings]` keyed by action name (e.g.
`sidebar_toggle = "ctrl+t"`). An unknown action name is a non-fatal
warning, not a boot failure — the valid overrides still apply. Project
repos cannot override keymaps — `[keymap]` in `.stado/config.toml` is
stripped (EP-0044).

### Vim modal editing (`schema = "vim"`)

Setting `[keymap].schema = "vim"` turns the chat input into a modal editor
with `NORMAL`, `INSERT`, and `VISUAL` modes. The current mode shows as a
pill in the inline status row under the input box (only on this schema). On
launch you start in `INSERT` so you can type immediately.

- **Mode switching:** `Esc` (in `INSERT`) → `NORMAL`. `i a I A o O` →
  `INSERT`. `v` → `VISUAL`. `Esc` in `NORMAL` is a **no-op** — to interrupt
  the model use **`Ctrl+G`** (its `SessionInterrupt` binding survives in
  every schema). `Enter` from `NORMAL` submits the line.
- **Motions** (count-prefixable, e.g. `3j`, `2w`): `h j k l`, `w b e`,
  `0 ^ $`, `gg G`.
- **Edits:** `x` (delete char), `D` (delete to end of line), `C` (change to
  end of line), `s` (substitute char), `r<char>` (replace char); line-wise
  `dd` `cc` `yy`; operator + motion — `dw de db d$ d0`, `cw ce cb c$`,
  `yw ye yb y$` (counts apply, e.g. `2dw`).
- **Registers / paste:** one unnamed register, fed by `x` / `d*` / `y*`;
  `p` pastes after, `P` before (line-wise vs char-wise tracked).
- **Visual:** `v` then a motion to extend the selection, then `d`/`x`
  (delete), `y` (yank), or `c` (change).
- **Modifier chords still work in `NORMAL`/`VISUAL`** — `Ctrl+P`, `Ctrl+D`,
  the `Ctrl+X` pickers, scroll chords, etc. are not swallowed by the modal
  layer.

Not implemented in v1 (deliberately): text objects (`ciw`), named registers,
marks, macros, `:` ex-commands, `/` search, `.` repeat, `>>`/`<<` indent,
and `J` join. `INSERT` mode keeps the standard emacs/readline input editing
(`Ctrl+A`/`Ctrl+E`/`Ctrl+W`/…), and the modal layer is entirely inert on the
`emacs` and `vscode` schemas (there, `Esc` keeps its interrupt behaviour).

### Plan, Do, and BTW mode

`Tab` toggles between the main two modes:

- **Do** (default) — all configured tools visible to the model.
- **Plan** — only non-mutating tools (`read`, `grep`, LSP lookup,
  …). Mutating (`write`, `edit`) and exec (`bash`) are filtered
  from the `TurnRequest.Tools`. The model naturally shifts to
  producing a plan / outline rather than executing.

The input rail changes colour by mode so Do, Plan, and BTW are visible
without reading the inline status row.

`Ctrl+X A` or `/agents` opens a picker for all three agents. `Tab`
still toggles the common Do/Plan path, and `/btw` or `Ctrl+X Ctrl+B`
still toggles **BTW** mode for off-band side questions. BTW replies
render in their own block and do not append to the main conversation
history.

The active agent shows in the input box's inline status row and in the
sidebar Agent section once the chat view is active.

Typing `@` in the message editor opens inline completion. Agent rows
come first; accepting Do, Plan, or BTW switches the active agent and
removes the mention from the draft. Session rows come next; accepting a
session-only mention switches to that session, while accepting a session
inside a longer prompt inserts `session:<id>`. Skill rows inject the
selected skill body into the conversation and remove the mention from
the draft. Doc rows surface root Markdown files and `docs/**/*.md`.
Symbol rows surface top-level Go declarations, top-level Python
`class`/`def` declarations, and top-level JavaScript/TypeScript class,
function, and variable declarations, plus top-level shell functions in
`.sh` and `.bash` scripts, with `path:line` locations.
Accepting docs, symbols, or files inserts the selected repo-relative
reference into the prompt.

Completed assistant responses include a compact muted footer with the
agent, model/provider, elapsed time, tool count, token delta, and cost
delta for that turn when the provider reports usage.

The bottom status row keeps cwd, branch, active session label or short
session id, version, usage, cost, and command hints visible when there
is enough width. On narrow terminals it drops the left context first and
keeps the active state and usage side readable.

Session switching caches the inactive session's editor draft, chat scroll
position, and selected provider/model in memory. Switching is blocked
during queued prompts, streams, approvals, compaction, running tools, and
background plugin ticks.

Thinking blocks and tool-output panes are display-controlled, not
capture-controlled. Each has its own setting that cycles through the same
four modes:

- **preview** (default) — clip to the last few lines (the thinking tail;
  the tool `tool_output_collapsed_height` panel) with a "… N more lines"
  footer.
- **auto** — render full while the block is streaming/running, then
  collapse it to a single line once it finishes.
- **collapsed** — always a single summary line (`▪ thinking · N lines`,
  `▸ <tool> · N lines`).
- **expanded** — always the full body, unclipped.

`Ctrl+X H` (or `/thinking [mode]`) controls thinking; `Ctrl+X O` (or
`/tool-display [mode]`) controls tool output; both persist to
`[tui].thinking_display` / `[tui].tool_display`. In any mode, clicking a
block — or `Shift+Tab` on the focused/latest one — flips just that block
between full and one-line for the rest of the session. The settings only
affect the TUI viewport; provider-native thinking + tool results are still
preserved in the session transcript. (Legacy `thinking_display` values
`show`/`tail`/`hide` still load, mapped to `expanded`/`preview`/`collapsed`.)

## Slash commands

See [features/slash-commands.md](../features/slash-commands.md) for
the full list. `/` opens inline fuzzy suggestions above the input;
`Ctrl+P` opens the full modal command palette. Quick reference:

- `/help` — overlay with every keybind + slash command
- `/clear` — wipe conversation; cancels any in-flight stream
- `/compact` — summarise and replace conversation (y/n confirm)
- `/verify [on|off|status]` — toggle the configured completion command gate
- `/retry` — regenerate the last assistant turn
- `/model` — model picker (`Ctrl+X M`); marks the current model and
  surfaces favorites/recents first. Press `Ctrl+F` in the picker to
  toggle a favorite. Selecting a model saves it as the new default.
- `/agents` — agent picker for Do, Plan, and BTW
- `/persona [name]` — bare opens the persona picker; `/persona <name>`
  swaps directly to the named persona and persists the choice in user
  `[defaults].persona`. Resolution: bundled + user
  (`~/.config/stado/personas/`) always; project (`.stado/personas/`)
  only when `[defaults] allow_project_persona = true` in user config
  (default false). Personas inject their system prompt and tool/cap
  surface into subsequent turns until the next swap; the system block
  reports `persona: <old> → <new>`
- `/theme` — theme picker; bundled choices are `stado-dark`,
  `stado-light`, `stado-contrast`, and `stado-rose`; `/theme light`,
  `/theme dark`, and `/theme toggle` switch without opening the picker;
  bundled choices persist via `[tui].theme`; custom `theme.toml`
  overrides appear as the current custom row
- `/status` — modal summary of provider, model, tools, plugins, MCP,
  provider credential health, LSP readiness, OTel, sandbox, and context,
  with next-step hints such as `/model`, `/tools`, `/plugin`, and
  `config.toml`; configured MCP server names are summarized without
  probing them, and cached plugin/MCP health is shown after lifecycle or
  attach attempts have run
- `/provider` — open the provider credential manager: a modal listing
  every known provider with its **redacted** credential status (the
  env-var name and a configured/unset marker, never the secret). Select a
  provider with the arrow keys and press Enter to add or modify its
  credential — confirm or edit the env-var name, set an optional base-URL
  override (only for anthropic-compat / OAI-compat providers), and, when
  an OS keyring is available, type a key into a **masked** field that is
  stored in the keyring (otherwise the form shows the `export` hint). Save
  records only the env-var reference in `config.toml`; the secret is never
  written to config or echoed to scrollback. `ctrl+d` unsets a provider's
  credential. This is the in-TUI counterpart to the `stado auth` CLI
- `/provider <name>` — setup guidance for a named provider such as
  `lmstudio`, `openai`, or `anthropic` (the old behavior, kept for the
  argument form)
- `/providers` — active provider credential health plus detected local
  runners, with load/start hints when a runner has no models ready
- `/thinking` — cycle and persist thinking display;
  `/thinking preview|auto|collapsed|expanded`
- `/tool-display` — cycle and persist tool-output display;
  `/tool-display preview|auto|collapsed|expanded`
- `/switch` — searchable session manager
- `/tree` — navigable session fork graph: switch, branch a fork at any
  past turn, or peek a read-only transcript (`Ctrl+X G`)
- `/new` — create and switch to a fresh session
- `/sessions` — textual session overview, including the
  active-session-only policy for inactive sessions
- `/subagents` — recent spawned child sessions with status, worktree,
  changed-file counts, scope violations, and adoption commands
- `/adopt [child] [--apply]` — dry-run or explicitly apply worker
  subagent changes into the current parent session
- `/tasks` — shared task manager for user/agent work items;
  `/tasks add <title>` creates a quick open task
- `/debug` — toggle sidebar diagnostics/log tail
- `/context` — session state (tokens, cost, budget, instructions, skills)
- `/memory [on|off|status]` — show or toggle approved-memory retrieval
  for this session
- `/btw` — off-band side-question mode
- `/skill` — list user-invocable skills (`[user-only]` marks entries hidden
  from the model); `/skill:<name>` injects a skill body into the conversation.
  Model-only (`user-invocable: false`) entries are omitted and direct user
  invocation is refused. Skills also surface to the model: each `name` +
  `description` enters the system prompt and the model can load one on
  its own via `skills__load` (EP-0045). A skill with
  `disable-model-invocation: true` stays operator-only; denying
  `skills__load` via `[tools].disabled` turns model invocation off while
  `/skill:` keeps working.

## Multi-session Overlay

`Ctrl+X L` or `/switch` opens the searchable session overlay. The
overlay owns its own keys:

| Key | Action |
|-----|--------|
| `Enter` | Switch/resume the highlighted session |
| `Ctrl+N` | Create and switch to a fresh session |
| `Ctrl+R` | Rename the highlighted session |
| `Ctrl+F` | Fork the highlighted session and switch to the child |
| `Ctrl+D` | Confirmed delete of the highlighted inactive session |
| `Esc` | Close or cancel the current overlay action |

Deleting the active session is blocked. Switch, new, and fork keep the
existing safety gate: submit or clear queued prompts, approval cards,
compaction prompts, streams, running tools, and background plugin ticks
first.

## Session Tree Overlay

`Ctrl+X G` or `/tree` opens the session fork graph for the current repo —
every session that descends from a fork, with the fork point labelled by
the parent turn it branched from. The current session's lineage is pinned
to the top and auto-expanded; the footer shows the session count (and a
`⚠ capped` warning when the forest hit the 5000-session cap). Sessions
with no worktree on disk (detached) render dimmed and offer neither
switch nor peek.

| Key | Action |
|-----|--------|
| `↑` / `k`, `↓` / `j` | Move (lands on session rows and expanded turn rows; clamps, no wrap) |
| `→` / `l`, `←` / `h` | Expand / collapse a session's turn list |
| `g` / `G` | Jump to the first / last row |
| `Enter` (session row) | Switch to / resume the highlighted session |
| `Enter` (turn row) | Peek that session's transcript, read-only |
| `b` (turn row) | Branch — fork the session at that turn and switch to the child |
| `Esc` / `Ctrl+C` | Close the tree |

**Branch** forks at the selected turn's exact file tree (not the
session's tip), leaving the parent untouched, then switches to the new
child — the same gate as a normal switch/fork applies. Pressing `b` on a
session header (not a turn) shows a reminder to land on a turn first.

**Peek** is strictly read-only: it loads the target session's saved
conversation from disk without opening or mutating it. The header is
honest about scope — it shows the **whole** conversation on disk, not a
point-in-time snapshot at turn N (turns are git tags and the transcript
carries no per-message turn field, so exact slicing isn't available in
this version); a banner flags when the session has turns past the one you
peeked. Inside the peek: `↑` / `↓` / `j` / `k` and `PgUp` / `PgDn`
scroll, `g` / `G` jump, `b` branches from the peeked turn. `Esc` /
`Ctrl+C` is layered — the first press closes the peek and returns to the
tree, the second closes the tree.

## Approvals

Native bundled tools no longer pause on the old TUI approval loop.
Control the native surface by narrowing `[tools].enabled` or
`[tools].disabled`.

Plugins can still ask for explicit user approval by declaring the
`ui:approval` capability and calling the approval host import. Those
requests render as a focused approval card with Allow/Deny actions while
the text input remains editable. The old `/approvals` slash command is
kept as a compatibility hint and explains this migration path.

## Config

`stado config show` prints the resolved effective config. The TUI-
relevant sections:

| Section | Purpose |
|---------|---------|
| `[defaults]` | provider, model, `allow_project_persona` (user only) |
| `[tools]` | trim the bundled tool set |
| `[context]` | soft / hard thresholds on context-window usage |
| `[budget]` | warn + hard caps on cumulative cost and tokens |
| `[hooks]` | `post_turn` lifecycle shell hook (user only) |
| `[plugins]` | `allow_project_plugins`, `background` (user only), CRL, Rekor |
| `[mcp.servers.<name>]` | external MCP tool servers (user only) |
| `[lsp]` | `auto_diagnostics` (user only; default false) |
| `[tui]` | display preferences (`theme`, `thinking_display`, `tool_display`, `mouse_capture`) |
| `[keymap]` | layout + overrides (**user only** — stripped from project config) |

### `[tui].mouse_capture`

Default `true` — stado captures mouse events so left-click expands a
tool block and the scroll wheel scrolls the conversation. Trade-off:
the terminal's native click-drag-to-select-text is suppressed.
Workarounds:

- **Hold `Shift` while click-dragging.** Most modern terminals
  (Alacritty, kitty, iTerm2, gnome-terminal, Konsole, WezTerm) honour
  this — selection passes through to the terminal regardless of app
  mouse capture.
- **Set `[tui].mouse_capture = false`** to disable app mouse capture
  entirely. Native click-drag-to-select works everywhere; click-to-
  expand and mouse-wheel scroll are unavailable. Use `Alt+Up` /
  `Alt+Down` to navigate tool blocks and `PageUp` / `PageDown` to
  scroll.

### Selecting & copying chat text cleanly

Terminal selection works on a rectangular grid: dragging across rows
that span both the chat and the sidebar copies the sidebar text too,
and trailing pad spaces on each chat row land in your clipboard.
Three escape hatches, picking the cheapest first:

1. **`Ctrl+T` to hide the sidebar before selecting.** stado then
   stops padding chat rows to a fixed width — your selection is just
   the visible text. This is usually the fastest path.
2. **`Alt+drag` (block / rectangular selection)** in terminals that
   support it — Alacritty, kitty, iTerm2, gnome-terminal (Ctrl+Alt
   on some configs), Konsole. Selects only the rectangle you drag
   over, ignoring the sidebar.
3. **`[tui].mouse_capture = false`** for permanent native selection
   (see above).

## Gotchas

- **The cwd matters.** The TUI opens a session for whichever repo
  owns the current directory. `cd` first, then launch.
- **AGENTS.md / CLAUDE.md is cwd-walk upwards.** Nearest wins;
  you can put one in a subdirectory for per-module instructions
  in a monorepo.
- **Slash commands during streaming route immediately** (since the
  queue-during-stream fix). Regular text prompts queue until the
  current turn drains.
- **The input starts taller than one line** so short multi-line prompts
  are visible without immediate growth. It still horizontally scrolls
  on long single lines; use `Shift+Enter` for explicit newlines.
- **Landing view** — empty sessions start with a compact stado logo,
  centered input, command hints, cwd, and version. Once the first block
  arrives, the normal chat layout takes over.
- **Token budget (`[budget].hard_tokens`).** The hard gate uses
  session-cumulative input plus output (v0.75.2 fixed per-turn input
  counting). See [features/budget.md](../features/budget.md).
- **Auto-LSP is opt-in.** `[lsp] auto_diagnostics = true` in user
  config enables language-server spawns after mutating edits; default
  is off (v0.75.0).

## See also

- [session.md](session.md) — session management
- [features/slash-commands.md](../features/slash-commands.md)
- [features/tasks.md](../features/tasks.md)
- [features/sandboxing.md](../features/sandboxing.md)
- [features/budget.md](../features/budget.md)
