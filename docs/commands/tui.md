# `stado` (TUI)

The primary surface. Launching `stado` with no subcommand opens the
TUI inside the current repo.

## What it does

Boot sequence:

1. Load config (koanf: `config.toml` + `STADO_*` env + defaults).
2. Resolve the provider — explicit `[defaults].provider` if set,
   otherwise probe bundled local runners (ollama / llamacpp / vllm /
   lmstudio) + user presets, picking the first reachable one.
3. Open or create a session for this cwd (sidecar bare repo at
   `$XDG_DATA_HOME/stado/sessions/<repo-id>.git`).
4. Walk cwd upward for `AGENTS.md` / `CLAUDE.md` → injected as the
   system prompt on every turn.
5. Load `.stado/skills/*.md` → available as `/skill:<name>`.
6. Load background plugins from `[plugins].background`.
7. Start the bubbletea event loop.

Shutdown: `Ctrl+D` or `/exit`.

## Why it exists

Interactive coding sessions benefit from:

- Visible conversation history with distinct user / assistant /
  thinking / tool blocks.
- Live-streaming assistant text with an elapsed counter during
  long thinking.
- Tool-call approvals (unless auto-approved via `[approvals]`).
- Mid-stream slash commands (`/clear`, `/compact`, `/retry`).
- Context-window visibility (soft/hard thresholds, cost, cache ratio).
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
| `Ctrl+T` | Toggle sidebar |
| `Ctrl+C` | Cancel stream / clear pending queue |
| `Ctrl+D` | Exit stado |
| `?` | Help overlay (keybinds + slash commands) |
| `Up` / `Down` | Prev/next in input history |
| `PageUp` / `PageDown` | Scroll chat viewport |
| `Ctrl+G` / `Home` | Scroll to top |
| `Ctrl+Alt+G` / `End` | Scroll to bottom |

### Plan vs Do mode

`Tab` toggles between:

- **Do** (default) — all configured tools visible to the model.
- **Plan** — only non-mutating tools (`read`, `grep`, LSP lookup,
  …). Mutating (`write`, `edit`) and exec (`bash`) are filtered
  from the `TurnRequest.Tools`. The model naturally shifts to
  producing a plan / outline rather than executing.

Mode indicator shows in the input box's inline status row.

## Slash commands

See [features/slash-commands.md](../features/slash-commands.md) for
the full list. Quick reference:

- `/help` — overlay with every keybind + slash command
- `/clear` — wipe conversation; cancels any in-flight stream
- `/compact` — summarise and replace conversation (y/n confirm)
- `/retry` — regenerate the last assistant turn
- `/model` — model picker
- `/context` — session state (tokens, cost, budget, instructions, skills)

## Approvals

By default, every tool call pauses for y/n confirmation. Change the
default:

```toml
[approvals]
mode      = "allowlist"             # or "prompt"
allowlist = ["read", "grep", "ripgrep", "ast_grep", "glob"]
```

Session-scoped overrides (don't touch the config file):

- `/approvals always <tool>` — auto-approve `<tool>` for this session
- `/approvals forget` — clear session-scoped overrides

## Config

`stado config show` prints the resolved effective config. The TUI-
relevant sections:

| Section | Purpose |
|---------|---------|
| `[defaults]` | provider + model pins |
| `[approvals]` | tool-call y/n policy |
| `[tools]` | trim the bundled tool set |
| `[context]` | soft / hard thresholds on context-window usage |
| `[budget]` | warn + hard cap on cumulative cost |
| `[hooks]` | `post_turn` shell hook |
| `[plugins]` | background plugin load list, CRL, Rekor URL |
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
- **The textarea horizontally scrolls** on long single lines — use
  `Shift+Enter` for explicit newlines.
- **Banner in empty-state** — the sheep mascot renders until the
  first block arrives. Terminals below 90 cols wide hide it.

## See also

- [session.md](session.md) — session management
- [features/slash-commands.md](../features/slash-commands.md)
- [features/sandboxing.md](../features/sandboxing.md)
- [features/budget.md](../features/budget.md)
