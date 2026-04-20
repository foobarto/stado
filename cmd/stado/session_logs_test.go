package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// logsEnv stands up a session with N tool-call trace commits. Each
// commit carries realistic trailer metadata so the log renderer has
// something to format.
func logsEnv(t *testing.T, calls []stadogit.CommitMeta) (string, *config.Config, func()) {
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
	sc, _ := openSidecar(cfg)
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "logs-test", plumbing.ZeroHash)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	for _, meta := range calls {
		if _, err := sess.CommitToTrace(meta); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	return sess.ID, cfg, restore
}

// TestSessionLogs_RendersToolCalls: each seeded trace commit
// produces a line with the expected fields.
func TestSessionLogs_RendersToolCalls(t *testing.T) {
	id, _, restore := logsEnv(t, []stadogit.CommitMeta{
		{Tool: "grep", ShortArg: "pattern", Summary: "found 12 matches", TokensIn: 100, TokensOut: 20, CostUSD: 0.005, DurationMs: 150},
		{Tool: "read", ShortArg: "main.go", Summary: "file body", TokensIn: 500, TokensOut: 30, CostUSD: 0.01, DurationMs: 80},
	})
	defer restore()

	logsLimit = 0
	out := captureStdout(t, func() {
		if err := sessionLogsCmd.RunE(sessionLogsCmd, []string{id}); err != nil {
			t.Fatalf("logs: %v", err)
		}
	})
	for _, want := range []string{
		"grep", "pattern", "found 12 matches",
		"read", "main.go", "file body",
		"100/20tok", "500/30tok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	// Two commits → two lines.
	if lines := strings.Count(out, "\n"); lines < 2 {
		t.Errorf("expected ≥2 lines, got %d: %q", lines, out)
	}
}

// TestSessionLogs_LimitHonored: --limit N stops early.
func TestSessionLogs_LimitHonored(t *testing.T) {
	calls := make([]stadogit.CommitMeta, 5)
	for i := range calls {
		calls[i] = stadogit.CommitMeta{
			Tool: "grep", Summary: "call-" + itoaLogs(i+1),
		}
	}
	id, _, restore := logsEnv(t, calls)
	defer restore()

	logsLimit = 2
	defer func() { logsLimit = 0 }()
	out := captureStdout(t, func() {
		if err := sessionLogsCmd.RunE(sessionLogsCmd, []string{id}); err != nil {
			t.Fatalf("logs: %v", err)
		}
	})
	if lines := strings.Count(out, "\n"); lines != 2 {
		t.Errorf("--limit 2 should produce 2 lines; got %d: %q", lines, out)
	}
}

// TestSessionLogs_NoTraceRef: session has no trace commits → friendly
// stderr message, exit 0.
func TestSessionLogs_NoTraceRef(t *testing.T) {
	id, _, restore := logsEnv(t, nil)
	defer restore()

	logsLimit = 0
	err := sessionLogsCmd.RunE(sessionLogsCmd, []string{id})
	if err != nil {
		t.Errorf("no-trace should be exit 0, got: %v", err)
	}
}

// TestSessionLogs_ErrorCommitMarksRed: a trace commit carrying an
// Error: trailer gets surfaced with a visible marker. We test the
// content side (the ✗ + error message) rather than colour; colour
// is off in tests (no TTY) anyway.
func TestSessionLogs_ErrorCommitMarksRed(t *testing.T) {
	id, _, restore := logsEnv(t, []stadogit.CommitMeta{
		{Tool: "bash", Summary: "exit 1", Error: "permission denied"},
	})
	defer restore()

	out := captureStdout(t, func() {
		if err := sessionLogsCmd.RunE(sessionLogsCmd, []string{id}); err != nil {
			t.Fatalf("logs: %v", err)
		}
	})
	if !strings.Contains(out, "✗") || !strings.Contains(out, "permission denied") {
		t.Errorf("error commit should show ✗ + message: %q", out)
	}
}

// TestSessionLogs_UnknownSession: bad id → resolver error surfaces.
func TestSessionLogs_UnknownSession(t *testing.T) {
	_, _, restore := logsEnv(t, nil)
	defer restore()

	err := sessionLogsCmd.RunE(sessionLogsCmd, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if !strings.Contains(err.Error(), "logs:") {
		t.Errorf("error should be wrapped by 'logs:': %v", err)
	}
}

// TestSessionLogs_FollowPicksUpNewCommits: initial history dumps,
// then follow-loop sees a newly-committed trace entry and prints it.
// Drives the helpers directly (not the full command) so we can
// control timing.
func TestSessionLogs_FollowPicksUpNewCommits(t *testing.T) {
	id, cfg, restore := logsEnv(t, []stadogit.CommitMeta{
		{Tool: "grep", Summary: "initial"},
	})
	defer restore()

	sc, _ := openSidecar(cfg)
	head, _ := sc.ResolveRef(stadogit.TraceRef(id))
	if head.IsZero() {
		t.Fatal("pre-condition: trace ref should have one commit")
	}

	// Append a second trace commit directly via the sess path.
	// Since we created the session in logsEnv via CreateSession,
	// just CommitToTrace a fresh entry through the sidecar.
	sess, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "read", Summary: "live-append"}); err != nil {
		t.Fatal(err)
	}

	// Verify the follow helper sees it.
	newTip, _ := sc.ResolveRef(stadogit.TraceRef(id))
	if newTip == head {
		t.Fatal("trace ref didn't advance")
	}
	out := captureStdout(t, func() {
		printNewCommitsForward(sc, newTip, head, false)
	})
	if !strings.Contains(out, "live-append") {
		t.Errorf("follow helper missed new commit: %q", out)
	}
}

// itoaLogs — small strconv-free helper matching Summary's needs.
// The test doesn't import strconv to match the production file's
// style in stats.go (which uses its own atoi helpers).
func itoaLogs(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [10]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
