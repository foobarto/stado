# Bundled plugins in `plugin list` + `stado tool run` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `stado plugin list` and `stado plugin info` aware of bundled (binary-embedded) plugins, and replace `stado plugin run <id> <tool>` with `stado tool run <canonical-or-wire-name>` that resolves bundled and installed tools uniformly through the live registry.

**Architecture:** Two cohesive, dependency-ordered components. (1) A new `internal/bundledplugins/list.go` module that owns bundled-plugin metadata via a package-level slice populated at registration time by `internal/runtime/bundled_plugin_tools.go`. (2) A new `cmd/stado/tool_run.go` subcommand that looks up a tool in the live registry and dispatches through a shared invoke helper extracted from the existing `plugin_run.go`. The old `pluginRunCmd` is then deleted; pre-1.0, no alias.

**Tech Stack:** Go 1.22+, cobra, wazero (existing), the architectural-reset `WireForm`/`ParseWireForm` helpers from `internal/tools/naming.go`.

**Spec:** `docs/superpowers/specs/2026-05-05-bundled-plugins-and-tool-run-design.md`. Follow the locked decisions there. Branch: `feat/bundled-plugins-and-tool-run` (already created off `main` at `9ef2fae`; spec committed at HEAD = `60844eb`).

**Scope OUT** (explicit):
- `--json` for `plugin list` (deferred; existing `plugin list` doesn't support it).
- Migrating legacy single-tool wasm wrappers (`read.wasm`, `write.wasm`, etc.) into the multi-tool shape (`fs.wasm`, etc.).
- Removing or disabling bundled plugins (they're baked in).

---

## File map

| Action | Path | Purpose |
|---|---|---|
| Create | `internal/bundledplugins/list.go` | `Info` struct + `RegisterModule` / `List` / `LookupByName` / `LookupModuleByToolName` |
| Create | `internal/bundledplugins/list_test.go` | Unit tests for the registry |
| Modify | `internal/runtime/bundled_plugin_tools.go` | `newBundledPluginTool` / `newBundledWasmTool` / `newBundledStaticTool` call `bundledplugins.RegisterModule` |
| Modify | `internal/bundledplugins/auto_compact.go` | Self-register at init (so the auto-compact entry shows up in `List()`) |
| Create | `cmd/stado/plugin_invoke_shared.go` | Shared invoke body extracted from `pluginRunCmd` |
| Create | `cmd/stado/plugin_invoke_shared_test.go` | Smoke test for the shared helper |
| Create | `cmd/stado/tool_run.go` | New `tool run` subcommand |
| Create | `cmd/stado/tool_run_test.go` | Tests for `tool run` |
| Modify | `cmd/stado/plugin_run.go` | Refactor to call shared helper (interim — file deletes in Task 8) |
| Modify | `cmd/stado/plugin_trust.go` (`pluginListCmd`) | Render bundled rows alongside installed |
| Modify | `cmd/stado/plugin_trust_test.go` | Verify bundled rows appear |
| Modify | `cmd/stado/plugin_info.go` | Look up bundled by name first |
| Modify | `cmd/stado/plugin_info_test.go` | Verify bundled `info` works |
| Delete | `cmd/stado/plugin_run.go` (after extraction) | Old subcommand body |
| Modify | `cmd/stado/plugin.go` | Drop `pluginRunCmd` registration |
| Modify | `cmd/stado/tool.go` | Add `toolRunCmd` registration |
| Modify | `CHANGELOG.md` | Breaking-change entry |
| Modify | `docs/eps/0037-tool-dispatch-and-operator-surface.md` | History note + verb-list update |

---

## Task 1: `bundledplugins.Info` + registry scaffolding

**Files:**
- Create: `internal/bundledplugins/list.go`
- Create: `internal/bundledplugins/list_test.go`

**Context:** Today bundled plugin metadata lives implicitly inside `internal/runtime/bundled_plugin_tools.go`'s registration calls — there's no enumerable view. This task adds an enumerable view via a package-level slice + thread-safe accessor.

- [ ] **Step 1.1: Write the failing test file**

Create `<repo-root>/internal/bundledplugins/list_test.go`:

```go
package bundledplugins

import (
	"reflect"
	"sort"
	"testing"
)

// TestRegisterModule_DedupsByModuleName: multiple RegisterModule calls
// with the same wasmName accumulate tools into a single Info entry.
func TestRegisterModule_DedupsByModuleName(t *testing.T) {
	resetForTest(t)

	RegisterModule("fs", "fs__read", []string{"fs:read:."})
	RegisterModule("fs", "fs__write", []string{"fs:write:."})
	RegisterModule("shell", "shell__exec", []string{"exec:proc"})

	got := List()
	if len(got) != 2 {
		t.Fatalf("expected 2 modules, got %d: %+v", len(got), got)
	}
	// Tools deduped + sorted within a module.
	for _, info := range got {
		if info.Name == "fs" {
			want := []string{"fs__read", "fs__write"}
			if !reflect.DeepEqual(info.Tools, want) {
				t.Errorf("fs.Tools = %v, want %v", info.Tools, want)
			}
		}
	}
}

// TestList_SortedByName: List returns entries sorted by Name.
func TestList_SortedByName(t *testing.T) {
	resetForTest(t)

	RegisterModule("shell", "shell__exec", nil)
	RegisterModule("fs", "fs__read", nil)
	RegisterModule("agent", "agent__spawn", nil)

	got := List()
	var names []string
	for _, info := range got {
		names = append(names, info.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("List() names not sorted: %v", names)
	}
}

// TestLookupByName_Found returns the Info plus wasm bytes.
func TestLookupByName_Found(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", []string{"fs:read:."})

	info, wasmBytes, ok := LookupByName("fs")
	if !ok {
		t.Fatal("LookupByName('fs') should succeed after RegisterModule")
	}
	if info.Name != "fs" {
		t.Errorf("info.Name = %q, want fs", info.Name)
	}
	if len(wasmBytes) == 0 {
		t.Errorf("wasmBytes should be non-empty (loaded via MustWasm); got %d bytes", len(wasmBytes))
	}
}

// TestLookupByName_NotFound: missing module reports not-found.
func TestLookupByName_NotFound(t *testing.T) {
	resetForTest(t)
	if _, _, ok := LookupByName("nonexistent"); ok {
		t.Error("LookupByName('nonexistent') should be ok=false")
	}
}

// TestLookupModuleByToolName_Found maps tool name back to its module.
func TestLookupModuleByToolName_Found(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", nil)
	RegisterModule("fs", "fs__write", nil)
	RegisterModule("shell", "shell__exec", nil)

	info, ok := LookupModuleByToolName("fs__read")
	if !ok {
		t.Fatal("LookupModuleByToolName('fs__read') should succeed")
	}
	if info.Name != "fs" {
		t.Errorf("info.Name = %q, want fs", info.Name)
	}

	if _, ok := LookupModuleByToolName("does__not_exist"); ok {
		t.Error("LookupModuleByToolName for unknown tool should be ok=false")
	}
}

// TestRegisterModule_DedupsCaps: capabilities deduped across tools.
func TestRegisterModule_DedupsCaps(t *testing.T) {
	resetForTest(t)
	RegisterModule("fs", "fs__read", []string{"fs:read:.", "fs:write:."})
	RegisterModule("fs", "fs__write", []string{"fs:write:."})

	info, _, _ := LookupByName("fs")
	if got := len(info.Capabilities); got != 2 {
		t.Errorf("expected 2 unique caps, got %d: %v", got, info.Capabilities)
	}
}

// resetForTest clears the package-level registry. Calls t.Cleanup to
// restore. Marked Helper so failures point at the call site.
func resetForTest(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	prev := append([]moduleEntry(nil), registry...)
	registry = nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = prev
		registryMu.Unlock()
	})
}
```

- [ ] **Step 1.2: Run the test, verify it fails to compile**

Run: `cd <repo-root> && go test ./internal/bundledplugins/ -run "TestRegisterModule|TestList_|TestLookup" -count=1`
Expected: build failure with `undefined: RegisterModule`, `undefined: Info`, `undefined: List`, etc.

- [ ] **Step 1.3: Implement `internal/bundledplugins/list.go`**

Create `<repo-root>/internal/bundledplugins/list.go`:

```go
package bundledplugins

import (
	"sort"
	"sync"

	"github.com/foobarto/stado/internal/version"
)

// Info describes one bundled plugin (a wasm module shipped with the
// stado binary). One Info per .wasm file in internal/bundledplugins/wasm/,
// produced by aggregating RegisterModule calls made at registration time.
type Info struct {
	Name         string   // wasm module basename (e.g. "fs", "shell")
	Version      string   // stado release version (version.Version)
	Author       string   // bundledplugins.Author == "stado"
	Tools        []string // registered tool names attributable to this module, sorted
	Capabilities []string // declared caps, deduped across all tools, sorted
}

type moduleEntry struct {
	Name string
	Tool string
	Caps []string
}

var (
	registryMu sync.Mutex
	registry   []moduleEntry
)

// RegisterModule records that a tool with name toolName, declaring caps,
// is provided by the bundled wasm module wasmName. Called by the
// bundled-tool registration code at startup. Idempotent on (wasmName,
// toolName) — the same pair recorded twice still produces one Tools entry.
func RegisterModule(wasmName, toolName string, caps []string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, moduleEntry{
		Name: wasmName,
		Tool: toolName,
		Caps: append([]string(nil), caps...),
	})
}

// List returns a deduplicated, alphabetically-sorted view of the
// bundled-plugin registry. Each Info aggregates the Tools and
// Capabilities recorded for that wasmName. Tools and Caps are sorted
// for deterministic output.
func List() []Info {
	registryMu.Lock()
	defer registryMu.Unlock()
	return buildList(registry)
}

// LookupByName returns the Info for the named module plus the embedded
// wasm bytes (panics if the wasm file is missing — that's a build-time
// invariant). Returns ok=false if no module with that name was
// registered.
func LookupByName(name string) (Info, []byte, bool) {
	registryMu.Lock()
	infos := buildList(registry)
	registryMu.Unlock()

	for _, info := range infos {
		if info.Name == name {
			return info, MustWasm(name), true
		}
	}
	return Info{}, nil, false
}

// LookupModuleByToolName returns the bundled-module Info that exposes
// the named tool (registry-form name, e.g. "fs__read"). Returns
// ok=false if no bundled module owns that tool.
func LookupModuleByToolName(toolName string) (Info, bool) {
	registryMu.Lock()
	infos := buildList(registry)
	registryMu.Unlock()

	for _, info := range infos {
		for _, tn := range info.Tools {
			if tn == toolName {
				return info, true
			}
		}
	}
	return Info{}, false
}

// buildList aggregates entries by module name, dedupes, sorts. Caller
// must hold registryMu.
func buildList(entries []moduleEntry) []Info {
	byName := map[string]*Info{}
	toolSeen := map[string]map[string]bool{}
	capSeen := map[string]map[string]bool{}

	for _, e := range entries {
		info, ok := byName[e.Name]
		if !ok {
			info = &Info{
				Name:    e.Name,
				Version: version.Version,
				Author:  Author,
			}
			byName[e.Name] = info
			toolSeen[e.Name] = map[string]bool{}
			capSeen[e.Name] = map[string]bool{}
		}
		if e.Tool != "" && !toolSeen[e.Name][e.Tool] {
			toolSeen[e.Name][e.Tool] = true
			info.Tools = append(info.Tools, e.Tool)
		}
		for _, c := range e.Caps {
			if !capSeen[e.Name][c] {
				capSeen[e.Name][c] = true
				info.Capabilities = append(info.Capabilities, c)
			}
		}
	}

	out := make([]Info, 0, len(byName))
	for _, info := range byName {
		sort.Strings(info.Tools)
		sort.Strings(info.Capabilities)
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
```

- [ ] **Step 1.4: Run the tests, verify PASS**

Run: `cd <repo-root> && go test ./internal/bundledplugins/ -run "TestRegisterModule|TestList_|TestLookup" -count=1 -v`
Expected: 6 PASS.

- [ ] **Step 1.5: Run the full bundledplugins package**

Run: `cd <repo-root> && go test ./internal/bundledplugins/ -count=1`
Expected: PASS.

- [ ] **Step 1.6: Commit**

```bash
cd <repo-root>
git add internal/bundledplugins/list.go internal/bundledplugins/list_test.go
git commit -m "feat(bundledplugins): Info + RegisterModule registry

Adds an enumerable view of bundled plugins. RegisterModule is
called by bundled-tool registration code at startup; List/
LookupByName/LookupModuleByToolName provide read access. No
callers yet — wiring lands in the next task."
```

---

## Task 2: Wire the registration calls

**Files:**
- Modify: `internal/runtime/bundled_plugin_tools.go` (call `bundledplugins.RegisterModule` from `newBundledPluginTool` / `newBundledStaticTool` / `newBundledWasmTool`)
- Modify: `internal/bundledplugins/auto_compact.go` (self-register at package init)
- Modify: `internal/bundledplugins/list_test.go` (add a "real registrations land in List()" smoke test)

**Context:** Task 1's registry is empty until something calls `RegisterModule`. The registration sites already know `wasmName`, the registered tool name, and the caps slice — they just need to also call into the new helper.

- [ ] **Step 2.1: Read the registration constructors**

`cd <repo-root> && sed -n '270,360p' internal/runtime/bundled_plugin_tools.go`

Confirm the three constructors:
- `newBundledPluginTool(native tool.Tool, class)` — wasmName = `native.Name()`, toolName = `native.Name()`, caps from `bundledToolCapabilities(native.Name())`.
- `newBundledStaticTool(name, desc, class, schema, caps)` — wasmName = `name`, toolName = `name`.
- `newBundledWasmTool(wasmName, toolExport, registeredName, desc, class, schema, caps)` — wasmName = arg, toolName = `registeredName`.

- [ ] **Step 2.2: Add a `RegisterModule` call to each constructor**

In `internal/runtime/bundled_plugin_tools.go`, add to `newBundledPluginTool` (after the existing `&bundledPluginTool{...}` block returns; the cleanest spot is just before `return ...`):

```go
bundledplugins.RegisterModule(native.Name(), native.Name(), bundledToolCapabilities(native.Name()))
```

In `newBundledStaticTool` (just before `return ...`):

```go
bundledplugins.RegisterModule(name, name, caps)
```

In `newBundledWasmTool` (just before `return &renamedTool{...}`):

```go
bundledplugins.RegisterModule(wasmName, registeredName, caps)
```

If `bundledplugins` is already imported at the top of the file, no import change. Verify with: `grep "bundledplugins" <repo-root>/internal/runtime/bundled_plugin_tools.go | head -3`.

- [ ] **Step 2.3: Self-register the auto-compact background plugin**

Modify `internal/bundledplugins/auto_compact.go` — append at the end of the file, after `autoCompactSchema()`:

```go
func init() {
	RegisterModule(autoCompactID, "compact",
		[]string{"session:observe", "session:read", "session:fork", "llm:invoke:30000"})
}
```

This makes the auto-compact module visible in `List()` even though no `internal/runtime/bundled_plugin_tools.go` constructor registers it.

- [ ] **Step 2.4: Add the smoke test**

Append to `internal/bundledplugins/list_test.go`:

```go
// TestList_RealRegistrations: when the runtime package is imported
// (via the runtime side-effect of the test binary linking), real
// registrations should appear in List().
//
// We don't import internal/runtime here directly (would cycle); we
// rely on internal/runtime/bundled_plugin_tools.go's package init
// being triggered transitively when this test binary builds. To
// keep the test deterministic without that linkage, we verify the
// auto_compact.go init registration that lives in this package.
func TestList_AutoCompactRegistered(t *testing.T) {
	// No reset — we want to observe the package-init registrations.
	got := List()
	found := false
	for _, info := range got {
		if info.Name == autoCompactID {
			found = true
			if !contains(info.Tools, "compact") {
				t.Errorf("auto-compact module should expose 'compact' tool; got %v", info.Tools)
			}
			if !contains(info.Capabilities, "llm:invoke:30000") {
				t.Errorf("auto-compact module should expose llm:invoke:30000; got %v", info.Capabilities)
			}
		}
	}
	if !found {
		t.Error("auto-compact module not present in List()")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2.5: Run focused tests**

Run: `cd <repo-root> && go test ./internal/bundledplugins/ -count=1 -v`
Expected: all PASS, including `TestList_AutoCompactRegistered`.

- [ ] **Step 2.6: Run full repo to verify the runtime-side wiring compiles**

Run: `cd <repo-root> && go build ./... && go test ./internal/runtime/ -count=1`
Expected: PASS.

- [ ] **Step 2.7: Sanity-print real registrations**

Add a tiny scratch test (delete in a moment) to verify the runtime registrations actually land:

```go
// In internal/bundledplugins/list_test.go, append:
func TestList_DebugDump(t *testing.T) {
	if testing.Short() {
		t.Skip("dump test")
	}
	got := List()
	for _, info := range got {
		t.Logf("module %q: %d tools, %d caps", info.Name, len(info.Tools), len(info.Capabilities))
	}
}
```

Run: `cd <repo-root> && go test ./internal/bundledplugins/ -run TestList_DebugDump -count=1 -v`
Expected: only the auto-compact module appears (because the runtime package isn't imported by this test binary). That's expected — full registration count comes through when other packages import runtime. **Delete the `TestList_DebugDump` test before commit** — it's transient.

- [ ] **Step 2.8: Commit**

```bash
cd <repo-root>
git add internal/runtime/bundled_plugin_tools.go internal/bundledplugins/auto_compact.go internal/bundledplugins/list_test.go
git commit -m "feat(runtime): wire bundled-tool registrations into bundledplugins.List

Each bundled-tool constructor now calls RegisterModule with the
wasm-module name, the registered tool name, and the caps slice.
auto-compact self-registers at package init. List() now reflects
the actual binary-bundled set when the runtime package is linked
into the test binary."
```

---

## Task 3: Extract shared invoke helper

**Files:**
- Create: `cmd/stado/plugin_invoke_shared.go`
- Modify: `cmd/stado/plugin_run.go` (call into the shared helper; preserves existing behavior)

**Context:** The body of `pluginRunCmd.RunE` mixes "load + verify a plugin" with "instantiate wasm + run a tool". Task 4 needs the second half (instantiate + run) for `tool run`. This task extracts the shared body without behavior change.

- [ ] **Step 3.1: Read the existing `pluginRunCmd.RunE` body**

`cd <repo-root> && sed -n '38,215p' cmd/stado/plugin_run.go`

Note the steps in order:
1. Load config
2. Parse args
3. Resolve install dir
4. Load + verify manifest
5. Resolve workdir
6. Build host
7. Detect sandbox
8. Refuse-no-runner check
9. Build runtime
10. Memory bridge
11. Tool host
12. Session bridge
13. Install host imports
14. Instantiate wasm
15. Look up tool in manifest
16. Build PluginTool
17. Run
18. Print result

The split point: steps 1-4 are "load" (becomes `pluginRunCmd`'s RunE OR `tool_run.go`'s lookup). Steps 5-18 are "invoke" (becomes the shared helper).

- [ ] **Step 3.2: Create the shared helper**

Create `<repo-root>/cmd/stado/plugin_invoke_shared.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
)

// pluginInvokeArgs is the input to runPluginInvocation. The caller
// (pluginRunCmd or tool_run.go) is responsible for loading + verifying
// the manifest and the wasm bytes; this helper handles the wasm
// instantiation, host-import wiring, and tool dispatch.
type pluginInvokeArgs struct {
	Manifest    plugins.Manifest // already loaded + verified by the caller
	WasmBytes   []byte           // already verified against Manifest.WASMSHA256
	ToolName    string           // tool def name (matches Manifest.Tools[i].Name)
	ArgsJSON    string           // JSON args; "{}" when omitted
	Cfg         *config.Config
	WorkdirArg  string           // raw --workdir arg ("" = default to install dir)
	InstallDir  string           // for default workdir + caller logging
	SessionID   string           // raw --session arg ("" = no session)
	Stdout      io.Writer        // typically cmd.OutOrStdout()
	Stderr      io.Writer        // typically cmd.ErrOrStderr()
}

// runPluginInvocation is the shared body that both pluginRunCmd and
// tool_run use. Returns nil on success; an error on any failure.
// Prints res.Content to Stdout on success, res.Error to Stderr on a
// plugin-reported error.
func runPluginInvocation(ctx context.Context, in pluginInvokeArgs) error {
	cfg := in.Cfg

	// Resolve workdir: default to install dir; --workdir overrides.
	workdir := in.InstallDir
	if in.WorkdirArg != "" {
		abs, err := filepath.Abs(in.WorkdirArg)
		if err != nil {
			return fmt.Errorf("--workdir %q: %w", in.WorkdirArg, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("--workdir %q: %w", in.WorkdirArg, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("--workdir %q: not a directory", in.WorkdirArg)
		}
		workdir = abs
	}

	host := pluginRuntime.NewHost(in.Manifest, workdir, nil)
	host.StateDir = cfg.StateDir()

	runner := sandbox.Detect()
	if host.ExecBash && !host.ExecProc && runner.Name() == "none" {
		if cfg.Sandbox.RefuseNoRunner {
			return fmt.Errorf(
				"plugin run: plugin %s declares exec:bash but no native sandbox runner is available on this host. Install bubblewrap or sandbox-exec, or set [sandbox] refuse_no_runner = false to run unsandboxed",
				in.Manifest.Name)
		}
		fmt.Fprintf(in.Stderr,
			"stado: warn: plugin %s declares exec:bash but no native sandbox runner is available — running unsandboxed.\n",
			in.Manifest.Name)
	}

	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	attachPluginMemoryBridge(cfg, host, in.Manifest.Name)
	host.ToolHost = newPluginRunToolHost(workdir, runner, host.NetHTTPRequestPrivate)

	if host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0 {
		if in.SessionID != "" {
			bridge, note, err := buildPluginRunBridge(ctx, cfg, in.SessionID, in.Manifest.Name, host.LLMInvokeBudget > 0)
			if err != nil {
				return err
			}
			host.SessionBridge = bridge
			if note != "" {
				fmt.Fprintln(in.Stderr, note)
			}
		} else {
			bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
			bridge.PluginName = in.Manifest.Name
			host.SessionBridge = bridge
			fmt.Fprintln(in.Stderr,
				"stado: session-aware capabilities declared; pass --session <id> to attach to a persisted session")
		}
	}

	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return fmt.Errorf("host imports: %w", err)
	}
	mod, err := rt.Instantiate(ctx, in.WasmBytes, in.Manifest)
	if err != nil {
		return fmt.Errorf("instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	var tdef *plugins.ToolDef
	for i := range in.Manifest.Tools {
		if in.Manifest.Tools[i].Name == in.ToolName {
			tdef = &in.Manifest.Tools[i]
			break
		}
	}
	if tdef == nil {
		return fmt.Errorf("tool %q not declared in plugin manifest %q", in.ToolName, in.Manifest.Name)
	}
	pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
	if err != nil {
		return err
	}
	res, err := pt.Run(ctx, []byte(in.ArgsJSON), nil)
	if err != nil {
		if res.Error != "" {
			fmt.Fprintln(in.Stderr, res.Error)
		}
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("plugin error: %s", res.Error)
	}
	fmt.Fprintln(in.Stdout, res.Content)
	return nil
}
```

- [ ] **Step 3.3: Refactor `pluginRunCmd.RunE` to call the shared helper**

Modify `cmd/stado/plugin_run.go`. Replace the full `pluginRunCmd.RunE` body (lines 39-214 of the existing file) with a thin caller that loads config, resolves the install dir, loads + verifies the manifest, then calls `runPluginInvocation`:

```go
var pluginRunCmd = &cobra.Command{
	Use:   "run <name>-<version> <tool> [json-args]",
	Short: "Run a single tool exported by an installed plugin",
	Long: "Loads the plugin from $XDG_DATA_HOME/stado/plugins/<name>-<version>/,\n" +
		"instantiates the wasm module in a wazero sandbox bound by the\n" +
		"manifest's declared capabilities, then invokes the named tool\n" +
		"with the supplied JSON args (default: empty object).\n\n" +
		"Primarily for local plugin authoring. Pass --session <id> to bind\n" +
		"the run to a persisted session so session-aware capabilities like\n" +
		"session:read, session:fork, and llm:invoke work on the CLI too.",
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		toolName := args[1]
		argsJSON := "{}"
		if len(args) >= 3 {
			argsJSON = args[2]
		}
		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return err
		}
		dir, err := plugins.InstalledDir(filepath.Join(cfg.StateDir(), "plugins"), args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("plugin %s not installed (run `stado plugin install <plugin-dir>` after building + signing it)", args[0])
		}
		m, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			return err
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		wasmBytes, err := plugins.ReadVerifiedWASM(m.WASMSHA256, wasmPath)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(m, sig); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("run: %w", err)
			}
		}
		if pluginRunWithToolHost {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"stado: warning: --with-tool-host is deprecated (EP-0038); ToolHost is now wired by default. Flag will be removed in a future release.\n")
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runPluginInvocation(ctx, pluginInvokeArgs{
			Manifest:   *m,
			WasmBytes:  wasmBytes,
			ToolName:   toolName,
			ArgsJSON:   argsJSON,
			Cfg:        cfg,
			WorkdirArg: pluginRunWorkdir,
			InstallDir: dir,
			SessionID:  pluginRunSession,
			Stdout:     cmd.OutOrStdout(),
			Stderr:     cmd.ErrOrStderr(),
		})
	},
}
```

The unused `_ = sig` is implicit in the `LoadFromDir` return — no change needed if the linter is unhappy with `sig`, the existing code uses it via `ts.VerifyManifest(m, sig)`.

Keep the existing `init()` block with its flag registrations as-is; keep `attachPluginMemoryBridge`, `buildPluginRunBridge`, etc. — they're called by the shared helper now.

- [ ] **Step 3.4: Run all tests in cmd/stado**

Run: `cd <repo-root> && go test ./cmd/stado/ -count=1`
Expected: PASS. The existing `pluginRun*` tests should pass unchanged because behavior is preserved.

- [ ] **Step 3.5: Run go vet**

Run: `cd <repo-root> && go vet ./cmd/stado/`
Expected: clean.

- [ ] **Step 3.6: Commit**

```bash
cd <repo-root>
git add cmd/stado/plugin_run.go cmd/stado/plugin_invoke_shared.go
git commit -m "refactor(cli): extract runPluginInvocation from pluginRunCmd

Shared body for the wasm instantiation + host wiring + tool
dispatch path. pluginRunCmd now loads/verifies a manifest from
disk, then calls runPluginInvocation. tool_run.go (next task)
will load via bundledplugins/registry and call the same helper."
```

---

## Task 4: `stado tool run` happy path

**Files:**
- Create: `cmd/stado/tool_run.go`
- Create: `cmd/stado/tool_run_test.go`
- Modify: `cmd/stado/tool.go` (register `toolRunCmd` under `toolCmd`)

**Context:** The new subcommand looks up a tool in the live registry (handling canonical/wire/metadata fallbacks), determines whether it's bundled or installed, prepares the manifest + wasm bytes, and dispatches via `runPluginInvocation`.

This task implements the happy path only. Disabled-tool refusal lands in Task 5.

- [ ] **Step 4.1: Write failing test**

Create `<repo-root>/cmd/stado/tool_run_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestToolRun_ResolvesByCanonicalForm confirms that `tool run fs.read`
// finds the bundled fs.read tool by canonical-dotted form.
func TestToolRun_ResolvesByCanonicalForm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	tmp := t.TempDir()
	target := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"fs.read",
		`{"path":"`+target+`"}`,
		toolRunOptions{
			Cfg:     cfg,
			Workdir: tmp,
			Stdout:  &stdout,
			Stderr:  &stderr,
		},
	)
	if err != nil {
		t.Fatalf("runToolByName: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello world") {
		t.Errorf("expected 'hello world' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_ResolvesByWireForm confirms wire-form input also works.
func TestToolRun_ResolvesByWireForm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	tmp := t.TempDir()
	target := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"read", // bare native fs.read name
		`{"path":"`+target+`"}`,
		toolRunOptions{
			Cfg:     cfg,
			Workdir: tmp,
			Stdout:  &stdout,
			Stderr:  &stderr,
		},
	)
	if err != nil {
		t.Fatalf("runToolByName: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hi") {
		t.Errorf("expected 'hi' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_ToolNotFound reports a clear error message that
// references stado tool list.
func TestToolRun_ToolNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"nope.foo",
		`{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr},
	)
	if err == nil {
		t.Fatal("expected error for unknown tool; got nil")
	}
	if !strings.Contains(err.Error(), "stado tool list") {
		t.Errorf("error message should reference 'stado tool list'; got: %q", err.Error())
	}
}
```

- [ ] **Step 4.2: Run the test, verify it fails to compile**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestToolRun_" -count=1`
Expected: build failure with `undefined: runToolByName`, `undefined: toolRunOptions`.

- [ ] **Step 4.3: Implement `cmd/stado/tool_run.go`**

Create `<repo-root>/cmd/stado/tool_run.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

var (
	toolRunSession string
	toolRunWorkdir string
	toolRunForce   bool
)

var toolRunCmd = &cobra.Command{
	Use:   "run <name> [json-args]",
	Short: "Run a single tool by canonical (fs.read) or wire (fs__read) name",
	Long: "Looks up the named tool in the live registry — bundled and\n" +
		"installed alike — and invokes it via the wasm runtime under the\n" +
		"manifest's declared capabilities. Accepts both canonical (fs.read)\n" +
		"and wire (fs__read) forms.\n\n" +
		"Bundled tools (fs.*, shell.*, agent.*, etc.) are dispatched from\n" +
		"the binary-embedded wasm; installed plugins are dispatched from\n" +
		"$XDG_DATA_HOME/stado/plugins/. Tools listed in [tools].disabled\n" +
		"are refused unless --force is passed.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		argsJSON := "{}"
		if len(args) >= 2 {
			argsJSON = args[1]
		}
		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runToolByName(ctx, args[0], argsJSON, toolRunOptions{
			Cfg:     cfg,
			Workdir: toolRunWorkdir,
			Session: toolRunSession,
			Force:   toolRunForce,
			Stdout:  cmd.OutOrStdout(),
			Stderr:  cmd.ErrOrStderr(),
		})
	},
}

type toolRunOptions struct {
	Cfg     *config.Config
	Workdir string // override workdir; "" = use bundled-plugin install dir or cwd
	Session string
	Force   bool
	Stdout  io.Writer
	Stderr  io.Writer
}

// runToolByName is the testable entry point. Resolves name → registered
// tool, determines bundled vs installed, prepares Manifest + WASM,
// dispatches via runPluginInvocation.
func runToolByName(ctx context.Context, name, argsJSON string, opts toolRunOptions) error {
	cfg := opts.Cfg
	reg := runtime.BuildDefaultRegistry()
	runtime.ApplyToolFilter(reg, cfg)

	registered, ok := lookupToolInRegistry(reg, name)
	if !ok {
		return fmt.Errorf("tool %q not found — try `stado tool list` to see available tools", name)
	}

	// Bundled vs installed.
	info, isBundled := bundledplugins.LookupModuleByToolName(registered.Name())
	if !isBundled {
		return fmt.Errorf("tool %q is not a bundled tool — installed-plugin invocation by tool-name is not yet supported (use `stado tool list` to see what's available)", registered.Name())
	}

	// Reconstruct synthetic manifest for the bundled module.
	pluginName := bundledplugins.ManifestNamePrefix + "-" + info.Name
	manifest := plugins.Manifest{
		Name:         pluginName,
		Version:      info.Version,
		Author:       info.Author,
		Capabilities: info.Capabilities,
		Tools:        []plugins.ToolDef{toolDefFromRegistered(registered)},
	}
	wasmBytes, err := bundledplugins.Wasm(info.Name)
	if err != nil {
		return fmt.Errorf("bundled wasm load: %w", err)
	}

	// The bundled bare tool name (matches manifest.Tools[0].Name) is the
	// trailing segment after "fs__" / etc. — see toolDefFromRegistered.
	bareToolName := manifest.Tools[0].Name

	// Workdir default: cwd when bundled (bundled plugins don't have a
	// natural install dir; cwd is the operator's repo).
	installDir, _ := os.Getwd()

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	return runPluginInvocation(ctx, pluginInvokeArgs{
		Manifest:   manifest,
		WasmBytes:  wasmBytes,
		ToolName:   bareToolName,
		ArgsJSON:   argsJSON,
		Cfg:        cfg,
		WorkdirArg: opts.Workdir,
		InstallDir: installDir,
		SessionID:  opts.Session,
		Stdout:     stdout,
		Stderr:     stderr,
	})
}

// lookupToolInRegistry tries (in order): exact name match, canonical
// → wire conversion, canonical-metadata fallback. Mirrors the lookup
// pattern in `stado tool info`.
func lookupToolInRegistry(reg *tools.Registry, query string) (pkgtool.Tool, bool) {
	if t, ok := reg.Get(query); ok {
		return t, true
	}
	if dot := strings.Index(query, "."); dot > 0 && dot < len(query)-1 {
		if wire, err := tools.WireForm(query[:dot], query[dot+1:]); err == nil {
			if t, ok := reg.Get(wire); ok {
				return t, true
			}
		}
	}
	for _, candidate := range reg.All() {
		if runtime.LookupToolMetadata(candidate.Name()).Canonical == query {
			return candidate, true
		}
	}
	return nil, false
}

// toolDefFromRegistered builds a plugins.ToolDef from a registered
// tool. The Name field uses the bare suffix from the wire-form name
// (e.g. fs__read → "read") because the wasm dispatcher in
// internal/plugins/runtime/tool.go prepends "stado_tool_" to def.Name
// to resolve the export.
func toolDefFromRegistered(t pkgtool.Tool) plugins.ToolDef {
	registered := t.Name()
	bare := registered
	if alias, sub, ok := tools.ParseWireForm(registered); ok && alias != "" {
		bare = sub
	}
	schemaJSON := mustMarshalToolSchema(t)
	return plugins.ToolDef{
		Name:        bare,
		Description: t.Description(),
		Schema:      schemaJSON,
	}
}

// mustMarshalToolSchema serializes the schema to JSON, falling back to
// a minimal stub on error (the wasm dispatcher tolerates that).
func mustMarshalToolSchema(t pkgtool.Tool) string {
	// Reuse the same helper as bundled_plugin_tools.go via a local
	// json.Marshal — keep this file independent of internal/runtime
	// internals.
	// schema is map[string]any; encoding/json handles it natively.
	// Fallback to "{}" if marshalling somehow fails.
	if t == nil {
		return `{"type":"object"}`
	}
	// inline import-free marshal to keep the function focused.
	return marshalToolSchemaInline(t)
}

// marshalToolSchemaInline is a helper to keep `mustMarshalToolSchema`
// short. Returns "{}" if marshalling fails.
func marshalToolSchemaInline(t pkgtool.Tool) string {
	// implementation inline to keep the file self-contained
	return marshalSchemaJSON(t.Schema())
}

func init() {
	toolRunCmd.Flags().StringVar(&toolRunSession, "session", "",
		"Bind the tool run to a persisted session ID so session-aware capabilities work on the CLI")
	_ = toolRunCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	toolRunCmd.Flags().StringVar(&toolRunWorkdir, "workdir", "",
		"Override the tool's Workdir (default: cwd for bundled tools, install dir for installed plugins)")
	toolRunCmd.Flags().BoolVar(&toolRunForce, "force", false,
		"Run even if the tool is disabled in [tools].disabled")
	_ = filepath.Join // keep import live until session bridge plumbing pulls it
}
```

The `marshalSchemaJSON` helper isn't yet defined; add it below the `init` block:

```go
// marshalSchemaJSON serializes a schema map as JSON. Returns "{}" on
// error so the wasm dispatcher receives a parseable empty schema.
func marshalSchemaJSON(schema map[string]any) string {
	if schema == nil {
		return `{"type":"object"}`
	}
	b, err := jsonMarshal(schema)
	if err != nil {
		return `{"type":"object"}`
	}
	return string(b)
}

// jsonMarshal indirection so the linter doesn't flag the import dead.
var jsonMarshal = func(v any) ([]byte, error) {
	return jsonStdLibMarshal(v)
}
```

Replace `jsonStdLibMarshal` and the inline helpers with a clean direct use of `encoding/json`. The simpler version that lives entirely above `init()`:

```go
import (
	"encoding/json"
	// ... other imports
)

// marshalSchemaJSON serializes a schema map as JSON. Returns "{}" on
// error so the wasm dispatcher receives a parseable empty schema.
func marshalSchemaJSON(schema map[string]any) string {
	if schema == nil {
		return `{"type":"object"}`
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return `{"type":"object"}`
	}
	return string(b)
}
```

And simplify `mustMarshalToolSchema` to:

```go
func mustMarshalToolSchema(t pkgtool.Tool) string {
	if t == nil {
		return `{"type":"object"}`
	}
	return marshalSchemaJSON(t.Schema())
}
```

Drop the `marshalToolSchemaInline` and `jsonMarshal` indirections. Final imports for `tool_run.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)
```

Drop the `path/filepath` import if not used.

- [ ] **Step 4.4: Register `toolRunCmd` under `toolCmd`**

Modify `cmd/stado/tool.go`. Find the `init()` function (last function in the file). Add `toolRunCmd` to the `AddCommand` call:

```go
toolCmd.AddCommand(toolListCmd, toolInfoCmd, toolCatsCmd, toolReloadCmd,
	toolEnableCmd, toolDisableCmd, toolAutoloadCmd, toolUnautoloadCmd,
	toolRunCmd)
```

- [ ] **Step 4.5: Run focused tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestToolRun_" -count=1 -v`
Expected: 3 PASS.

- [ ] **Step 4.6: Run full cmd/stado tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -count=1`
Expected: PASS.

- [ ] **Step 4.7: Run go vet**

Run: `cd <repo-root> && go vet ./cmd/stado/`
Expected: clean.

- [ ] **Step 4.8: Commit**

```bash
cd <repo-root>
git add cmd/stado/tool_run.go cmd/stado/tool_run_test.go cmd/stado/tool.go
git commit -m "feat(cli): stado tool run <name> for bundled tools

New subcommand that resolves a tool by canonical (fs.read) or wire
(fs__read) form, looks up the bundled module via
bundledplugins.LookupModuleByToolName, reconstructs a synthetic
manifest, and dispatches via runPluginInvocation.

Installed-plugin invocation by tool-name is not yet wired (errors
with a clear message); for installed plugins, use stado plugin run
until a follow-up. The disabled-tool refusal + --force lands in
the next task."
```

---

## Task 5: `tool run` disabled refusal + `--force`

**Files:**
- Modify: `cmd/stado/tool_run.go`
- Modify: `cmd/stado/tool_run_test.go`

**Context:** Operators expect `[tools] disabled` to apply to CLI invocation too — disabling a tool should refuse it via `tool run` unless `--force` is passed.

- [ ] **Step 5.1: Write failing tests**

Append to `cmd/stado/tool_run_test.go`:

```go
// TestToolRun_DisabledRefuses: a tool listed in cfg.Tools.Disabled
// is refused unless --force is passed.
func TestToolRun_DisabledRefuses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"read"}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "read", `{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("expected error for disabled tool; got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should mention 'disabled'; got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should hint at --force; got: %q", err.Error())
	}
}

// TestToolRun_DisabledForceOverrides: --force runs the tool anyway.
func TestToolRun_DisabledForceOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"read"}

	tmp := t.TempDir()
	target := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(target, []byte("forced"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "read",
		`{"path":"`+target+`"}`,
		toolRunOptions{Cfg: cfg, Workdir: tmp, Force: true, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("with --force, expected success; got: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "forced") {
		t.Errorf("expected 'forced' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_DisabledByGlob: glob in [tools].disabled also refuses.
func TestToolRun_DisabledByGlob(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"fs.*"}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "fs.read", `{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("expected error for tool matching disabled glob; got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should mention 'disabled'; got: %q", err.Error())
	}
}
```

- [ ] **Step 5.2: Run tests, verify FAIL**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestToolRun_Disabled" -count=1 -v`
Expected: 3 FAIL — there's no disabled check in `runToolByName` yet.

- [ ] **Step 5.3: Add the disabled check**

In `cmd/stado/tool_run.go`, modify `runToolByName`. After the `lookupToolInRegistry` call, before the bundled lookup, insert:

```go
	if !opts.Force {
		registeredName := registered.Name()
		canonical := runtime.LookupToolMetadata(registeredName).Canonical
		for _, pat := range cfg.Tools.Disabled {
			if runtime.ToolMatchesGlob(registeredName, pat) ||
				(canonical != "" && runtime.ToolMatchesGlob(canonical, pat)) {
				return fmt.Errorf("tool %q is disabled in [tools].disabled (matched pattern %q); remove it from disabled, or re-run with --force",
					name, pat)
			}
		}
	}
```

- [ ] **Step 5.4: Run tests, verify PASS**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestToolRun_" -count=1 -v`
Expected: all 6 tests (3 happy + 3 disabled) PASS.

- [ ] **Step 5.5: Run full cmd/stado tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -count=1`
Expected: PASS.

- [ ] **Step 5.6: Commit**

```bash
cd <repo-root>
git add cmd/stado/tool_run.go cmd/stado/tool_run_test.go
git commit -m "feat(cli): tool run honours [tools].disabled with --force escape

A tool whose registered or canonical name matches any pattern in
cfg.Tools.Disabled is refused with an actionable error message
mentioning --force and the matched pattern. --force bypasses the
check for one-off invocation."
```

---

## Task 6: `plugin list` shows bundled rows

**Files:**
- Modify: `cmd/stado/plugin_trust.go` (`pluginListCmd`)
- Modify: `cmd/stado/plugin_trust_test.go` (or whichever test file owns `pluginListCmd` — if absent, create `cmd/stado/plugin_list_test.go`)

**Context:** Today `pluginListCmd` enumerates only disk-installed plugins. Extend it to render bundled rows from `bundledplugins.List()`.

- [ ] **Step 6.1: Read the existing renderer**

`cd <repo-root> && sed -n '75,180p' cmd/stado/plugin_trust.go`

Note the `row` struct (lines 95-104), the population loop (107-133), the summary line (151-156), and the output format (159-172).

- [ ] **Step 6.2: Locate or create the test file**

`cd <repo-root> && ls cmd/stado/plugin_trust_test.go cmd/stado/plugin_list_test.go 2>/dev/null`

If neither exists, create `cmd/stado/plugin_list_test.go`. If one exists, append to it.

- [ ] **Step 6.3: Write failing test**

Create `cmd/stado/plugin_list_test.go` (or append to the existing test file):

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPluginList_ShowsBundled: with no installed plugins, the list
// still shows bundled ones with the ✓ bundled status.
func TestPluginList_ShowsBundled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var stdout bytes.Buffer
	pluginListCmd.SetOut(&stdout)
	pluginListCmd.SetErr(&stdout)
	defer func() {
		pluginListCmd.SetOut(nil)
		pluginListCmd.SetErr(nil)
	}()

	if err := pluginListCmd.RunE(pluginListCmd, nil); err != nil {
		t.Fatalf("pluginListCmd.RunE: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "bundled") {
		t.Errorf("output should mention 'bundled' status; got:\n%s", out)
	}
	// The auto-compact module is always registered (init() in
	// internal/bundledplugins/auto_compact.go).
	if !strings.Contains(out, "auto-compact") {
		t.Errorf("output should list 'auto-compact' bundled module; got:\n%s", out)
	}
}

// TestPluginList_BundledHasDashFingerprint: bundled rows render the
// fingerprint column as "-" rather than empty/truncated noise.
func TestPluginList_BundledHasDashFingerprint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var stdout bytes.Buffer
	pluginListCmd.SetOut(&stdout)
	pluginListCmd.SetErr(&stdout)
	defer func() {
		pluginListCmd.SetOut(nil)
		pluginListCmd.SetErr(nil)
	}()

	if err := pluginListCmd.RunE(pluginListCmd, nil); err != nil {
		t.Fatalf("pluginListCmd.RunE: %v", err)
	}
	out := stdout.String()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "bundled") {
			continue
		}
		// Bundled rows should show "-" in the fingerprint position;
		// the column delimiter in tabwriter output is whitespace, so
		// looking for "  -  " (with surrounding whitespace) is robust.
		if !strings.Contains(line, " -  ") && !strings.Contains(line, "\t-\t") {
			t.Errorf("bundled row should show '-' fingerprint; got: %q", line)
		}
	}
}
```

- [ ] **Step 6.4: Run tests, verify FAIL**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestPluginList_" -count=1 -v`
Expected: FAIL — current output contains neither "bundled" nor a `-` fingerprint.

- [ ] **Step 6.5: Extend the `row` struct + populate from `bundledplugins.List()`**

In `cmd/stado/plugin_trust.go`, add `bundled bool` to the `row` struct:

```go
type row struct {
	name        string
	version     string
	tools       int
	toolNames   string
	author      string
	fingerprint string
	trusted     bool
	bundled     bool // NEW
	caps        int
}
```

Add `"github.com/foobarto/stado/internal/bundledplugins"` to the imports if not already present.

Inside `pluginListCmd.RunE`, after the existing `for _, id := range ids` loop (around line 133), append:

```go
	// Also enumerate bundled plugins.
	for _, b := range bundledplugins.List() {
		toolsList := strings.Join(b.Tools, ", ")
		if len(toolsList) > 40 {
			toolsList = toolsList[:37] + "..."
		}
		rows = append(rows, row{
			name:        b.Name,
			version:     b.Version,
			tools:       len(b.Tools),
			toolNames:   toolsList,
			author:      b.Author,
			fingerprint: "",
			trusted:     true,
			bundled:     true,
			caps:        len(b.Capabilities),
		})
	}
```

- [ ] **Step 6.6: Update the summary + status rendering**

Replace the summary block (lines 143-157) and status rendering (lines 161-172) to handle bundled rows:

```go
	bundledCount, installedCount, trustedCount := 0, 0, 0
	for _, r := range rows {
		switch {
		case r.bundled:
			bundledCount++
			trustedCount++
		case r.trusted:
			installedCount++
			trustedCount++
		default:
			installedCount++
		}
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if bundledCount > 0 && installedCount > 0 {
		fmt.Fprintf(w, "%d plugins (%d bundled, %d installed", len(rows), bundledCount, installedCount)
	} else if bundledCount > 0 {
		fmt.Fprintf(w, "%d plugins (%d bundled", len(rows), bundledCount)
	} else {
		fmt.Fprintf(w, "%d plugins (%d installed", len(rows), installedCount)
	}
	if trustedCount < len(rows) {
		fmt.Fprintf(w, "; %d trusted, %d untrusted)", trustedCount, len(rows)-trustedCount)
	} else {
		fmt.Fprintf(w, "; all trusted)")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "NAME\tVERSION\tTOOLS\tAUTHOR\tFINGERPRINT\tSTATUS")
	fmt.Fprintln(w, "────\t───────\t─────\t──────\t───────────\t──────")
	for _, r := range rows {
		status := "✓ trusted"
		switch {
		case r.bundled:
			status = "✓ bundled"
		case !r.trusted:
			status = "⚠ untrusted"
		}
		fpr := r.fingerprint
		if r.bundled {
			fpr = "-"
		} else if len(fpr) > 16 {
			fpr = fpr[:16]
		}
		fmt.Fprintf(w, "%s\tv%s\t%d\t%s\t%s\t%s\n",
			r.name, r.version, r.tools, r.author, fpr, status)
	}
```

- [ ] **Step 6.7: Run tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestPluginList_" -count=1 -v`
Expected: 2 PASS.

- [ ] **Step 6.8: Run full cmd/stado tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -count=1`
Expected: PASS. Existing `pluginList`-related tests should still pass — the format additions are purely additive.

- [ ] **Step 6.9: Commit**

```bash
cd <repo-root>
git add cmd/stado/plugin_trust.go cmd/stado/plugin_list_test.go
git commit -m "feat(cli): plugin list shows bundled plugins

Extends pluginListCmd to enumerate bundledplugins.List() alongside
disk-installed plugins. Bundled rows render with status '✓ bundled'
and fingerprint '-'. Summary line splits the count: '15 plugins
(12 bundled, 3 installed; all trusted)'."
```

---

## Task 7: `plugin info` looks up bundled by name

**Files:**
- Modify: `cmd/stado/plugin_info.go`
- Modify: `cmd/stado/plugin_info_test.go`

**Context:** Symmetric with `plugin list` — once an operator sees `fs` in the list, `stado plugin info fs` should print its tools.

- [ ] **Step 7.1: Write failing test**

Append to `cmd/stado/plugin_info_test.go` (or create if absent):

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPluginInfo_BundledLookup: stado plugin info <bundled-name>
// finds the bundled module and prints its tools.
func TestPluginInfo_BundledLookup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var stdout bytes.Buffer
	pluginInfoCmd.SetOut(&stdout)
	pluginInfoCmd.SetErr(&stdout)
	defer func() {
		pluginInfoCmd.SetOut(nil)
		pluginInfoCmd.SetErr(nil)
	}()

	if err := pluginInfoCmd.RunE(pluginInfoCmd, []string{"auto-compact"}); err != nil {
		t.Fatalf("pluginInfoCmd.RunE: %v\nstdout: %s", err, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "compact") {
		t.Errorf("output should mention the 'compact' tool; got:\n%s", out)
	}
	if !strings.Contains(out, "stado") {
		t.Errorf("output should show author 'stado'; got:\n%s", out)
	}
}
```

- [ ] **Step 7.2: Run test, verify FAIL**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestPluginInfo_BundledLookup" -count=1 -v`
Expected: FAIL — `auto-compact` isn't installed on disk; the existing `plugins.InstalledDir` lookup errors with "plugin not installed".

- [ ] **Step 7.3: Add bundled-first lookup to `pluginInfoCmd.RunE`**

In `cmd/stado/plugin_info.go`, modify `pluginInfoCmd.RunE`. After `cfg, err := config.Load()` (line 27-30) and before the existing `pluginsDir := filepath.Join(...)` line, insert:

```go
	if info, _, ok := bundledplugins.LookupByName(args[0]); ok {
		// Reconstruct synthetic manifest for printing.
		mf := plugins.Manifest{
			Name:         bundledplugins.ManifestNamePrefix + "-" + info.Name,
			Version:      info.Version,
			Author:       info.Author,
			Capabilities: info.Capabilities,
			Tools:        bundledToolDefsFromList(info),
		}
		return printManifestInfo(cmd.OutOrStdout(), mf, info.Name, true)
	}
```

Add the imports: `"github.com/foobarto/stado/internal/bundledplugins"` and ensure `"github.com/foobarto/stado/internal/plugins"` is present.

Add the helpers below the `pluginInfoCmd` definition (or in an appropriate spot):

```go
// bundledToolDefsFromList synthesises minimal ToolDef entries from a
// bundledplugins.Info. Schema/description aren't tracked at the Info
// level; the resulting ToolDefs carry just the tool name. Operators
// reading `plugin info` for a bundled module should also use
// `stado tool info <toolname>` for full schema detail.
func bundledToolDefsFromList(info bundledplugins.Info) []plugins.ToolDef {
	out := make([]plugins.ToolDef, 0, len(info.Tools))
	for _, name := range info.Tools {
		out = append(out, plugins.ToolDef{
			Name:        name,
			Description: "(bundled — use `stado tool info " + name + "` for full schema)",
		})
	}
	return out
}

// printManifestInfo renders the manifest details. Refactored from the
// inline body of pluginInfoCmd.RunE to allow reuse from the bundled
// path. When bundled is true, certain fields (fingerprint, wasm
// sha256) are omitted with sentinel values.
func printManifestInfo(o io.Writer, mf plugins.Manifest, displayID string, bundled bool) error {
	header := fmt.Sprintf("📦 %s  v%s", mf.Name, mf.Version)
	if bundled {
		header += "  (bundled)"
	}
	fmt.Fprintln(o, header)
	fmt.Fprintf(o, "   Author:       %s\n", mf.Author)
	if bundled {
		fmt.Fprintln(o, "   Fingerprint:  -  (built into stado binary)")
	} else {
		fmt.Fprintf(o, "   Fingerprint:  %s\n", mf.AuthorPubkeyFpr)
		fmt.Fprintf(o, "   Wasm SHA256:  %s\n", mf.WASMSHA256)
	}
	if mf.MinStadoVersion != "" {
		fmt.Fprintf(o, "   Requires:     stado >= %s\n", mf.MinStadoVersion)
	}
	fmt.Fprintln(o)

	fmt.Fprintf(o, "Capabilities (%d):\n", len(mf.Capabilities))
	for _, c := range mf.Capabilities {
		fmt.Fprintf(o, "  • %s\n", c)
	}
	fmt.Fprintln(o)

	fmt.Fprintf(o, "Tools (%d):\n", len(mf.Tools))
	for _, t := range mf.Tools {
		fmt.Fprintf(o, "  %-30s  %s\n", t.Name, truncateStr(t.Description, 80))
	}

	fmt.Fprintln(o)
	if bundled {
		fmt.Fprintf(o, "  stado tool info <toolname>   for individual schemas\n")
	} else {
		fmt.Fprintf(o, "  stado plugin info %s --json | jq '.tools[].name'\n", displayID)
	}
	return nil
}
```

Add `"io"` to imports.

The existing render code (lines 50-105 of `plugin_info.go`) becomes a single call to `printManifestInfo` from the disk-install path too — ensure the existing path still works:

```go
	mf, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if pluginInfoJSON {
		out, _ := json.MarshalIndent(mf, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
	return printManifestInfo(cmd.OutOrStdout(), *mf, args[0], false)
```

This keeps the JSON path and renames the long inline render block. Delete the previous inline render block.

- [ ] **Step 7.4: Run tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -run "TestPluginInfo_" -count=1 -v`
Expected: PASS for the new test. The existing `pluginInfo` tests (if any) should still pass — verify next.

- [ ] **Step 7.5: Run full cmd/stado tests**

Run: `cd <repo-root> && go test ./cmd/stado/ -count=1`
Expected: PASS.

- [ ] **Step 7.6: Commit**

```bash
cd <repo-root>
git add cmd/stado/plugin_info.go cmd/stado/plugin_info_test.go
git commit -m "feat(cli): plugin info finds bundled plugins by name

stado plugin info <bundled-name> now resolves through
bundledplugins.LookupByName before the disk-install lookup.
Bundled rows skip the fingerprint/wasm-sha256 lines (showing '-'
instead) and point operators at 'stado tool info <toolname>' for
full per-tool schema detail."
```

---

## Task 8: Remove `plugin run`, register `tool run`, document the change

**Files:**
- Delete: `cmd/stado/plugin_run.go`'s `pluginRunCmd` (the file becomes minimal — keep only the helpers `attachPluginMemoryBridge`, `buildPluginRunBridge`, `countPluginRunTokens` since `plugin_invoke_shared.go` uses them).
- Modify: `cmd/stado/plugin.go` (drop `pluginRunCmd` from `AddCommand`)
- Modify: `CHANGELOG.md` (Unreleased / Breaking changes)
- Modify: `docs/eps/0037-tool-dispatch-and-operator-surface.md` (history note)

**Context:** Pre-1.0 — drop the old subcommand cleanly.

- [ ] **Step 8.1: Delete `pluginRunCmd` and its flag-state vars**

In `cmd/stado/plugin_run.go`, delete:
- The `var pluginRunCmd = &cobra.Command{...}` block (the entire definition, formerly the `RunE` body now lives in `plugin_invoke_shared.go`).
- The package-level vars `pluginRunSession`, `pluginRunWorkdir`, `pluginRunWithToolHost`, `pluginRunBuildProvider` — but ONLY if they're not still referenced elsewhere. Check via:
  ```
  grep -n "pluginRunSession\|pluginRunWorkdir\|pluginRunWithToolHost\|pluginRunBuildProvider" cmd/stado/
  ```
- The `init()` block that registers the flags on `pluginRunCmd`.

Keep:
- `attachPluginMemoryBridge`, `buildPluginRunBridge`, `countPluginRunTokens` — these are used by `plugin_invoke_shared.go`.

If `pluginRunBuildProvider` is referenced by tests (it might be), leave it as a package-level var unmoved (it's the test-injection point for the provider builder).

- [ ] **Step 8.2: Drop `pluginRunCmd` from `pluginCmd.AddCommand`**

In `cmd/stado/plugin.go`, remove `pluginRunCmd` from the `AddCommand(...)` call (line 31-33). The new line:

```go
pluginCmd.AddCommand(pluginTrustCmd, pluginUntrustCmd, pluginListCmd, pluginInstalledCmd, pluginVerifyCmd,
	pluginDigestCmd, pluginInstallCmd, pluginGenKeyCmd, pluginSignCmd,
	pluginGCCmd, pluginDoctorCmd, pluginInfoCmd,
	// EP-0039: distribution and trust additions.
	pluginUseCmd, pluginDevCmd)
```

- [ ] **Step 8.3: Update CHANGELOG.md**

Find the existing `## Unreleased` section (added in the cleanup-batch PR). Append under its `### Breaking changes` subsection:

```markdown
- **CLI breaking** — `stado plugin run <plugin-id> <tool> [args]` removed. Use `stado tool run <name> [args]` instead — it resolves bundled and installed tools uniformly through the live registry. The new form accepts both canonical (`fs.read`) and wire (`fs__read`) names; `--session` and `--workdir` flags carry over identically; `--force` overrides `[tools].disabled` for one-off invocation.
```

- [ ] **Step 8.4: Update EP-0037 history**

Append to the `history:` block in `docs/eps/0037-tool-dispatch-and-operator-surface.md`:

```yaml
  - date: 2026-05-05
    status: Implemented
    note: >
      stado plugin list / plugin info now show bundled plugins
      (per-wasm-module unit; '✓ bundled' status). stado plugin run
      replaced by stado tool run <name>; the new form accepts both
      canonical and wire forms, refuses [tools].disabled tools
      unless --force, and resolves bundled + installed uniformly.
```

- [ ] **Step 8.5: Run full repo tests**

Run: `cd <repo-root> && go build ./... && go test ./... -count=1 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 8.6: Run go vet**

Run: `cd <repo-root> && go vet ./...`
Expected: clean.

- [ ] **Step 8.7: Commit**

```bash
cd <repo-root>
git add cmd/stado/plugin_run.go cmd/stado/plugin.go CHANGELOG.md docs/eps/0037-tool-dispatch-and-operator-surface.md
git commit -m "feat(cli)!: remove plugin run; tool run replaces it

stado plugin run <plugin-id> <tool> [args] is gone. Use
stado tool run <name> [args] — it resolves bundled and installed
tools uniformly through the live registry, accepts canonical and
wire forms, and respects [tools].disabled (with --force escape).

Pre-1.0; no back-compat alias kept. Helpers
(attachPluginMemoryBridge, buildPluginRunBridge,
countPluginRunTokens) remain in plugin_run.go for use by
plugin_invoke_shared.go."
```

---

## Task 9: Self-review + branch sanity

**Files:** none (verification only)

**Context:** After all 8 tasks land, run a final whole-branch verification.

- [ ] **Step 9.1: Full repo `go test`**

Run: `cd <repo-root> && go test ./... -count=1 2>&1 | tail -15`
Expected: every package PASS.

- [ ] **Step 9.2: Full repo `go vet`**

Run: `cd <repo-root> && go vet ./...`
Expected: clean.

- [ ] **Step 9.3: Manual smoke — `plugin list`**

Run: `cd <repo-root> && go run ./cmd/stado plugin list 2>&1 | head -30`
Expected: bundled rows visible (`fs`, `shell`, `agent`, `web`, `dns`, `bash`, `auto-compact`, etc.) with `✓ bundled` status.

- [ ] **Step 9.4: Manual smoke — `plugin info auto-compact`**

Run: `cd <repo-root> && go run ./cmd/stado plugin info auto-compact`
Expected: prints the auto-compact module's synthetic manifest, including the `compact` tool.

- [ ] **Step 9.5: Manual smoke — `tool run` happy path**

```bash
cd <repo-root>
echo "smoke" > /tmp/stado-smoke.txt
go run ./cmd/stado tool run fs.read --workdir /tmp '{"path":"/tmp/stado-smoke.txt"}'
```
Expected: prints `smoke` (or the tool's response containing it).

Try wire form too:
```bash
go run ./cmd/stado tool run read --workdir /tmp '{"path":"/tmp/stado-smoke.txt"}'
```
Expected: same output.

- [ ] **Step 9.6: Manual smoke — `tool run` disabled**

```bash
cd <repo-root>
mkdir -p /tmp/stado-smoke-cfg/stado
cat > /tmp/stado-smoke-cfg/stado/config.toml <<EOF
[tools]
disabled = ["read"]
EOF
XDG_CONFIG_HOME=/tmp/stado-smoke-cfg go run ./cmd/stado tool run read '{}'
```
Expected: errors with "disabled" + "--force" mention.

```bash
XDG_CONFIG_HOME=/tmp/stado-smoke-cfg go run ./cmd/stado tool run read --force --workdir /tmp '{"path":"/tmp/stado-smoke.txt"}'
```
Expected: succeeds.

- [ ] **Step 9.7: Manual smoke — `plugin run` is gone**

```bash
cd <repo-root>
go run ./cmd/stado plugin run foo bar '{}'
```
Expected: cobra "unknown command" error or similar.

- [ ] **Step 9.8: Inspect the diff**

Run: `cd <repo-root> && git log main..HEAD --oneline`
Expected: 8 task commits + 1 plan commit (`60844eb`) + 1 spec commit (the design doc commit). All scoped, no surprises.

Run: `cd <repo-root> && git diff main..HEAD --stat | tail -15`
Expected: ~13 files, ~+500/-200 lines (rough estimate).

- [ ] **Step 9.9: Self-review summary**

The 8 tasks together should:
- ✅ Add `bundledplugins.List/LookupByName/LookupModuleByToolName/RegisterModule`.
- ✅ Wire registrations from `internal/runtime/bundled_plugin_tools.go` and `auto_compact.go`.
- ✅ Extract shared invoke helper.
- ✅ Add `tool run` with canonical+wire form lookup.
- ✅ Add disabled-tool refusal + `--force`.
- ✅ Show bundled rows in `plugin list`.
- ✅ Find bundled plugins via `plugin info`.
- ✅ Remove `plugin run`; document via CHANGELOG + EP-0037 history.

If any of these don't trace cleanly to a task, mark this checkbox unchecked + open a follow-up.

---

## Spec coverage

Each spec section traces to a task:

- **Component 1 — Bundled-plugin enumeration** → Task 1 (scaffolding) + Task 2 (wiring).
- **Component 2 — `plugin list` rendering** → Task 6.
- **Component 3 — `plugin info` extension** → Task 7.
- **Component 4 — `stado tool run` subcommand** → Task 4 (happy) + Task 5 (disabled refusal).
- **Component 5 — `plugin run` removal** → Task 3 (extract shared helper, prepare) + Task 8 (delete + docs).

No placeholders. No "TBD". Function/method names match across tasks (`RegisterModule`, `List`, `LookupByName`, `LookupModuleByToolName`, `runPluginInvocation`, `pluginInvokeArgs`, `runToolByName`, `toolRunOptions`, `lookupToolInRegistry`, `marshalSchemaJSON`, `bundledToolDefsFromList`, `printManifestInfo`).

Type consistency: `Info` struct shape stable across Tasks 1, 2, 6, 7. `pluginInvokeArgs` shape stable from Task 3 onward. `toolRunOptions` shape stable from Task 4 onward.
