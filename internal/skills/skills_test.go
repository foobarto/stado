package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_ParsesFrontmatter: a canonical skill file yields Name,
// Description, and a body with the frontmatter stripped.
func TestLoad_ParsesFrontmatter(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: refactor
description: Extract a function
---
find dup
factor out`
	if err := os.WriteFile(filepath.Join(dir, "refactor.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(got))
	}
	if got[0].Name != "refactor" {
		t.Errorf("Name = %q", got[0].Name)
	}
	if got[0].Description != "Extract a function" {
		t.Errorf("Description = %q", got[0].Description)
	}
	if got[0].Body != "find dup\nfactor out" {
		t.Errorf("Body = %q", got[0].Body)
	}
}

// TestLoad_FilenameFallback: a skill file without frontmatter falls
// back to the filename stem for the name. The whole file is body.
func TestLoad_FilenameFallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "just-body.md"), []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "just-body" {
		t.Fatalf("expected single skill named 'just-body'; got %+v", got)
	}
	if got[0].Body != "do the thing\n" {
		t.Errorf("Body = %q", got[0].Body)
	}
}

// TestLoad_NearestWins: a monorepo with the same skill name at two
// levels resolves to the nearer one. Mirrors internal/instructions's
// resolution policy so users don't have to learn two rules.
func TestLoad_NearestWins(t *testing.T) {
	root := t.TempDir()
	rootSk := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(rootSk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootSk, "share.md"), []byte("root body"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg", "mod")
	subSk := filepath.Join(sub, ".stado", "skills")
	if err := os.MkdirAll(subSk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subSk, "share.md"), []byte("module body"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "module body" {
		t.Errorf("nearest-wins failed; got %+v", got)
	}
}

// TestLoad_NoSkillsIsNotAnError: a cwd without .stado/skills returns
// an empty slice and nil error. Users shouldn't get a "whoops" just
// for having no skills.
func TestLoad_NoSkillsIsNotAnError(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("no-skills should not error; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice; got %+v", got)
	}
}
