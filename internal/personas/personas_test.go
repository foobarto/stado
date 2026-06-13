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
		"":                      false,
		"a":                     true,
		"prose-writer":          true,
		"prose_writer":          true,
		"PROSE":                 false, // uppercase
		"writer.exe":            false,
		"../escape":             false,
		"writer/sub":            false,
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

// --- per-persona skills/plugins (2026-06-13) ---------------------------------

// TestParsePersona_ScopeKeys: the new additive-scope frontmatter keys
// (tools / skills / plugins) parse, and recommended_tools still parses.
func TestParsePersona_ScopeKeys(t *testing.T) {
	src := `---
name: pentester
recommended_tools: [bash, "ad.*"]
tools: [nmap, "exploit.*"]
skills: [skills/recon.md, ../shared/notes.md]
plugins: [recorder, telemetry]
---
BODY
`
	p, _, err := parsePersona([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bash", "ad.*"}; !equalSlices(p.RecommendedTools, want) {
		t.Errorf("recommended_tools: %v want %v", p.RecommendedTools, want)
	}
	if want := []string{"nmap", "exploit.*"}; !equalSlices(p.Tools, want) {
		t.Errorf("tools: %v want %v", p.Tools, want)
	}
	if want := []string{"skills/recon.md", "../shared/notes.md"}; !equalSlices(p.Skills, want) {
		t.Errorf("skills: %v want %v", p.Skills, want)
	}
	if want := []string{"recorder", "telemetry"}; !equalSlices(p.Plugins, want) {
		t.Errorf("plugins: %v want %v", p.Plugins, want)
	}
}

// TestPersona_EffectiveTools: EffectiveTools = union(Tools, RecommendedTools),
// de-duped, order-stable (Tools first, then RecommendedTools).
func TestPersona_EffectiveTools(t *testing.T) {
	p := Persona{
		Tools:            []string{"nmap", "bash"},
		RecommendedTools: []string{"bash", "ad.*"},
	}
	got := p.EffectiveTools()
	want := []string{"nmap", "bash", "ad.*"}
	if !equalSlices(got, want) {
		t.Errorf("EffectiveTools = %v, want %v", got, want)
	}
}

// TestResolver_ScopeInheritanceUnion: a child persona's effective
// tools/skills/plugins are the union(parent, child), mirroring body
// inheritance (additive up the chain).
func TestResolver_ScopeInheritanceUnion(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfg, personasSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := `---
name: parent
tools: [bash]
recommended_tools: [grep]
skills: [skills/parent.md]
plugins: [recorder]
---
PARENT
`
	child := `---
name: child
inherits: parent
tools: [nmap]
skills: [skills/child.md]
plugins: [telemetry]
---
CHILD
`
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
	// Parent entries first, then child's, deduped.
	if want := []string{"bash", "nmap"}; !equalSlices(p.Tools, want) {
		t.Errorf("inherited tools: %v want %v", p.Tools, want)
	}
	if want := []string{"grep"}; !equalSlices(p.RecommendedTools, want) {
		t.Errorf("inherited recommended_tools: %v want %v", p.RecommendedTools, want)
	}
	if want := []string{"skills/parent.md", "skills/child.md"}; !equalSlices(p.Skills, want) {
		t.Errorf("inherited skills: %v want %v", p.Skills, want)
	}
	if want := []string{"recorder", "telemetry"}; !equalSlices(p.Plugins, want) {
		t.Errorf("inherited plugins: %v want %v", p.Plugins, want)
	}
	// EffectiveTools spans the merged Tools + RecommendedTools.
	eff := p.EffectiveTools()
	for _, name := range []string{"bash", "nmap", "grep"} {
		if !containsStr(eff, name) {
			t.Errorf("EffectiveTools %v missing %q", eff, name)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// TestResolver_RejectsSymlinkedPersona: a symlinked persona file in the
// repo-controlled .stado/personas/ dir must NOT be followed (exfil guard, #013).
func TestResolver_RejectsSymlinkedPersona(t *testing.T) {
	cwd := t.TempDir()
	pdir := filepath.Join(cwd, ".stado", personasSubdir)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("---\nname: evil\n---\nSECRET-LOCAL-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(pdir, "evil.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	r := Resolver{CWD: cwd}
	if _, err := r.Load("evil"); err == nil {
		t.Fatal("symlinked persona must not be followed; expected not-found")
	}

	// Control: a regular file resolves.
	if err := os.WriteFile(filepath.Join(pdir, "good.md"), []byte("---\nname: good\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := r.Load("good"); err != nil || p == nil {
		t.Fatalf("regular persona should load: %v", err)
	}
}

// TestLoadNotFoundMessageCleanAndNamed guards the R6 error-rendering fix:
// a missing persona must produce a single, name-bearing "persona %q: not
// found" — not the old "persona: not found" sentinel that callers re-prefixed
// into "persona: persona: not found" (and triple via resolvePersona).
func TestLoadNotFoundMessageCleanAndNamed(t *testing.T) {
	r := Resolver{CWD: t.TempDir(), ConfigDir: t.TempDir()}
	_, err := r.Load("no-such-role-xyz")
	if err == nil {
		t.Fatal("expected an error for a missing persona")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false (sentinel must still match): %v", err)
	}
	want := `persona "no-such-role-xyz": not found`
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if strings.Count(err.Error(), "persona") != 1 {
		t.Errorf("doubled 'persona' prefix in %q", err.Error())
	}
}
