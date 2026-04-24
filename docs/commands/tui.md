# `stado` (TUI)

The primary surface. Launching `stado` with no subcommand opens the
TUI inside the current repo.

## What it does

Boot sequence:

1. Load config (koanf: `config.toml` + `STADO_*` env + defaults).
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
5. Load `.stado/skills/*.md` → available as `/skill:<name>`.
6. Load the bundled `auto-compact` background plugin, then any extra
   installed plugins from `[plugins].background`.
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
loop, so `stado run` / `stado acp` / `stado headless` produce
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

- Theme overrides live at `$XDG_CONFIG_HOME/stado/theme.toml`.
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
- **Status bar** — streaming state, elapsed-during-stream pill,
  tokens / context %, cost, keybind hint.
- **Sidebar** — pinned metadata. Toggle with `Ctrl+T` or `/sidebar`.
  Debug diagnostics and the info log tail are hidden by default; use
  `/debug` to expand them when investigating runtime/provider issues.

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
| `Enter` | Submit the current input |
| `Shift+Enter` | Newline in input (multi-line prompt) |
| `Tab` | Toggle Plan / Do mode |
| `Ctrl+P` / `/` | Open command palette |
| `Ctrl+X L` | Open session switcher |
| `Ctrl+X N` | Create and switch to a fresh session |
| `Ctrl+T` | Toggle sidebar |
| `Ctrl+X Ctrl+B` | Toggle BTW mode |
| `Ctrl+C` | Cancel stream / clear pending queue |
| `Ctrl+D` | Exit stado |
| `?` | Help overlay (keybinds + slash commands) |
| `Up` / `Down` | Prev/next in input history |
| `PageUp` / `PageDown` | Scroll chat viewport |
| `Ctrl+G` / `Home` | Scroll to top |
| `Ctrl+Alt+G` / `End` | Scroll to bottom |

### Plan, Do, and BTW mode

`Tab` toggles between the main two modes:

- **Do** (default) — all configured tools visible to the model.
- **Plan** — only non-mutating tools (`read`, `grep`, LSP lookup,
  …). Mutating (`write`, `edit`) and exec (`bash`) are filtered
  from the `TurnRequest.Tools`. The model naturally shifts to
  producing a plan / outline rather than executing.

`/btw` or `Ctrl+X Ctrl+B` toggles **BTW** mode for off-band side
questions. BTW replies render in their own block and do not append to
the main conversation history.

Mode indicator shows in the input box's inline status row.

## Slash commands

See [features/slash-commands.md](../features/slash-commands.md) for
the full list. Quick reference:

- `/help` — overlay with every keybind + slash command
- `/clear` — wipe conversation; cancels any in-flight stream
- `/compact` — summarise and replace conversation (y/n confirm)
- `/retry` — regenerate the last assistant turn
- `/model` — model picker; marks the current model and surfaces recent
  model/provider selections first
- `/switch` — session switcher
- `/new` — create and switch to a fresh session
- `/sessions` — textual session overview
- `/debug` — toggle sidebar diagnostics/log tail
- `/context` — session state (tokens, cost, budget, instructions, skills)
- `/btw` — off-band side-question mode

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
| `[defaults]` | provider + model pins |
| `[tools]` | trim the bundled tool set |
| `[context]` | soft / hard thresholds on context-window usage |
| `[budget]` | warn + hard cap on cumulative cost |
| `[hooks]` | `post_turn` lifecycle shell hook |
| `[plugins]` | extra background plugin IDs, CRL, Rekor URL |
| `[mcp.servers.<name>]` | external MCP tool servers |

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
- **Landing view** — empty sessions start with the stado ANSI logo,
  centered input, command hints, cwd, and version. Once the first block
  arrives, the normal chat layout takes over.

## See also

- [session.md](session.md) — session management
- [features/slash-commands.md](../features/slash-commands.md)
- [features/sandboxing.md](../features/sandboxing.md)
- [features/budget.md](../features/budget.md)
