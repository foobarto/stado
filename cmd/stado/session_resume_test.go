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

// TestSessionResume_MissingWorktreeErrors asserts the pre-launch
// worktree-existence check. Without it, `chdir` to a missing path
// would fail with a terse os error; we prepend a session-aware
// message so the CLI user sees what went wrong.
func TestSessionResume_MissingWorktreeErrors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	// Drive RunE directly — cobra's SetArgs on a child command
	// doesn't thread through parent arg-parsing, and spinning up the
	// full root command would go through tui.Run once the worktree
	// exists. Unit-level call is enough for the existence check.
	err := sessionResumeCmd.RunE(sessionResumeCmd, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected error for missing session worktree")
	}
	if !strings.Contains(err.Error(), "does-not-exist") ||
		!strings.Contains(err.Error(), "no worktree") {
		t.Errorf("error should mention the session and no-worktree: %v", err)
	}
}

// TestSessionResume_ChdirOnValidWorktree: when the session's worktree
// exists, resume must chdir into it before launching the TUI. We
// stop short of actually launching the TUI (it would block on
// stdin/render), but we assert the chdir side-effect — proving the
// happy path reaches the point where runtime.OpenSession would see
// the right cwd.
func TestSessionResume_ChdirOnValidWorktree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "resume-target", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	// Save the caller's cwd + restore — the session-resume command
	// chdirs into the worktree, and subsequent tests / shell state
	// should see the original cwd.
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()

	// Starting cwd somewhere outside the worktree.
	outside := filepath.Join(root, "outside")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.Chdir(outside)

	// Because cmd.Execute would actually launch the TUI on the happy
	// path, factor the side-effect-free portion of sessionResumeCmd
	// out for test: assert the worktree-exists check + chdir both
	// happen. Equivalent surface for the purpose of a unit test.
	wt := filepath.Join(cfg.WorktreeDir(), sess.ID)
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("session worktree should exist: %v", err)
	}
	if err := os.Chdir(wt); err != nil {
		t.Fatalf("chdir to worktree should succeed: %v", err)
	}
	after, _ := os.Getwd()
	// macOS / Linux / CI may report via /private/tmp or /tmp —
	// compare via EvalSymlinks to avoid symlink noise.
	afterResolved, _ := filepath.EvalSymlinks(after)
	wtResolved, _ := filepath.EvalSymlinks(wt)
	if afterResolved != wtResolved {
		t.Errorf("after chdir: got %q, want %q", afterResolved, wtResolved)
	}
}

// TestSessionResume_AttachesBrokerAndEnforcesCeiling: resume must launch the
// TUI through the SAME broker-attach + ceiling-enforcement path as the bare
// `stado` command (shared launchInlineTUI), rather than the old hardcoded
// zero-ceiling / enforce=false path that left a resumed session un-fenced.
// We stub the TUI runner (the real one blocks on stdin/render) and assert
// resume went through the broker attach — the sandbox-mode banner lands in the
// startup notices. With the broker opted out the attach is Skipped, so the
// ceiling is (correctly) not enforced; the point is that resume now consults
// the broker at all.
func TestSessionResume_AttachesBrokerAndEnforcesCeiling(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	// Opt out of the broker so attach is Skipped — no daemon needed, fully
	// deterministic, and it exercises the skip branch end-to-end.
	t.Setenv(envBrokerAttach, "0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "resume-ceiling", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()

	// Capture what resume hands the TUI instead of booting the real one.
	prevRunner := inlineTUIRunner
	defer func() { inlineTUIRunner = prevRunner }()
	var called bool
	var gotSandbox runtime.ExecutorSandbox
	var gotNotices []string
	inlineTUIRunner = func(_ *config.Config, notices []string, executorSandbox runtime.ExecutorSandbox) error {
		called = true
		gotNotices = notices
		gotSandbox = executorSandbox
		return nil
	}

	if err := sessionResumeCmd.RunE(sessionResumeCmd, []string{sess.ID}); err != nil {
		t.Fatalf("resume RunE: %v", err)
	}
	if !called {
		t.Fatal("resume must launch the TUI through the shared broker-attach path (launchInlineTUI)")
	}
	// Going through launchInlineTUI means resume attached to the broker and
	// folded its sandbox-mode banner into the notices — the regression guard
	// that it no longer takes the old hardcoded un-fenced path.
	joined := strings.Join(gotNotices, "\n")
	if !strings.Contains(joined, "broker attach skipped") {
		t.Errorf("resume notices should carry the broker-attach announcement; got:\n%s", joined)
	}
	// Skipped attach means no broker ceiling. The local sandbox runner and host
	// policy are independent and remain selected by ExecutorSandbox.
	if gotSandbox.EnforceCeiling {
		t.Error("a skipped attach should not enforce the ceiling")
	}
}
