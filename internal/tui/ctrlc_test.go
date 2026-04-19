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

// TestCtrlDStillQuits regression-guards the intended exit keybinding.
// ctrl+d must still quit since the user's complaint was "ctrl+c
// should not close the editor", not "nothing should close the editor".
func TestCtrlDStillQuits(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Error("ctrl+d should return a tea.Cmd (tea.Quit)")
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
