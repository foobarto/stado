# `stado run --headless`

Run stado as an editor-neutral JSON-RPC 2.0 daemon over stdio.

> Was `stado headless` through v0.75.x. The standalone command was folded
> into `stado run` (the non-interactive surface); `--headless` selects the
> daemon instead of a one-shot prompt. Daemon behavior and the wire protocol
> are unchanged; one-shot flags combined with `--headless` are rejected.

## What it does

`stado run --headless` exposes the shared runtime without the terminal UI.
Clients create in-memory sessions, send prompts, inspect tools and
providers, run installed plugins against sessions, and shut the daemon
down after draining in-flight RPCs.

The server loads the same config (user → project overlay with security
strip → env), provider, system prompt template, background plugins, and
hook settings as the TUI. Project-local plugins under `.stado/plugins/`
autoload only when `[plugins] allow_project_plugins = true` in user config.
Background plugin IDs in `[plugins].background` are user-only (stripped
from project config).

“Background plugins” here means the legacy tick-only form. Headless is not a
lifecycle-application host in v0.80/v1: a configured lifecycle manifest
(including one selected by a session persona) is rejected before provider or
session work. `plugin.run` also rejects lifecycle manifests because an
ephemeral tool call cannot share the required persistent command/tool/hook/event
instance. Use the interactive TUI for application-backed workflows.

## Usage

```sh
stado run --headless
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
child session id, child worktree, parent session id, status, role, mode,
and `timeout_seconds`. Finished worker notifications may also include
`forkTree`, `changedFiles`, `scopeViolations`, and `adoptionCommand`.
Child edits stay in the child session until explicitly adopted:

```sh
stado session adopt <parent-session> <child-session> --fork-tree <forkTree> --apply
```

Omit `--fork-tree` only when the notification has no `forkTree`.

## Gotchas

- `--headless` accepts `--persona`, `--provider`, `--model`, and `--no-sandbox`
  as daemon invocation controls but rejects one-shot prompt, skill, session,
  tool-filter, harness, sampling, output, and turn-limit flags. Configure
  daemon sessions through config and JSON-RPC so values are not silently
  ignored.
- By default, every executor created for a daemon session is bounded by the
  broker-projected ceiling. `--no-sandbox` explicitly selects `NoneRunner` and
  removes the autonomous host's default sandbox policy for the daemon lifetime.
- Sessions are daemon-local by default. When tools or session-aware
  plugins attach a git-backed session, prompts are also appended to
  that session's `.stado/conversation.jsonl` so later compaction and
  resume paths see the same transcript. Tools operate on the session's launch
  cwd; the sidecar worktree remains the audit/fork substrate rather than a
  hidden replacement working directory.
- `session.compact` applies immediately; unlike TUI `/compact`, it has
  no preview/edit/confirm loop.
- `plugin.run` requires a live headless session because session-aware
  ordinary plugins need a provider and session bridge. Lifecycle applications
  are explicitly unavailable through this method.

## See also

- [run.md](run.md) — one-shot non-daemon CLI.
- [plugin.md](plugin.md) — installing plugins before `plugin.run`.
- [../features/context.md](../features/context.md) — context warnings
  and compaction behavior.
