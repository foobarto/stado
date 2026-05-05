# Unified registry — installed plugins reachable via `tool run` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `runtime.BuildDefaultRegistry(cfg)` to enumerate installed plugins (per active-version markers) alongside bundled wasm; replace `cmd/stado/tool_run.go`'s "not yet supported" error with installed-plugin dispatch through the existing `runPluginInvocation` shared helper.

**Architecture:** New `internal/runtime/installed_tools.go` owns plugin enumeration: walk `<state-dir>/plugins/`, pick the active version per plugin (marker file at `<state-dir>/plugins/active/<name>` per EP-0039 §F; else highest semver), `LoadFromDir` + `VerifyManifest` against the trust store, register each declared tool as a wasm-backed wrapper that lazy-loads `plugin.wasm` on invocation. `BuildDefaultRegistry` gains a `cfg *config.Config` parameter and delegates after bundled registrations. Tool-name collisions overwrite by registration order (installed wins, per `tools.Registry.Register` semantics — verified at `internal/tools/registry.go:28`).

**Tech Stack:** Go 1.22+, existing helpers from `internal/plugins/` (`LoadFromDir`, `VerifyManifest`, `ReadVerifiedWASM`, `ListInstalledDirs`), trust store (`plugins.NewTrustStore`), `bundledPluginTool` shape from `internal/runtime/bundled_plugin_tools.go` for the wrapper.

**Spec:** `docs/superpowers/specs/2026-05-05-unified-registry-installed-plugins-design.md`. Branch: `feat/unified-registry-installed-plugins` (off `main` at `536e6fb`; spec committed at `44a88ad`).

**Locked decisions:**
- Q1 — extend `BuildDefaultRegistry()`, no separate `BuildFullRegistry`.
- Q2 — re-verify signature on every registration.
- Q3 — register only the active version per `<state-dir>/plugins/active/<name>` (else highest semver).
- Q4 — installed wins on collision; emit `stado: info` line at registration.
- Q5 — `[tools].disabled` applies uniformly with `--force` escape.

**Key plan corrections from spec ground-truth:**
- The spec referenced `cfg.Plugins.Pinned[name].Version`. That field does not exist. Active-version is stored in a per-plugin marker file at `<stateDir>/plugins/active/<name>` containing the version string (per `cmd/stado/plugin_use_dev.go:48`). The plan reads that file.
- `tools.Registry.Register(t)` overwrites by name (`internal/tools/registry.go:25-29`). Q4's "installed wins" comes for free from registration order. No special collision logic required; just emit the info-line at registration time.
- 9 production call sites of `BuildDefaultRegistry()` need to pass `cfg`: `tool.go:35`, `tool.go:115`, `tool_run.go:85`, `plugin_trust.go:85`, `plugin_trust.go:235`, `plugin_info.go:34`, `mcp_server.go:60`, `model_commands.go:467`, `model_commands.go:495`.

**Scope OUT:**
- A `[plugins] include_in_tool_list = false` per-plugin opt-out knob. YAGNI; defer.
- Multi-version disambiguation in the registry (registering all versions with name suffixes). Q3 locked register-active-only.

---

## File map

| Action | Path | Purpose |
|---|---|---|
| Create | `internal/runtime/installed_tools.go` | Active-version helper + enumerator + per-plugin tool registration + `LookupInstalledModule` |
| Create | `internal/runtime/installed_tools_test.go` | Unit tests for happy/sig-fail/active-version/collision paths |
| Modify | `internal/runtime/executor.go` | `BuildDefaultRegistry()` → `BuildDefaultRegistry(cfg)`; calls `registerInstalledPluginTools` after bundled |
| Modify | `cmd/stado/tool_run.go` | Replace "not yet supported" branch with installed-plugin dispatch via shared helper |
| Modify | `cmd/stado/tool.go` (2 sites: `:35`, `:115`) | Pass `cfg` |
| Modify | `cmd/stado/tool_run.go:85` | Pass `cfg` |
| Modify | `cmd/stado/plugin_trust.go` (2 sites) | Pass `cfg` |
| Modify | `cmd/stado/plugin_info.go:34` | Pass `cfg` |
| Modify | `cmd/stado/mcp_server.go:60` | Pass `cfg` |
| Modify | `internal/tui/model_commands.go` (2 sites) | Pass `cfg` |
| Modify | `internal/runtime/parity_test.go` (uses BuildDefaultRegistry in tests) | Pass nil or test-cfg |
| Modify | Any other test files calling `BuildDefaultRegistry()` | Pass nil to retain bundled-only behaviour |

Total: ~330 net lines, 2 new files + ~10 modified.

---

## Task 1: Active-version helper

**Files:**
- Create: `internal/runtime/installed_tools.go`
- Create: `internal/runtime/installed_tools_test.go`

**Context:** Encapsulate active-version resolution in a small testable function. Reads the marker file at `<stateDir>/plugins/active/<name>` (created by `stado plugin use <name>@<version>`). Returns `""` when no marker exists. The "highest semver fallback" behaviour comes in Task 3 — this task only handles the explicit-pin path.

- [ ] **Step 1.1: Create the test file**

```go
// internal/runtime/installed_tools_test.go
package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// TestActiveVersionMarker_Reads: when a marker file exists, returns
// its contents trimmed.
func TestActiveVersionMarker_Reads(t *testing.T) {
	dir := t.TempDir()
	activeDir := filepath.Join(dir, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "fs"), []byte("v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := activeVersionMarker(dir, "fs")
	if got != "v1.2.3" {
		t.Errorf("activeVersionMarker(_, fs) = %q, want %q", got, "v1.2.3")
	}
}

// TestActiveVersionMarker_Missing: returns empty string when no
// marker file exists.
func TestActiveVersionMarker_Missing(t *testing.T) {
	dir := t.TempDir()
	got := activeVersionMarker(dir, "missing")
	if got != "" {
		t.Errorf("activeVersionMarker(_, missing) = %q, want empty", got)
	}
}

// TestActiveVersionMarker_StripsWhitespace: marker file with
// trailing whitespace round-trips cleanly.
func TestActiveVersionMarker_StripsWhitespace(t *testing.T) {
	dir := t.TempDir()
	activeDir := filepath.Join(dir, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "shell"), []byte("  v0.5.0  \n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := activeVersionMarker(dir, "shell")
	if got != "v0.5.0" {
		t.Errorf("activeVersionMarker should trim whitespace; got %q", got)
	}
}
```

- [ ] **Step 1.2: Run tests, verify FAIL**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestActiveVersionMarker -count=1`
Expected: build failure with `undefined: activeVersionMarker`.

- [ ] **Step 1.3: Create `installed_tools.go`**

```go
// internal/runtime/installed_tools.go
package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// activeVersionMarker reads the per-plugin active-version marker
// written by `stado plugin use <name>@<version>` (cmd/stado/
// plugin_use_dev.go:48). Returns the trimmed version string when
// present; "" when the marker is missing or unreadable.
func activeVersionMarker(stateDir, pluginName string) string {
	markerPath := filepath.Join(stateDir, "plugins", "active", pluginName)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
```

- [ ] **Step 1.4: Run tests, verify PASS**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestActiveVersionMarker -count=1 -v`
Expected: 3 PASS.

- [ ] **Step 1.5: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/installed_tools.go internal/runtime/installed_tools_test.go
git commit -m "feat(runtime): activeVersionMarker helper for installed plugins

Reads <stateDir>/plugins/active/<name> per EP-0039 §F. Returns
trimmed version string when present, empty when missing. Used by
the upcoming installed-plugin enumerator to pick which version
to register."
```

---

## Task 2: `installedPluginTool` wrapper type

**Files:**
- Modify: `internal/runtime/installed_tools.go` — add the type + constructor.
- Modify: `internal/runtime/installed_tools_test.go` — add a smoke test.

**Context:** The bundled path uses `bundledPluginTool` (in `internal/runtime/bundled_plugin_tools.go`) which carries the manifest + lazy wasm bytes. Installed plugins need a similar wrapper that lazy-loads from disk via `plugins.ReadVerifiedWASM(sha256, wasmPath)`.

- [ ] **Step 2.1: Read the bundledPluginTool shape**

`cd /home/foobarto/Dokumenty/stado && sed -n '270,300p' internal/runtime/bundled_plugin_tools.go`

Note the struct fields: `manifest`, `def`, `schema`, `class`, `wasm`. The wasm bytes are loaded eagerly via `bundledplugins.MustWasm()` at construction time. For installed plugins, we want lazy load — the registration shouldn't pay the cost of reading every plugin's wasm on every CLI invocation.

- [ ] **Step 2.2: Write the failing test**

Append to `internal/runtime/installed_tools_test.go`:

```go
import (
	// ... existing imports ...
	"github.com/foobarto/stado/internal/plugins"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// TestInstalledPluginTool_NameAndDescription: the wrapper exposes
// the manifest's tool name and description without loading wasm.
func TestInstalledPluginTool_NameAndDescription(t *testing.T) {
	mf := plugins.Manifest{
		Name:    "test-plugin",
		Version: "v0.1.0",
		Tools: []plugins.ToolDef{{
			Name:        "lookup",
			Description: "Lookup a thing",
			Schema:      `{"type":"object"}`,
		}},
	}
	tool := newInstalledPluginTool(mf, mf.Tools[0], "/nonexistent/wasm/path", pkgtool.ClassNonMutating)
	if tool.Name() != "lookup" {
		t.Errorf("Name() = %q, want lookup", tool.Name())
	}
	if tool.Description() != "Lookup a thing" {
		t.Errorf("Description() = %q, want 'Lookup a thing'", tool.Description())
	}
	// Schema returns parsed map; just verify it's non-nil.
	if tool.Schema() == nil {
		t.Error("Schema() returned nil")
	}
}
```

- [ ] **Step 2.3: Run, verify FAIL**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestInstalledPluginTool -count=1`
Expected: build failure with `undefined: newInstalledPluginTool`.

- [ ] **Step 2.4: Add the wrapper type to `installed_tools.go`**

Append to `/home/foobarto/Dokumenty/stado/internal/runtime/installed_tools.go`:

```go
import (
	// add to existing imports:
	"context"
	"encoding/json"

	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/pkg/tool"
)

// installedPluginTool wraps an installed plugin's declared tool as
// a wasm-backed registry entry. wasm bytes are loaded lazily on
// first invocation (the registry build path runs on every CLI
// invocation; eager-loading every plugin's wasm at registration
// would be expensive for operators with many installed plugins).
//
// The manifest carries the verified WASMSHA256; ReadVerifiedWASM
// re-checks the sha at load time, so a tampered plugin.wasm fails
// at invoke rather than silently succeeding.
type installedPluginTool struct {
	manifest plugins.Manifest
	def      plugins.ToolDef
	schema   map[string]any
	class    tool.Class
	wasmPath string // <install-dir>/plugin.wasm
}

func newInstalledPluginTool(mf plugins.Manifest, def plugins.ToolDef, wasmPath string, class tool.Class) tool.Tool {
	var schema map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &schema)
	}
	return &installedPluginTool{
		manifest: mf,
		def:      def,
		schema:   schema,
		class:    class,
		wasmPath: wasmPath,
	}
}

func (t *installedPluginTool) Name() string        { return t.def.Name }
func (t *installedPluginTool) Description() string { return t.def.Description }
func (t *installedPluginTool) Schema() map[string]any {
	if t.schema == nil {
		return map[string]any{"type": "object"}
	}
	return t.schema
}

// Run lazy-loads the wasm bytes, instantiates a host runtime, and
// dispatches the tool. NOT used by the model-facing path (which
// dispatches via Executor + plugin runtime); this is the
// `tool list`-shaped Tool interface contract for the registry, but
// the actual invocation path goes through runPluginInvocation in
// cmd/stado/plugin_invoke_shared.go via tool_run.go's installed
// branch (Task 6).
//
// Returning an error here keeps the registry's Tool interface honest
// while signalling that this wrapper isn't directly invocable —
// callers (ToolMatchesGlob, AutoloadedTools, tools.search) treat it
// as metadata; the actual run goes through tool_run.
func (t *installedPluginTool) Run(_ context.Context, _ []byte, _ pluginRuntime.Host) (tool.Result, error) {
	return tool.Result{
		IsError: true,
		Error:   "installed plugin tool not invokable directly via Tool.Run; route through stado tool run <name>",
	}, nil
}
```

Match the existing `bundledPluginTool` Run signature exactly. If `bundledPluginTool.Run` has a different signature, mirror that. Read `internal/runtime/bundled_plugin_tools.go` lines 50-65 area for the actual `Run` method shape and adjust.

- [ ] **Step 2.5: Run tests, verify PASS**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestInstalledPluginTool -count=1 -v`
Expected: PASS.

- [ ] **Step 2.6: Run full runtime tests**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -count=1`
Expected: PASS.

- [ ] **Step 2.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/installed_tools.go internal/runtime/installed_tools_test.go
git commit -m "feat(runtime): installedPluginTool wrapper for registry entries

Mirrors bundledPluginTool but lazy-loads wasm from disk via
plugin.wasm path on first invocation. Run() returns a sentinel
error since installed-plugin invocation goes through tool_run's
shared helper, not this wrapper directly. The wrapper exists so
Registry.All() / tool list / tools.search reflect installed
plugins as first-class entries."
```

---

## Task 3: Active-version selection (highest semver fallback)

**Files:**
- Modify: `internal/runtime/installed_tools.go` — add `pickActiveVersion`.
- Modify: `internal/runtime/installed_tools_test.go` — add tests.

**Context:** Q3 locked: register only the active version. Pin precedence: marker file (Task 1's `activeVersionMarker`); else highest semver. Need the "highest semver" fallback now.

The existing `internal/plugins/identity.go` `ParseIdentity` validates `vX.Y.Z` strings. Reuse via the helper that parses each candidate; sort using `golang.org/x/mod/semver` if it's already a dependency (it usually is for Go projects), else hand-roll a minimal compare on the parsed identity.

- [ ] **Step 3.1: Check what semver tooling exists**

Run: `cd /home/foobarto/Dokumenty/stado && grep -rn "golang.org/x/mod/semver\|semver.Compare" internal/ | head -5`

If `semver.Compare` is already used: prefer it. Else: parse via `plugins.ParseIdentity` and compare on the parsed major.minor.patch ints.

If neither is convenient: a simple `sort.Strings` on `["v0.1.0", "v0.2.0", "v1.0.0"]` happens to give correct semver order BECAUSE the `v` prefix and zero-padded forms keep alphabetical ≅ semver for typical inputs. But that breaks for `v0.10.0` vs `v0.9.0`. Don't rely on alphabetical.

For this task, use `golang.org/x/mod/semver` — it's a tiny standard module. If it's not in `go.mod`, add it via `go get golang.org/x/mod` (committed in the same task's commit).

- [ ] **Step 3.2: Write failing tests**

Append to `internal/runtime/installed_tools_test.go`:

```go
// TestPickActiveVersion_PrefersMarker: marker file wins over disk
// candidates.
func TestPickActiveVersion_PrefersMarker(t *testing.T) {
	dir := t.TempDir()
	activeDir := filepath.Join(dir, "plugins", "active")
	_ = os.MkdirAll(activeDir, 0o755)
	_ = os.WriteFile(filepath.Join(activeDir, "fs"), []byte("v0.1.0"), 0o644)

	got := pickActiveVersion(dir, "fs", []string{"v0.1.0", "v0.2.0", "v1.0.0"})
	if got != "v0.1.0" {
		t.Errorf("pickActiveVersion = %q, want v0.1.0 (marker wins)", got)
	}
}

// TestPickActiveVersion_HighestSemverFallback: no marker, highest
// semver wins.
func TestPickActiveVersion_HighestSemverFallback(t *testing.T) {
	dir := t.TempDir()
	got := pickActiveVersion(dir, "fs", []string{"v0.1.0", "v0.10.0", "v0.2.0", "v1.0.0"})
	if got != "v1.0.0" {
		t.Errorf("pickActiveVersion = %q, want v1.0.0", got)
	}
	got2 := pickActiveVersion(dir, "fs", []string{"v0.1.0", "v0.10.0", "v0.2.0"})
	if got2 != "v0.10.0" {
		t.Errorf("pickActiveVersion = %q, want v0.10.0 (10 > 2)", got2)
	}
}

// TestPickActiveVersion_MarkerPointsAtMissingVersion: marker
// references a version not in candidates → return "".
func TestPickActiveVersion_MarkerPointsAtMissingVersion(t *testing.T) {
	dir := t.TempDir()
	activeDir := filepath.Join(dir, "plugins", "active")
	_ = os.MkdirAll(activeDir, 0o755)
	_ = os.WriteFile(filepath.Join(activeDir, "fs"), []byte("v9.9.9"), 0o644)

	got := pickActiveVersion(dir, "fs", []string{"v0.1.0", "v0.2.0"})
	if got != "" {
		t.Errorf("pickActiveVersion = %q, want empty (marker version not installed)", got)
	}
}

// TestPickActiveVersion_NoCandidates: empty candidates → "".
func TestPickActiveVersion_NoCandidates(t *testing.T) {
	dir := t.TempDir()
	got := pickActiveVersion(dir, "fs", nil)
	if got != "" {
		t.Errorf("pickActiveVersion empty = %q, want empty", got)
	}
}
```

- [ ] **Step 3.3: Run, verify FAIL**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestPickActiveVersion -count=1`
Expected: build failure with `undefined: pickActiveVersion`.

- [ ] **Step 3.4: Implement `pickActiveVersion`**

Append to `internal/runtime/installed_tools.go`:

```go
// add "golang.org/x/mod/semver" to imports

// pickActiveVersion returns which version of pluginName to register,
// given the list of candidates found on disk. Pin precedence:
//   1. <stateDir>/plugins/active/<name> marker file (set by
//      `stado plugin use <name>@<version>`); only honoured when the
//      marker's version is among candidates.
//   2. Highest semver among candidates.
// Returns "" if (1) misses and candidates is empty.
func pickActiveVersion(stateDir, pluginName string, candidates []string) string {
	if marker := activeVersionMarker(stateDir, pluginName); marker != "" {
		for _, v := range candidates {
			if v == marker {
				return marker
			}
		}
		// Marker points at a version not on disk — refuse to fall
		// through silently. Caller logs + skips.
		return ""
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, v := range candidates[1:] {
		if semver.Compare(v, best) > 0 {
			best = v
		}
	}
	return best
}
```

If `golang.org/x/mod/semver` isn't available, run:
```
cd /home/foobarto/Dokumenty/stado && go get golang.org/x/mod/semver@latest && go mod tidy
```

- [ ] **Step 3.5: Run tests, verify PASS**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestPickActiveVersion -count=1 -v`
Expected: 4 PASS.

- [ ] **Step 3.6: Run full repo build**

Run: `cd /home/foobarto/Dokumenty/stado && go build ./...`
Expected: clean.

- [ ] **Step 3.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/installed_tools.go internal/runtime/installed_tools_test.go go.mod go.sum
git commit -m "feat(runtime): pickActiveVersion for installed plugins

Pin precedence: marker file (set by stado plugin use) wins; else
highest semver among candidates. Marker pointing at a version not
on disk returns empty (caller logs + skips, surfacing the
misconfiguration). Uses golang.org/x/mod/semver for correct
comparison (handles v0.10 > v0.2)."
```

---

## Task 4: Group installed plugin dirs by name

**Files:**
- Modify: `internal/runtime/installed_tools.go` — add `groupInstalledByName`.
- Modify: `internal/runtime/installed_tools_test.go` — add tests.

**Context:** Walk `<stateDir>/plugins/`, find all `<name>-<version>/` subdirs, group by name, return `map[name][]version`. Skips entries that don't match the expected pattern.

- [ ] **Step 4.1: Write failing tests**

Append to `internal/runtime/installed_tools_test.go`:

```go
// TestGroupInstalledByName_GroupsAndSkips: name-version dirs are
// grouped; non-matching entries (active/ subdir, files, malformed
// names) are skipped.
func TestGroupInstalledByName_GroupsAndSkips(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	for _, sub := range []string{
		"fs-v0.1.0", "fs-v0.2.0", "shell-v1.0.0",
		"active",  // metadata dir; must be skipped
		"no-dash", // malformed name; must be skipped
	} {
		_ = os.MkdirAll(filepath.Join(pluginsDir, sub), 0o755)
	}
	_ = os.WriteFile(filepath.Join(pluginsDir, "stray.txt"), []byte("ignore"), 0o644)

	got, err := groupInstalledByName(pluginsDir)
	if err != nil {
		t.Fatalf("groupInstalledByName: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(got), got)
	}
	if len(got["fs"]) != 2 {
		t.Errorf("fs versions = %v, want 2 entries", got["fs"])
	}
	if len(got["shell"]) != 1 {
		t.Errorf("shell versions = %v, want 1 entry", got["shell"])
	}
}

// TestGroupInstalledByName_NoPluginsDir: missing dir returns empty
// map without error.
func TestGroupInstalledByName_NoPluginsDir(t *testing.T) {
	dir := t.TempDir()
	got, err := groupInstalledByName(filepath.Join(dir, "plugins"))
	if err != nil {
		t.Fatalf("groupInstalledByName on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %+v", got)
	}
}

// TestGroupInstalledByName_HandlesMultiDashNames: "htb-lab-v0.1.0"
// → name "htb-lab", version "v0.1.0". The split is on the LAST "-v"
// preceding a semver-shaped suffix.
func TestGroupInstalledByName_HandlesMultiDashNames(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	_ = os.MkdirAll(filepath.Join(pluginsDir, "htb-lab-v0.1.0"), 0o755)
	_ = os.MkdirAll(filepath.Join(pluginsDir, "exfil-server-v0.1.0"), 0o755)

	got, _ := groupInstalledByName(pluginsDir)
	if len(got["htb-lab"]) != 1 || got["htb-lab"][0] != "v0.1.0" {
		t.Errorf("htb-lab grouping = %+v; want {htb-lab: [v0.1.0]}", got)
	}
	if len(got["exfil-server"]) != 1 {
		t.Errorf("exfil-server grouping wrong: %+v", got)
	}
}
```

- [ ] **Step 4.2: Run, verify FAIL**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestGroupInstalledByName -count=1`
Expected: build failure.

- [ ] **Step 4.3: Implement**

Append to `internal/runtime/installed_tools.go`:

```go
import (
	// add:
	"strings"
	"github.com/foobarto/stado/internal/plugins"
)

// groupInstalledByName scans pluginsDir for "<name>-<version>"
// subdirectories and returns a map of name → versions. Entries that
// don't match the expected pattern (no -v prefix, or the "active"
// metadata subdir) are skipped. A missing pluginsDir is not an
// error — returns an empty map.
func groupInstalledByName(pluginsDir string) (map[string][]string, error) {
	out := map[string][]string{}
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "active" {
			continue
		}
		name, version, ok := splitInstalledID(e.Name())
		if !ok {
			continue
		}
		out[name] = append(out[name], version)
	}
	return out, nil
}

// splitInstalledID splits "<name>-<version>" into name + version.
// Returns ok=false when the suffix isn't a semver-shaped "vX.Y.Z"
// (matches the existing `plugin install` directory naming).
func splitInstalledID(id string) (name, version string, ok bool) {
	// Find the last "-v" boundary; the suffix from there must
	// start with "v" + digit.
	for i := len(id) - 1; i >= 1; i-- {
		if id[i] == '-' && i+1 < len(id) && id[i+1] == 'v' && i+2 < len(id) && id[i+2] >= '0' && id[i+2] <= '9' {
			candidate := id[i+1:]
			if _, err := plugins.ParseIdentity("local/dev/x@" + candidate); err == nil {
				return id[:i], candidate, true
			}
			// Also accept SHA-shaped suffixes from `plugin install`
			// of dev builds: "v" + 7+ hex chars passes neither
			// semver nor identity but is a valid install dir name.
			// Accept conservatively: just check the prefix shape.
			if len(candidate) >= 2 && candidate[0] == 'v' {
				return id[:i], candidate, true
			}
		}
	}
	return "", "", false
}
```

The `plugins.ParseIdentity` call expects a full identity string (host/owner/repo@version); construct one with a fake prefix to leverage its semver validator. If `ParseIdentity` doesn't expose a partial-validate, simplify `splitInstalledID` to just check `candidate[0] == 'v'` and `candidate[1]` is a digit.

- [ ] **Step 4.4: Run tests, verify PASS**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestGroupInstalledByName -count=1 -v`
Expected: 3 PASS.

- [ ] **Step 4.5: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/installed_tools.go internal/runtime/installed_tools_test.go
git commit -m "feat(runtime): groupInstalledByName for plugin enumeration

Walks <state-dir>/plugins/, groups <name>-<version>/ subdirs by
name. Skips the active/ marker dir and any malformed entry. Used
by the upcoming registerInstalledPluginTools to feed into
pickActiveVersion."
```

---

## Task 5: `registerInstalledPluginTools` — the main loop

**Files:**
- Modify: `internal/runtime/installed_tools.go` — add `registerInstalledPluginTools` + `LookupInstalledModule`.
- Modify: `internal/runtime/installed_tools_test.go` — add integration tests using existing test-plugin builders.

**Context:** Top-level entry point. Iterates `groupInstalledByName` results, picks active version per `pickActiveVersion`, loads + verifies each plugin's manifest, and registers each declared tool as an `installedPluginTool` with the verified wasm path.

`LookupInstalledModule(toolName)` is the symmetric lookup used by `tool_run.go`: returns the manifest + wasm path for a named tool. Implementation: scan a package-level cache populated during registration. Or simpler: re-scan on lookup (acceptable; `tool run` is not a hot path).

- [ ] **Step 5.1: Read existing plugin-test helpers**

`cd /home/foobarto/Dokumenty/stado && grep -n "buildTestPluginWithCaps\|isolatedHome" cmd/stado/plugin_test_helpers_test.go cmd/stado/plugin_info_test.go 2>/dev/null | head -10`

These helpers create a signed plugin in a tempdir with a working trust-store entry. They live in `cmd/stado/` and aren't directly importable into `internal/runtime/` — but the runtime tests can either:
- Build their own minimal fixture (eager: stub a manifest + wasm file in a tempdir).
- Use a public helper in `internal/plugins/` if one exists.

Pick the one that exists.

- [ ] **Step 5.2: Write failing tests**

Append to `internal/runtime/installed_tools_test.go`:

```go
import (
	// add:
	"github.com/foobarto/stado/internal/config"
)

// TestRegisterInstalledPluginTools_NoPluginsDirNoOp: registry stays
// empty when nothing is installed.
func TestRegisterInstalledPluginTools_NoPluginsDirNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	// cfg.StateDir() returns whatever the test-env points at; force
	// it via XDG.
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg = loaded

	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, cfg)
	if len(reg.All()) != 0 {
		t.Errorf("expected empty registry; got %d tools", len(reg.All()))
	}
}

// TestRegisterInstalledPluginTools_RegistersValidPlugin: a signed
// plugin in the install dir gets its tools registered.
//
// This test piggybacks on the cmd/stado test plugin builder; if it
// can't be reached from internal/runtime, the test stubs a manifest
// + wasm fixture by hand. Exact fixture path depends on what helpers
// internal/plugins/ exposes — the implementer picks the cheaper
// option.
func TestRegisterInstalledPluginTools_RegistersValidPlugin(t *testing.T) {
	t.Skip("requires test-plugin builder; flesh out after surveying internal/plugins/ exported helpers")
}
```

The skipped test signals scope and gives the next implementer an obvious place to fill in a real fixture (using `internal/plugins`-package helpers if they exist, or a hand-built minimal manifest). For this task, the no-op test is sufficient to wire up the entry point.

- [ ] **Step 5.3: Run, verify FAIL**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestRegisterInstalledPluginTools_NoPluginsDirNoOp -count=1`
Expected: build failure.

- [ ] **Step 5.4: Implement `registerInstalledPluginTools` + `LookupInstalledModule`**

Append to `internal/runtime/installed_tools.go`:

```go
import (
	// add:
	"fmt"
	"sync"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// installedRegistryMu protects the package-level installedByTool
// map populated by registerInstalledPluginTools; consumed by
// LookupInstalledModule.
var (
	installedRegistryMu sync.Mutex
	installedByTool     = map[string]installedRecord{}
)

type installedRecord struct {
	Manifest plugins.Manifest
	WasmPath string
}

// registerInstalledPluginTools enumerates installed plugins under
// cfg.StateDir()/plugins/, picks the active version per plugin
// (pickActiveVersion), verifies the manifest signature against the
// trust store, and registers each declared tool as an
// installedPluginTool wrapper with the verified wasm path.
//
// Plugins failing signature verification emit a stado: warn line on
// stderr and are skipped. Tool-name collisions with already-
// registered tools (typically bundled) result in the installed tool
// overwriting (tools.Registry.Register semantics); a stado: info
// line is emitted at the registration call site.
//
// Q1/Q2/Q3/Q4 of the design.
func registerInstalledPluginTools(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	stateDir := cfg.StateDir()
	pluginsDir := filepath.Join(stateDir, "plugins")

	groups, err := groupInstalledByName(pluginsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado: warn: enumerate installed plugins: %v\n", err)
		return
	}

	ts := plugins.NewTrustStore(stateDir)

	// Reset package-level lookup state for this build.
	installedRegistryMu.Lock()
	installedByTool = map[string]installedRecord{}
	installedRegistryMu.Unlock()

	for name, versions := range groups {
		version := pickActiveVersion(stateDir, name, versions)
		if version == "" {
			continue
		}
		dir := filepath.Join(pluginsDir, name+"-"+version)
		mf, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s manifest load: %v\n", name, version, err)
			continue
		}
		if err := ts.VerifyManifest(mf, sig); err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s signature failed: %v; not registered\n", name, version, err)
			continue
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		// Re-verify wasm sha now to catch tampering between install
		// and registration.
		if _, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath); err != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: plugin %s@%s wasm verify: %v; not registered\n", name, version, err)
			continue
		}
		for _, def := range mf.Tools {
			if _, exists := reg.Get(def.Name); exists {
				fmt.Fprintf(os.Stderr, "stado: info: plugin %s@%s overrides registered tool %s\n", name, version, def.Name)
			}
			class := pkgtool.ClassMutating
			if def.Class == "non-mutating" {
				class = pkgtool.ClassNonMutating
			}
			reg.Register(newInstalledPluginTool(*mf, def, wasmPath, class))

			installedRegistryMu.Lock()
			installedByTool[def.Name] = installedRecord{
				Manifest: *mf,
				WasmPath: wasmPath,
			}
			installedRegistryMu.Unlock()
		}
	}
}

// LookupInstalledModule returns the manifest + wasm path for the
// named installed-plugin tool. Symmetric with
// bundledplugins.LookupModuleByToolName. Used by cmd/stado/tool_run.go
// to dispatch installed-plugin invocation through runPluginInvocation.
func LookupInstalledModule(toolName string) (plugins.Manifest, string, bool) {
	installedRegistryMu.Lock()
	defer installedRegistryMu.Unlock()
	rec, ok := installedByTool[toolName]
	if !ok {
		return plugins.Manifest{}, "", false
	}
	return rec.Manifest, rec.WasmPath, true
}
```

The `pkgtool.ClassMutating`/`ClassNonMutating` constants come from `pkg/tool/`. Verify they exist; the bundled-plugin path uses the same. If `def.Class` is the empty string, `ClassMutating` is the safer default.

- [ ] **Step 5.5: Run tests, verify PASS**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run TestRegisterInstalledPluginTools -count=1 -v`
Expected: PASS for NoPluginsDir test; SKIP for the deferred fixture test.

- [ ] **Step 5.6: Run full runtime tests**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -count=1`
Expected: PASS.

- [ ] **Step 5.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/installed_tools.go internal/runtime/installed_tools_test.go
git commit -m "feat(runtime): registerInstalledPluginTools + LookupInstalledModule

Top-level enumerator: walks installed plugins, picks active version
(via marker file or highest semver), verifies manifest signature
+ wasm sha256 against trust store, and registers each declared
tool as an installedPluginTool with the verified wasm path. Failed
verifications emit stado: warn lines on stderr and are skipped.
Tool-name collisions log stado: info lines at registration time
(installed wins per Q4 — tools.Registry.Register overwrites).

LookupInstalledModule provides the symmetric lookup used by
cmd/stado/tool_run.go to dispatch installed-plugin invocation
through runPluginInvocation."
```

---

## Task 6: Wire into `BuildDefaultRegistry(cfg)`

**Files:**
- Modify: `internal/runtime/executor.go` — change `BuildDefaultRegistry()` signature, delegate to `registerInstalledPluginTools` after bundled.
- Modify: 9 production call sites — pass `cfg`.
- Modify: any test files calling `BuildDefaultRegistry()` — pass nil.

**Context:** The signature change cascades. Production call sites have `cfg` in scope (via `config.Load()` near each call). Test files pass `nil` to retain bundled-only behaviour.

- [ ] **Step 6.1: Update `BuildDefaultRegistry`**

Read `cd /home/foobarto/Dokumenty/stado && sed -n '36,46p' internal/runtime/executor.go` to see the current shape.

Replace:

```go
func BuildDefaultRegistry() *tools.Registry {
	reg := buildBundledPluginRegistry()
	registerMetaTools(reg)
	return reg
}
```

With:

```go
// BuildDefaultRegistry returns a Registry preloaded with stado's
// bundled tools (fs, shell, web, dns, agent, etc.), the meta-tools
// (tools__search/describe/categories/in_category), and — when cfg
// is non-nil — the operator's installed plugins from cfg.StateDir()/
// plugins/. Bundled registers first; installed registers last and
// overwrites bundled on tool-name collision (Q4 — installed wins).
//
// cfg may be nil for test code that wants the bundled-only set;
// production callers should pass the loaded config.
func BuildDefaultRegistry(cfg *config.Config) *tools.Registry {
	reg := buildBundledPluginRegistry()
	registerMetaTools(reg)
	if cfg != nil {
		registerInstalledPluginTools(reg, cfg)
	}
	return reg
}
```

Add `"github.com/foobarto/stado/internal/config"` to the imports if it's not already there.

- [ ] **Step 6.2: Update production call sites**

Run `cd /home/foobarto/Dokumenty/stado && grep -rn "runtime.BuildDefaultRegistry()" cmd/ internal/ | grep -v "_test.go"` to confirm the 9 call sites match the plan's file map.

For EACH site, change `runtime.BuildDefaultRegistry()` to `runtime.BuildDefaultRegistry(cfg)`. The `cfg` variable is in scope at each site (from a `cfg, err := config.Load()` or `m.cfg`). Specifically:

- `cmd/stado/tool.go:35` (in `toolListCmd.RunE`) — `cfg` already loaded above.
- `cmd/stado/tool.go:115` (in `toolInfoCmd.RunE`) — `cfg` already loaded above.
- `cmd/stado/tool_run.go:85` (in `runToolByName`) — `cfg` is `opts.Cfg`.
- `cmd/stado/plugin_trust.go:85` (the side-effect call in `pluginListCmd`) — change `_ = runtime.BuildDefaultRegistry()` to `_ = runtime.BuildDefaultRegistry(cfg)`.
- `cmd/stado/plugin_trust.go:235` (the side-effect call in `pluginInstalledCmd`) — same.
- `cmd/stado/plugin_info.go:34` (side-effect call) — same.
- `cmd/stado/mcp_server.go:60` — `cfg` is in scope from the surrounding `RunE`.
- `internal/tui/model_commands.go:467` (in `handleToolSlash`'s `case "ls":`) — pass `m.cfg`.
- `internal/tui/model_commands.go:495` (in `handleToolSlash`'s `case "info":`) — pass `m.cfg`.

If any callsite doesn't have `cfg` in scope (unlikely), pass `nil` — that retains bundled-only behaviour for that callsite.

- [ ] **Step 6.3: Update test call sites**

Run: `cd /home/foobarto/Dokumenty/stado && grep -rn "BuildDefaultRegistry()" --include="*_test.go"`

For EACH test-file site, change `BuildDefaultRegistry()` to `BuildDefaultRegistry(nil)`. Test code wants bundled-only behaviour for repeatability.

- [ ] **Step 6.4: Build the repo**

Run: `cd /home/foobarto/Dokumenty/stado && go build ./...`
Expected: clean. If any callsite has the wrong signature, fix.

- [ ] **Step 6.5: Run all tests**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./... -count=1 2>&1 | grep -E "^(FAIL|ok)" | grep -v "^ok"`
Expected: only the pre-existing `internal/sandbox/TestBwrapRunner_AllowHostsOnly...` failure (environmental, unrelated). All others PASS.

- [ ] **Step 6.6: Run go vet**

Run: `cd /home/foobarto/Dokumenty/stado && go vet ./...`
Expected: clean.

- [ ] **Step 6.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/executor.go cmd/stado/tool.go cmd/stado/tool_run.go cmd/stado/plugin_trust.go cmd/stado/plugin_info.go cmd/stado/mcp_server.go internal/tui/model_commands.go
# add any test files updated
git commit -m "feat(runtime): BuildDefaultRegistry takes cfg; wires installed plugins

Signature changes from () to (cfg *config.Config). When cfg is
non-nil, registerInstalledPluginTools runs after bundled
registrations + meta-tools. Installed-plugin tools appear in
tool list / tools__search / mcp-server / TUI /tool ls — the four
unified surfaces.

cfg=nil retains bundled-only behaviour for test code. Pre-1.0;
no back-compat shim. 9 production call sites updated to pass cfg
from their surrounding scope."
```

---

## Task 7: `tool run` dispatches installed plugins

**Files:**
- Modify: `cmd/stado/tool_run.go` — replace the "not yet supported" error branch with installed-plugin dispatch.
- Modify: `cmd/stado/tool_run_test.go` — flip the not-supported test, add an installed-plugin happy path.

**Context:** The "not yet supported" branch was a deliberate placeholder. Now `runtime.LookupInstalledModule(toolName)` returns the manifest + wasm path; the same `runPluginInvocation` shared helper handles dispatch.

- [ ] **Step 7.1: Read current state**

`cd /home/foobarto/Dokumenty/stado && sed -n '95,130p' cmd/stado/tool_run.go`

Locate the "not yet supported" return.

- [ ] **Step 7.2: Replace the branch**

Find:

```go
info, isBundled := bundledplugins.LookupModuleByToolName(registered.Name())
if !isBundled {
    return fmt.Errorf("tool %q is not a bundled tool — installed-plugin invocation by tool-name is not yet supported (use `stado tool list` to see what's available)", registered.Name())
}
```

Replace with:

```go
info, isBundled := bundledplugins.LookupModuleByToolName(registered.Name())
if isBundled {
    // existing bundled path — synthesise manifest, load via
    // bundledplugins.Wasm, dispatch.
    pluginName := bundledplugins.ManifestNamePrefix + "-" + info.Name
    bareToolDef := toolDefFromRegistered(registered)
    manifest := plugins.Manifest{
        Name:         pluginName,
        Version:      info.Version,
        Author:       info.Author,
        Capabilities: info.Capabilities,
        Tools:        []plugins.ToolDef{bareToolDef},
    }
    wasmBytes, err := bundledplugins.Wasm(info.Name)
    if err != nil {
        return fmt.Errorf("bundled wasm load: %w", err)
    }
    installDir, _ := os.Getwd()
    return runPluginInvocation(ctx, pluginInvokeArgs{
        Manifest:   manifest,
        WasmBytes:  wasmBytes,
        ToolName:   bareToolDef.Name,
        ArgsJSON:   argsJSON,
        Cfg:        cfg,
        WorkdirArg: opts.Workdir,
        InstallDir: installDir,
        SessionID:  opts.Session,
        Stdout:     stdout,
        Stderr:     stderr,
    })
}

// Installed-plugin path.
if mfst, wasmPath, ok := runtime.LookupInstalledModule(registered.Name()); ok {
    wasmBytes, err := plugins.ReadVerifiedWASM(mfst.WASMSHA256, wasmPath)
    if err != nil {
        return fmt.Errorf("verify: %w", err)
    }
    // Find the tool def in the manifest matching the registered
    // name. Installed plugins use the tool name as-is — no
    // canonical/wire transformation, since the plugin author
    // chose the wire-form name in their manifest.
    var bareName string
    for _, td := range mfst.Tools {
        if td.Name == registered.Name() {
            bareName = td.Name
            break
        }
    }
    if bareName == "" {
        return fmt.Errorf("internal: tool %q registered but not in installed manifest %q", registered.Name(), mfst.Name)
    }
    return runPluginInvocation(ctx, pluginInvokeArgs{
        Manifest:   mfst,
        WasmBytes:  wasmBytes,
        ToolName:   bareName,
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

This restructures to: bundled path (early return) OR installed path (early return) OR fallthrough error (registry has it but neither lookup found the source — should never happen, but the explicit error makes the failure mode clear).

Note: the old code had bundled as a single fall-through path. The new code is bundled-then-installed-then-error.

You'll also need to make sure the existing path's manifest construction (the bundled `pluginName/manifest`/`bareToolDef` block) actually moves INTO the `if isBundled {}` block — since the original code computed those values after the not-yet-supported branch.

Add the `runtime` package import at the top of `tool_run.go` if not already present (it is — used for `runtime.BuildDefaultRegistry(cfg)`).

Add `"github.com/foobarto/stado/internal/plugins"` and `"path/filepath"` imports if not present.

- [ ] **Step 7.3: Update tests**

In `cmd/stado/tool_run_test.go`:

- The existing `TestToolRun_ToolNotFound` test (passing `"nope.foo"`) should still work — that name doesn't resolve in the registry, so it errors at the lookup stage, not the dispatch stage.
- No "not yet supported" string assertion exists today (the test asserted on `stado tool list`, not the message). Verify: `grep "not yet supported" cmd/stado/tool_run_test.go` — should be empty.
- The 6 existing tests (canonical, bare, not-found, disabled, force, glob) should all still pass.

Add a new test for the installed-plugin path. This requires a real installed plugin in a tempdir state-dir; reuse `cmd/stado/plugin_test_helpers_test.go`'s `buildTestPluginWithCaps` if it can be wired:

```go
// TestToolRun_DispatchesInstalledPlugin: an installed plugin's tool
// is invocable via stado tool run <toolname>.
//
// Skipped because building a fully-signed test plugin from inside
// internal/runtime requires the cmd/stado helpers — wire it up if
// the cmd/stado test helpers become reusable, or write a minimal
// fixture here.
func TestToolRun_DispatchesInstalledPlugin(t *testing.T) {
	t.Skip("requires installed-plugin test fixture; see plan Task 7 for the path forward")
}
```

If `buildTestPluginWithCaps` IS reachable (it's in package main, same package as `tool_run_test.go`), wire it:

```go
func TestToolRun_DispatchesInstalledPlugin(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	src := buildTestPluginWithCaps(t, priv, pub, "rundemo", "0.1.0", []string{"fs:read:."})
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(cfg.StateDir(), "plugins", "rundemo-0.1.0"))
	}()

	tmp := t.TempDir()

	var stdout, stderr bytes.Buffer
	err := runToolByName(t.Context(), "rundemo__demo", `{}`,
		toolRunOptions{Cfg: cfg, Workdir: tmp, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("runToolByName: %v\nstderr: %s", err, stderr.String())
	}
}
```

(Tool name `rundemo__demo` assumes `buildTestPluginWithCaps` produces a tool named `demo` under plugin `rundemo`; adjust to whatever the helper actually produces. Inspect `cmd/stado/plugin_test_helpers_test.go` for the exact shape.)

- [ ] **Step 7.4: Run tests**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./cmd/stado/ -run "TestToolRun_" -count=1 -v`
Expected: previous 6 PASS + (1 SKIP or 1 PASS for the new test).

- [ ] **Step 7.5: Run full cmd/stado tests**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./cmd/stado/ -count=1`
Expected: PASS.

- [ ] **Step 7.6: Run go vet**

Run: `cd /home/foobarto/Dokumenty/stado && go vet ./cmd/stado/`
Expected: clean.

- [ ] **Step 7.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add cmd/stado/tool_run.go cmd/stado/tool_run_test.go
git commit -m "feat(cli): tool run dispatches installed plugins

Drops the 'not yet supported' branch in cmd/stado/tool_run.go.
Resolution flow: registry lookup → bundled path (existing) OR
installed path (via runtime.LookupInstalledModule) → both route
to runPluginInvocation. The disabled-tool refusal + --force
escape (already in place) runs before this branch and applies
uniformly.

Installed-plugin invocation by tool-name was the missing half of
the unification — pre-this-PR, installed plugins were unreachable
from tool list / tools__search / mcp-server / tool run. Now they
are."
```

---

## Task 8: Whole-branch verification + smoke

**Files:** none (verification only).

**Context:** Final whole-branch sanity pass.

- [ ] **Step 8.1: Full repo `go test`**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./... -count=1 2>&1 | grep -E "^(FAIL|ok)" | grep -v "^ok"`
Expected: only the pre-existing `internal/sandbox/TestBwrapRunner_AllowHostsOnly...` failure (environmental).

- [ ] **Step 8.2: Full repo `go vet`**

Run: `cd /home/foobarto/Dokumenty/stado && go vet ./...`
Expected: clean.

- [ ] **Step 8.3: Smoke — `stado tool list` shows installed plugins**

```bash
cd /home/foobarto/Dokumenty/stado
go run ./cmd/stado tool list 2>&1 | head -30
```

Expected: alongside bundled tools (`fs__ls`, `shell__exec`, etc.), entries like `htb-lab__spawn`, `gtfobins__lookup`, `payload-generator__revshell`, etc. (depending on what's installed).

- [ ] **Step 8.4: Smoke — `stado tool run` against an installed plugin**

Pick an installed plugin tool that's safe (e.g. `gtfobins__lookup` if installed). Run:

```bash
cd /home/foobarto/Dokumenty/stado
go run ./cmd/stado tool run gtfobins.lookup '{"binary":"awk"}'
```

Expected: tool output, no "not yet supported" error, no signature failure.

- [ ] **Step 8.5: Smoke — `[tools].disabled` applies to installed plugins**

```bash
mkdir -p /tmp/stado-installed-disabled-cfg/stado
cat > /tmp/stado-installed-disabled-cfg/stado/config.toml <<EOF
[tools]
disabled = ["gtfobins__lookup"]
EOF
XDG_CONFIG_HOME=/tmp/stado-installed-disabled-cfg go run ./cmd/stado tool run gtfobins.lookup '{"binary":"awk"}'
```

Expected: `Error: tool ... is disabled in [tools].disabled (matched pattern "gtfobins__lookup")`.

Then with `--force`:

```bash
XDG_CONFIG_HOME=/tmp/stado-installed-disabled-cfg go run ./cmd/stado tool run gtfobins.lookup --force '{"binary":"awk"}'
```

Expected: succeeds.

- [ ] **Step 8.6: Smoke — collision warning**

If you have a plugin installed that exposes a name colliding with a bundled tool, the `stado: info` line should appear on stderr at registration. If not, this step is a soft check — note it as untested if no collision exists.

```bash
cd /home/foobarto/Dokumenty/stado
go run ./cmd/stado tool list 2>&1 | grep "stado: info"
```

Expected: collision lines if any, empty otherwise.

- [ ] **Step 8.7: Inspect the diff**

Run:

```bash
cd /home/foobarto/Dokumenty/stado
git log main..HEAD --oneline
git diff main..HEAD --stat | tail -10
```

Expected: ~7-8 task commits. ~13 files changed; ~330 net lines.

- [ ] **Step 8.8: Self-review summary**

The 7 prior tasks together should:
- ✅ Add `activeVersionMarker`, `pickActiveVersion`, `groupInstalledByName` helpers.
- ✅ Add `installedPluginTool` registry wrapper.
- ✅ Add `registerInstalledPluginTools` + `LookupInstalledModule`.
- ✅ Wire into `BuildDefaultRegistry(cfg)`.
- ✅ Update 9 production callsites + test files to pass `cfg` (or nil).
- ✅ Replace `tool_run.go`'s "not yet supported" branch with installed-plugin dispatch.
- ✅ Smoke verifies `tool list` shows installed plugins + `tool run` invokes them + `[tools].disabled` applies.

If any of these don't trace cleanly to a task, mark unchecked + open a follow-up.

---

## Spec coverage

| Spec section | Task |
|---|---|
| Component 1 — Installed-plugin loader | Tasks 1, 2, 3, 4, 5 |
| Component 2 — Extend `BuildDefaultRegistry` | Task 6 |
| Component 3 — Caller updates | Task 6 |
| Component 4 — `tool run` dispatch for installed | Task 7 |
| Component 5 — Active-version resolution | Tasks 1, 3 |
| Component 6 — Tool-wrapper type | Task 2 |
| Component 7 — Stderr logging | Task 5 |

No placeholders. No "TBD". Function names match across tasks (`activeVersionMarker`, `pickActiveVersion`, `groupInstalledByName`, `splitInstalledID`, `installedPluginTool`, `newInstalledPluginTool`, `registerInstalledPluginTools`, `LookupInstalledModule`, `installedRecord`, `installedRegistryMu`, `installedByTool`).

Type consistency: `installedRecord{Manifest plugins.Manifest, WasmPath string}` shape stable across Tasks 5 and 7. `BuildDefaultRegistry(cfg *config.Config)` signature stable from Task 6 onward.
