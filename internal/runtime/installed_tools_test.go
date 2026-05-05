package runtime

import (
	"context"
	"os"
	"path/filepath"
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
	tl := newInstalledPluginTool(mf, mf.Tools[0], "/nonexistent/wasm/path", tool.ClassNonMutating)
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

// TestInstalledPluginTool_RunReturnsSentinel: direct .Run() returns
// a sentinel Result with Error populated since installed-plugin
// invocation goes through tool_run's shared helper, not the
// registry's Tool interface.
func TestInstalledPluginTool_RunReturnsSentinel(t *testing.T) {
	mf := plugins.Manifest{
		Name: "test-plugin", Version: "v0.1.0",
		Tools: []plugins.ToolDef{{Name: "lookup"}},
	}
	tl := newInstalledPluginTool(mf, mf.Tools[0], "/nonexistent", tool.ClassNonMutating)
	res, err := tl.Run(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("Run() error = %v, want nil (returns Result.Error instead)", err)
	}
	if res.Error == "" {
		t.Error("Run() should populate Result.Error with sentinel message")
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
