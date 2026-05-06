package personas

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePersona_NoFrontmatter(t *testing.T) {
	p, body, err := parsePersona([]byte("# Hello\n\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "" || p.Title != "" {
		t.Errorf("expected empty frontmatter; got %+v", p)
	}
	if body != "# Hello\n\nbody" {
		t.Errorf("body: %q", body)
	}
}

func TestParsePersona_WithFrontmatter(t *testing.T) {
	src := `---
name: writer
title: Prose Writer
description: Long-form
collaborators: [editor]
version: 1
---
# Body

Operating manual goes here.
`
	p, body, err := parsePersona([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "writer" || p.Title != "Prose Writer" {
		t.Errorf("frontmatter: %+v", p)
	}
	if len(p.Collaborators) != 1 || p.Collaborators[0] != "editor" {
		t.Errorf("collaborators: %v", p.Collaborators)
	}
	if !strings.HasPrefix(body, "# Body") {
		t.Errorf("body: %q", body)
	}
}

func TestParsePersona_UnclosedFrontmatter(t *testing.T) {
	src := "---\nname: x\n# missing close"
	_, _, err := parsePersona([]byte(src))
	if err == nil {
		t.Error("expected error for unclosed frontmatter")
	}
}

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"":                     false,
		"a":                    true,
		"prose-writer":         true,
		"prose_writer":         true,
		"PROSE":                false, // uppercase
		"writer.exe":           false,
		"../escape":            false,
		"writer/sub":           false,
		strings.Repeat("a", 65): false,
	}
	for in, want := range cases {
		if got := validName(in); got != want {
			t.Errorf("validName(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolver_LoadBundledDefault(t *testing.T) {
	r := Resolver{}
	p, err := r.Load("default")
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if p.Name != "default" {
		t.Errorf("name: %q", p.Name)
	}
	if p.SourcePath != "" {
		t.Errorf("bundled SourcePath should be empty, got %q", p.SourcePath)
	}
	if p.Body == "" {
		t.Error("body empty")
	}
}

func TestResolver_LoadNotFound(t *testing.T) {
	r := Resolver{}
	_, err := r.Load("does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolver_InvalidName(t *testing.T) {
	r := Resolver{}
	_, err := r.Load("../escape")
	if err == nil {
		t.Error("expected error for invalid name")
	}
}

func TestResolver_UserShadowsBundled(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfg, personasSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	override := `---
name: default
title: Override
description: User override
---
USER OVERRIDE BODY
`
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "default.md"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{ConfigDir: cfg}
	p, err := r.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Override" {
		t.Errorf("title: %q (expected Override)", p.Title)
	}
	if !strings.Contains(p.Body, "USER OVERRIDE BODY") {
		t.Errorf("body: %q", p.Body)
	}
	if p.SourcePath == "" {
		t.Error("SourcePath should reflect file path, got empty")
	}
}

func TestResolver_ProjectShadowsUser(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "proj")
	cfg := filepath.Join(tmp, "config")
	for _, d := range []string{
		filepath.Join(cwd, ".stado", personasSubdir),
		filepath.Join(cfg, personasSubdir),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	user := "---\nname: x\n---\nUSER\n"
	proj := "---\nname: x\n---\nPROJ\n"
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "x.md"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".stado", personasSubdir, "x.md"), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{CWD: cwd, ConfigDir: cfg}
	p, err := r.Load("x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "PROJ") {
		t.Errorf("project should shadow user; body: %q", p.Body)
	}
}

func TestResolver_Inheritance(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfg, personasSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := "---\nname: parent\n---\nPARENT BODY\n"
	child := "---\nname: child\ninherits: parent\n---\nCHILD BODY\n"
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "parent.md"), []byte(parent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "child.md"), []byte(child), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{ConfigDir: cfg}
	p, err := r.Load("child")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "PARENT BODY") || !strings.Contains(p.Body, "CHILD BODY") {
		t.Errorf("inheritance merge: %q", p.Body)
	}
	// Parent should appear before child.
	if strings.Index(p.Body, "PARENT BODY") > strings.Index(p.Body, "CHILD BODY") {
		t.Error("parent should appear before child in merged body")
	}
}

func TestResolver_InheritanceCycle(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfg, personasSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	a := "---\nname: a\ninherits: b\n---\nA\n"
	b := "---\nname: b\ninherits: a\n---\nB\n"
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "a.md"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "b.md"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{ConfigDir: cfg}
	_, err := r.Load("a")
	if !errors.Is(err, ErrInheritanceCycle) {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestResolver_List_Dedupes(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfg, personasSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	override := "---\nname: default\n---\nOVERRIDE\n"
	if err := os.WriteFile(filepath.Join(cfg, personasSubdir, "default.md"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{ConfigDir: cfg}
	personas, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range personas {
		if p.Name == "default" {
			count++
			if !strings.Contains(p.Body, "OVERRIDE") {
				t.Errorf("List should return shadowed version; body: %q", p.Body)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one default in list, got %d", count)
	}
}

func TestAssembleSystem(t *testing.T) {
	p := &Persona{Body: "  PERSONA BODY  "}
	got := AssembleSystem(p, "PROJECT", "MEMORY", "EXTRA")
	expected := "PERSONA BODY\n\nPROJECT\n\nMEMORY\n\nEXTRA"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
	// Missing sections elided.
	got = AssembleSystem(p, "", "", "")
	if got != "PERSONA BODY" {
		t.Errorf("only persona: %q", got)
	}
	// Nil persona drops persona section.
	got = AssembleSystem(nil, "PROJECT", "", "")
	if got != "PROJECT" {
		t.Errorf("nil persona: %q", got)
	}
}
