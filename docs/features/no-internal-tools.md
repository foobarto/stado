# No Internal Tools

**Status as of 2026-05-07:** Steps 0–7 shipped. Steps 8–10 remain
planned. See [EP-0002 — All Tools as WASM Plugins](../eps/0002-all-tools-as-plugins.md)
and [EP-0037 — Tool dispatch and operator surface](../eps/0037-tool-dispatch-and-operator-surface.md).

The model-facing tool surface in stado is plugin-shaped end-to-end.
Stado without any wasm plugins exposes only an LLM chat plus a tight
**registry-bootstrap carve-out** of eight tools. Every other tool the
model can call — `fs.read`, `shell.bash`, `web.fetch`, `rg.search`,
agent-spawn, etc. — comes from a wasm plugin (bundled or installed).

## Why

- **Stado provides primitives. Plugins decide policy.** The runtime
  is unbiased: it offers mechanisms (exec, file ops, network, sandbox
  wrap, approval prompts, http proxy, etc.) that plugin authors
  compose into tools. Stado does not embed policy choices like "bash
  gets bwrap" into the runtime — that lives in the plugin manifest
  and source.

- **Single dispatch path.** Every plugin invocation site (agent loop,
  MCP server, CLI `stado tool run`, the `stado_tool_invoke` callback,
  plugin-override wrapping) routes through one shared
  `internal/runtime/pluginrun.Run` entry point. No path-specific
  special-cases means cap enforcement, audit trails, lifecycle
  bridges, and progress emission behave identically everywhere.

- **Auditability.** Tool implementations are wasm modules with
  signed manifests. Verification happens against the same machinery
  whether the plugin is "bundled" (shipped inside the stado binary
  via `go:embed`) or "installed" (downloaded by the operator).
  Step 10 of the migration extends this to a `stado plugin verify
  --bundled` walk over every embedded manifest.

## Carve-outs

Eight registry-self-management tools are registered as native
`tool.Tool` instances on the agent and MCP-server registries
unconditionally, since plugins need somewhere to be discoverable
from before they're loaded:

| Tool | Purpose |
|---|---|
| `tools.search` | fuzzy search over registered tools |
| `tools.describe` | schema + cap surface for one or more tools |
| `tools.in_category` | tools by category tag |
| `tools.categories` | list categories |
| `tools.activate` | activate an autoloaded tool |
| `tools.deactivate` | deactivate an active tool |
| `plugin.load` | load a plugin into the live registry |
| `plugin.unload` | unload a plugin |

These survive `ApplyToolFilter` regardless of operator config
patterns — the model needs at least these to reach the rest of the
plugin surface.

## What's NOT a carve-out

- **`llm.invoke`** is registered on the MCP server's tool list only,
  not on the agent's. It wraps the `stado_llm_invoke` host primitive
  so external MCP clients can ask stado's configured provider to run
  inference. The agent itself uses `stado_agent_spawn` and the
  `agent.*` plugin family when it wants to delegate to a sub-agent.
  Structurally separated, not a special case.

- **`tasks`** is migrated to a wasm plugin (Step 8) using
  `stado_fs_*` + `stado_instance_*` state primitives. Until Step 8
  lands the native `internal/tools/tasktool/` is still in the tree
  as a transitional implementation; it will be deleted when the
  wasm rewrite ships.

## Migration phases

| Step | Status | Cut |
|---|---|---|
| 0 / 0.1 / 0.2 / 0.5 | **Shipped** | `pluginrun` invoker carved out; all four invocation sites unified |
| 1 | **Shipped** | `stado_http_request` is a primitive (was a delegate to native `httpreq`) |
| 2 | **Shipped** | `webfetch` + `web` plugins use `stado_http_request` |
| 3 | **Shipped** | `sandbox` arg on `stado_exec` / `stado_proc_spawn`; new `stado_fs_readdir` / `stado_fs_stat` primitives |
| 4 | **Shipped** | `bash` plugin uses `stado_exec`; `stado_exec_bash` import + `exec:bash` cap deleted |
| 5 | **Shipped** | `ripgrep` + `ast_grep` plugins use `stado_exec`; native `internal/tools/{rg,astgrep}/` deleted |
| 6 | **Shipped** | `stado_lsp_*` lifted to primitive; native `internal/tools/lspfind/` deleted |
| 7 | **Shipped (v0.45.0)** | `fs.*` family + `readctx.*` rewritten in wasm using `stado_fs_*` primitives |
| 8 | Planned | `tasks` plugin wasm rewrite; deletion of `internal/tools/{tasktool,llmtool}/` |
| 9 | Planned | `VerifiedPluginSource` abstraction; deletion of `bundledPluginTool` shim, `wasmFamilies`, `[runtime.use_wasm]` config; manifest+sig embedding |
| 10 | Planned | `stado plugin verify --bundled` walks embedded manifests + signatures |

## Operator-facing impact

- **Tool wire vs. display names.** Wire form is what the model sees
  and what `stado tool run <name>` accepts: `fs__read`, `shell__bash`,
  `web__fetch`. Human surfaces (TUI listings, `stado tool list` output,
  CHANGELOG) display dotted form (`fs.read`, `shell.bash`) via the
  wire-to-display computation. Both names route to the same plugin
  tool — no need to memorise the mapping.

- **Tool filtering and overrides** keep working unchanged. The
  carve-out class is filter-immune so `[tools].include = []` doesn't
  accidentally lock the operator out of `tools.activate`.

- **`STADO_NO_BUNDLED_PLUGINS=1`** (planned for Step 9) will pin
  stado to "carve-outs only," primarily for tests and minimal-image
  deployments where the operator brings every plugin themselves.

- **Plugin authors** can target stado as a standard wasm host with
  documented host imports
  ([`docs/plugins/host-imports.md`](../plugins/host-imports.md)),
  no internal-tool-specific contracts.

## Stale state to know about

Until Steps 8–9 ship, the following migration scaffolding is
intentionally still present in the tree:

- `internal/tools/tasktool/` — replaced by Step 8's wasm `tasks`
  plugin.
- `internal/tools/llmtool/` — superseded by the MCP-server-only
  `llm.invoke` registration that calls `stado_llm_invoke` directly.
- `bundledPluginTool` shim and `wasmFamilies` map in
  `internal/runtime/{bundled_plugin_tools,wasm_migration}.go` —
  collapse into `VerifiedPluginSource` at Step 9.
- `[runtime.use_wasm]` config block — deleted at Step 9.

These are visible in source today; the spec's risk-and-self-critique
section calls them out explicitly. None of them are model-facing —
the agent only sees the post-migration tool registry shape.
