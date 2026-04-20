package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// gcEnv builds a clean-room config + sidecar for the gc tests.
// Returns cfg, sidecar, and a chdir cleanup so tests can call
// sessionGCCmd.RunE directly.
func gcEnv(t *testing.T) (*config.Config, *stadogit.Sidecar, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	return cfg, sc, restore
}

// createZeroTurnSession creates a session, backdates its worktree
// mtime, and returns its id. No turns, no messages, no compactions —
// a clean gc candidate.
func createZeroTurnSession(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, age time.Duration) string {
	t.Helper()
	id := "gc-candidate-" + t.Name() + "-" + strings.ReplaceAll(age.String(), " ", "-")
	if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(cfg.WorktreeDir(), id)
	past := time.Now().Add(-age)
	_ = os.Chtimes(wt, past, past)
	return id
}

// TestSessionGC_DryRunListsCandidates: by default (no --apply) gc
// should list candidates without deleting them.
func TestSessionGC_DryRunListsCandidates(t *testing.T) {
	cfg, sc, restore := gcEnv(t)
	defer restore()

	id := createZeroTurnSession(t, cfg, sc, 48*time.Hour)

	sessionGCOlderThan = 24 * time.Hour
	sessionGCApply = false
	if err := sessionGCCmd.RunE(sessionGCCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	// Worktree should still exist after dry-run.
	if _, err := os.Stat(filepath.Join(cfg.WorktreeDir(), id)); err != nil {
		t.Errorf("dry-run deleted worktree: %v", err)
	}
	// CreateSession alone leaves no refs — the ref-presence check isn't
	// meaningful here (SessionHasRefs is false either way). The worktree
	// stat above is the real signal that dry-run is non-destructive.
	_ = sc
}

// TestSessionGC_ApplyActuallyDeletes: --apply moves beyond listing
// and removes worktree + refs.
func TestSessionGC_ApplyActuallyDeletes(t *testing.T) {
	cfg, sc, restore := gcEnv(t)
	defer restore()

	id := createZeroTurnSession(t, cfg, sc, 48*time.Hour)

	sessionGCOlderThan = 24 * time.Hour
	sessionGCApply = true
	defer func() { sessionGCApply = false }()
	if err := sessionGCCmd.RunE(sessionGCCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorktreeDir(), id)); !os.IsNotExist(err) {
		t.Errorf("apply should have removed worktree, got %v", err)
	}
	had, _ := sc.SessionHasRefs(id)
	if had {
		t.Error("apply should have removed refs")
	}
}

// TestSessionGC_SkipsYoungSessions: a session worktree touched within
// the threshold must NOT be swept — protects against GC racing a
// freshly-born `run --prompt` or `session new` call.
func TestSessionGC_SkipsYoungSessions(t *testing.T) {
	cfg, sc, restore := gcEnv(t)
	defer restore()

	id := createZeroTurnSession(t, cfg, sc, 1*time.Hour) // young

	sessionGCOlderThan = 24 * time.Hour
	sessionGCApply = true
	defer func() { sessionGCApply = false }()
	if err := sessionGCCmd.RunE(sessionGCCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorktreeDir(), id)); err != nil {
		t.Errorf("young session was swept: %v", err)
	}
}

// TestSessionGC_SkipsSessionsWithWork: sessions that committed a turn
// or left a conversation message shouldn't be swept regardless of age.
func TestSessionGC_SkipsSessionsWithWork(t *testing.T) {
	cfg, sc, restore := gcEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "gc-worked", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Produce a turn by committing something + tagging.
	writeWorkFile(t, sess.WorktreePath, "x.txt", "hi\n")
	_ = commitAndTag(t, sess, 1)
	// Backdate worktree aggressively so age wouldn't save it.
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(sess.WorktreePath, past, past)

	sessionGCOlderThan = 24 * time.Hour
	sessionGCApply = true
	defer func() { sessionGCApply = false }()
	if err := sessionGCCmd.RunE(sessionGCCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(sess.WorktreePath); err != nil {
		t.Errorf("session with work was swept: %v", err)
	}
}
