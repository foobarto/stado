# `stado acp`

Run stado as a Zed Agent Client Protocol server over stdio.

## What it does

ACP is the editor-facing server surface. It speaks JSON-RPC 2.0 using
Zed's Agent Client Protocol shape and reuses stado's provider/runtime
wiring. By default it is a prompt/response agent. With `--tools`, it
opens a git-native session and runs the full audited tool loop.

## Usage

```sh
stado acp
stado acp --tools
```

Example Zed config:

```json
{
  "agent_servers": {
    "stado": {
      "command": "stado",
      "args": ["acp", "--tools"]
    }
  }
}
```

## Protocol Surface

The current server supports:

- `initialize`
- `session/new`
- `session/prompt`
- `session/cancel`
- `shutdown`

When tools are enabled, tool-call notifications are sent as
`session/update` events and tool execution is committed to the same
sidecar `tree` and `trace` refs used by the TUI and `stado run`; the
git-backed transcript is appended to `.stado/conversation.jsonl` as
turns complete.

## Gotchas

- ACP sessions are editor sessions, not the same as `stado session`
  CLI entries unless `--tools` opens the sidecar path for execution.
- Provider init is lazy. Startup can succeed even when provider
  credentials are missing; the first prompt surfaces the failure.
- Use `stado headless` when you want an editor-neutral JSON-RPC method
  set rather than Zed ACP.

## See also

- [headless.md](headless.md) — general JSON-RPC daemon.
- [session.md](session.md) — persisted sidecar sessions.
- [../eps/0010-interop-surfaces-mcp-acp-headless.md](../eps/0010-interop-surfaces-mcp-acp-headless.md)
