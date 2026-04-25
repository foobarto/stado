package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestAgentsList_HidesStaleEmptyByDefault(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "agent-live", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.WorktreeDir(), "agent-stale"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := agentsListAll
	agentsListAll = false
	t.Cleanup(func() { agentsListAll = prev })

	stdout, stderr := captureOutput(t, func() {
		if err := agentsListCmd.RunE(agentsListCmd, nil); err != nil {
			t.Fatalf("agents list: %v", err)
		}
	})

	if !strings.Contains(stdout, "agent-live\t-\t") {
		t.Fatalf("expected live agent row in stdout, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "agent-stale") {
		t.Fatalf("stale agent should be hidden by default, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "1 stale/empty agent(s) hidden") {
		t.Fatalf("expected hidden footer in stderr, got:\n%s", stderr)
	}
}

func TestAgentsList_AllIncludesStaleRows(t *testing.T) {
	cfg, _, restore := statsEnv(t)
	defer restore()

	if err := os.MkdirAll(filepath.Join(cfg.WorktreeDir(), "agent-stale"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := agentsListAll
	agentsListAll = true
	t.Cleanup(func() { agentsListAll = prev })

	stdout, stderr := captureOutput(t, func() {
		if err := agentsListCmd.RunE(agentsListCmd, nil); err != nil {
			t.Fatalf("agents list --all: %v", err)
		}
	})

	if !strings.Contains(stdout, "agent-stale\t-\ttree=-\ttrace=-") {
		t.Fatalf("expected stale row in stdout, got:\n%s", stdout)
	}
	if strings.Contains(stderr, "hidden") {
		t.Fatalf("did not expect hidden footer with --all, got:\n%s", stderr)
	}
}

func TestAgentsAttach_PrintsWorktreePath(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	const id = "agent-attach"
	if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := agentsAttachCmd.RunE(agentsAttachCmd, []string{id}); err != nil {
			t.Fatalf("agents attach: %v", err)
		}
	})

	want := filepath.Join(cfg.WorktreeDir(), id)
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("attach output = %q, want %q", got, want)
	}
}

func TestAgentsKill_RemovesWorktree(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	const id = "agent-kill"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	_, stderr := captureOutput(t, func() {
		if err := agentsKillCmd.RunE(agentsKillCmd, []string{id}); err != nil {
			t.Fatalf("agents kill: %v", err)
		}
	})

	if _, err := os.Stat(sess.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed, stat err = %v", err)
	}
	if !strings.Contains(stderr, "killed "+id) {
		t.Fatalf("expected kill confirmation in stderr, got:\n%s", stderr)
	}
}

func TestReadPidFileRejectsSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "pid")
	if err := os.WriteFile(outside, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(worktree, ".stado-pid")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if got := readPidFile(worktree); got != 0 {
		t.Fatalf("readPidFile followed symlink escape: %d", got)
	}
}
