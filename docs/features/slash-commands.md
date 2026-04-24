# Slash commands

Every TUI command reachable with `/` or `Ctrl+P`. Commands are
grouped by intent — Quick / Session / View — so the palette stays
scannable as the list grows.

Types a `/` and see them all:

```
                Commands                                    esc

                Search

                Quick
                Show keyboard shortcuts and help     /help  ?
                Clear the message history            /clear
                Quit stado                           /exit  ctrl+d
                Toggle BTW mode                      /btw   ctrl+x ctrl+b

                Session
                Open a model picker                  /model
                ...

                View
                Toggle the right-hand sidebar        /sidebar  ctrl+t
                Split chat into activity+conversation /split
                ...
```

Right column shows `/name  shortcut` when a keybind exists. `Ctrl+P`
is an alias for the `/` opener so touch-typists don't have to
context-switch to the slash key.

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
| `/model` | Open a model picker (no args) or set id directly: `/model claude-opus-4-7` |
| `/provider` | Show active provider + capabilities (cache, thinking, vision, ctx size) |
| `/tools` | List tools visible to the model (honours `[tools]` filter + plan mode) |
| `/approvals` | Compatibility hint: native tool approvals were removed; plugins can request explicit UI approval |
| `/compact` | Summarise the conversation and replace prior turns (requires y/n confirmation) |
| `/context` | One-stop session state: session id, cost, budget caps, loaded instructions, skills, hook |
| `/providers` | Active provider + detected local runners (ollama / lmstudio / vllm / llamacpp) |
| `/plugin` | List installed plugins; `/plugin:<id>-<ver> <tool> [json]` to run one |
| `/sessions` | Other resumable sessions for this repo (with `stado session resume` hints) |
| `/describe <text>` | Label the current session (visible in `session list`, sidebar, etc.) |
| `/budget` | Show current cost + caps; `/budget ack` continues past the hard cap |
| `/skill` | List `.stado/skills/*.md`; `/skill:<name>` injects a skill body as a user prompt |
| `/retry` | Regenerate the last assistant turn from the same user prompt |
| `/session` | Print the current session id + worktree (copy for other shells) |

## View

| Command | Shortcut | What |
|---------|----------|------|
| `/sidebar` | `Ctrl+T` | Toggle the right-hand sidebar |
| `/split` | | Split the chat pane into activity (top) + conversation (bottom) |
| `/todo <title>` | | Add a todo item to the sidebar's Todo list |

## Behavioural notes

- **Slash commands during streaming.** `/clear`, `/retry`, etc. fire
  immediately — they bypass the mid-stream queue that otherwise
  defers regular user prompts until after the current turn drains.
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
