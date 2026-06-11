package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestOpenTreePickerBuildsForest: /tree's open path builds a forest over the
// two real sessions and opens the picker with the current session selected.
func TestOpenTreePickerBuildsForest(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	if !m.treePick.Visible {
		t.Fatal("tree picker should be visible after open")
	}
	if got := m.treePick.SelectedID(); got != ids.first {
		t.Fatalf("tree picker selection = %q, want current %q", got, ids.first)
	}
}

// TestTreePickerEnterSwitches: navigating the tree picker to another session
// and pressing Enter routes through onPickerKey, calls switchToSession, and
// closes the picker — the open→Enter→switch path for stage 4.
func TestTreePickerEnterSwitches(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}

	// Walk the cursor until it lands on the second (non-current) session, then
	// press Enter. Both sessions are forest roots (independent NewSession), so
	// down/up steps over the root rows.
	var landed bool
	for i := 0; i < 8; i++ {
		if m.treePick.SelectedID() == ids.second {
			landed = true
			break
		}
		_, _, handled := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
		if !handled {
			t.Fatal("down keypress not handled by tree picker")
		}
	}
	if !landed {
		t.Fatalf("could not navigate to second session %q (selection stuck at %q)", ids.second, m.treePick.SelectedID())
	}

	_, _, handled := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("enter keypress not handled by tree picker")
	}
	if m.treePick.Visible {
		t.Fatal("tree picker should close after a switch")
	}
	if m.session == nil || m.session.ID != ids.second {
		t.Fatalf("session after switch = %v, want %q", m.session, ids.second)
	}
}

// TestTreePickerEscCloses: Esc routes through onPickerKey and closes the
// picker without switching.
func TestTreePickerEscCloses(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	_, _, handled := onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled {
		t.Fatal("esc not handled")
	}
	if m.treePick.Visible {
		t.Fatal("esc should close the tree picker")
	}
	if m.session.ID != ids.first {
		t.Fatalf("esc must not switch sessions; got %q want %q", m.session.ID, ids.first)
	}
}

// TestTreePickerInAnyModalOpen: the tree picker participates in the modal
// bookkeeping (anyModalOpen / closeAllModals).
func TestTreePickerInModalBookkeeping(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)

	if m.anyModalOpen() {
		t.Fatal("no modal should be open initially")
	}
	if err := m.openTreePicker(); err != nil {
		t.Fatalf("openTreePicker: %v", err)
	}
	if !m.anyModalOpen() {
		t.Fatal("anyModalOpen should report the tree picker")
	}
	m.closeAllModals()
	if m.treePick.Visible {
		t.Fatal("closeAllModals should dismiss the tree picker")
	}
}
