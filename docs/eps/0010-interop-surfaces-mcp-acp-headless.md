---
ep: 10
title: Interop Surfaces: MCP, ACP, and Headless
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [3, 4, 5, 6, 8, 11]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped editor, RPC, and MCP integration surfaces present by v0.0.1.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: ACP, headless, MCP client attachment, and `mcp-server` are current integration surfaces over the shared runtime.
---

# EP-10: Interop Surfaces: MCP, ACP, and Headless

## Problem

stado cannot be only a local TUI if it wants to participate in editor,
automation, and tool-interop workflows. The project needs stable ways
to drive the agent loop remotely, to consume external tool servers, and
to expose stado's own tools to other MCP-aware clients.

Those integrations must share the same core runtime instead of quietly
forking behavior across multiple entry points.

## Goals

- Reuse the same session, executor, and provider wiring across surfaces.
- Provide both editor-specific and editor-neutral RPC entry points.
- Consume MCP tool servers without making them first-class trusted code.
- Expose stado's own tools as a narrow MCP server for external clients.

## Non-goals

- One universal protocol that replaces every other integration surface.
- MCP resources, prompts, or sampling in `stado mcp-server`.
- Treating external MCP tools as safer than local exec by default.

## Design

`internal/runtime` is the shared assembly point for provider creation,
session wiring, executor construction, and the agent loop. The TUI,
`stado run`, ACP server, and headless daemon all build on that layer
instead of maintaining separate execution models.

Two RPC servers sit on top of the shared runtime:

- `stado acp` for editor integration, notably Zed's ACP surface
- `stado headless` for editor-neutral JSON-RPC automation

The two servers expose different method shapes, but both model the same
underlying session lifecycle. ACP currently surfaces the narrower
editor-facing notification set for text and tool calls, while headless
also emits richer session updates such as plugin-fork and
context-warning notifications.

MCP appears in both directions. As a client, stado can attach external
MCP servers and register their tools into the local registry. Attached
servers are treated conservatively as privileged integration points, and
stdio MCP servers can be launched under per-server sandbox policies. As
a server, `stado mcp-server` exposes stado's own tools over stdio with
the stado tool schemas and an intentionally small tools-only scope.

The interop principle is separation of responsibilities: ACP and
headless are separate orchestration protocols over the shared runtime,
MCP is responsible for tool interop, and `mcp-server` intentionally
trusts the caller as the authorization boundary.

## Decision log

### D1. Share one runtime across every surface

- **Decided:** TUI, run, ACP, and headless all compose through
  `internal/runtime`.
- **Alternatives:** separate per-surface execution stacks.
- **Why:** agent behavior should not drift just because the caller is an
  editor, a shell, or another process.

### D2. Keep ACP and headless as separate protocols

- **Decided:** ACP remains the editor-facing surface and headless stays a
  general JSON-RPC daemon.
- **Alternatives:** force every caller through ACP or invent a new
  custom protocol for all clients.
- **Why:** ACP already solves a concrete editor use case, while headless
  is freer to stay small and scripting-oriented.

### D3. Treat external MCP tools as privileged integration points

- **Decided:** attached MCP tools are conservative exec-class entries and
  can be sandboxed per server.
- **Alternatives:** trust external MCP tools as read-only by default.
- **Why:** an MCP server can perform arbitrary local or remote actions,
  so the runtime should default to the safer classification.

### D4. Keep `stado mcp-server` intentionally small

- **Decided:** `mcp-server` exposes only tools and trusts the MCP client
  as the authorization boundary.
- **Alternatives:** mirror the whole runtime over MCP or layer a second
  approval UX into the server.
- **Why:** the value of `mcp-server` is making stado's tool registry
  available elsewhere, not rebuilding the full agent runtime inside MCP.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md)
- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-8: Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [DESIGN.md](../../DESIGN.md#extension-points)
- [README.md](../../README.md#mcp-server-isolation)
