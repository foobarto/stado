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
| `/persona` | Open the persona picker (no args) or switch directly: `/persona <name>` (saves user `[defaults].persona`; project personas require `allow_project_persona`) |
| `/status` | Open the status modal for provider, tools, plugins, MCP, LSP readiness, OTel, sandbox, and context, with next-step hints (`Ctrl+X S`) |
| `/stats` | Show session token usage, cost, agent count, and uptime |
| `/config` | Show the effective config; `/config <section>` filters (e.g. `/config sandbox`) |
| `/sandbox` | Show the current sandbox posture — mode, runner, network, binds |
| `/provider` | Open the provider credential manager — a modal listing every known provider with its redacted credential status (env-var name + configured/unset marker, never the secret). Add/modify a credential (env-var name, optional base-URL, masked key into the OS keyring when available), or `Ctrl+D` to unset. In-TUI counterpart to `stado auth`. `/provider <name>` shows setup/remediation hints for a named provider |
| `/tools` | List tools visible to the model (honours `[tools]` filter + plan mode) |
| `/tool` | Run a tool by name: `/tool fs.read [json]` (`/t` for short). Verbs (`ls`/`info`/`enable`/`disable`/`autoload`/`unautoload`/`reload`) flow through the same command |
| `/alias` | Manage operator slash shortcuts — `create`/`list`/`rm`; `{N}` = positional args |
| `/reload` | Re-read config from disk (tools, system prompt, persona, display, plugin invoke executor) without restarting |
| `/approvals` | Compatibility hint: native tool approvals were removed; plugins can request explicit UI approval |
| `/steer` | Inject a message into the current turn at the next tool boundary (Enter while busy; `/steer <msg>`) |
| `/queue` | Queue a message to run when the current turn finishes (`Alt+Enter`; `/queue <msg>`) |
| `/interrupt` | Cancel the current turn and run a message now (`Ctrl+Enter`; `/interrupt [msg]`) |
| `/cancel` | Cancel the in-flight turn or tool (alias: `/stop`); keeps any queued prompt |
| `/compact` | Summarise the conversation and replace prior turns (requires y/n confirmation) |
| `/context` | One-stop session state: session id, cost, budget caps, loaded instructions, skills, hook |
| `/memory [on\|off\|status]` | Show or toggle approved-memory retrieval for this session |
| `/learn [focus]` | Review the completed trajectory; `candidates`, `show <id>`, and `approve <id>` manage evidence-backed lessons |
| `/providers` | Active provider + detected local runners (ollama / lmstudio / vllm / llamacpp), including load/start hints when a runner has no models ready |
| `/plugin` | List installed plugins; `/plugin:<id>-<ver> <tool> [json]` to run one |
| `/switch` | Open the searchable session manager (`Ctrl+X L`) |
| `/tree` | Open the session tree — navigate the fork graph and switch (`Ctrl+X G`) |
| `/sessions` | Other resumable sessions for this repo, with switch/resume hints and inactive-session policy |
| `/subagents` | Recent spawned child sessions with status, worktree, changed-file counts, scope violations, and adoption commands |
| `/spawn <prompt>` | Spawn a background agent on a prompt |
| `/fleet` | Open the fleet modal — running background agents at a glance |
| `/ps` | List running fleet agents with status, model, and age |
| `/kill <agent-id>` | Cancel a running agent (ids from `/ps`) |
| `/supervisor` | Show or toggle the supervisor lane (`/supervisor on\|off\|status`) |
| `/loop` | Repeat a prompt automatically: `/loop [duration] <prompt>` or `/loop stop` |
| `/monitor` | Stream process stdout as session notifications: `/monitor <cmd>` or `/monitor stop` |
| `/adopt [child] [--apply]` | Dry-run or explicitly apply worker subagent changes into the current parent session |
| `/tasks` | Open the shared task manager (`Ctrl+X K`); `/tasks add <title>` creates a quick task |
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
| `/thinking` | `Ctrl+X H` | Cycle thinking display; `/thinking preview\|auto\|collapsed\|expanded` sets it directly |
| `/tool-display` | `Ctrl+X O` | Cycle tool-output display; `/tool-display preview\|auto\|collapsed\|expanded` sets it directly |
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
- **Task manager.** `/tasks` opens the same shared task browser as
  `Ctrl+X K`. It edits the state-backed task store used by the model's
  `tasks` tool in Do mode. `/tasks add <title>` creates an open task
  without opening the browser.
- **Session switching safety.** Switch, new, and fork actions are
  blocked while a queued prompt, stream, approval, compaction, or tool
  is active, so prompts and writes do not silently land in the wrong
  session. Editor drafts and chat scroll position are cached per
  session and restored when switching back.
- **Theme selection.** `/theme` offers the bundled `stado-dark`,
  `stado-light`, `stado-contrast`, and `stado-rose` themes. Selecting
  one updates the current TUI and writes `[tui].theme` in
  `$XDG_CONFIG_HOME/stado/config.toml` so the next run starts with the
  same bundled theme. `/theme light`, `/theme dark`, and `/theme toggle`
  provide direct light/dark switching. If `[tui].theme` is unset and the
  current `theme.toml` is custom, the picker shows it as the current
  custom row. Custom themes can set `[markdown].style` to `auto`,
  `light`, or `dark`.
- **Thinking + tool display.** `/thinking` / `Ctrl+X H` and
  `/tool-display` / `Ctrl+X O` only affect the TUI viewport, persisting the
  selected mode to `[tui].thinking_display` / `[tui].tool_display`. Both
  cycle `preview` → `auto` → `collapsed` → `expanded`. Thinking blocks and
  tool results remain captured and persisted regardless of the display mode;
  in any mode a click (or `Shift+Tab` on the focused block) overrides one
  block between full and one-line for the session.
- **Unknown commands.** Typing `/notacommand` produces
  `unknown command: /notacommand (try /help)` as a system block
  rather than silently eating the input.
- **Case sensitivity.** Names are lowercase. `/HELP` won't match.
- **Arguments.** Split on whitespace — tokens after the command name
  are passed through to the handler.

## Adding a new slash command

A new command has three touch points:

1. **Handler** in `internal/tui/model_commands.go`'s `handleSlash`
   switch. For early-return handlers (with their own rendering), the
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
- [features/tasks.md](./tasks.md) — shared user/agent task store
- [features/skills.md](./skills.md) — `/skill` loader
- [commands/session.md](../commands/session.md) — every session subcommand mirrors a slash-or-not variant
