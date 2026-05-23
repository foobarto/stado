package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
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
	tl := newInstalledPluginTool(mf, mf.Tools[0], "/nonexistent/wasm/path", tool.ClassNonMutating, nil, nil)
	if tl.Name() != "lookup" {
		t.Errorf("Name() = %q, want lookup", tl.Name())
	}
	if tl.Description() != "Lookup a thing" {
		t.Errorf("Description() = %q, want 'Lookup a thing'", tl.Description())
	}
	// Schema returns parsed map; just verify it's non-nil.
	if tl.Schema() == nil {
		t.Error("Schema() returned nil")
	}
}

// TestInstalledPluginTool_RunDispatchesViaPluginrun: Step 0.1 changed
// installedPluginTool.Run from a sentinel-error returner to a real
// invoker that dispatches via pluginrun.Run. Verify the new contract:
// the call returns a real error when prerequisites aren't met (here,
// the wasm path doesn't exist on disk so verification fails), not the
// pre-Step-0.1 "not invokable directly" sentinel string.
func TestInstalledPluginTool_RunDispatchesViaPluginrun(t *testing.T) {
	mf := plugins.Manifest{
		Name: "test-plugin", Version: "v0.1.0",
		Tools: []plugins.ToolDef{{Name: "lookup"}},
	}
	tl := newInstalledPluginTool(mf, mf.Tools[0], "/nonexistent", tool.ClassNonMutating, nil, nil)
	res, err := tl.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Run() with nonexistent wasm path should error")
	}
	if res.Error == "" {
		t.Error("Run() should populate Result.Error alongside the returned error")
	}
	// The pre-Step-0.1 sentinel string MUST NOT appear — its presence
	// would mean we're back to the broken state where agent loop / MCP
	// server callers silently fail for installed plugins.
	if got := res.Error; strings.Contains(got, "not invokable directly via Tool.Run") {
		t.Errorf("Result.Error still contains pre-Step-0.1 sentinel string: %q", got)
	}
}

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
	// Also verify no-v form (matches real install-dir convention).
	got3 := pickActiveVersion(dir, "fs", []string{"0.1.0", "0.10.0", "0.2.0"})
	if got3 != "0.10.0" {
		t.Errorf("pickActiveVersion no-v form = %q, want 0.10.0", got3)
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

// TestGroupInstalledByName_GroupsAndSkips: name-version dirs are
// grouped; non-matching entries (active/ subdir, files, malformed
// names) are skipped.
func TestGroupInstalledByName_GroupsAndSkips(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	for _, sub := range []string{
		"fs-v0.1.0", "fs-v0.2.0", "shell-v1.0.0", // v-prefixed
		"agent-1.0.0", // no-v form (real install convention)
		"active",      // metadata dir; must be skipped
		"no-dash",     // malformed name; must be skipped
	} {
		_ = os.MkdirAll(filepath.Join(pluginsDir, sub), 0o755)
	}
	_ = os.WriteFile(filepath.Join(pluginsDir, "stray.txt"), []byte("ignore"), 0o644)

	got, err := groupInstalledByName(pluginsDir)
	if err != nil {
		t.Fatalf("groupInstalledByName: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 groups, got %d: %+v", len(got), got)
	}
	if len(got["fs"]) != 2 {
		t.Errorf("fs versions = %v, want 2 entries", got["fs"])
	}
	if len(got["shell"]) != 1 {
		t.Errorf("shell versions = %v, want 1 entry", got["shell"])
	}
	if len(got["agent"]) != 1 || got["agent"][0] != "1.0.0" {
		t.Errorf("agent versions = %v, want [1.0.0]", got["agent"])
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
// preceding a digit.
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

// TestRegisterInstalledPluginTools_NoPluginsDirNoOp: registry stays
// empty when nothing is installed.
func TestRegisterInstalledPluginTools_NoPluginsDirNoOp(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, cfg)
	if got := len(reg.All()); got != 0 {
		t.Errorf("expected empty registry; got %d tools", got)
	}
}

// TestRegisterInstalledPluginTools_NilCfgNoOp: nil config is a
// silent no-op (matches BuildDefaultRegistry's nil-cfg contract).
func TestRegisterInstalledPluginTools_NilCfgNoOp(t *testing.T) {
	reg := tools.NewRegistry()
	registerInstalledPluginTools(reg, nil)
	if got := len(reg.All()); got != 0 {
		t.Errorf("expected empty registry on nil cfg; got %d tools", got)
	}
}

// TestLookupInstalledModule_NotFound: looking up a tool that
// hasn't been registered returns ok=false.
func TestLookupInstalledModule_NotFound(t *testing.T) {
	// Reset the package-level state so prior tests don't leak.
	installedRegistryMu.Lock()
	installedByTool = map[string]installedRecord{}
	installedRegistryMu.Unlock()

	if _, _, ok := LookupInstalledModule("nope__missing"); ok {
		t.Error("LookupInstalledModule for unknown tool should be ok=false")
	}
}

// TestResolveInstalledPluginDir_BareName: bare-name input resolves
// to the active version's install dir.
func TestResolveInstalledPluginDir_BareName(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pluginsDir := filepath.Join(dataDir, "stado", "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "gtfobins-0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	dir, ok := ResolveInstalledPluginDir(cfg, "gtfobins")
	if !ok {
		t.Fatalf("ResolveInstalledPluginDir returned ok=false; want true")
	}
	if dir != filepath.Join(pluginsDir, "gtfobins-0.1.0") {
		t.Errorf("dir = %q, want gtfobins-0.1.0 install path", dir)
	}
}

// TestResolveInstalledPluginDir_PrefersMarker: when an active-version
// marker is set, the resolver returns that version's dir even if a
// higher version is installed.
func TestResolveInstalledPluginDir_PrefersMarker(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pluginsDir := filepath.Join(dataDir, "stado", "plugins")
	for _, sub := range []string{"foo-0.1.0", "foo-0.2.0"} {
		_ = os.MkdirAll(filepath.Join(pluginsDir, sub), 0o755)
	}
	activeDir := filepath.Join(pluginsDir, "active")
	_ = os.MkdirAll(activeDir, 0o755)
	_ = os.WriteFile(filepath.Join(activeDir, "foo"), []byte("0.1.0"), 0o644)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	dir, ok := ResolveInstalledPluginDir(cfg, "foo")
	if !ok {
		t.Fatal("ResolveInstalledPluginDir returned ok=false")
	}
	if dir != filepath.Join(pluginsDir, "foo-0.1.0") {
		t.Errorf("dir = %q, want foo-0.1.0 (marker pin)", dir)
	}
}

// TestResolveInstalledPluginDir_NotInstalled: a bare name with no
// matching directory on disk returns ok=false.
func TestResolveInstalledPluginDir_NotInstalled(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, ok := ResolveInstalledPluginDir(cfg, "nope"); ok {
		t.Error("ResolveInstalledPluginDir for missing plugin should be ok=false")
	}
}

// TestResolveInstalledPluginDir_NilCfg: nil cfg returns ok=false
// (mirrors BuildDefaultRegistry's nil-cfg contract).
func TestResolveInstalledPluginDir_NilCfg(t *testing.T) {
	if _, ok := ResolveInstalledPluginDir(nil, "fs"); ok {
		t.Error("ResolveInstalledPluginDir(nil, _) should be ok=false")
	}
}

// Codex C4/Q P2 regression: pre-fix the package globals
// installedRunCfg + installedInvokeReg were re-bound by every
// BuildDefaultRegistry / registerInstalledPluginTools call. When
// `/tool info` triggered an unfiltered registry build, the next
// stado_tool_invoke from any plugin saw the freshly-rebound
// unfiltered globals — silently widening that plugin's nested-invoke
// surface past whatever [tools].enabled / .disabled scoped on the
// session's actual registry.
//
// After fix: each bundled tool (and each pluginOverrideTool) holds
// its own cfg + invokeReg pointers, set at build time by
// BuildDefaultRegistry / ApplyToolOverrides. Two BuildDefaultRegistry
// calls produce two independent sets of bundled tools, each pointing
// at the registry that built them. This test verifies that invariant.
func TestBundledPluginTool_PerBuildRuntimeIsolation(t *testing.T) {
	cfgA := &config.Config{}
	cfgA.Tools.Enabled = []string{"fs.read"}
	cfgB := &config.Config{}
	cfgB.Tools.Enabled = []string{"shell.bash"}

	regA := BuildDefaultRegistry(cfgA)
	regB := BuildDefaultRegistry(cfgB)

	// Pick the same tool name from both registries — fs__read is
	// bundled, so it's a bundledPluginTool in both regs.
	tA, okA := regA.Get("fs__read")
	tB, okB := regB.Get("fs__read")
	if !okA || !okB {
		t.Fatalf("fs__read missing: A=%v B=%v", okA, okB)
	}

	// Bundled tools are wrapped in renamedTool to expose the wire-
	// form name (fs__read instead of the inner bundledPluginTool's
	// own Name()). Unwrap to compare cfg/invokeReg pointers — the
	// per-build isolation has to hold on the inner instance since
	// that's what's wired by BuildDefaultRegistry's setRuntime loop.
	bA := unwrapBundled(t, tA)
	bB := unwrapBundled(t, tB)

	// Per-build isolation: each tool's invokeReg points at the
	// registry that built it, not the most-recently-built registry.
	if bA.invokeReg != regA {
		t.Errorf("regA's fs__read.invokeReg should be regA; got %p (want %p)", bA.invokeReg, regA)
	}
	if bB.invokeReg != regB {
		t.Errorf("regB's fs__read.invokeReg should be regB; got %p (want %p)", bB.invokeReg, regB)
	}
	// Defensive: regA and regB are different pointers, so the two
	// invokeRegs must differ. If they don't, the global is back.
	if bA.invokeReg == bB.invokeReg {
		t.Error("regA and regB bundled tools share an invokeReg — package globals reintroduced")
	}

	// Same per-tool cfg isolation. cfgA and cfgB are distinct
	// pointers; the per-build tools must hold their own.
	if bA.cfg != cfgA {
		t.Errorf("regA's fs__read.cfg should be cfgA; got %p", bA.cfg)
	}
	if bB.cfg != cfgB {
		t.Errorf("regB's fs__read.cfg should be cfgB; got %p", bB.cfg)
	}
}

// unwrapBundled walks past renamedTool wrappers to reach the underlying
// *bundledPluginTool. Bundled tools registered under wire names go
// through newBundledWasmTool → renamedTool{inner: bundledPluginTool},
// so a direct .(*bundledPluginTool) assertion on a registry lookup
// always misses. Fails the test fatally when the inner isn't bundled
// (caller's intent: it must be, for the assertion to be meaningful).
func unwrapBundled(t *testing.T, tl tool.Tool) *bundledPluginTool {
	t.Helper()
	if rt, ok := tl.(*renamedTool); ok {
		tl = rt.inner
	}
	b, ok := tl.(*bundledPluginTool)
	if !ok {
		t.Fatalf("expected *bundledPluginTool after unwrap, got %T", tl)
	}
	return b
}
