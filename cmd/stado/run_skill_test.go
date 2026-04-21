package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_SkillFlagSelectsBodyAsPrompt: --skill <name> loads the
// body of .stado/skills/<name>.md and uses it as the prompt. We
// test the resolution step directly by twiddling runSkill and
// runPrompt + invoking the skills loader (which run.go invokes
// inside RunE); exercising the full RunE would need a live
// provider.
func TestRun_SkillFlagSelectsBodyAsPrompt(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: refactor\ndescription: extract\n---\nRefactor the code\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "refactor.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := chdir(t, root)
	defer restore()

	// Reset globals the test touches so one test's state doesn't
	// bleed into the next.
	runSkill = "refactor"
	runPrompt = ""
	defer func() { runSkill = ""; runPrompt = "" }()

	if err := resolveRunPromptFromFlags(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(runPrompt, "Refactor the code") {
		t.Errorf("expected skill body in runPrompt; got %q", runPrompt)
	}
}

// TestRun_SkillFlagUnknownErrorsWithAvailableList: a --skill that
// doesn't match any loaded skill returns a helpful error that lists
// the skills the user could have meant — avoids the "oh I misspelled
// the name and now I have to grep the filesystem" dance.
func TestRun_SkillFlagUnknownErrorsWithAvailableList(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tidy", "refactor"} {
		body := "---\nname: " + name + "\n---\nbody"
		if err := os.WriteFile(filepath.Join(skillsDir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restore := chdir(t, root)
	defer restore()

	runSkill = "nonexistent"
	runPrompt = ""
	defer func() { runSkill = ""; runPrompt = "" }()

	err := resolveRunPromptFromFlags()
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nonexistent") {
		t.Errorf("error missing skill name: %q", msg)
	}
	if !strings.Contains(msg, "tidy") || !strings.Contains(msg, "refactor") {
		t.Errorf("error missing available names: %q", msg)
	}
}

// TestRun_SkillCombinesWithPrompt: passing both --skill and --prompt
// layers the per-invocation prompt on top of the reusable skill body
// (skill first, prompt appended). Useful for "use this reusable
// skill, but also fix THIS specific bug."
func TestRun_SkillCombinesWithPrompt(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: hello\n---\nBase skill body"
	if err := os.WriteFile(filepath.Join(skillsDir, "hello.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, root)
	defer restore()

	runSkill = "hello"
	runPrompt = "specific question"
	defer func() { runSkill = ""; runPrompt = "" }()

	if err := resolveRunPromptFromFlags(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(runPrompt, "Base skill body") || !strings.Contains(runPrompt, "specific question") {
		t.Errorf("expected both segments; got %q", runPrompt)
	}
	// Ordering: skill first so the model treats the skill as context
	// and the user prompt as the immediate ask.
	skillIdx := strings.Index(runPrompt, "Base skill body")
	promptIdx := strings.Index(runPrompt, "specific question")
	if skillIdx > promptIdx {
		t.Errorf("expected skill before prompt; got %q", runPrompt)
	}
}
