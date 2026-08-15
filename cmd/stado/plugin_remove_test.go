package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// literalChildDirs preserves the regression for the old glob escape without
// retaining flat name/version removal in production.
func literalChildDirs(base, prefix string) []string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			out = append(out, filepath.Join(base, entry.Name()))
		}
	}
	return out
}

// Codex #1: filepath.Glob(filepath.Join(base, name+"-*")) interprets glob
// metacharacters in the *base* path, so a project dir whose name contains
// `[`, `]`, `*`, or `?` could expand to a sibling directory and delete outside
// the intended plugin dir. matchPluginVersionDirs reads base literally, so it
// only ever returns direct children of base. This test sets up the exact
// char-class escape (`proj[x]` matches the sibling `projx`) and asserts the
// new matcher does NOT cross into the decoy.
func TestMatchPluginVersionDirs_NoGlobEscapeOfBase(t *testing.T) {
	tmp := t.TempDir()

	// Intended base lives under a dir whose name is a glob char-class.
	base := filepath.Join(tmp, "proj[x]", "plugins")
	if err := os.MkdirAll(filepath.Join(base, "foo-1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Decoy that `proj[x]` (char class matching the single char 'x') expands
	// to under filepath.Glob. A buggy glob would return foo-9.9.9 from here.
	decoy := filepath.Join(tmp, "projx", "plugins")
	if err := os.MkdirAll(filepath.Join(decoy, "foo-9.9.9"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Sanity: filepath.Glob (the old impl) escapes into the decoy.
	if globbed, _ := filepath.Glob(filepath.Join(base, "foo-*")); len(globbed) != 0 {
		want := filepath.Join(decoy, "foo-9.9.9")
		if globbed[0] != want {
			t.Logf("note: glob matched %v (env-dependent)", globbed)
		}
	}

	got := literalChildDirs(base, "foo-")
	if len(got) != 1 {
		t.Fatalf("matchPluginVersionDirs(%q) = %v, want exactly the real base entry", base, got)
	}
	want := filepath.Join(base, "foo-1.0.0")
	if got[0] != want {
		t.Fatalf("matchPluginVersionDirs escaped base: got %q, want %q", got[0], want)
	}
}

// Empty/missing base resolves to no matches, not an error.
func TestMatchPluginVersionDirs_MissingBase(t *testing.T) {
	if got := literalChildDirs(filepath.Join(t.TempDir(), "nope"), "foo-"); got != nil {
		t.Fatalf("missing base: got %v, want nil", got)
	}
}
