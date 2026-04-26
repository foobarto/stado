package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginGenKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.seed")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin.seed")
	if err := os.Symlink("decoy.seed", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := pluginGenKeyCmd.RunE(pluginGenKeyCmd, []string{path}); err == nil {
		t.Fatal("plugin gen-key should reject symlinked seed path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}
