# mcp-client — speak MCP from inside a stado plugin

Implements the [Model Context Protocol](https://modelcontextprotocol.io/)
streamable-HTTP transport on top of `stado_http_request`. Lets a
plugin initialize a session against any MCP server reachable by HTTP,
list its tools, and call them.

The stdio transport (child-process subprocess) is **not** supported —
wazero has no subprocess facility — and won't be. If you need an
MCP server with only stdio, run a small adapter (e.g.
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote)) that
exposes it over HTTP, then point this plugin at the adapter.

## Tools

```
mcp_init {endpoint, headers?}
  → {session_id, server_info, capabilities}

mcp_list_tools {session_id}
  → {tools: [{name, description, inputSchema}]}

mcp_call_tool {session_id, name, arguments?}
  → {content: [...], isError?: bool}
```

`mcp_init` performs the spec handshake (`initialize` →
`notifications/initialized`), captures any `Mcp-Session-Id` the
server returns, and persists everything (including caller-supplied
headers like `Authorization`) to
`<workdir>/.cache/stado-mcp/<session_id>.json`. Subsequent calls
re-send the same headers and session id — so auth and sticky
sessions survive the wasm-instance freshness model.

## Build + install

```sh
stado plugin gen-key mcp-client-demo.seed
./build.sh
stado plugin trust "$(cat author.pubkey)" mcp-client-demo
stado plugin install .
mkdir -p $PWD/.cache/stado-mcp
```

## Run

```sh
# initialize
stado plugin run --with-tool-host --workdir $PWD \
  mcp-client-0.1.0 mcp_init \
  '{"endpoint":"https://my-mcp.example.com/mcp",
    "headers":{"Authorization":"Bearer $TOKEN"}}'

# list tools
stado plugin run --with-tool-host --workdir $PWD \
  mcp-client-0.1.0 mcp_list_tools \
  '{"session_id":"<from-init>"}'

# call a tool
stado plugin run --with-tool-host --workdir $PWD \
  mcp-client-0.1.0 mcp_call_tool \
  '{"session_id":"<from-init>",
    "name":"echo",
    "arguments":{"msg":"hi"}}'
```

## Capabilities

```toml
capabilities = [
  "net:http_request",
  "net:http_request_private",   # drop if you only hit public servers
  "fs:read:.cache/stado-mcp",
  "fs:write:.cache/stado-mcp",
]
```

`net:http_request_private` is included because most MCP servers run
on `localhost:<port>` during development. Drop it if you only call
public hosts (Smithery-hosted servers, Cloudflare-deployed servers,
etc.) — the strict dial guard is safer for those.

## Transport details

The streamable-HTTP transport allows servers to reply with either
`application/json` (single response) or `text/event-stream`
(SSE-framed responses with multiple `data:` lines). The plugin
accepts both. For SSE, it walks every `data:` JSON envelope and
picks the one whose `id` matches our request id; unrelated server-
sent notifications are ignored.

## Smoke-testing without a real server

There's a tiny mock server in this repo's CI fixtures
(`mock_mcp.py`) that responds to `initialize`, `tools/list`, and
`tools/call` for a single `echo` tool. Useful for verifying the
plugin builds and roundtrips JSON-RPC correctly without standing
up a real MCP server.
