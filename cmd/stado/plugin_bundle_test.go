package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadBundleFile_Roundtrip: write a small bundle.toml, load it,
// verify the parsed shape.
func TestLoadBundleFile_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.toml")
	content := `output = "stado-custom"
allow_unsigned = false

[[plugin]]
name = "htb-lab"

[[plugin]]
name = "gtfobins"
version = "0.1.0"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	bf, err := loadBundleFile(path)
	if err != nil {
		t.Fatalf("loadBundleFile: %v", err)
	}
	if bf.Output != "stado-custom" {
		t.Errorf("Output = %q, want stado-custom", bf.Output)
	}
	if len(bf.Plugins) != 2 {
		t.Fatalf("Plugins count = %d, want 2", len(bf.Plugins))
	}
	if !strings.EqualFold(bf.Plugins[1].Version, "0.1.0") {
		t.Errorf("Plugin[1].Version = %q, want 0.1.0", bf.Plugins[1].Version)
	}
}
