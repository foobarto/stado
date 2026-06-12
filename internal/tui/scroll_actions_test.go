package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestMessagesScrollActionsWired verifies the Messages* scroll actions are
// dispatched in onKey and move the conversation viewport. Before this wiring
// the actions were DEFINED in keys/defaults.go but had no handler — only
// pgup/pgdown scrolled (via the viewport's own restricted keymap). The
// non-text chords (ctrl+alt+f/b/u/d, ctrl+alt+g) now drive the viewport.
func TestMessagesScrollActionsWired(t *testing.T) {
	scrollable := func(t *testing.T, yoff int) *Model {
		t.Helper()
		m := queueModel(t)
		m.state = stateIdle
		m.vp.SetWidth(80)
		m.vp.SetHeight(5)
		m.vp.SetContent(strings.Repeat("line\n", 80))
		m.vp.SetYOffset(yoff)
		return m
	}

	// MessagesPageDown (ctrl+alt+f) scrolls down from the top.
	t.Run("page down ctrl+alt+f", func(t *testing.T) {
		m := scrollable(t, 0)
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl | tea.ModAlt})
		if m.vp.YOffset() <= before {
			t.Fatalf("MessagesPageDown (ctrl+alt+f) did not scroll down: YOffset %d -> %d", before, m.vp.YOffset())
		}
	})

	// MessagesPageUp (ctrl+alt+b) scrolls up from a mid offset.
	t.Run("page up ctrl+alt+b", func(t *testing.T) {
		m := scrollable(t, 40)
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl | tea.ModAlt})
		if m.vp.YOffset() >= before {
			t.Fatalf("MessagesPageUp (ctrl+alt+b) did not scroll up: YOffset %d -> %d", before, m.vp.YOffset())
		}
	})

	// MessagesHalfPageDown (ctrl+alt+d) scrolls down.
	t.Run("half page down ctrl+alt+d", func(t *testing.T) {
		m := scrollable(t, 0)
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl | tea.ModAlt})
		if m.vp.YOffset() <= before {
			t.Fatalf("MessagesHalfPageDown (ctrl+alt+d) did not scroll down: YOffset %d -> %d", before, m.vp.YOffset())
		}
	})

	// MessagesHalfPageUp (ctrl+alt+u) scrolls up.
	t.Run("half page up ctrl+alt+u", func(t *testing.T) {
		m := scrollable(t, 40)
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl | tea.ModAlt})
		if m.vp.YOffset() >= before {
			t.Fatalf("MessagesHalfPageUp (ctrl+alt+u) did not scroll up: YOffset %d -> %d", before, m.vp.YOffset())
		}
	})

	// MessagesFirst (home) jumps to the top.
	t.Run("first home", func(t *testing.T) {
		m := scrollable(t, 40)
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
		if m.vp.YOffset() != 0 {
			t.Fatalf("MessagesFirst (home) did not go to top: YOffset = %d, want 0", m.vp.YOffset())
		}
	})

	// MessagesLast (end / ctrl+alt+g) jumps to the bottom.
	t.Run("last end", func(t *testing.T) {
		m := scrollable(t, 0)
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
		if m.vp.YOffset() == 0 {
			t.Fatalf("MessagesLast (end) did not scroll toward the bottom: YOffset stayed 0")
		}
	})
}

// TestScrollActionsDoNotShadowTyping guards the #142 text-safe behaviour: the
// scroll wiring uses non-text chords only, so plain 'd' / 'j' still type into
// the input and never scroll the conversation. (ctrl+alt+d scrolls; bare d
// does not.)
func TestScrollActionsDoNotShadowTyping(t *testing.T) {
	m := queueModel(t)
	m.state = stateIdle
	m.vp.SetWidth(80)
	m.vp.SetHeight(5)
	m.vp.SetContent(strings.Repeat("line\n", 80))
	m.vp.SetYOffset(10)

	for _, ch := range []string{"d", "j", "u", "b", "f", "g"} {
		before := m.vp.YOffset()
		_, _ = m.Update(tea.KeyPressMsg{Text: ch})
		if got := m.vp.YOffset(); got != before {
			t.Fatalf("typing %q scrolled the messages viewport: YOffset %d -> %d", ch, before, got)
		}
	}
	if !strings.Contains(m.input.Value(), "djubfg") {
		t.Fatalf("typed letters were not inserted into the input: %q", m.input.Value())
	}
}
