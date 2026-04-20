package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFilterOSCResponses_DropsBgColorReply: the exact shape the user
// saw leak into the textarea — KeyMsg{Alt: true, Runes: "]11;rgb:...}"
// synthesised by bubbletea v1.3's ESC+runes parser for an OSC 11
// response — must be dropped by the filter.
func TestFilterOSCResponses_DropsBgColorReply(t *testing.T) {
	msg := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Alt:   true,
		Runes: []rune("]11;rgb:1e1e/1e1e/1e1e\\"),
	}
	if got := filterOSCResponses(nil, msg); got != nil {
		t.Errorf("expected nil (drop), got %+v", got)
	}
}

// TestFilterOSCResponses_DropsFgColorReply: OSC 10 (foreground) has
// the same shape as OSC 11 and must be filtered too.
func TestFilterOSCResponses_DropsFgColorReply(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("]10;rgb:ffff/ffff/ffff")}
	if got := filterOSCResponses(nil, msg); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestFilterOSCResponses_PassesLegitAltBracket: a user actually
// pressing Alt+] (if their terminal maps it to an Alt-rune event)
// should NOT be dropped. Filter requires a digit after ']' to fire.
func TestFilterOSCResponses_PassesLegitAltBracket(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("]")}
	if got := filterOSCResponses(nil, msg); got == nil {
		t.Error("lone Alt+] should pass through the filter")
	}

	msg2 := tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("]next")}
	if got := filterOSCResponses(nil, msg2); got == nil {
		t.Error("Alt+] followed by non-digit should pass")
	}
}

// TestFilterOSCResponses_PassesNonKeyMsgs: filter only cares about
// KeyMsg; anything else (tea.WindowSizeMsg, custom msgs) must flow
// through untouched.
func TestFilterOSCResponses_PassesNonKeyMsgs(t *testing.T) {
	msg := tea.WindowSizeMsg{Width: 80, Height: 24}
	if got := filterOSCResponses(nil, msg); got == nil {
		t.Error("WindowSizeMsg must pass through")
	}
}

// TestFilterOSCResponses_PassesRegularTyping: plain rune input (no
// Alt modifier) must pass. Regression guard — no false-positives on
// ordinary keystrokes.
func TestFilterOSCResponses_PassesRegularTyping(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}
	if got := filterOSCResponses(nil, msg); got == nil {
		t.Error("plain typing must pass through")
	}
}
