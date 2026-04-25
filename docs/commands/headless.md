# `stado headless`

Run stado as an editor-neutral JSON-RPC 2.0 daemon over stdio.

## What it does

`stado headless` exposes the shared runtime without the terminal UI.
Clients create in-memory sessions, send prompts, inspect tools and
providers, run installed plugins against sessions, and shut the daemon
down after draining in-flight RPCs.

The server loads the same config, provider, system prompt template,
background plugins, and hook settings as the TUI.

## Usage

```sh
stado headless
```

Requests are line-delimited JSON-RPC 2.0 messages on stdin. Responses
and notifications are written to stdout.

Minimal flow:

```json
{"jsonrpc":"2.0","id":1,"method":"session.new","params":{}}
{"jsonrpc":"2.0","id":2,"method":"session.prompt","params":{"sessionId":"h-1","prompt":"summarise this repo"}}
{"jsonrpc":"2.0","id":3,"method":"shutdown","params":{}}
```

## Methods

| Method | Purpose |
|--------|---------|
| `session.new` | Create an in-memory headless session rooted at cwd |
| `session.prompt` | Send `{ sessionId, prompt }` and receive `{ text }` |
| `session.list` | List live daemon sessions |
| `session.cancel` | Cancel an in-flight prompt |
| `session.delete` | Remove a daemon session from memory |
| `session.compact` | Immediately compact the session's in-memory history |
| `tools.list` | List configured tools and classes |
| `providers.list` | Show available providers and current provider |
| `plugin.list` | List installed plugins |
| `plugin.run` | Run an installed plugin tool against a live session |
| `shutdown` | Drain in-flight calls, then close the daemon |

Notifications use `session.update` with `kind` values such as `text`,
`tool_call`, `subagent`, `plugin_fork`, `context_warning`, and `system`.
`subagent` notifications report `phase` (`started` / `finished`),
child session id, child worktree, status, role, mode, and timeout.

## Gotchas

- Sessions are daemon-local by default. When tools or session-aware
  plugins attach a git-backed session, prompts are also appended to
  that session's `.stado/conversation.jsonl` so later compaction and
  resume paths see the same transcript.
- `session.compact` applies immediately; unlike TUI `/compact`, it has
  no preview/edit/confirm loop.
- `plugin.run` requires a live headless session because session-aware
  plugins need a provider and session bridge.

## See also

- [run.md](run.md) — one-shot non-daemon CLI.
- [plugin.md](plugin.md) — installing plugins before `plugin.run`.
- [../features/context.md](../features/context.md) — context warnings
  and compaction behavior.
