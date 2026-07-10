//go:build darwin

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestSessionKill_DarwinFailsClosedAndPreservesWorktree(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "kill-darwin-safe", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.WriteSessionPID(sess.WorktreePath, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	err = sessionKillCmd.RunE(sessionKillCmd, []string{sess.ID})
	if err == nil || !strings.Contains(err.Error(), "unavailable on darwin") {
		t.Fatalf("session kill error = %v", err)
	}
	if _, statErr := os.Stat(sess.WorktreePath); statErr != nil {
		t.Fatalf("worktree must be preserved: %v", statErr)
	}
}
