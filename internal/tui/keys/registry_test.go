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

func TestRegistryPrefix(t *testing.T) {
	r := NewRegistry()

	// Debug:  print what bubbletea gives us for chord comparison.
	msgX := tea.KeyMsg{Type: tea.KeyCtrlX}
	t.Logf("ctrl+x msg.String()=%q", msgX.String())

	// IsPrefixChord should recognise ctrl+x (first chord of ModeToggleBtw).
	if !r.IsPrefixChord(msgX) {
		t.Errorf("Expected Ctrl+X to be a prefix chord")
	}

	// Entering the first chord should start a prefix sequence (ok=true) but
	// not yet complete an action.
	action, ok := r.TryPrefix(msgX)
	if !ok {
		t.Errorf("Expected TryPrefix(ctrl+x) to return (_, true) on primer, got (%q, %v)", action, ok)
	}
	if action != "" {
		t.Errorf("Expected action empty on primer, got %q", action)
	}

	// Second chord completes the prefix.
	msgB := tea.KeyMsg{Type: tea.KeyCtrlB}
	t.Logf("ctrl+b msg.String()=%q", msgB.String())
	action, ok = r.TryPrefix(msgB)
	if !ok || action != ModeToggleBtw {
		t.Errorf("Expected TryPrefix(ctrl+b) after ctrl+x to return (ModeToggleBtw, true), got (%q, %v)", action, ok)
	}

	// After completion the prefix state is reset; another ctrl+b alone
	// is no longer recognised as a prefix.
	action, ok = r.TryPrefix(msgB)
	if ok {
		t.Errorf("Expected bare Ctrl+B after completion to not match, got (%q, %v)", action, ok)
	}

	// Ctrl+x ctrl+c is another prefix: AppExit alias.
	r.ResetPrefix()
	msgC := tea.KeyMsg{Type: tea.KeyCtrlC}
	action, ok = r.TryPrefix(msgX)
	if !ok || action != "" {
		t.Fatalf("Expected primer before ctrl+c alias")
	}
	action, ok = r.TryPrefix(msgC)
	if !ok || action != AppExit {
		t.Errorf("Expected TryPrefix(ctrl+c) after ctrl+x to return (AppExit, true), got (%q, %v)", action, ok)
	}
}

func TestRegistryHelpKeys(t *testing.T) {
	r := NewRegistry()

	appExit := r.HelpKeys(AppExit)
	if len(appExit) != 2 || appExit[0] != "ctrl+d" || appExit[1] != "ctrl+x ctrl+c" {
		t.Fatalf("AppExit help keys = %v, want [ctrl+d ctrl+x ctrl+c]", appExit)
	}

	btw := r.HelpKeys(ModeToggleBtw)
	if len(btw) != 1 || btw[0] != "ctrl+x ctrl+b" {
		t.Fatalf("ModeToggleBtw help keys = %v, want [ctrl+x ctrl+b]", btw)
	}

	sidebarWider := r.HelpKeys(SidebarWider)
	if len(sidebarWider) != 1 || sidebarWider[0] != "ctrl+x ]" {
		t.Fatalf("SidebarWider help keys = %v, want [ctrl+x ]]", sidebarWider)
	}
}
