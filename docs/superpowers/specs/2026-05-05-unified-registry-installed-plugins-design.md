# Unified registry — installed plugins reachable via `tool run` / `tool list` / `tools__search` / `mcp-server`

**Status:** approved 2026-05-05; awaiting writing-plans pass.
**Author:** Bartosz Ptaszynski (with brainstorming assistance).
**Branch:** `feat/unified-registry-installed-plugins` (off `main` at `536e6fb`).

## Problem

The 2026-05-05 bundled-plugins/tool-run merge (`536e6fb`) delivered the bundled half of the unification: `stado plugin list / info` show bundled plugins, and `stado tool run <name>` dispatches them through the live registry.

But `runtime.BuildDefaultRegistry()` (`internal/runtime/executor.go:51`) returns ONLY bundled wasm modules. Four surfaces share that registry:
- `cmd/stado/tool.go:33` — `stado tool list`
- `cmd/stado/tool_run.go:80` — `stado tool run`
- `cmd/stado/mcp_server.go:60` — MCP server
- `internal/tui/model_commands.go` — TUI `/tool ls`

Plus `tools__search` (the meta-tool) operates on the same registry.

Result: installed plugins (under `~/.local/share/stado/plugins/`) are unreachable from any of these surfaces. `cmd/stado/tool_run.go:108` errors with "tool %q is not a bundled tool — installed-plugin invocation by tool-name is not yet supported". Pre-merge they were reachable via `stado plugin run <plugin-id> <tool>`; post-merge that subcommand is gone, leaving installed plugins effectively orphaned.

## Locked decisions

From the 2026-05-05 brainstorming session (Q1–Q5):

| # | Decision | Rationale |
|---|---|---|
| Q1 | **Extend `BuildDefaultRegistry()`** to enumerate installed plugins; no separate `BuildFullRegistry`. | Single source of truth; nothing in the codebase needs a bundled-only registry. |
| Q2 | **Re-verify signature on every registration.** | Matches existing `plugin run` behaviour; keeps `tool list` honest about what's actually invocable; cost is small (Ed25519 verify). |
| Q3 | **Register only the active version** per `plugin use` / `[plugins.<name>].version` config. | Multi-version-coexistence's purpose is rollback; LLM-facing surface should reflect ONE choice at a time. |
| Q4 | **Installed wins on bundled-vs-installed name collision.** Operator info-line on stderr. | Plugins are stado's customization mechanism since inception; replacing fs/shell with policy-wrapped variants is the intended use case. |
| Q5 | **`[tools].disabled` applies uniformly** to bundled + installed; `--force` is the universal escape. | The disable mechanism's intent is "I don't want this tool right now" regardless of source — possibly more so for installed overrides which may have weaker safety. |

## Architecture

### Component 1 — Installed-plugin loader

New file `internal/runtime/installed_tools.go`:

```go
// registerInstalledPluginTools enumerates the operator's installed-
// plugin directory (default $XDG_DATA_HOME/stado/plugins/), picks
// the active version per plugin (per [plugins.<name>].version config
// or highest semver), verifies each plugin's manifest signature
// against the trust store, and registers its declared tools as
// wasm-backed entries with lazy load.
//
// Plugins failing signature verification emit a stado: warn line on
// stderr and are skipped. Tool-name collisions with already-
// registered tools (typically bundled) result in the installed tool
// overwriting; a stado: info line is emitted.
func registerInstalledPluginTools(reg *tools.Registry, cfg *config.Config)
```

Internals:
- Walk `cfg.PluginsDir()` (or `filepath.Join(cfg.StateDir(), "plugins")`).
- For each `<name>-<version>/` subdir, parse name + version. Group by name.
- For each name, pick active version: `cfg.Plugins.Pinned[name].Version` if set; else highest semver per the existing `plugins.SortVersions` (or equivalent). Skip if no version found.
- Load manifest + sig via `plugins.LoadFromDir(dir)`.
- Verify via `plugins.NewTrustStore(cfg.StateDir()).VerifyManifest(m, sig)`. On failure: log + skip.
- For each tool in the manifest's `Tools` slice: register a `bundledPluginTool`-shaped wrapper (or new `installedPluginTool` type — see Component 6) that knows how to lazy-load `<dir>/plugin.wasm` on first invocation.

`LookupInstalledModule(toolName) (Manifest, wasmPath, bool)`:
- Symmetric with `bundledplugins.LookupModuleByToolName`. Used by `tool_run.go` to recover the manifest + wasm path for dispatch.

### Component 2 — Extend `BuildDefaultRegistry`

`internal/runtime/executor.go`:

Current:
```go
func BuildDefaultRegistry() *tools.Registry {
    reg := buildBundledPluginRegistry()
    registerMetaTools(reg)
    return reg
}
```

New:
```go
func BuildDefaultRegistry(cfg *config.Config) *tools.Registry {
    reg := buildBundledPluginRegistry()
    registerMetaTools(reg)
    if cfg != nil {
        registerInstalledPluginTools(reg, cfg)
    }
    return reg
}
```

Order: bundled first, meta-tools, installed last. Installed-overrides-bundled comes from registration order — `tools.Registry.Register` overwrites by name (verify this is the actual behaviour; if not, add explicit override + log).

The `cfg != nil` guard makes the function tolerant of test code that passes a nil config (existing tests use `BuildDefaultRegistry()` with no config; they get bundled-only).

### Component 3 — Caller updates

Four call sites need to pass `cfg`:

1. **`cmd/stado/tool.go:33`** (`toolListCmd`): already loads `cfg`. Change `runtime.BuildDefaultRegistry()` → `runtime.BuildDefaultRegistry(cfg)`.
2. **`cmd/stado/tool_run.go:80`** (`runToolByName`): already has `cfg`. Same change.
3. **`cmd/stado/mcp_server.go:60`** (`mcpServerCmd`): loads cfg in the surrounding scope. Pass it.
4. **`internal/tui/model_commands.go:467/469`** (`/tool ls` slash): has `m.cfg`. Pass it.

### Component 4 — `tool run` dispatch for installed

`cmd/stado/tool_run.go:108`:

Current:
```go
info, isBundled := bundledplugins.LookupModuleByToolName(registered.Name())
if !isBundled {
    return fmt.Errorf("tool %q is not a bundled tool — installed-plugin invocation by tool-name is not yet supported (use `stado tool list` to see what's available)", registered.Name())
}
```

New:
```go
info, isBundled := bundledplugins.LookupModuleByToolName(registered.Name())
if isBundled {
    // Bundled path (existing) — synthesise manifest, load via
    // bundledplugins.Wasm, dispatch via runPluginInvocation.
    ...
} else if mfst, wasmPath, ok := runtime.LookupInstalledModule(registered.Name()); ok {
    // Installed path — load wasm bytes from disk, dispatch via the
    // same shared helper.
    wasmBytes, err := plugins.ReadVerifiedWASM(mfst.WASMSHA256, wasmPath)
    if err != nil {
        return fmt.Errorf("verify: %w", err)
    }
    return runPluginInvocation(ctx, pluginInvokeArgs{
        Manifest:   mfst,
        WasmBytes:  wasmBytes,
        ToolName:   /* derive from registered.Name() */,
        ArgsJSON:   argsJSON,
        Cfg:        cfg,
        WorkdirArg: opts.Workdir,
        InstallDir: filepath.Dir(wasmPath),
        SessionID:  opts.Session,
        Stdout:     stdout,
        Stderr:     stderr,
    })
}
return fmt.Errorf("tool %q registered but its source plugin not found — try `stado plugin list`", registered.Name())
```

The disabled check + `--force` escape (already in place from the bundled-plugins work) runs before this branch and applies uniformly.

### Component 5 — Active-version resolution

New helper `internal/runtime/installed_tools.go:activeVersion(name string, candidates []string, cfg *config.Config) string`:

```go
// activeVersion picks the version of plugin name to register, given
// the list of versions found on disk. Pin precedence:
//   1. cfg.Plugins.Pinned[name].Version (explicit, set via plugin use
//      or [plugins.<name>] version = "X" in config.toml)
//   2. Highest semver among candidates
// Returns "" if no candidate matches the pin (meaning the pinned
// version isn't installed).
func activeVersion(name string, candidates []string, cfg *config.Config) string
```

Reuses the existing version-comparison helpers from `internal/plugins/identity.go` (`ParseIdentity` etc.) — no new semver code.

### Component 6 — Tool-wrapper type

The bundled path uses `bundledPluginTool` in `internal/runtime/bundled_plugin_tools.go`. For installed plugins, we need an analogous wrapper that lazy-loads from disk instead of the embed.FS.

Two options:
- **Reuse `bundledPluginTool`** with a path-based wasm loader. Risk: muddies the type's "bundled" meaning.
- **New `installedPluginTool` type** in `installed_tools.go`. Cleaner separation; tiny extra type.

**Pick: new type.** Mirror the bundledPluginTool interface (Tool.Name/Description/Schema/Run) but its wasm-load path reads from disk via `plugins.ReadVerifiedWASM(manifest.WASMSHA256, wasmPath)` lazily on first call.

### Component 7 — Stderr logging

Both warn (signature failure) and info (override) lines write to `os.Stderr`. The TUI captures stderr separately, so these don't pollute the chat surface — they're operator diagnostics. CLI surfaces print them inline.

## File map

| Action | Path | Net lines |
|---|---|---|
| Create | `internal/runtime/installed_tools.go` | ~150 |
| Create | `internal/runtime/installed_tools_test.go` | ~120 |
| Modify | `internal/runtime/executor.go` (sig + delegate) | ~15 |
| Modify | `cmd/stado/tool_run.go` (drop "not supported" branch + installed dispatch) | ~30 |
| Modify | `cmd/stado/tool.go` (pass cfg) | ~3 |
| Modify | `cmd/stado/mcp_server.go` (pass cfg) | ~3 |
| Modify | `internal/tui/model_commands.go` (pass cfg, possibly 2 sites) | ~6 |
| Modify | tests of `BuildDefaultRegistry` (signature changed) | ~varies |

Total: ~330 net, 6 modified + 2 new files.

## Testing strategy

### Unit — `registerInstalledPluginTools`

- **Happy path:** temp `XDG_DATA_HOME`, install one signed plugin via existing test helpers, call `registerInstalledPluginTools(reg, cfg)`, assert the plugin's tools appear in `reg.All()`.
- **Signature failure:** install a plugin then mutate its `plugin.wasm` bytes (changes the sha256), call register, assert: tool is NOT in registry; stderr contains "signature failed".
- **Active-version pinning:** install two versions of the same plugin, set `[plugins.<name>].version = "v0.1.0"` in config, call register, assert only the v0.1.0 tools appear.
- **No active version match:** `[plugins.<name>].version = "v9.9.9"` (not installed), call register, assert no tools registered for that name.
- **Bundled override:** register bundled (`buildBundledPluginRegistry`) first, then `registerInstalledPluginTools` for an installed plugin that exposes `fs__read`. Assert `reg.Get("fs__read")` returns the installed wrapper, not the bundled. Stderr contains "overrides bundled".

### Integration — `tool run`

- **Installed plugin happy path:** install a known plugin (e.g. one of the existing fixtures), `runToolByName(t.Context(), "<canonical>", argsJSON, opts)`, assert success and content.
- **Disabled refusal still applies:** install a plugin, set its tool in `cfg.Tools.Disabled`, assert refusal; with `--force`, succeeds.
- **MCP-server smoke:** boot mcp-server with installed plugins present, list tools via the MCP protocol, assert installed tools appear (probably via existing mcp-server test infrastructure).

## Risks + mitigations

- **Risk:** `registerInstalledPluginTools` runs on every CLI invocation that builds the registry; cost scales with installed-plugin count.
  - *Mitigation:* lazy wasm load (only the manifest + sig load happen at registration; wasm bytes load on first tool invocation). For 13 installed plugins this is ~13 manifest reads + 13 sig verifies — small.

- **Risk:** signature failure during registration is a hard error that kills the whole CLI invocation.
  - *Mitigation:* per Q2, refuse-and-skip; emit warn to stderr; continue registering other plugins. The CLI invocation still succeeds, just with the failed plugin missing from the surface.

- **Risk:** installed-plugin name collision with bundled silently degrades the user experience (operator gets the override; doesn't realize bundled is shadowed).
  - *Mitigation:* per Q4 + Component 7, emit `stado: info: plugin X@V overrides bundled tool W` to stderr at registration time. Operator sees it.

- **Risk:** `BuildDefaultRegistry` signature change breaks unrelated tests.
  - *Mitigation:* nil-cfg guard (Component 2) keeps existing test code working without modification — it just gets bundled-only behaviour.

- **Risk:** TestPluginList output now includes installed plugins by default; existing tests that asserted on bundled-only output may flake.
  - *Mitigation:* run full test suite after each task in the plan; fix any test regressions inline.

## Out of scope

- A `[plugins] include_in_tool_list = false` knob for opting individual plugins out of the surface. YAGNI; defer until someone asks.
- Extending the existing `tool list` `--bundled` / `--installed` filter flags — operator can grep the existing output by status column for now.
- Multi-version disambiguation in the registry (registering all versions with name suffixes). Per Q3, register-active-only is the chosen design; the tester can `plugin use <name> <ver>` to switch.

## Verification plan

After implementation:

1. `go test ./... -count=1` passes.
2. `go vet ./...` clean.
3. Manual smoke:
   - `stado plugin list` shows bundled + installed (already does post-merge).
   - `stado tool list` shows installed tools (e.g. `htb-lab.spawn`, `gtfobins.lookup`, etc.) alongside bundled.
   - `stado tool run htb-lab.spawn '{"target":"..."}'` succeeds against an installed plugin.
   - `stado tool run` for an installed-plugin tool whose name collides with bundled: invocation goes through the installed override; stderr at registration time noted the override.
   - Setting `[tools].disabled = ["htb-lab.spawn"]` refuses the tool; `--force` overrides.
   - MCP server `tools/list` returns both bundled and installed.
