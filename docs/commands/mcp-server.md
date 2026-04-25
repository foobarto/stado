# `stado mcp-server`

Expose stado's configured tool registry as an MCP server over stdio.

## What it does

`stado mcp-server` registers the bundled stado tools with MCP
`tools/list` and `tools/call`. It applies the same `[tools].enabled`,
`[tools].disabled`, and `[tools].overrides` config as the TUI/run
surfaces before exposing tools to the client.

This mode is tools-only: no MCP resources, prompts, or sampling.
The bundled `tasks` tool is exposed here too, so MCP clients can store,
list, read, update, and delete the same shared tasks visible in the TUI.

## Usage

Configure an MCP-aware client to launch:

```sh
stado mcp-server
```

The server runs on stdin/stdout. Status messages go to stderr.

## Authorization Boundary

`mcp-server` trusts the MCP client as the authorization boundary. Tool
calls run with an auto-approve host rooted at the process cwd. Use the
TUI or a wrapper plugin when a human approval step is required.

Local subprocess sandboxing still applies inside tools that use the
stado sandbox runner, and tool visibility still follows `[tools]`.

## Gotchas

- Start the server from the directory you want tools to treat as cwd.
- `read` deduplication is disabled because single MCP calls do not have
  a live stado conversation/session.
- `tasks` persists to stado state, not the current cwd. Use
  `[tools].disabled = ["tasks"]` if a client should not write shared
  task state.
- This command exposes stado as an MCP server. Configured
  `[mcp.servers.*]` entries are the opposite direction: external MCP
  servers consumed by stado.

## See also

- [../features/sandboxing.md](../features/sandboxing.md) — MCP server
  capability sandboxing when stado is the MCP client.
- [../features/tasks.md](../features/tasks.md) — shared task store and
  tool bounds.
- [../eps/0010-interop-surfaces-mcp-acp-headless.md](../eps/0010-interop-surfaces-mcp-acp-headless.md)
