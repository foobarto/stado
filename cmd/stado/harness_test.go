package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSecurityHarness_RejectsSymlink: a symlinked .stado/harness/security.md
// must not be followed into an arbitrary local file (exfil guard, #039).
func TestLoadSecurityHarness_RejectsSymlink(t *testing.T) {
	wd := t.TempDir()
	hdir := filepath.Join(wd, ".stado", "harness")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-LOCAL-FILE-CONTENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(hdir, "security.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got := loadSecurityHarness(wd)
	if strings.Contains(got, "SECRET-LOCAL-FILE-CONTENTS") {
		t.Fatal("symlinked harness file was followed — exfil")
	}
	if got != securityHarnessBuiltin {
		t.Errorf("expected builtin harness when override is a symlink")
	}

	// Control: a regular override is honored.
	if err := os.Remove(filepath.Join(hdir, "security.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "security.md"), []byte("CUSTOM HARNESS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSecurityHarness(wd); got != "CUSTOM HARNESS" {
		t.Errorf("regular override should be honored, got %q", got)
	}
}
