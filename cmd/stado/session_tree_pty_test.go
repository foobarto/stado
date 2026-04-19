package main

// Phase 11.5 PTY harness — DESIGN §"Fork-from-point ergonomics"
// requirement that the interactive `session tree` path be testable
// end-to-end against a real terminal pseudo-tty, not just via direct
// Update() calls. teatest from charmbracelet/x runs the actual
// tea.Program with simulated stdin/stdout, so this exercises the
// keymap → render → quit lifecycle as the user experiences it.
//
// The existing TestTreeModelForkAtCursorIntegration test reaches into
// the model and pokes Update directly. This one drives keys through
// the program and waits for rendered frames before asserting state —
// catching regressions like a key being swallowed by alt-screen
// init, an early Quit racing the fork, etc.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// TestSessionTree_PTY_NavigateAndFork is the full PTY-driven story:
// the program renders, the user navigates with 'j', presses 'f', and
// the post-quit final model carries a forkOutcome whose child session
// resolves to the targeted turn's tree.
func TestSessionTree_PTY_NavigateAndFork(t *testing.T) {
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
		t.Fatalf("worktree dir: %v", err)
	}

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatalf("openSidecar: %v", err)
	}
	parentID := "pty-tree-test"
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	writeWorkFile(t, parent.WorktreePath, "a.txt", "turn 1\n")
	_ = commitAndTag(t, parent, 1)
	writeWorkFile(t, parent.WorktreePath, "b.txt", "turn 2\n")
	turn2 := commitAndTag(t, parent, 2)

	turns, err := sc.ListTurnRefs(parentID)
	if err != nil {
		t.Fatalf("ListTurnRefs: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}

	m := &treeModel{
		turns:     turns,
		sessionID: parentID,
		cfg:       cfg,
	}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))

	// Wait for the initial frame to land — the title carries the session
	// id, so seeing it means the program is up + rendering.
	teatest.WaitFor(t, tm.Output(),
		func(b []byte) bool { return bytes.Contains(b, []byte(parentID)) },
		teatest.WithDuration(3*time.Second),
		teatest.WithCheckInterval(20*time.Millisecond),
	)

	// Navigate: 'j' down → cursor moves from turn 0 to turn 1 (turns/2).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Wait until the cursor marker (▶) is on the turns/2 row before
	// firing the fork. Without this the fork can race ahead of the
	// re-render and target the wrong row.
	teatest.WaitFor(t, tm.Output(),
		func(b []byte) bool {
			// Rendered output is paint-over-paint; look for the marker
			// adjacent to turns/2.
			return bytes.Contains(b, []byte("▶ turns/2"))
		},
		teatest.WithDuration(3*time.Second),
		teatest.WithCheckInterval(20*time.Millisecond),
	)

	// Fork at cursor + Quit (forkAtCursor returns tea.Quit on success).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})

	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	final := tm.FinalModel(t).(*treeModel)
	if final.err != nil {
		t.Fatalf("fork errored: %v", final.err)
	}
	if final.forked == nil {
		t.Fatal("fork didn't record outcome")
	}
	if final.forked.fromTurn != 2 {
		t.Errorf("fromTurn = %d, want 2", final.forked.fromTurn)
	}

	// Round-trip: child's tree HEAD must equal turns/2 commit.
	childHead, err := sc.ResolveRef(stadogit.TreeRef(final.forked.childID))
	if err != nil {
		t.Fatalf("child tree ref: %v", err)
	}
	if childHead != turn2 {
		t.Errorf("child head = %s, want turns/2 %s", childHead, turn2)
	}
	// Worktree carries both files (turn 2 is HEAD).
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(final.forked.worktreePath, f)); err != nil {
			t.Errorf("child worktree missing %s: %v", f, err)
		}
	}

	// Drain final output so the test doesn't leave the goroutine reading.
	_, _ = io.Copy(io.Discard, tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second)))
}

// TestSessionTree_PTY_QuitWithoutFork: q exits cleanly without a
// fork outcome. Catches regressions where forkAtCursor accidentally
// runs on q (a real bug class with bubbletea key dispatch).
func TestSessionTree_PTY_QuitWithoutFork(t *testing.T) {
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
	parentID := "pty-quit-test"
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	writeWorkFile(t, parent.WorktreePath, "x.txt", "hi\n")
	_ = commitAndTag(t, parent, 1)
	turns, _ := sc.ListTurnRefs(parentID)

	m := &treeModel{turns: turns, sessionID: parentID, cfg: cfg}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))

	teatest.WaitFor(t, tm.Output(),
		func(b []byte) bool { return bytes.Contains(b, []byte(parentID)) },
		teatest.WithDuration(3*time.Second),
		teatest.WithCheckInterval(20*time.Millisecond),
	)
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final := tm.FinalModel(t).(*treeModel)
	if final.forked != nil {
		t.Errorf("q produced a fork outcome: %+v", final.forked)
	}
	if final.err != nil {
		t.Errorf("q produced an error: %v", final.err)
	}

	_, _ = io.Copy(io.Discard, tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second)))
}
