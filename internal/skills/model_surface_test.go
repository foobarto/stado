package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_DirectorySKILLmd(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, ".stado", "skills", "summarize")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "Run ${STADO_SKILL_DIR}/scripts/check.sh before summarizing."
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte(`---
name: summarize
description: Summarize git changes
when_to_use: user asks for a changelog or summary
---
`+body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1: %+v", len(got), got)
	}
	sk := got[0]
	if sk.Name != "summarize" {
		t.Errorf("name = %q", sk.Name)
	}
	if sk.Dir != bundle {
		t.Errorf("Dir = %q want %q", sk.Dir, bundle)
	}
	if sk.Scope != ScopeProject {
		t.Errorf("Scope = %v want project", sk.Scope)
	}
	rendered := sk.RenderedBody()
	if !strings.Contains(rendered, bundle+"/scripts/check.sh") {
		t.Errorf("RenderedBody missing expanded dir: %q", rendered)
	}
}

func TestLoad_FlatFileBackCompat(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "refactor.md"), []byte(`---
description: refactor helper
---
Do the refactor.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "refactor" || got[0].Dir != "" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestParse_FrontmatterEP45(t *testing.T) {
	sk := parse(`---
name: deploy
description: ship it
disable-model-invocation: true
user-invocable: false
allowed-tools: fs.read, shell.bash
---
body`)
	if !sk.DisableModelInvocation {
		t.Error("expected disable-model-invocation")
	}
	if sk.UserInvocable {
		t.Error("expected user-invocable false")
	}
	if len(sk.AllowedTools) != 2 || sk.AllowedTools[0] != "fs.read" {
		t.Errorf("allowed-tools: %v", sk.AllowedTools)
	}
}

func TestModelVisible_and_FormatModelListing(t *testing.T) {
	sks := []Skill{
		{Name: "public", Description: "visible"},
		{Name: "secret", Description: "hidden", DisableModelInvocation: true},
	}
	vis := ModelVisible(sks)
	if len(vis) != 1 || vis[0].Name != "public" {
		t.Fatalf("ModelVisible: %+v", vis)
	}
	listing := FormatModelListing(sks)
	if !strings.Contains(listing, "public") || strings.Contains(listing, "secret") {
		t.Fatalf("listing: %q", listing)
	}
	if !strings.Contains(listing, "skills__load") {
		t.Error("listing should mention skills__load")
	}
}

func TestFormatModelListing_LargeCatalogStaysWithinBudget(t *testing.T) {
	sks := make([]Skill, 100)
	for i := range sks {
		sks[i] = Skill{
			Name:        fmt.Sprintf("skill-%03d-%s", i, strings.Repeat("n", 32)),
			Description: strings.Repeat("description ", 20),
		}
	}

	listing := FormatModelListing(sks)
	if len(listing) > maxModelListingBytes {
		t.Fatalf("listing size = %d, want <= %d", len(listing), maxModelListingBytes)
	}
	if !strings.Contains(listing, "skill-000") {
		t.Fatal("budgeted listing lost the catalog prefix")
	}
}

// TestEffective_PreservesPartialOnLoadError: Load returns successfully-parsed
// skills alongside a per-file error for one bad entry; Effective must keep that
// partial catalog (and still merge persona skills) rather than black-holing
// every valid skill on a single bad file. Regression for the Codex P2 where
// Effective returned (nil, err), wiping the model listing + skills__load for
// run/ACP/headless/subagent whenever any skill file was oversized/unreadable.
func TestEffective_PreservesPartialOnLoadError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("---\nname: good\ndescription: ok\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Oversized file → Load reports an error for it but still returns `good`.
	if err := os.WriteFile(filepath.Join(dir, "big.md"), make([]byte, (1<<20)+1), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Effective(root, nil, "")
	if err == nil {
		t.Fatal("expected a per-file warning error for the oversized skill")
	}
	if Find(got, "good") == nil {
		t.Fatalf("partial catalog discarded: 'good' missing (got %d skills)", len(got))
	}
}

func TestAllowedToolsEffective_ProjectFailClosed(t *testing.T) {
	project := Skill{Scope: ScopeProject, AllowedTools: []string{"bash"}}
	if project.AllowedToolsEffective() {
		t.Error("project skill allowed-tools must be fail-closed")
	}
	persona := Skill{Scope: ScopePersona, AllowedTools: []string{"bash"}}
	if !persona.AllowedToolsEffective() {
		t.Error("persona skill allowed-tools should apply")
	}
}

func TestEffective_PreservesPersonaSkillsAndReturnsWarnings(t *testing.T) {
	personaDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(personaDir, "good.md"), []byte("---\nname: good\ndescription: valid\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Effective(t.TempDir(), []string{"good.md", "missing.md"}, personaDir)
	if err == nil || !strings.Contains(err.Error(), "missing.md") {
		t.Fatalf("Effective warning = %v, want missing persona skill diagnostic", err)
	}
	if Find(got, "good") == nil {
		t.Fatalf("valid persona skill was discarded: %+v", got)
	}
}
