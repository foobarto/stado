# No Internal Tools — Design (v2)

**Status:** v2 after codex + gemini external review. Awaiting final
sign-off before plan + execute.

## Goal

The model-facing tool surface is plugin-shaped end-to-end. Stado
without any wasm plugins exposes only the LLM chat plus a tight
registry-bootstrap carve-out. External clients (model turns, MCP
clients, CLI `stado tool run`) interact only with tools that came
from a wasm plugin (or the carve-out).

**Stado provides primitives. Plugins decide policy.** The runtime is
unbiased — it offers mechanisms (exec, file ops, network, sandbox
wrap, approval prompts, http proxy, etc.) that plugin authors compose
into tools. Stado does not embed policy choices like "bash gets bwrap"
into the runtime.

## Carve-outs

These are registered as native `tool.Tool` instances on the agent and
MCP-server registries. They are registry self-management — required
for plugins to be discoverable, loadable, activatable.

| Tool | Purpose |
|---|---|
| `tools.search` | fuzzy search over registered tools |
| `tools.describe` | schema + capability surface for one or more tools |
| `tools.in_category` | tools by category tag |
| `tools.categories` | list categories |
| `tools.activate` | activate an autoloaded tool |
| `tools.deactivate` | deactivate an active tool |
| `plugin.load` | load a plugin into the live registry |
| `plugin.unload` | unload a plugin |

Eight tools total (codex caught I had only listed four; the autoload
logic at `internal/runtime/executor.go:159-166` already treats all
eight as a single bootstrap class).

### Not a carve-out — structural

`llm.invoke` is registered on the **MCP server registry only**, not
on the agent registry. It wraps `stado_llm_invoke` (host import) so
external MCP clients can ask stado's configured provider to run
inference. The agent itself never sees `llm.invoke` as a tool — when
the model wants to delegate to a sub-agent, it uses
`stado_agent_spawn` and the `agent.*` plugin family.

This is structural, not a carve-out: the tool exists only on a
non-agent surface. Documented separately so it's not accidentally
re-introduced into the agent registry.

### `tasks` is NOT a carve-out

Earlier draft kept `tasks` as bootstrap. It isn't — it's a stateful
JSON-store CRUD tool the model uses. **Migrated to a wasm plugin**
that uses `stado_fs_*` primitives + an `instance` state primitive for
the sequence counter.

## Primitive surface changes

### Drop delegates entirely

| Host import | Replacement | Cap dropped |
|---|---|---|
| `stado_exec_bash` | `stado_exec` (with optional sandbox arg) | `exec:bash`, `exec:shallow_bash` |
| `stado_search_ripgrep` | `stado_exec` | `exec:search` |
| `stado_search_ast_grep` | `stado_exec` | `exec:ast_grep` |
| `stado_http_get` | `stado_http_request` | — |
| `stado_fs_tool_read` | `stado_fs_read_partial` (offset/length already supported) | — |
| `stado_fs_tool_write` | `stado_fs_write` | — |
| `stado_fs_tool_edit` | wasm composes `stado_fs_read` + `stado_fs_write` | — |
| `stado_fs_tool_glob` | wasm walks via `stado_fs_readdir` + match | — |
| `stado_fs_tool_grep` | wasm walks + regexes | — |
| `stado_fs_tool_read_context` | wasm reads + formats | — |

### Refactor delegate → true primitive (same export, new impl location)

| Host import | Action |
|---|---|
| `stado_http_request` | Move impl from `internal/tools/httpreq/` to `internal/httpreq/` (subsystem package). Host wrapper in `internal/plugins/runtime/host_http_request.go`. Same export name and arg shape. Keeps proxy-disable, dial-by-IP-after-validation, redirect URL validation, SOCKS5 pivot. |
| `stado_lsp_find_definition` | Move impl from `internal/tools/lspfind/` to `internal/lsp/` (subsystem package). Host wrapper in `internal/plugins/runtime/host_lsp.go`. |
| `stado_lsp_find_references` | Same. |
| `stado_lsp_document_symbols` | Same. |
| `stado_lsp_hover` | Same. |

After: `internal/tools/{httpreq,lspfind}/` deleted. The Go code that
backs these host imports lives in `internal/{httpreq,lsp}/` as plain
functions, not `tool.Tool` instances.

### Add new primitives

| Host import | Signature | Why |
|---|---|---|
| `stado_fs_readdir` | `(path_ptr, path_len, offset, buf_ptr, buf_cap) → int32`. Buffer receives JSON array of `{name, type, mode}` entries starting at `offset`. Return: bytes written, or `0` for "no more entries past offset". Plugin paginates by re-calling with the next offset. | Wasm directory walk for glob/grep/edit. Offset-based paging avoids unbounded buffer requirements without state in the host. |
| `stado_fs_stat` | `(path_ptr, path_len, buf_ptr, buf_cap) → int32`. Buffer receives JSON `{mode, size, mtime, type}`. | Existence + type check; needed by glob/grep/edit. Composing reads to infer is brittle. |

Both implemented in `internal/plugins/runtime/host_fs.go`.

### Extend existing primitives — sandbox option

| Host import | Change |
|---|---|
| `stado_exec` | Args struct gains optional `sandbox` field. When set, the call routes through `sandbox.Runner` with that policy. When omitted, runs as today. |
| `stado_proc_spawn` | Same. |
| `stado_terminal_open` | **Codex's #7:** PTY-spawned children today bypass `sandbox.Runner` entirely (`pty/manager.go:129` calls `exec.Command` directly). For the unbiased model, terminal_open also takes optional `sandbox` policy — `shell.spawn` plugin author can opt PTY-spawned shells into bwrap if they want. Implementation: PTYManager.Spawn gains a `sandbox.Runner`+`sandbox.Policy` parameter; when provided, wraps the child via `runner.Command`. |

The `sandbox` arg is a JSON object matching `sandbox.Policy`:

```json
{
  "fs_read":  ["/tmp", "/path/to/workdir"],
  "fs_write": ["/tmp"],
  "exec":     ["bash"],
  "net":      "deny",
  "cwd":      "/path/to/workdir",
  "env":      ["PATH", "HOME"]
}
```

Plugin authors opt in. The runtime is unbiased — bash plugin can pass
the bwrap-on-bash policy if it wants; offsec plugins skip it. `stado`
ships `sandbox.Runner` as a *mechanism* the plugin uses or doesn't.

### Cap vocabulary (final)

Existing `exec:proc[:<binary-glob>]` is the only exec cap form.
Variants `exec:bash`, `exec:shallow_bash`, `exec:search`,
`exec:ast_grep` are removed.

**Bug from codex's review:** `procAllowed` (`host_proc.go:34-45`)
matches the cap glob against the resolved absolute path, so a
manifest cap of `exec:proc:bash` won't match. Fix in this batch:
extend matcher to also try basename match, OR document that all
binary caps must be absolute paths. Picking absolute paths only —
manifests need to declare `exec:proc:/usr/bin/bash` or
`exec:proc:/usr/bin/bash*`.

## Architectural changes

### Invoker extracted to shared package

**Codex caught a layering bug** in v2 wording: `runPluginInvocation`
lives in `cmd/stado` (`package main`) while `installedPluginTool`
lives in `internal/runtime`. They can't directly call across that
boundary. Step 0's first substep is **extracting the invocation
helper into a new package** — likely `internal/runtime/pluginrun/`
— with a `tool.Tool`-shaped signature:

```go
package pluginrun

func Run(ctx context.Context, args RunArgs, h tool.Host) (tool.Result, error)
```

`RunArgs` carries Manifest + Wasm bytes + ToolName + per-call config.
The CLI helper at `cmd/stado/plugin_invoke_shared.go` becomes a thin
caller that builds `RunArgs` from CLI flags, captures stdout via a
buffer, and prints. Everything that runs plugins routes through
`pluginrun.Run`.

### All plugin invocation paths converge

**Codex's #2 — also missed in v2.** Today there are FOUR distinct
plugin-instantiation sites:

1. `bundledPluginTool.Run` (`bundled_plugin_tools.go:421`) — agent loop
2. `runPluginInvocation` (`plugin_invoke_shared.go:42`) — CLI
3. `pluginOverrideTool.Run` (`plugin_overrides.go:305`) — `[tools].overrides`
4. `stado_tool_invoke` callback (`plugin_invoke_shared.go:103`) —
   wasm-to-wasm calls

Each wires a slightly different host setup and bypasses parts of
the others' machinery. Step 0 collapses all four onto `pluginrun.Run`.
Validation criterion 5 only holds after this convergence; until then
"all dispatch paths are uniform" is false.

The `stado_tool_invoke` callback in particular currently rebuilds
`BuildDefaultRegistry` per call, bypassing active filters /
overrides / MCP-attached tools. After convergence: `stado_tool_invoke`
hands off to the surrounding executor's registry, not a fresh one.

### `installedPluginTool.Run` becomes a real invoker

**Codex caught this is broken end-to-end as drafted.** Today
`installedPluginTool.Run` returns a sentinel error; only
`stado tool run`'s CLI dispatcher (`tool_run.go:145`) special-cases
installed plugins and routes through `runPluginInvocation`. The
agent loop and MCP server call `executor.Run` (`executor.go:52`)
which calls `Tool.Run` directly — they hit the sentinel.

**Fix:** `installedPluginTool.Run` calls `runPluginInvocation` (or
its refactored equivalent). All three dispatch paths (CLI, agent,
MCP) become uniform.

**Lifecycle host divergence — gemini's catch.**
`runPluginInvocation` today (`plugin_invoke_shared.go:42`) builds a
minimal CLI-shaped host via `newPluginRunToolHost` — no Session, no
FleetBridge, no ApprovalBridge, no PTYManager. The agent loop's
`bundledPluginTool.Run` (`bundled_plugin_tools.go:421-467`) carries
all of these from the outer `tool.Host` it received. Naive
unification breaks `agent.spawn`, `shell.spawn`, approvals, and
progress emission for installed-plugin tools called from the agent
loop.

**Mitigation:** as part of Step 0, refactor `runPluginInvocation` to
accept an *optional* `tool.Host` from the caller:

- When non-nil (agent loop, MCP server): use the host's bridges.
  Type-assertions on the host pull out FleetBridge, PTYManager,
  ApprovalBridge, ProgressEmitter, AgentFleetProvider — same shape
  as `bundledPluginTool.Run` does today.
- When nil (CLI direct invocation): fall back to
  `newPluginRunToolHost` mocks.

What the unified host setup must carry:
- `*stadogit.Session` (for trace audit)
- `pluginRuntime.FleetBridge` (for `agent.*`)
- `*pty.Manager` (for `shell.*`)
- `pluginRuntime.ApprovalBridge` (for `ui:approval`)
- `Progress` callback (for `stado_progress` emissions)
- `*personas.Persona` resolution context (carried via ctx already)

This is a prerequisite for any unification. **Step 0 includes the
host-parameter refactor**, not just the sentinel removal.

### `bundledPluginTool` shim — deleted, replaced by unified plugin source

**Codex's third path is real:** today bundled plugins, installed
plugins, and user-bundled plugins are three separate verification +
registration paths (`internal/runtime/bundled_plugin_tools.go`,
`internal/runtime/installed_tools.go`,
`internal/userbundled/init.go`). The clean unification is a
`VerifiedPluginSource` abstraction:

```go
type VerifiedPluginSource interface {
    Manifest() *plugins.Manifest
    Wasm() []byte
    Trust() TrustResult       // bundled-by-build / signed-by-trust-store / etc.
    Origin() string           // for logging / `plugin info`
}
```

Three implementations:

- `bundledSource` — reads from `embed.FS`. Manifests + sigs ship
  alongside `internal/bundledplugins/wasm/*.wasm` (new files we
  generate at build time from a manifest template). Trust = "bundled
  by stado build."
- `installedSource` — reads from `cfg.StateDir()/plugins/<name>-<ver>/`,
  signature-verifies via `plugins.NewTrustStore`. Trust =
  signed-by-trust-store.
- `userBundledSource` — existing user-bundled flow.

The registration path is one loop:

```go
for _, src := range allPluginSources(cfg) {
    if err := src.Trust().Verify(); err != nil {
        warn(...); continue
    }
    reg.Register(newPluginTool(src))
}
```

`newPluginTool` returns a `tool.Tool` whose `Run` invokes the wasm
via `runPluginInvocation` (the fix from Step 0).

### Lifecycle wiring preserved

**Codex caught this:** `bundledPluginTool.Run` today wires fleet
bridge, PTY manager, approval bridge, and progress emitter into the
per-call host (`bundled_plugin_tools.go:428-467`). The unified
`runPluginInvocation` path must do the same — otherwise agent.spawn
breaks, shell.spawn loses PTY, approval popups disappear, etc.

The lifecycle wiring already exists in `runPluginInvocation` for
some of these; gaps must be filled in Step 0.

### MCP server converges with agent path

**Codex's #2:** `cmd/stado/mcp_server.go:64-76` calls
`BuildDefaultRegistry` directly and adds `tasks` + `llm.invoke`
manually. After this work the same set of tools should be available
across paths (with the divergence noted above for `llm.invoke`).

**Fix:** factor a shared `BuildRegistryWithPlugins(cfg) (*Registry,
error)` helper that both `BuildExecutor` and `mcp_server.go` call.
Both get plugin tools + carve-out. The MCP server then registers
`llm.invoke` on top.

### Carve-out filter protection

**Codex's missing-scope:** `ApplyToolFilter` can remove carve-outs
after registration. Add a sentinel `class` (or boolean field) marking
a tool as carve-out; `ApplyToolFilter` skips them.

### Naming uniformity — `<plugin>__<tool>` everywhere

**Invariant:** every bundled-plugin tool registers under
`<plugin>__<tool>` wire form. No bare-name registrations
(`bash`, `read`, `webfetch`, etc.) survive after the migration.
Display computes from wire form: split on `__`, join with `.`.

**Consolidation:**
- `bash` → `shell__exec` (lives in existing shell wasm plugin
  alongside `shell__spawn` for PTY)
- `webfetch` → `web__fetch` (already exists; the native+shim path
  goes away)
- `ripgrep` → `rg__search` (already exists)
- `ast_grep` → `astgrep__search` (already exists)
- `read`/`write`/`edit`/`glob`/`grep` → `fs__read` etc.
- `read_with_context` → `readctx__read`
- `find_definition`/`find_references`/`document_symbols`/`hover`
  → `lsp__definition` etc.

**`bundledToolMetadata` table at `internal/runtime/tool_metadata.go`
is deleted** as part of Step 9 — no longer needed once every tool
registers in canonical form. Display logic becomes
`strings.Replace(name, "__", ".", -1)` with no per-tool exceptions.

**Model receives wire form** (`fs__read`); most providers reject
`.` in tool names. TUI / CLI / `tool list` show display form
(`fs.read`) computed at print time. The model's tool-call result
references the wire form (the agent loop maps display ↔ wire).

**Installed-plugin naming convention.** Installed plugins (offsec
toolkit, etc.) currently use bare underscore-form names
(`ad_acl_abuse`, `crypto_caesar`). They're authored externally;
stado doesn't rewrite them. **Stado adds validation at install
time:** if a manifest tool name doesn't match
`^[a-z][a-z0-9_]*__[a-z][a-z0-9_]*$`, install warns (initially) or
refuses (eventually). Documented as a follow-up — the htb-toolkit
manifests are updated in their own repo to follow the convention.
Out of this migration's scope but called out explicitly.

### Test mode: registry without bundled plugins

**Codex's missing-scope:** add a build tag or env flag (e.g.
`STADO_NO_BUNDLED_PLUGINS=1`) that suppresses the bundled-plugin
registration path. Used by tests validating the "no plugins =
chat-only" invariant. Production builds default to bundled-plugins-
on.

## Wasm plugins to write or rewrite

| Plugin | Status | Source |
|---|---|---|
| `bash` | rewrite | `stado_exec` with optional sandbox |
| `ripgrep` | rewrite | `stado_exec` rg --json + parse |
| `ast_grep` | rewrite | `stado_exec` ast-grep + parse |
| `webfetch` | rewrite | `stado_http_request` |
| `web` | rewrite | `stado_http_request` |
| `read` | rewrite | `stado_fs_read_partial` |
| `write` | rewrite | `stado_fs_write` |
| `edit` | rewrite | `stado_fs_read` + `stado_fs_write` |
| `glob` | rewrite | `stado_fs_readdir` walk + match |
| `grep` | rewrite | `stado_fs_readdir` walk + regex |
| `read_with_context` | rewrite | `stado_fs_read` + format |
| `find_definition` | thin shim | `stado_lsp_find_definition` (now primitive) |
| `find_references` | thin shim | same |
| `document_symbols` | thin shim | same |
| `hover` | thin shim | same |
| `tasks` | NEW | `stado_fs_*` + `stado_instance_*` |

`approval_demo` already exists as `newBundledStaticTool` — promotes
to a normal bundled wasm plugin via the unified registration path.
Same applies to other static-tool registrations in
`bundled_plugin_tools.go` (fs.ls, shell.* — these already use proper
wasm modules; just need the unified registration path).

## Things deleted at the end

- `internal/tools/{bash,webfetch,httpreq,rg,astgrep,fs,readctx,lspfind,llmtool,tasktool}/`
- `internal/runtime/bundled_plugin_tools.go` (shim)
- `internal/runtime/wasm_migration.go` (table + apply)
- `[runtime.use_wasm.*]` config field
- `STADO_PARITY_*` test env gating + parity tests
- 14 delegate host imports (listed above)
- 4 cap variants (listed above)
- The deviation note in `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md:1391`
  is rewritten to "reversed 2026-05-06."

## Decomposition (revised — 9 steps)

Each step ships green; main is never broken.

| Step | Work | Risk | Prereqs |
|---|---|---|---|
| 0 | **Extract `pluginrun` invoker.** Pull `runPluginInvocation` body from `cmd/stado/plugin_invoke_shared.go` (`package main`) into a new `internal/runtime/pluginrun/` package as `pluginrun.Run(ctx, args, host) (tool.Result, error)`. CLI helper becomes a thin caller. Add optional `tool.Host` parameter to carry Session/FleetBridge/ApprovalBridge/PTYManager/Progress when called from agent loop or MCP server. | High | None |
| 0.1 | Convert `installedPluginTool.Run` from sentinel to `pluginrun.Run` call. Preserve lifecycle wiring. | High | 0 |
| 0.2 | Convert `bundledPluginTool.Run`, `pluginOverrideTool.Run`, and the `stado_tool_invoke` callback to all dispatch through `pluginrun.Run`. The callback also stops rebuilding `BuildDefaultRegistry` per call — receives the active executor's registry instead. | High | 0.1 |
| 0.5 | Factor `BuildRegistryWithPlugins(cfg)` shared helper. MCP server calls it (today bypasses via `BuildDefaultRegistry` directly). | Medium | 0.2 |
| 1 | Refactor `stado_http_request` from delegate to primitive. Move impl to `internal/httpreq/`. Delete `internal/tools/httpreq/`. | Low | 0 |
| 2 | Rewrite `webfetch` + `web` plugins to use `stado_http_request`. Delete `stado_http_get` import. Delete `internal/tools/webfetch/`. | Low | 1 |
| 3 | Add `sandbox` arg to `stado_exec` and `stado_proc_spawn`. Add `stado_fs_readdir`, `stado_fs_stat` primitives. Fix `procAllowed` to require absolute-path caps (or accept basename — pick absolute). | Medium | None |
| 4 | Rewrite `bash` plugin (using `stado_exec` + optional sandbox arg). Drop `stado_exec_bash` import. Drop `exec:bash`/`exec:shallow_bash` caps. Delete `internal/tools/bash/`. | Medium | 3 |
| 5 | Rewrite `ripgrep` + `ast_grep` plugins (`stado_exec` + parse JSON). Drop `stado_search_*` imports. Drop `exec:search`/`exec:ast_grep` caps. Delete `internal/tools/{rg,astgrep}/`. | Medium | 3 |
| 6 | Refactor `stado_lsp_*` from delegate to primitive. Move impl to `internal/lsp/`. Wasm shims become thin (already most-of-the-way). Delete `internal/tools/lspfind/`. | Medium | 0 |
| 7 | Rewrite fs family (`read`, `write`, `edit`, `glob`, `grep`, `read_with_context`) using `stado_fs_*` primitives + new readdir/stat. Drop `stado_fs_tool_*` imports. Delete `internal/tools/{fs,readctx}/`. | High | 3 |
| 8 | Migrate `tasks` to wasm plugin (using `stado_fs_*` + `stado_instance_*`). Delete `internal/tools/tasktool/`. Delete `internal/tools/llmtool/` (replaced by mcp-server-only `llm.invoke` registration that calls `stado_llm_invoke` directly). | Medium | 0 |
| 9 | Build `VerifiedPluginSource` abstraction. Bundled plugins ship manifests + sigs in `embed.FS`. Delete `bundledPluginTool` shim, `wasmFamilies`, `[runtime.use_wasm]` config. Delete `bundledToolMetadata` table at `tool_metadata.go` — display computes from wire form. Carve-out registration loop assigns `meta` category + canonical name inline (carve-outs have no manifest to source from). Add carve-out filter protection. Add `STADO_NO_BUNDLED_PLUGINS` test mode. Test-infra pass: update integration tests that assume native-tool registry; add `FakeVerifiedSource` test helper for unit tests needing mock plugin tools. Validate `BuildRegistryWithPlugins` with no plugins yields only the 8 carve-outs. | High | All prior |
| 10 | Extend `stado plugin verify` to verify bundled plugins against the external anchor pubkey URL (EP-0039 pattern). Add stado-shipped constant for the canonical bundled-plugin pubkey URL. **Airgapped fallback:** ship the canonical pubkey *also* embedded in `embed.FS`. When external fetch fails (network down, airgapped op), verify against the embedded copy with a clear "freshness not confirmed against remote" warning. Embedded pubkey isn't a runtime trust check (still short-circuits there); it's just a cached known-good for offline `verify --bundled`. CLI: `stado plugin verify --bundled` walks every embedded manifest+sig and verifies. | Low | 9 |

Step 0 is load-bearing — the architecture as drafted in v1 was end-to-end broken without it (codex's #1).

Steps 1, 2, 3, 4, 5, 6, 7, 8 each leave main green and reduce delegate-import count + delete one or more native tool packages.

Step 9 is the final unification: shim deletion, manifest embedding,
test mode for the invariant.

## Validation criteria

1. `BuildRegistryWithPlugins(cfg)` with `STADO_NO_BUNDLED_PLUGINS=1`
   and `cfg` having no installed plugins returns a registry containing
   only the 8 carve-outs. Tested in CI.
2. Same call with bundled plugins available returns the 8 carve-outs
   plus all bundled plugin tools.
3. `go build ./...` and `go test ./...` are clean after every step.
4. `stado tool run read '{"path":"./foo"}'` invokes the bundled wasm
   `read` plugin (no native fallback).
5. All four plugin invocation sites (agent loop, MCP server, CLI,
   `stado_tool_invoke` callback, `pluginOverrideTool`) dispatch
   through the same `pluginrun.Run` invoker. No path-specific
   special cases remain.
6. The *set* of model-facing tools is unchanged from pre-migration
   to post-migration (same operations available). The *names*
   migrate to wire form (`fs__read`, `shell__exec`). TUI/CLI human
   surfaces display dotted form (`fs.read`, `shell.exec`) via the
   wire-to-display computation. Default autoload list and tests
   update accordingly.
7. `llm.invoke` is in MCP server's tool list, not in agent's.
8. Carve-outs survive `ApplyToolFilter` regardless of config patterns.

## Risk and self-critique

- **Step 0 is load-bearing.** Getting `installedPluginTool.Run` right
  with full lifecycle wiring is non-trivial. It must preserve fleet,
  PTY, approvals, progress, the new persona system, and any other
  per-call hooks `bundledPluginTool.Run` does today. Validation: run
  the existing test suite against the unified path before Step 1.

- **`procAllowed` cap-matching change.** Final policy:
  absolute-path glob OR slash-free basename glob (mixed-relative
  rejected). Existing manifests using either form keep working;
  the matcher gains a single `strings.Contains(glob, "/")` branch
  to choose between absolute-path-match and basename-match. No
  hard cut.

- **Wasm-side glob/grep walking budget.** Native `fs.GlobTool` has
  `maxGlobWalkEntries = 200000`. The wasm rewrite must enforce its
  own bound or the host budget at `stado_fs_readdir` level. Picking
  the latter — make readdir cap entries per call (default 50000).
  Plugin paginates if needed.

- **Sandbox-policy arg semantics.** Plugin author specifies the
  policy; runtime executes it as-is. If the runner is unavailable
  (no bwrap on Linux, no sandbox-exec on macOS), behavior must be
  defined: refuse-with-error vs run-unsandboxed-with-warning. Pick
  refuse-with-error when policy is non-empty; matches the existing
  `[sandbox] refuse_no_runner` config semantics.

- **Bundled manifest signing — see "Resolved policy decisions"
  above.** Runtime trust short-circuits; bundled manifests still
  carry signatures for offline verification via
  `stado plugin verify --bundled` against an external anchor URL.

- **Test surface explosion.** Many tests today mock the registry
  with native `tool.Tool` impls. After Step 9 the registry contains
  only plugin tools + carve-outs. Tests that need a fake tool can
  use a fake-plugin source. Adds boilerplate but is consistent.

- **External plugin authors will break.** Manifests using
  `exec:bash`, `exec:search`, `exec:ast_grep` need re-signing with
  new caps. Single-user repo today, but the migration note in
  CHANGELOG should be explicit.

- **VerifiedPluginSource interface shape — codex's #5.** Final
  shape carries lazy-load + error-propagating Bytes + policy-aware
  Verify:
  ```go
  type VerifiedPluginSource interface {
      Manifest() *plugins.Manifest
      Bytes(ctx context.Context) ([]byte, error)
      Verify(ctx context.Context, policy VerifyPolicy) (TrustResult, error)
      Origin() string
  }
  ```
  `VerifyPolicy` carries CRL+Rekor+offline-anchor settings.
  `TrustResult` includes trust kind (`bundled` / `signed` /
  `unsafe-skip`) plus signing identity for audit. User-bundled
  source expresses its bundler-sig + per-entry-sig + unsafe-skip
  flow through the same Verify call.

- **`tasks` plugin uses durable file state — codex's #3.** Sequence
  counter via `stado_instance_*` would reset every call (per-Runtime
  store, cleared on `Runtime.Close`; each tool call gets a fresh
  Runtime). Tasks plugin uses the existing on-disk task store
  (`internal/tasks/store.go`) accessed via `stado_fs_*` primitives
  with file-lock guarded read-modify-write. May need
  `stado_fs_flock(path, mode) → handle` if no equivalent exists yet
  — confirm during Step 8 implementation.

- **Naming conflict — codex's #4.** Validation criterion #5 in
  v2 was internally inconsistent. Resolved: the migration *is* a
  surface change — model receives wire form (`fs__read`,
  `shell__exec`) post-migration; default autoload list updates;
  tests hardcoding bare names update. Display surface (TUI/CLI)
  shows dotted form via the existing canonical-display layer.
  Validation #5 rewritten to match.

- **`stado plugin verify --bundled` meta-attestation limit —
  codex's #6.** A tampered binary can alter the verifier itself or
  the embedded URL constant. The verify command provides meaningful
  attestation only when run by an externally-trusted binary (e.g.,
  a freshly downloaded stado release). Documented in the command's
  help text and `docs/features/no-internal-tools.md`. Adds
  `--pubkey-file <path>` flag for fully-offline verification
  against an externally-shipped pubkey.

- **External MCP-attached tools — codex's #8.** `attachMCP`
  (`mcp_glue.go:99`) registers `mcpbridge.MCPTool` instances
  directly into the registry. These are not wasm plugins and not
  carve-outs. Documented as an explicit *transport-bridge
  exception*: tools brought in over the MCP protocol are
  semantically external (the operator chose to attach a remote
  server) and travel through their own dispatch path. The
  invariant "model-facing tools are wasm plugins or carve-outs"
  holds for *stado-originated* tools. MCP-attached tools are
  external-originated.

## Done definition

- All 9 steps shipped to main, each as its own merge commit.
- Validation criteria 1–8 all pass.
- CHANGELOG entry per step plus a summary at the cut.
- Tag the cut as v0.45.0 (minor — meaningful surface change, breaks
  third-party plugin authors).
- The deviation note in EP-0038 is rewritten to "reversed 2026-05-06;
  native registrations removed; plugin shape is the only model-facing
  surface."
- New doc `docs/features/no-internal-tools.md` written: short
  narrative for future readers explaining the invariant and the 8
  carve-outs.

## Resolved policy decisions

**Bundled plugin trust — runtime short-circuit + operator-driven external verify.**

Two-layer design:

1. **Runtime fast path: `bundledSource.Trust()` short-circuits.**
   No per-call signature verification of bundled plugins. The runtime
   does not embed a pubkey for bundled plugins — embedding one would
   be theater (an attacker who rebuilds stado embeds their own
   pubkey alongside their plugins). Trust at this layer = "compiled
   into this stado build."

2. **Bundled manifests still carry signatures.** Same shape as
   installed plugins (uniform manifest format; no special-case
   stripping). The signatures are not consumed at runtime but are
   present for offline verification.

3. **`stado plugin verify` extended to verify bundled plugins
   against an external authoritative pubkey.** The verify command
   fetches the canonical pubkey from a stado-shipped URL constant
   (per EP-0039 anchor pattern, e.g.
   `github.com/foobarto/stado-plugins/.stado/author.pub`), then
   verifies every bundled plugin's manifest signature against that
   key. Fail = either the binary was tampered with OR the canonical
   key changed at the source.

This catches the threat model that matters: tampered stado binary
with replaced bundled plugins. The external pubkey URL is hardcoded
in stado source; an attacker rebuilding stado to replace bundled
plugins must *also* compromise the canonical-pubkey-hosting repo to
forge a verification pass. That's a substantially bigger compromise.

Installed plugins keep real signature verification at runtime —
that's a different threat model (untrusted plugin from outside the
build).

**`procAllowed` cap matcher — absolute paths OR slash-free basename
glob.** Two acceptable forms:

- Absolute path glob: `exec:proc:/usr/bin/bash`,
  `exec:proc:/usr/bin/impacket-*` — matches against the resolved
  absolute path.
- Slash-free basename glob: `exec:proc:bash`, `exec:proc:impacket-*`
  — matches against `filepath.Base(resolved_path)`. Still constrains
  to whatever the operator's PATH resolves the binary to; preserves
  cross-distro portability (`/usr/bin/bash` vs `/bin/bash` vs
  Homebrew paths) without requiring distro-specific manifests.

Mixed forms (relative paths with slashes, e.g. `bin/bash`) are
rejected — the only two valid shapes are absolute-with-slashes or
basename-without-slashes. Today's matcher (`host_proc.go:34-45`)
already does abs-path resolution; the fix is one short branch on
`strings.Contains(glob, "/")`.

**fs glob/grep/edit go to wasm, not held as primitives.**
Each is composition over `stado_fs_readdir`/`stado_fs_read`/
`stado_fs_write`. ~30-50 lines of wasm-side Go each. If multiple
plugins later need the same pattern, extract a shared
`internal/bundledplugins/lib/` utility package (still wasm-side) or
promote back to a primitive then. YAGNI for the migration.

**Atomicity caveat for `edit`.** The native `fs.EditTool.Run` is
read-then-write — not actually atomic against concurrent writers.
The wasm rewrite preserves the same semantics (no atomicity loss).
If atomic edit becomes important, add a `stado_fs_replace_string`
primitive then.

## Open issue (Step 3 detail)

**Sandbox-policy arg validation.** JSON shape spec is part of Step 3
implementation; not a design-level decision.
