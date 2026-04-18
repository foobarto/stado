package keys

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	bindings := r.Get(AppExit)

	if len(bindings) == 0 {
		t.Errorf("Expected bindings for AppExit")
	}

	keys := bindings[0].Keys()
	if len(keys) == 0 || keys[0] != "ctrl+d" {
		t.Errorf("Expected 'ctrl+d' for AppExit, got %v", keys)
	}
}

func TestRegistryMatches(t *testing.T) {
	r := NewRegistry()

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	if !r.Matches(msg, AppExit) {
		t.Errorf("Expected Ctrl+D to match AppExit")
	}

	msg2 := tea.KeyMsg{Type: tea.KeyUp}
	if !r.Matches(msg2, HistoryPrevious) {
		t.Errorf("Expected Up to match HistoryPrevious")
	}

	msg3 := tea.KeyMsg{Type: tea.KeyCtrlP}
	if !r.Matches(msg3, HistoryPrevious) {
		t.Errorf("Expected Ctrl+P to match HistoryPrevious")
	}
}
