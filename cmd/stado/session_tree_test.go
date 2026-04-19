package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func sampleTurns() []stadogit.TurnEntry {
	return []stadogit.TurnEntry{
		{Turn: 1, Commit: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Author: "bot", When: time.Now(), Summary: "turn 1"},
		{Turn: 2, Commit: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Author: "bot", When: time.Now(), Summary: "turn 2"},
		{Turn: 3, Commit: plumbing.NewHash("cccccccccccccccccccccccccccccccccccccccc"), Author: "bot", When: time.Now(), Summary: "turn 3"},
	}
}

// TestTreeModelCursorNavigation checks arrow-key / j/k navigation stays
// clamped at the ends.
func TestTreeModelCursorNavigation(t *testing.T) {
	m := &treeModel{turns: sampleTurns()}

	// Down past end clamps.
	for i := 0; i < 10; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.cursor != len(m.turns)-1 {
		t.Errorf("down-clamp: cursor = %d, want %d", m.cursor, len(m.turns)-1)
	}

	// Up past start clamps.
	for i := 0; i < 10; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.cursor != 0 {
		t.Errorf("up-clamp: cursor = %d, want 0", m.cursor)
	}

	// j / k variants work too.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.cursor != 1 {
		t.Errorf("j: cursor = %d, want 1", m.cursor)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.cursor != 0 {
		t.Errorf("k: cursor = %d, want 0", m.cursor)
	}
}

// TestTreeModelViewHighlightsCursor verifies the rendered View marks the
// current row with the ▶ marker and includes all turn numbers.
func TestTreeModelViewHighlightsCursor(t *testing.T) {
	m := &treeModel{turns: sampleTurns(), cursor: 1}
	view := m.View()

	if !strings.Contains(view, "turns/1") ||
		!strings.Contains(view, "turns/2") ||
		!strings.Contains(view, "turns/3") {
		t.Errorf("view missing turn labels:\n%s", view)
	}
	// Find the ▶ marker and assert it precedes turns/2 (cursor=1).
	lines := strings.Split(view, "\n")
	var markedLine string
	for _, l := range lines {
		if strings.Contains(l, "▶") {
			markedLine = l
			break
		}
	}
	if markedLine == "" {
		t.Fatalf("no ▶ marker in view:\n%s", view)
	}
	if !strings.Contains(markedLine, "turns/2") {
		t.Errorf("cursor marker on wrong row: %q (expected turns/2)", markedLine)
	}
}

// TestTreeModelQuitKeys covers the three quit paths — q, esc, ctrl+c.
func TestTreeModelQuitKeys(t *testing.T) {
	for _, key := range []string{"q", "esc", "ctrl+c"} {
		m := &treeModel{turns: sampleTurns()}
		var keyMsg tea.KeyMsg
		switch key {
		case "q":
			keyMsg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
		case "esc":
			keyMsg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+c":
			keyMsg = tea.KeyMsg{Type: tea.KeyCtrlC}
		}
		_, cmd := m.Update(keyMsg)
		if cmd == nil {
			t.Errorf("%s: expected tea.Quit cmd, got nil", key)
		}
	}
}

// TestTreeModelHomeEndKeys confirms g/G / home/end jump to ends.
func TestTreeModelHomeEndKeys(t *testing.T) {
	m := &treeModel{turns: sampleTurns(), cursor: 1}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.cursor != 2 {
		t.Errorf("G: cursor = %d, want 2", m.cursor)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.cursor != 0 {
		t.Errorf("g: cursor = %d, want 0", m.cursor)
	}
}

// TestTreeModelForkAtCursorIntegration — the interactive-path test DESIGN
// §"Fork-from-point ergonomics" requires. Builds a real parent session,
// runs the tree model's fork-at-cursor primitive (what pressing 'f' or
// enter invokes), and asserts the resulting session's tree-ref and
// materialised worktree match the targeted turn.
func TestTreeModelForkAtCursorIntegration(t *testing.T) {
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

	parentID := "parent-tree-test"
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	writeWorkFile(t, parent.WorktreePath, "a.txt", "turn 1\n")
	turn1 := commitAndTag(t, parent, 1)
	writeWorkFile(t, parent.WorktreePath, "b.txt", "turn 2\n")
	_ = commitAndTag(t, parent, 2)

	// Re-enumerate via the public helper — same path the real subcommand uses.
	turns, err := sc.ListTurnRefs(parentID)
	if err != nil {
		t.Fatalf("ListTurnRefs: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turn tags, got %d", len(turns))
	}

	// Simulate landing on turns/1 and pressing 'f'.
	m := &treeModel{
		turns:     turns,
		sessionID: parentID,
		cfg:       cfg,
		cursor:    0,
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd == nil {
		t.Fatal("'f' should have produced a tea.Quit cmd")
	}
	if m.err != nil {
		t.Fatalf("fork errored: %v", m.err)
	}
	if m.forked == nil {
		t.Fatal("fork didn't record outcome")
	}
	if m.forked.fromTurn != 1 {
		t.Errorf("fromTurn = %d, want 1", m.forked.fromTurn)
	}

	// The new session's tree head must match turn 1.
	childHead, err := sc.ResolveRef(stadogit.TreeRef(m.forked.childID))
	if err != nil {
		t.Fatalf("child tree ref: %v", err)
	}
	if childHead != turn1 {
		t.Errorf("child head = %s, want turns/1 %s", childHead, turn1)
	}

	// Child worktree: a.txt from turn 1 present, b.txt from turn 2 absent.
	if _, err := os.Stat(filepath.Join(m.forked.worktreePath, "a.txt")); err != nil {
		t.Errorf("child worktree missing a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.forked.worktreePath, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("child worktree has b.txt (should be absent at turns/1): %v", err)
	}
}

// TestListTurnRefsOrdered asserts turns come back in ascending order.
func TestListTurnRefsOrdered(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatalf("worktree dir: %v", err)
	}
	sc, _ := openSidecar(cfg)
	id := "ordered"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Three turns, each with a unique file.
	for i := 1; i <= 3; i++ {
		writeWorkFile(t, sess.WorktreePath, "f.txt", "content v"+string(rune('0'+i)))
		_ = commitAndTag(t, sess, i)
	}

	turns, err := sc.ListTurnRefs(id)
	if err != nil {
		t.Fatalf("ListTurnRefs: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}
	for i, te := range turns {
		want := i + 1
		if te.Turn != want {
			t.Errorf("turns[%d].Turn = %d, want %d", i, te.Turn, want)
		}
	}
}
