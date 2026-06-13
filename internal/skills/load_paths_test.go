package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadPaths_RelativeRoundTrip: LoadPaths reads skill files named by
// relative path under a base dir and parses them like Load does.
func TestLoadPaths_RelativeRoundTrip(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "skills")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	recon := "---\nname: recon\ndescription: Recon sweep\nslash: recon\n---\nDo the recon.\n"
	if err := os.WriteFile(filepath.Join(sub, "recon.md"), []byte(recon), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := "---\nname: notes\n---\nNotes body.\n"
	if err := os.WriteFile(filepath.Join(base, "notes.md"), []byte(notes), 0o644); err != nil {
		t.Fatal(err)
	}

	got, warnings := LoadPaths(base, []string{"skills/recon.md", "notes.md"})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %d (%+v)", len(got), got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["recon"].Description != "Recon sweep" || byName["recon"].Slash != "recon" {
		t.Errorf("recon parse: %+v", byName["recon"])
	}
	if !strings.Contains(byName["recon"].Body, "Do the recon.") {
		t.Errorf("recon body: %q", byName["recon"].Body)
	}
	if byName["notes"].Name != "notes" {
		t.Errorf("notes parse: %+v", byName["notes"])
	}
	// Path is recorded absolute for error/debug surfaces.
	if !filepath.IsAbs(byName["recon"].Path) {
		t.Errorf("expected absolute Path, got %q", byName["recon"].Path)
	}
}

// TestLoadPaths_UnknownPathWarns: a missing skill path yields a non-fatal
// warning; the valid entries still load.
func TestLoadPaths_UnknownPathWarns(t *testing.T) {
	base := t.TempDir()
	good := "---\nname: good\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(base, "good.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warnings := LoadPaths(base, []string{"good.md", "missing.md"})
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("expected only good to load, got %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "missing.md") {
		t.Fatalf("expected a warning mentioning missing.md, got %v", warnings)
	}
}

// TestLoadPaths_RejectsSymlinkEscape: a skill path that is a symlink (e.g.
// pointing outside the base dir) must NOT be followed — same exfil guard
// the cwd loader applies. The valid sibling still loads.
func TestLoadPaths_RejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("---\nname: evil\n---\nSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(base, "evil.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	good := "---\nname: good\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(base, "good.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warnings := LoadPaths(base, []string{"evil.md", "good.md"})
	for _, s := range got {
		if s.Name == "evil" || strings.Contains(s.Body, "SECRET") {
			t.Fatalf("symlinked skill must not be followed; got %+v", got)
		}
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("expected only good to load, got %+v", got)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning for the rejected symlink")
	}
}

// TestLoadPaths_RejectsTraversalEscape: a "../" path that escapes the base
// dir is rejected (os.Root confinement), valid sibling still loads.
func TestLoadPaths_RejectsTraversalEscape(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "personas")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.md"), []byte("---\nname: outside\n---\nx"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := "---\nname: good\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(base, "good.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warnings := LoadPaths(base, []string{"../outside.md", "good.md"})
	for _, s := range got {
		if s.Name == "outside" {
			t.Fatalf("traversal-escape skill must not load; got %+v", got)
		}
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("expected only good to load, got %+v", got)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning for the rejected traversal path")
	}
}

// TestLoadPaths_EmptyBaseDir: a persona with no on-disk dir (bundled) has
// base="" — LoadPaths returns nothing, no panic.
func TestLoadPaths_EmptyBaseDir(t *testing.T) {
	got, warnings := LoadPaths("", []string{"skills/x.md"})
	if len(got) != 0 {
		t.Fatalf("expected no skills for empty base, got %+v", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for empty base, got %v", warnings)
	}
}
