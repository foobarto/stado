package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepend_OnlySecurityMode: the helper every surface (run/TUI/ACP/headless)
// now uses must inject only in security mode, and put the harness BEFORE the
// project prompt. EP-0030.
func TestPrepend_OnlySecurityMode(t *testing.T) {
	base := "PROJECT RULES"
	if got := Prepend(base, "", ""); got != base {
		t.Errorf("empty mode must pass base through unchanged; got %q", got)
	}
	if got := Prepend(base, "", "off"); got != base {
		t.Errorf("mode 'off' must pass base through; got %q", got)
	}
	got := Prepend(base, "", "security")
	if !strings.HasPrefix(got, "# Security-research mode") {
		t.Errorf("security mode must prepend the harness BEFORE the base; got %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, base) {
		t.Error("security mode must retain the base prompt")
	}
}

// TestPrepend_EmptyBase: security mode with no project prompt yields exactly the
// harness (no dangling separator).
func TestPrepend_EmptyBase(t *testing.T) {
	if got := Prepend("", "", "security"); got != SecurityBuiltin {
		t.Error("security mode with empty base must equal the builtin harness")
	}
}

// TestLoadSecurity_ProjectOverride: .stado/harness/security.md overrides the
// built-in; absence falls back to the builtin.
func TestLoadSecurity_ProjectOverride(t *testing.T) {
	dir := t.TempDir()
	hdir := filepath.Join(dir, ".stado", "harness")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "security.md"), []byte("CUSTOM HARNESS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadSecurity(dir); got != "CUSTOM HARNESS" {
		t.Errorf("project override should win; got %q", got)
	}
	if got := LoadSecurity(t.TempDir()); got != SecurityBuiltin {
		t.Error("no override → builtin")
	}
}

// TestLoadSecurity_RejectsSymlink: a symlinked .stado/harness/security.md must
// NOT be followed into an arbitrary local file (exfil guard, #039). Moved from
// cmd/stado when the loader moved to internal/harness (EP-0030).
func TestLoadSecurity_RejectsSymlink(t *testing.T) {
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
	got := LoadSecurity(wd)
	if strings.Contains(got, "SECRET-LOCAL-FILE-CONTENTS") {
		t.Fatal("symlinked harness override was followed — exfil")
	}
	if got != SecurityBuiltin {
		t.Errorf("expected builtin harness when override is a symlink; got %q", got[:min(40, len(got))])
	}
}
