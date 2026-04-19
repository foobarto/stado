package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// captureStdout runs fn and returns what it printed to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- buf
	}()

	fn()
	w.Close()
	os.Stdout = orig
	return string(<-done)
}

// TestSessionShow_EnrichedOutput builds a session with 2 turns + a
// trace commit + verifies `session show` emits turn count, latest-turn
// line, and audit line.
func TestSessionShow_EnrichedOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatalf("openSidecar: %v", err)
	}

	id := "show-fixture"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	writeWorkFile(t, sess.WorktreePath, "a.txt", "v1\n")
	_ = commitAndTag(t, sess, 1)
	writeWorkFile(t, sess.WorktreePath, "b.txt", "v1\n")
	_ = commitAndTag(t, sess, 2)

	// Also drop a trace commit so the audit row has something to count.
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{
		Tool: "read", ShortArg: "a.txt", Summary: "read a.txt",
	}); err != nil {
		t.Fatalf("CommitToTrace: %v", err)
	}

	var buf bytes.Buffer
	out := captureStdout(t, func() {
		sessionShowCmd.SetArgs([]string{id})
		if err := sessionShowCmd.RunE(sessionShowCmd, []string{id}); err != nil {
			buf.WriteString("run error: " + err.Error())
		}
	})

	for _, want := range []string{
		"session:  " + id,
		"worktree:",
		"tree",
		"trace",
		"turns     2",
		"latest    turns/2",
		"audit     1 tool call",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("session show output missing %q\n---\n%s", want, out)
		}
	}
}

// TestSessionShow_FreshSessionIsGraceful: a never-committed session
// prints (unset) and 0 turns without erroring.
func TestSessionShow_FreshSessionIsGraceful(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	out := captureStdout(t, func() {
		sessionShowCmd.SetArgs([]string{"nonexistent"})
		_ = sessionShowCmd.RunE(sessionShowCmd, []string{"nonexistent"})
	})
	if !strings.Contains(out, "session:  nonexistent") {
		t.Errorf("missing session line: %q", out)
	}
	if !strings.Contains(out, "(unset)") {
		t.Errorf("fresh session should report (unset) refs: %q", out)
	}
	_ = cfg
}
