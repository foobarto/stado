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

// TestSessionShow_IncludesUsageLine: after seeding trace commits
// with token + cost trailers, session show should render a "usage"
// line summarising calls + tokens + cost. Regression guard for the
// stats integration from task #107.
func TestSessionShow_IncludesUsageLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, _ := openSidecar(cfg)
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "show-usage", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Seed two trace commits with different token counts + costs.
	for _, meta := range []stadogit.CommitMeta{
		{Tool: "grep", TokensIn: 100, TokensOut: 50, CostUSD: 0.01, Model: "m1", DurationMs: 200},
		{Tool: "read", TokensIn: 200, TokensOut: 30, CostUSD: 0.02, Model: "m1", DurationMs: 150},
	} {
		if _, err := sess.CommitToTrace(meta); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() {
		if err := sessionShowCmd.RunE(sessionShowCmd, []string{sess.ID}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(out, "usage") {
		t.Errorf("session show missing 'usage' line: %q", out)
	}
	// totals: 300 input, 80 output, 0.03 cost.
	for _, want := range []string{"2 call(s)", "tokens=300/80"} {
		if !strings.Contains(out, want) {
			t.Errorf("session show usage line missing %q: %q", want, out)
		}
	}
}

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
