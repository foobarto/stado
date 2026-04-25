# Slash commands

Every TUI command reachable with `/` or `Ctrl+P`. `/` opens compact
inline fuzzy suggestions above the chat input; `Ctrl+P` opens the full
modal command palette. Commands are grouped by intent — Quick /
Session / View — in both surfaces so the list stays scannable as it
grows.

Press `Ctrl+P` to see them all in the modal palette:

```
                Commands                                    esc

                Search

                Quick
                Show keyboard shortcuts and help     /help  ?
                Clear the message history            /clear
                Quit stado                           /exit  ctrl+d
                Toggle BTW mode                      /btw   ctrl+x ctrl+b

                Session
                Open the agent picker                 /agents  ctrl+x a
                Open a model picker                  /model
                Open the status modal                /status  ctrl+x s
                ...

                View
                Toggle the right-hand sidebar        /sidebar  ctrl+t
                Open the theme picker                /theme    ctrl+x t
                Split chat into activity+conversation /split
                ...
```

Right column shows `/name  shortcut` when a keybind exists. `/` uses the
same command list, but renders it inline near the prompt instead of
taking over the screen.

## Quick

| Command | Shortcut | What |
|---------|----------|------|
| `/help` | `?` | Show the help overlay (keybinds + slash commands) |
| `/clear` | | Wipe conversation state; cancels any in-flight stream |
| `/exit` | `Ctrl+D` | Quit stado cleanly |
| `/btw` | `Ctrl+X Ctrl+B` | Toggle off-band BTW mode for side questions |

## Session

| Command | What |
|---------|------|
| `/agents` | Open the agent picker for Do, Plan, and BTW (`Ctrl+X A`) |
| `/model` | Open a model picker (no args) or set id directly: `/model claude-opus-4-7`; `Ctrl+X M` opens the picker, `Ctrl+F` toggles favorites, and `Ctrl+A` shows provider setup for the selected row |
| `/status` | Open the status modal for provider, tools, plugins, MCP, LSP readiness, OTel, sandbox, and context, with next-step hints (`Ctrl+X S`) |
| `/provider` | Show active provider + capabilities (cache, thinking, vision, ctx size) |
| `/tools` | List tools visible to the model (honours `[tools]` filter + plan mode) |
| `/approvals` | Compatibility hint: native tool approvals were removed; plugins can request explicit UI approval |
| `/compact` | Summarise the conversation and replace prior turns (requires y/n confirmation) |
| `/context` | One-stop session state: session id, cost, budget caps, loaded instructions, skills, hook |
| `/providers` | Active provider + detected local runners (ollama / lmstudio / vllm / llamacpp), including load/start hints when a runner has no models ready |
| `/plugin` | List installed plugins; `/plugin:<id>-<ver> <tool> [json]` to run one |
| `/switch` | Open the searchable session manager (`Ctrl+X L`) |
| `/sessions` | Other resumable sessions for this repo (with switch/resume hints) |
| `/subagents` | Recent spawned child sessions with status, worktree, changed-file counts, scope violations, and adoption commands |
| `/new` | Create and switch to a fresh session (`Ctrl+X N`) |
| `/describe <text>` | Label the current session (visible in `session list`, sidebar, etc.) |
| `/budget` | Show current cost + caps; `/budget ack` continues past the hard cap |
| `/skill` | List `.stado/skills/*.md`; `/skill:<name>` injects a skill body as a user prompt |
| `/retry` | Regenerate the last assistant turn from the same user prompt |
| `/session` | Print the current session id + worktree (copy for other shells) |

## View

| Command | Shortcut | What |
|---------|----------|------|
| `/sidebar` | `Ctrl+T` | Toggle the right-hand sidebar |
| `/theme` | `Ctrl+X T` | Open the bundled theme picker; `/theme <id>`, `/theme light`, `/theme dark`, and `/theme toggle` switch directly |
| `/thinking` | `Ctrl+X H` | Cycle thinking display; `/thinking show`, `/thinking tail`, and `/thinking hide` set it directly |
| `/debug` | | Toggle sidebar diagnostics and the info log tail |
| `/split` | | Split the chat pane into activity (top) + conversation (bottom) |
| `/todo <title>` | | Add a todo item to the sidebar's Todo list |

## Behavioural notes

- **Slash commands during streaming.** `/clear`, `/retry`, etc. fire
  immediately — they bypass the mid-stream queue that otherwise
  defers regular user prompts until after the current turn drains.
- **Slash suggestions vs command palette.** `/` opens inline fuzzy
  suggestions above the input. `Ctrl+P` opens the full modal command
  palette.
- **Model defaults.** Selecting a model from the picker, or setting one
  with `/model <id>`, writes `[defaults].model` in `config.toml`; when
  the picker selection changes provider, `[defaults].provider` is saved
  too.
- **Provider setup.** `Ctrl+A` inside the model picker closes the
  picker and prints provider-specific setup: missing API-key env vars,
  configured preset endpoints, or local-runner startup hints. Secrets
  stay outside `config.toml`.
- **Session manager.** `/switch` opens the same TUI manager as
  `Ctrl+X L`: search, switch/resume, rename, fork, confirmed delete of
  inactive sessions, or create a fresh session.
- **Session switching safety.** Switch, new, and fork actions are
  blocked while a queued prompt, stream, approval, compaction, or tool
  is active, so prompts and writes do not silently land in the wrong
  session. Editor drafts and chat scroll position are cached per
  session and restored when switching back.
- **Theme selection.** `/theme` offers the bundled `stado-dark`,
  `stado-light`, and `stado-contrast` themes. Selecting one updates the
  current TUI and writes `$XDG_CONFIG_HOME/stado/theme.toml` so the
  next run starts with the same theme. `/theme light`, `/theme dark`,
  and `/theme toggle` provide direct light/dark switching. If the
  current `theme.toml` is custom, the picker shows it as the current
  custom row. Custom themes can set `[markdown].style` to `auto`,
  `light`, or `dark`.
- **Thinking display.** `/thinking` and `Ctrl+X H` only affect the TUI
  viewport. Thinking blocks remain captured and persisted even when the
  current display mode is `hide` or `tail`.
- **Unknown commands.** Typing `/notacommand` produces
  `unknown command: /notacommand (try /help)` as a system block
  rather than silently eating the input.
- **Case sensitivity.** Names are lowercase. `/HELP` won't match.
- **Arguments.** Split on whitespace — tokens after the command name
  are passed through to the handler.

## Adding a new slash command

A new command has three touch points:

1. **Handler** in `internal/tui/model.go`'s `handleSlash` switch.
   For early-return handlers (with their own rendering), the
   `defer m.renderBlocks()` at the top of `handleSlash` ensures
   the system block they append reaches the viewport — no need to
   call it explicitly.
2. **Palette entry** in `internal/tui/palette/slash.go`'s
   `Commands` slice. Fields: name, description, shortcut (optional),
   group (`Quick` / `Session` / `View`).
3. **Help overlay** — nothing to do; the `?` overlay reads from the
   same palette.Commands slice, so your new entry shows up for free.

Follow the surrounding style: imperative descriptions ("List
installed plugins"), shortcut-if-obvious, group by purpose. Look
at existing handlers for conventions (appendBlock + no explicit
render is the common pattern now that `defer renderBlocks()` is in
place).

## See also

- [features/budget.md](./budget.md) — the `/budget` gate
- [features/skills.md](./skills.md) — `/skill` loader
- [commands/session.md](../commands/session.md) — every session subcommand mirrors a slash-or-not variant
