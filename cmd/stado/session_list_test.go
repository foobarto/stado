package main

import (
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestSessionList_ShowsLiveZeroTurnSession(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	const id = "live-empty-session"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.WriteUserRepoPin(sess.WorktreePath, sc.UserRepoRoot); err != nil {
		t.Fatal(err)
	}
	if err := runtime.WriteSessionPID(sess.WorktreePath, os.Getpid()); err != nil {
		t.Fatal(err)
	}

	oldAll := sessionListAll
	sessionListAll = false
	t.Cleanup(func() { sessionListAll = oldAll })
	stdout, stderr := captureOutput(t, func() {
		if err := sessionListCmd.RunE(sessionListCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "live") {
		t.Fatalf("live zero-turn session was hidden:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}
