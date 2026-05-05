package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
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
