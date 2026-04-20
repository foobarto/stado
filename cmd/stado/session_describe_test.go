package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// describeEnv stands up a session and returns (id, cfg, restore).
// Same shape as exportEnv in session_export_test.go but kept
// separate so the two test files can evolve independently.
func describeEnv(t *testing.T) (string, *config.Config, func()) {
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
	id := "describe-test"
	if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
		restore()
		t.Fatal(err)
	}
	return id, cfg, restore
}

// TestSessionDescribe_WriteThenRead: describe <id> "text" writes the
// description; reading (describe <id> without text) prints it.
func TestSessionDescribe_WriteThenRead(t *testing.T) {
	id, cfg, restore := describeEnv(t)
	defer restore()

	describeClear = false
	if err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{id, "react refactor session"}); err != nil {
		t.Fatalf("describe write: %v", err)
	}

	// Read back via the runtime helper (same data path).
	wt := filepath.Join(cfg.WorktreeDir(), id)
	got := runtime.ReadDescription(wt)
	if got != "react refactor session" {
		t.Errorf("ReadDescription = %q, want 'react refactor session'", got)
	}
}

// TestSessionDescribe_Clear: --clear removes the description.
func TestSessionDescribe_Clear(t *testing.T) {
	id, cfg, restore := describeEnv(t)
	defer restore()

	_ = sessionDescribeCmd.RunE(sessionDescribeCmd, []string{id, "initial"})
	wt := filepath.Join(cfg.WorktreeDir(), id)
	if got := runtime.ReadDescription(wt); got != "initial" {
		t.Fatalf("pre-condition: description = %q, want 'initial'", got)
	}

	describeClear = true
	defer func() { describeClear = false }()
	if err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{id}); err != nil {
		t.Fatalf("describe clear: %v", err)
	}
	if got := runtime.ReadDescription(wt); got != "" {
		t.Errorf("after --clear: description = %q, want empty", got)
	}
}

// TestSessionDescribe_UnknownSession: missing worktree errors out
// with a clear not-found message.
func TestSessionDescribe_UnknownSession(t *testing.T) {
	_, _, restore := describeEnv(t)
	defer restore()

	describeClear = false
	err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{"does-not-exist", "whatever"})
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found: %v", err)
	}
}

// TestSessionDescribe_EmptyTextErrors: passing empty-string text
// without --clear is a user error (hint to use --clear).
func TestSessionDescribe_EmptyTextErrors(t *testing.T) {
	id, _, restore := describeEnv(t)
	defer restore()

	describeClear = false
	err := sessionDescribeCmd.RunE(sessionDescribeCmd, []string{id, "   "})
	if err == nil {
		t.Fatal("expected error for empty text")
	}
	if !strings.Contains(err.Error(), "--clear") {
		t.Errorf("error should point at --clear: %v", err)
	}
}

// TestSessionDescribe_SurfacedInList: SummariseSession carries the
// description into SessionSummary so the list row shows it.
func TestSessionDescribe_SurfacedInList(t *testing.T) {
	id, cfg, restore := describeEnv(t)
	defer restore()

	describeClear = false
	_ = sessionDescribeCmd.RunE(sessionDescribeCmd, []string{id, "my custom label"})

	sc, _ := openSidecar(cfg)
	summary := runtime.SummariseSession(cfg.WorktreeDir(), sc, id)
	if summary.Description != "my custom label" {
		t.Errorf("SessionSummary.Description = %q, want 'my custom label'", summary.Description)
	}
}
