package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCtrlCEmptyInputDoesNotQuit: the canonical bug report. Before
// this fix, ctrl+c on an empty input returned tea.Quit (exit). Now
// it should no-op.
func TestCtrlCEmptyInputDoesNotQuit(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		// tea.Quit is the only cmd we'd ever return here; bail.
		t.Errorf("ctrl+c on empty input returned a tea.Cmd; expected nil")
	}
}

// TestCtrlDShowsQuitConfirm: ctrl+d now opens a confirmation step
// instead of quitting immediately.
func TestCtrlDShowsQuitConfirm(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd != nil {
		t.Errorf("ctrl+d should not quit immediately, got %T", cmd)
	}
	if m.state != stateQuitConfirm {
		t.Errorf("state = %v, want stateQuitConfirm", m.state)
	}
}

// TestCtrlXCtrlCShowsQuitConfirm: the Emacs-style prefix alias
// should land in the same confirmation state.
func TestCtrlXCtrlCShowsQuitConfirm(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// First chord.
	_, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd1 != nil {
		t.Errorf("ctrl+x (primer) should return nil, got %T", cmd1)
	}
	// Second chord.
	_, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd2 != nil {
		t.Errorf("ctrl+x ctrl+c should not quit immediately, got %T", cmd2)
	}
	if m.state != stateQuitConfirm {
		t.Errorf("state = %v, want stateQuitConfirm", m.state)
	}
}

func TestQuitConfirmAcceptQuits(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Error("confirming quit should return tea.Quit")
	}
}

func TestQuitConfirmDenyReturnsToIdle(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil {
		t.Errorf("denying quit should not return a cmd, got %T", cmd)
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
}

func TestQuitConfirmRendersModal(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})

	out := m.View()
	if !contains(out, "Confirm exit?") {
		t.Fatalf("quit confirm modal missing prompt: %q", out)
	}
}

// TestCtrlCClearsNonEmptyInput: preserved behaviour — ctrl+c on a
// non-empty input resets the editor (delegated to internal/tui/input).
func TestCtrlCClearsNonEmptyInput(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// Type some content.
	for _, r := range "hello" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.input.Value() != "hello" {
		t.Fatalf("setup: input = %q", m.input.Value())
	}
	// ctrl+c should clear.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.input.Value() != "" {
		t.Errorf("input = %q, want empty after ctrl+c", m.input.Value())
	}
}
