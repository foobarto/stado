# Bundled plugins in `plugin list` + `stado tool run` — design

**Status:** approved 2026-05-05; awaiting writing-plans pass.
**Author:** Bartosz Ptaszynski (with brainstorming assistance).
**Branch:** `feat/bundled-plugins-and-tool-run` (off `main` at `9ef2fae`).

## Problem

Two tightly coupled gaps in stado's plugin-level UX:

1. **`stado plugin list` doesn't show bundled plugins.** It enumerates only installed-on-disk plugins under `<state-dir>/plugins/` via `plugins.ListInstalledDirs(pluginsDir)`. Meanwhile `stado tool list` walks the live registry and shows tools FROM bundled plugins (because they're registered as wasm-wrapped tools at startup). The asymmetry is confusing: operators see `fs.read`, `shell.exec`, `agent.spawn` etc. in `tool list` and reasonably ask "where's the `fs` plugin in `plugin list`?"

2. **`stado plugin run` only resolves disk-installed plugins.** A bundled plugin can't be invoked through the CLI even though it can be invoked through the LLM (via the registered tool surface).

The fix has two prongs. The listing-side fix is straightforward enumeration + rendering. The run-side fix is a deeper rethink: the `plugin run` interface is by *plugin instance*, but operators almost always want to invoke by *tool*. Replacing `plugin run` with `tool run` aligns the CLI with the LLM's tool-name dispatch and incidentally resolves the bundled-vs-installed distinction (the live registry knows about both).

## Locked decisions

From the 2026-05-05 brainstorming session:

| # | Decision | Rationale |
|---|---|---|
| 1 | **Plugin unit = wasm module** (~15-20 bundled rows) | Matches operator mental model; tools-count column carries the rich variable; `plugin info` drills into per-tool detail. |
| 2 | **Version column = stado's release version** (`v0.34.0`) | Already what synthetic manifests carry. Operators correlating issues to a stado release is a real workflow. |
| 3 | **Status column = `✓ bundled` sentinel** | Third state alongside `✓ trusted` / `⚠ untrusted`. No fingerprint shown for bundled (rendered as `-`). |
| 4 | **Replace `plugin run` with `tool run`** | Operators dispatch by tool, not plugin instance; matches the LLM's mental model; eliminates bundled-vs-installed special-casing in the run path. Pre-1.0; no back-compat alias. |
| 5 | **`tool run` accepts both canonical (`fs.read`) and wire (`fs__read`) forms** | Consistent with existing `stado tool info`. |
| 6 | **Disabled tools refuse with `--force` escape** | CLI mirrors the model-facing surface's disable list by default; explicit override is one-flag away. |

## Architecture

### Component 1 — Bundled-plugin enumeration

Add `internal/bundledplugins/list.go`:

```go
type Info struct {
	Name         string   // wasm module name (e.g. "fs", "shell", "bash")
	Version      string   // stado's version.Version
	Author       string   // bundledplugins.Author == "stado"
	Tools        []string // canonical tool names registered against this module
	Capabilities []string // declared caps, deduplicated across constituent tools
	WasmSHA256   string   // sha256 of the embedded wasm bytes
}

// List returns one Info per bundled wasm module + the auto-compact
// background plugin. Reads from the package-level registration slice
// populated by internal/runtime/bundled_plugin_tools.go's init.
func List() []Info
```

The list is populated lazily on first call from a package-level slice that `internal/runtime/bundled_plugin_tools.go`'s registration code maintains (since that's where `wasmName` ↔ tools mapping already exists). The slice grows as `newBundledPluginTool` and `newBundledWasmTool` register tools, grouping by `wasmName`. A small lock-free init pattern (sync.Once on first `List` call) materialises the deduplicated, sorted result.

The auto-compact background plugin (`internal/bundledplugins/auto_compact.go`'s `BundledBackgroundPlugin`) appears as a single entry with `Name: "auto-compact"`, `Tools: []` (it has no model-facing tools), and `Capabilities: ["session:observe", "session:read", "session:fork", "llm:invoke:30000"]` (carried verbatim from the existing `BundledBackgroundPlugin` definition at `internal/bundledplugins/auto_compact.go:44`).

### Component 2 — `plugin list` rendering

Modify `cmd/stado/plugin_trust.go pluginListCmd`:

```go
// Bundled plugins from the embedded set.
bundled := bundledplugins.List()
for _, b := range bundled {
    rows = append(rows, row{
        name:        b.Name,
        version:     b.Version,
        tools:       len(b.Tools),
        toolNames:   strings.Join(b.Tools, ", "),
        author:      b.Author,
        fingerprint: "",        // rendered as "-"
        trusted:     true,      // bundled implies trusted
        bundled:     true,      // new field on row
        caps:        len(b.Capabilities),
    })
}
```

Add a `bundled bool` field on the existing `row` struct. Render logic:

- Sort: bundled rows first (alphabetical), installed rows second (alphabetical), no separator line — the status column distinguishes them visually.
- Status column: `✓ bundled` for bundled rows, `✓ trusted` / `⚠ untrusted` for installed rows.
- Fingerprint column: `-` for bundled rows.
- Summary line updates from `"3 plugins installed (all trusted)"` to `"15 plugins (12 bundled, 3 installed; all trusted)"`.

The `--json` output (today implicit via the existing JSON-emitting paths in `tool list`; `plugin list` doesn't currently support `--json` — out of scope for this spec, file follow-up).

### Component 3 — `plugin info` extension

Modify `cmd/stado/plugin_info.go` to look up bundled plugins by name first, falling back to disk-install lookup:

```go
if b, ok := bundledplugins.LookupByName(args[0]); ok {
    return printBundledInfo(b, mf)  // mf is the synthetic manifest
}
// existing disk lookup
```

`bundledplugins.LookupByName(name)` returns `(Info, bool)` for direct access. The synthetic manifest is reconstructed using the same logic the tools registration uses today.

### Component 4 — New `stado tool run` subcommand

Add `cmd/stado/tool_run.go`:

```
stado tool run <name> [json-args]
  --session <id>     Bind to an existing session for session-aware caps
  --workdir <path>   Override the working directory
  --force            Run even if the tool is disabled in [tools]
```

Resolution algorithm (in order):

1. **Direct registry lookup** via `executor.Registry.Get(name)` — handles wire-form input.
2. **Canonical → wire conversion** if name contains `.`: split on first dot, call `tools.WireForm(plugin, tool)`, retry registry lookup.
3. **Canonical-metadata fallback**: scan `executor.Registry.All()` for tools whose `runtime.LookupToolMetadata(t.Name()).Canonical == name`. Mirrors the existing `tool info` lookup pattern.

After lookup:

4. **Disabled check**: if the tool's name (in either form) matches any pattern in `cfg.Tools.Disabled` via `runtime.ToolMatchesGlob`, refuse with: `"tool <name> is disabled in [tools].disabled; remove it from disabled, or re-run with --force"` — unless `--force` was passed.
5. **Invoke**: dispatches through the same wasm-runtime + sandbox + session-bridge machinery `pluginRunCmd` uses today.

Args validation: `cobra.RangeArgs(1, 2)`. JSON args default to `{}` when absent. `toolinput.CheckLen` enforces the existing payload-size limit.

### Component 5 — `plugin run` removal

`pluginRunCmd` is deleted entirely. The shared invoke body — currently inside `pluginRunCmd.RunE` — extracts to `cmd/stado/plugin_invoke_shared.go` so `tool_run.go` can call it. The shared function takes:

```go
type invokeArgs struct {
    Tool       tool.Tool
    ArgsJSON   string
    SessionID  string
    Workdir    string
    Cfg        *config.Config
}

func runToolWithBridges(ctx context.Context, args invokeArgs) error
```

Internals: build the session bridge (existing `buildPluginRunBridge`), build the sandbox runner (existing `sandbox.Detect()`), wrap stdout/stderr per existing semantics, dispatch via the wasm runtime.

`pluginCmd.AddCommand(...)` line in `cmd/stado/plugin.go:31` drops the `pluginRunCmd` reference. CHANGELOG gets a `Breaking changes` entry under the existing Unreleased section.

## File map

| Action | Path | Net lines |
|---|---|---|
| New | `internal/bundledplugins/list.go` | ~80 |
| New | `internal/bundledplugins/list_test.go` | ~60 |
| Modify | `internal/runtime/bundled_plugin_tools.go` (add registration into bundled-plugin metadata slice) | ~20 |
| New | `cmd/stado/tool_run.go` | ~100 |
| New | `cmd/stado/tool_run_test.go` | ~80 |
| Renamed/refactored | `cmd/stado/plugin_run.go` → split into `plugin_invoke_shared.go` (kept) + `tool_run.go` (new) | ~−60 net (the body moves; pluginRunCmd vanishes) |
| Modify | `cmd/stado/plugin_trust.go` (extend `pluginListCmd`) | ~30 |
| Modify | `cmd/stado/plugin_trust_test.go` (verify bundled rows appear) | ~40 |
| Modify | `cmd/stado/plugin_info.go` (bundled-first lookup) | ~25 |
| Modify | `cmd/stado/plugin_info_test.go` | ~20 |
| Modify | `cmd/stado/plugin.go` (drop `pluginRunCmd` registration; add `toolRunCmd` to `toolCmd`) | ~5 |
| Modify | `cmd/stado/tool.go` (`AddCommand(toolRunCmd)`) | ~5 |
| Modify | `CHANGELOG.md` (Unreleased / Breaking changes entry) | ~5 |
| Modify | `docs/eps/0037-tool-dispatch-and-operator-surface.md` (history note + verb-list update) | ~10 |

Total net: ~+400 / -60 across 13 files (mostly tests and the new helpers; production-code shrinks slightly).

## Testing strategy

### `bundledplugins.List()` (unit)
- Returns the expected number of entries (verified against the actual registration count, not hardcoded).
- `Tools` is non-empty for multi-tool modules; empty for the auto-compact background plugin.
- `Version` matches `version.Version` exactly.
- `Author` matches `bundledplugins.Author` exactly.
- Two consecutive `List()` calls return the same data (caching/sync.Once works).

### `plugin list` (CLI integration)
- With no installed plugins, lists the bundled ones — output contains `bundled` status and at least the `fs`, `shell`, `agent`, `bash` rows.
- Trailing summary line counts bundled and installed separately.
- Bundled rows render with `-` in fingerprint column.

### `plugin info` (CLI integration)
- `stado plugin info fs` finds the bundled `fs` module and prints its tools list. (Today this would fail — verifies the bundled-first lookup landed.)

### `tool run` (CLI integration)
- `stado tool run fs.read --workdir <tmp> '{"path":"<tmp>/foo.txt"}'` returns the file contents (round-trip through wasm).
- `stado tool run fs__read ...` (wire form) succeeds identically.
- `stado tool run shell.exec '{"command":"echo hi"}'` errors when `cfg.Tools.Disabled = ["shell.exec"]`. Passes with `--force`.
- `stado tool run nonexistent.foo` errors with a message mentioning `stado tool list`.
- A test confirms `plugin run` is no longer a registered subcommand (negative test against the cobra dispatch tree).

## Risks + mitigations

- **Risk:** the bundled-plugin metadata slice in `internal/runtime/bundled_plugin_tools.go` becomes a hidden dependency between two packages (`runtime` populates it; `bundledplugins.List()` reads it).
  - *Mitigation:* the slice lives in `internal/bundledplugins/` (the natural owner), with a `Register` function that `runtime/` calls during its init. Keeps the directionality clean (`runtime → bundledplugins`).

- **Risk:** an operator's existing scripts call `stado plugin run`.
  - *Mitigation:* pre-1.0; the user (sole operator today per project memory) green-lit the breaking change. CHANGELOG entry is the documented advisory.

- **Risk:** `tool run` shadows tool-author dev workflows that relied on `plugin run` for forced-version invocation.
  - *Mitigation:* dev-time forced-version invocation now flows through `stado plugin use <name> <version>` (already exists per EP-0039) followed by `stado tool run <name>`. One extra step but cleaner separation of concerns.

- **Risk:** a tool's name collides between bundled and installed (e.g. operator installs a `fs` plugin).
  - *Mitigation:* the registry-level resolution already orders installations after bundled (registry registration order). No special-casing needed in `tool run`.

## Out of scope

- Adding `--json` to `plugin list`. Separate item; the existing list output works without it.
- Bundled-plugin removal/disable. Bundled plugins are baked in; `plugin remove fs` / `tool disable fs.*` already cover the operator-facing levers.
- Migrating the older single-tool legacy wasm wrappers (`read.wasm`, `write.wasm`, `bash.wasm` etc.) into the new multi-tool `fs.wasm` / `shell.wasm` shape. That's a separate refactor (BACKLOG-related); this design respects the current set as-is.

## Verification plan

After implementation:

1. `go test ./...` passes.
2. `go vet ./...` clean.
3. Manual smoke:
   - `stado plugin list` shows bundled `fs`, `shell`, `agent`, `web`, `dns`, `bash`, etc. with `✓ bundled` status.
   - `stado plugin info fs` prints the bundled fs synthetic manifest.
   - `stado tool run fs.read --workdir /tmp '{"path":"/tmp/x"}'` round-trips. Same with `fs__read`.
   - `stado tool run shell.exec '{...}'` errors when `[tools] disabled = ["shell.exec"]`; `--force` overrides.
   - `stado plugin run` returns "unknown command" (cobra default).
4. The smoke runs match the exit codes documented above (non-zero on errors).
