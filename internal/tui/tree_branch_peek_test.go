package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// seedTurns commits nTurns turn boundaries on sess so the tree picker shows
// real, branchable/peekable turn rows. Each turn writes a distinct file body so
// a fork-at-turn can be verified to land on the right tree.
func seedTurns(t *testing.T, sess *stadogit.Session, nTurns int) {
	t.Helper()
	for i := 1; i <= nTurns; i++ {
		if err := os.WriteFile(filepath.Join(sess.WorktreePath, "f.txt"),
			[]byte(fmt.Sprintf("%s-v%d", sess.ID, i)), 0o644); err != nil {
			t.Fatal(err)
		}
		tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: fmt.Sprintf("turn %d work", i)}); err != nil {
			t.Fatal(err)
		}
		if err := sess.NextTurn(); err != nil {
			t.Fatal(err)
		}
	}
}

// landOnTurnRow expands the current session and steps the cursor down onto the
// first turn row, asserting it got there.
func landOnTurnRow(t *testing.T, m *Model) {
	t.Helper()
	// → expands the current (selected) session, ↓ lands on its first turn row.
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyRight}); !h {
		t.Fatal("right not handled")
	}
	for i := 0; i < 12; i++ {
		if m.treePick.SelectedIsTurn() {
			return
		}
		if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyDown}); !h {
			t.Fatal("down not handled")
		}
	}
	t.Fatalf("never landed on a turn row (selection %q)", m.treePick.SelectedID())
}

// TestTreePickerBranchForksAtTurn: `b` over a turn row forks the current
// session at that turn and switches to the child. The parent stays untouched.
func TestTreePickerBranchForksAtTurn(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	seedTurns(t, m.session, 3)

	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	landOnTurnRow(t, m)

	parentID := m.session.ID
	if parentID != ids.first {
		t.Fatalf("current session = %q, want %q", parentID, ids.first)
	}

	// `b` on the turn row → fork-at-turn + switch + close.
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Text: "b"}); !h {
		t.Fatal("b not handled")
	}
	if m.treePick.Visible {
		t.Fatal("tree should close after a branch")
	}
	if m.session.ID == parentID || m.session.ID == ids.second {
		t.Fatalf("branch did not switch to a fresh child: %q", m.session.ID)
	}
	if _, err := os.Stat(m.session.WorktreePath); err != nil {
		t.Fatalf("child worktree missing after branch: %v", err)
	}
	// Child seeded at turn 1's tree → f.txt == "<parent>-v1", not the v3 tip.
	got, err := os.ReadFile(filepath.Join(m.session.WorktreePath, "f.txt"))
	if err != nil {
		t.Fatalf("read child worktree: %v", err)
	}
	if want := parentID + "-v1"; string(got) != want {
		t.Fatalf("child f.txt = %q, want %q (turn 1's tree, not the tip)", got, want)
	}
}

// TestTreePickerPeekReadOnly: Enter over a turn row opens a read-only peek with
// the honest label and NEVER mutates — no session opened, no ref written,
// m.session / m.blocks untouched, tree stays open.
func TestTreePickerPeekReadOnly(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	seedTurns(t, m.session, 2)

	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	landOnTurnRow(t, m)

	// Snapshot the never-mutate invariants BEFORE the peek.
	beforeSession := m.session
	beforeID := m.session.ID
	beforeBlocks := len(m.blocks)
	beforeMsgs := len(m.msgs)
	refsBefore := refSnapshot(t, m.session.Sidecar)

	// Enter on the turn row → peek opens; tree stays visible.
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}); !h {
		t.Fatal("enter not handled")
	}
	if !m.treePick.Peeking() {
		t.Fatal("Enter over a turn row should open the peek")
	}
	if !m.treePick.Visible {
		t.Fatal("peek must layer over the tree, not replace it")
	}

	// NEVER-MUTATE assertions.
	if m.session != beforeSession || m.session.ID != beforeID {
		t.Fatalf("peek mutated m.session: %q -> %q", beforeID, m.session.ID)
	}
	if len(m.blocks) != beforeBlocks || len(m.msgs) != beforeMsgs {
		t.Fatalf("peek mutated conversation state: blocks %d->%d msgs %d->%d",
			beforeBlocks, len(m.blocks), beforeMsgs, len(m.msgs))
	}
	refsAfter := refSnapshot(t, m.session.Sidecar)
	if len(refsAfter) != len(refsBefore) {
		t.Fatalf("peek wrote/removed a session ref: %d -> %d refs", len(refsBefore), len(refsAfter))
	}
	for name, hash := range refsBefore {
		if refsAfter[name] != hash {
			t.Fatalf("peek mutated ref %s: %s -> %s", name, hash, refsAfter[name])
		}
	}

	// Honest label is rendered (full-conversation, read-only — NOT a snapshot).
	out := m.treePick.View(140, 40)
	for _, want := range []string{
		"read-only",
		"not a point-in-time snapshot",
		shortSessionID(ids.first),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("peek view missing honest-label fragment %q:\n%s", want, out)
		}
	}
}

// TestTreePeekLayeredEscCtrlC: the first Esc closes the peek (tree stays), the
// second closes the tree; Ctrl+C is layered the same way via onKey.
func TestTreePeekLayeredEscCtrlC(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	seedTurns(t, m.session, 2)
	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	landOnTurnRow(t, m)
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}); !h {
		t.Fatal("enter not handled")
	}
	if !m.treePick.Peeking() {
		t.Fatal("peek should be open")
	}

	// First Esc → peek closes, tree stays.
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEscape}); !h {
		t.Fatal("first esc not handled")
	}
	if m.treePick.Peeking() {
		t.Fatal("first esc should close the peek")
	}
	if !m.treePick.Visible {
		t.Fatal("first esc must NOT close the tree")
	}
	// Second Esc → tree closes.
	if _, _, h := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEscape}); !h {
		t.Fatal("second esc not handled")
	}
	if m.treePick.Visible {
		t.Fatal("second esc should close the tree")
	}

	// Re-open + peek, then exercise Ctrl+C's layered close through onKey.
	if err := m.openTreePicker(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	landOnTurnRow(t, m)
	onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.treePick.Peeking() {
		t.Fatal("peek should be open before ctrl+c")
	}
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	if _, _, h := onKey(m, ctrlC); !h {
		t.Fatal("ctrl+c not handled")
	}
	if m.treePick.Peeking() {
		t.Fatal("first ctrl+c should close just the peek")
	}
	if !m.treePick.Visible {
		t.Fatal("first ctrl+c must NOT close the tree")
	}
	if _, _, h := onKey(m, ctrlC); !h {
		t.Fatal("second ctrl+c not handled")
	}
	if m.treePick.Visible {
		t.Fatal("second ctrl+c should close the tree")
	}
}

// TestTreePeekDetachedGated: peeking a detached session (no worktree on disk)
// surfaces an error notice and opens no peek.
func TestTreePeekDetachedGated(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	// No worktree on disk for this id, but call openTreePeek directly to prove
	// the Avail gate (a real detached row never emits CommandPeek — the picker
	// gates it — but the host re-checks defensively).
	err := m.openTreePeek("ffffffff", 1, 2, "deadbeef")
	if err == nil {
		t.Fatal("expected an error peeking a session with no worktree")
	}
	if m.treePick.Peeking() {
		t.Fatal("no peek should open for a detached session")
	}
}

// refSnapshot captures every refs/sessions/* ref so a test can assert the peek
// path writes nothing.
func refSnapshot(t *testing.T, sc *stadogit.Sidecar) map[string]string {
	t.Helper()
	iter, err := sc.Repo().References()
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	out := map[string]string{}
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		out[string(ref.Name())] = ref.Hash().String()
		return nil
	})
	return out
}
