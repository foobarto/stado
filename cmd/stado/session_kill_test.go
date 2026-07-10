package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestSessionKill_RemovesWorktreeKeepsRefs(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	const id = "kill-target"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Commit something so the session has real history, then assert kill
	// leaves the sidecar refs alone (unlike `session delete`).
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	_, stderr := captureOutput(t, func() {
		if err := sessionKillCmd.RunE(sessionKillCmd, []string{id}); err != nil {
			t.Fatalf("session kill: %v", err)
		}
	})

	if _, err := os.Stat(sess.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed, stat err = %v", err)
	}
	if !strings.Contains(stderr, "killed "+id) {
		t.Fatalf("expected kill confirmation in stderr, got:\n%s", stderr)
	}
	if has, err := sc.SessionHasRefs(id); err != nil || !has {
		t.Fatalf("kill must keep sidecar refs (has=%v err=%v)", has, err)
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
