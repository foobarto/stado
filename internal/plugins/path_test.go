package plugins

import (
	"path/filepath"
	"testing"
)

func TestInstalledDir(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "plugins")
	key := "remote-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := InstalledDir(root, key)
	if err != nil {
		t.Fatalf("InstalledDir valid: %v", err)
	}
	want := filepath.Join(root, key)
	if got != want {
		t.Fatalf("InstalledDir = %q, want %q", got, want)
	}
}

func TestInstalledDir_RejectsTraversal(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "plugins")
	for _, id := range []string{"", ".", "..", "../demo", "demo/../x", "demo/x", `demo\x`, "/abs", "demo-1.0.0", "remote-short"} {
		if _, err := InstalledDir(root, id); err == nil {
			t.Fatalf("InstalledDir(%q) unexpectedly succeeded", id)
		}
	}
}
