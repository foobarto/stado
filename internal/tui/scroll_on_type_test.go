package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTypingDoesNotScrollMessages reproduces the bug where typing into the
// always-focused chat input scrolled the conversation history. The fall-through
// in Model.Update forwards an unhandled key to BOTH the input editor and the
// messages viewport, and the viewport's default keymap binds j/k/h/l/b/f/u/d
// and space to scroll — so typing those letters scrolled the history (and
// ctrl+u, the editor's delete-to-line-start, double-acted). Only PageUp/
// PageDown (and the mouse wheel) should scroll; every other key is text.
func TestTypingDoesNotScrollMessages(t *testing.T) {
	m := queueModel(t)
	m.state = stateIdle

	// Make the messages viewport scrollable: content taller than the window.
	m.vp.SetWidth(80)
	m.vp.SetHeight(5)
	m.vp.SetContent(strings.Repeat("line\n", 50))
	m.vp.SetYOffset(10)

	// Type letters the viewport default keymap would otherwise scroll on.
	for _, ch := range []string{"d", "j", "k", "u", "b", "f"} {
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Text: ch})
		if got := m.vp.YOffset(); got != before {
			t.Fatalf("typing %q scrolled the messages viewport: YOffset %d -> %d", ch, before, got)
		}
	}
	if !strings.Contains(m.input.Value(), "djkubf") {
		t.Fatalf("typed letters were not inserted into the input: %q", m.input.Value())
	}

	// Control: PageDown must still scroll the history.
	before := m.vp.YOffset()
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.vp.YOffset() == before {
		t.Errorf("PageDown should still scroll the messages viewport (YOffset stayed %d)", before)
	}
}
