package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/toolinput"
)

func TestSessionDescribe_RejectsTraversalID(t *testing.T) {
	_, _, restore := describeEnv(t)
	defer restore()

	describeClear = false
	err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{"../../escape", "x"})
	if err == nil {
		t.Fatal("expected traversal id to fail")
	}
	if !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("expected invalid session id error, got %v", err)
	}
}

func TestAgentsKill_RejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(cfg.WorktreeDir(), "..", "..", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = agentsKillCmd.RunE(agentsKillCmd, []string{"../../victim"})
	if err == nil {
		t.Fatal("expected traversal id to fail")
	}
	if !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("expected invalid session id error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Fatalf("victim path was modified: %v", err)
	}
}

func TestAgentsKill_RejectsSymlinkedWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	const id = "agent-root-symlink"
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "worktrees", id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "worktrees", id, "keep.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateHome, "stado")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := agentsKillCmd.RunE(agentsKillCmd, []string{id})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("agents kill error = %v, want symlink rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "worktrees", id, "keep.txt")); err != nil {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestSessionLand_RejectsInvalidBranchName(t *testing.T) {
	for _, branch := range []string{"", "../escape", "bad..name", "bad\nname", `bad\name`} {
		if _, err := branchRefName(branch); err == nil {
			t.Fatalf("branchRefName(%q) succeeded, want error", branch)
		}
	}
	if got, err := branchRefName("feature/hardening"); err != nil {
		t.Fatalf("valid branch rejected: %v", err)
	} else if string(got) != "refs/heads/feature/hardening" {
		t.Fatalf("branch ref = %q", got)
	}
}

func TestPluginRun_RejectsTraversalID(t *testing.T) {
	_ = isolatedHome(t)
	err := pluginRunCmd.RunE(pluginRunCmd, []string{"../escape", "compact"})
	if err == nil {
		t.Fatal("expected traversal plugin id to fail")
	}
	if !strings.Contains(err.Error(), "invalid plugin id") {
		t.Fatalf("expected invalid plugin id error, got %v", err)
	}
}

func TestPluginRun_RejectsOversizedArgsBeforePluginLookup(t *testing.T) {
	_ = isolatedHome(t)
	err := pluginRunCmd.RunE(pluginRunCmd, []string{
		"missing-0.0.0",
		"compact",
		strings.Repeat("x", toolinput.MaxBytes+1),
	})
	if err == nil {
		t.Fatal("expected oversized args to fail")
	}
	if !strings.Contains(err.Error(), "tool input exceeds") {
		t.Fatalf("expected tool input error, got %v", err)
	}
}
