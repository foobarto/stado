package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGithubInstall_WritesWorkflow: install writes a yaml file at the
// default path and populates it with the template body.
func TestGithubInstall_WritesWorkflow(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	githubWorkflowPath = ""
	githubForce = false
	if err := githubInstallCmd.RunE(githubInstallCmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(".github", "workflows", "stado-bot.yml"))
	if err != nil {
		t.Fatalf("read written workflow: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"name: stado-bot",
		"issue_comment:",
		"startsWith(github.event.comment.body, '@stado ')",
		`contains(fromJson('["OWNER","MEMBER","COLLABORATOR"]'), github.event.comment.author_association)`,
		"STADO_DEFAULTS_PROVIDER",
		"STADO_DEFAULTS_MODEL",
		"ANTHROPIC_API_KEY",
		"stado run --prompt",
		"gh api",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("workflow missing %q:\n%s", want, s)
		}
	}
}

// TestGithubInstall_RefusesOverwrite: second install without --force
// should error — protect the user's customised workflow.
func TestGithubInstall_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	githubWorkflowPath = ""
	githubForce = false
	if err := githubInstallCmd.RunE(githubInstallCmd, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	err := githubInstallCmd.RunE(githubInstallCmd, nil)
	if err == nil {
		t.Fatal("second install should error without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention existing file: %v", err)
	}
}

// TestGithubInstall_ForceOverwrites: --force lets a rewrite land.
func TestGithubInstall_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	githubWorkflowPath = ""
	githubForce = false
	if err := githubInstallCmd.RunE(githubInstallCmd, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	// Mutate the file so we can observe the overwrite.
	p := filepath.Join(".github", "workflows", "stado-bot.yml")
	if err := os.WriteFile(p, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	githubForce = true
	defer func() { githubForce = false }()
	if err := githubInstallCmd.RunE(githubInstallCmd, nil); err != nil {
		t.Fatalf("force install: %v", err)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "stale") {
		t.Error("--force should have overwritten")
	}
	if !strings.Contains(string(body), "name: stado-bot") {
		t.Error("--force wrote wrong content")
	}
}

// TestGithubUninstall_RemovesFile: uninstall deletes the installed
// workflow. Missing file is a benign no-op (prints a hint).
func TestGithubUninstall_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	githubWorkflowPath = ""
	githubForce = false
	if err := githubInstallCmd.RunE(githubInstallCmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := githubUninstallCmd.RunE(githubUninstallCmd, nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	p := filepath.Join(".github", "workflows", "stado-bot.yml")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("uninstall should remove %s: %v", p, err)
	}

	// Second uninstall — file already gone — should succeed silently.
	if err := githubUninstallCmd.RunE(githubUninstallCmd, nil); err != nil {
		t.Errorf("uninstall on missing file should no-op, got %v", err)
	}
}
