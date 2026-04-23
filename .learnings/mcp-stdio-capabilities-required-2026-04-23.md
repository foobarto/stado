# MCP stdio servers must declare capabilities

`internal/runtime/attachMCP` used to let stdio MCP servers start with an
empty capability list, which meant they inherited the full privileges of
the calling process. That made the config opt-in where it needed to be
mandatory.

Fix pattern:

- keep HTTP MCP servers unchanged; they are remote and not locally wrapped
- for stdio MCP servers (`command` set, no `url`), require a non-empty
  capability list before connecting
- update user-facing config/docs/examples at the same time, because the
  old "legacy unsandboxed default" wording was easy to cargo-cult

Verification:

- `/usr/local/go/bin/go test ./internal/runtime`
- `/usr/local/go/bin/go test ./...`
