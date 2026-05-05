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
