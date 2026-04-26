package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritePluginStateFileRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "state-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := writePluginStateFileAtomic(filepath.Join(link, "trusted_keys.json"), []byte("[]\n"), 0o600)
	if err == nil {
		t.Fatal("writePluginStateFileAtomic should reject symlinked parent dirs")
	}
	if _, statErr := os.Stat(filepath.Join(target, "trusted_keys.json")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestReadPluginStateFileRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target", "state")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "trusted_keys.json"), []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "state-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := readPluginStateFile(filepath.Join(link, "state", "trusted_keys.json")); err == nil {
		t.Fatal("readPluginStateFile should reject symlinked parent dirs")
	}
}

func TestReadPluginStateFileRejectsFinalSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted_keys.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := readPluginStateFile(path); err == nil {
		t.Fatal("readPluginStateFile should reject final symlinks")
	}
}
