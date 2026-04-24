package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStatusSlashOpensModal(t *testing.T) {
	m := scenarioModel(t)

	_ = m.handleSlash("/status")

	if !m.showStatus {
		t.Fatal("/status should open the status modal")
	}
	out := m.View()
	for _, want := range []string{"Status", "Agent", "Runtime", "Context", "Extensions", "provider", "lsp", "activates when supported files are read"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status modal missing %q: %q", want, out)
		}
	}
}

func TestStatusKeybindOpensAndEscClosesModal(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !m.showStatus {
		t.Fatal("ctrl+x s should open the status modal")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showStatus {
		t.Fatal("esc should close the status modal")
	}
}

func TestStatusKeybindClosesAfterFullChord(t *testing.T) {
	m := scenarioModel(t)
	m.showStatus = true

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.showStatus {
		t.Fatal("ctrl+x primer alone should not close the status modal")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.showStatus {
		t.Fatal("ctrl+x s should close the status modal")
	}
}

func TestStatusCommandInPalette(t *testing.T) {
	m := scenarioModel(t)
	m.slash.Open()

	for _, r := range "status" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.showStatus {
		t.Fatal("selecting /status from command palette should open status modal")
	}
}
