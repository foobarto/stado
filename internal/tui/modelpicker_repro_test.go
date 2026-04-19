package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSlashModelTypedFlow is a regression test for the bug where
// typing "/model" + Enter didn't open the picker. Before the palette
// refactor the fuzzy matcher searched Name+Desc together, so typing
// "model" ranked `/tools` higher than `/model` (its description
// happened to contain the word "model"). Name-only matching fixes it.
func TestSlashModelTypedFlow(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")

	// Press '/' — opens the palette.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.slash.Visible {
		t.Fatal("palette should be visible after '/'")
	}

	// Type "model" — palette fuzzy-filters. /model must rank first.
	for _, r := range "model" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if sel := m.slash.Selected(); sel == nil || sel.Name != "/model" {
		t.Fatalf("palette cursor should be on /model, got %+v", sel)
	}

	// Enter — selects the currently-highlighted palette entry.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.slash.Visible {
		t.Error("palette should have closed after Enter on a selection")
	}
	if !m.modelPicker.Visible {
		t.Errorf("model picker should be open; instead: slash.Visible=%v picker.Visible=%v",
			m.slash.Visible, m.modelPicker.Visible)
	}
}
