package treepicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		return tea.KeyPressMsg{Text: s}
	}
}

func sample() []Node {
	return []Node{
		{ID: "aaaaaaaa", Label: "root", Avail: AvailIdle, Depth: 0, TurnCount: 2,
			Turns: []string{"turn 1 · init", "turn 2 · work"}},
		{ID: "bbbbbbbb", Label: "child", Avail: AvailLive, Depth: 1, HasParent: true, ParentTurn: 2,
			IsCurrent: true, TurnCount: 1, Turns: []string{"turn 1 · forked"}},
		{ID: "cccccccc", Label: "sibling", Avail: AvailDetached, Depth: 0},
	}
}

// TestCursorClampNoWrap: ↑ at the top and ↓ at the bottom stay put (no wrap).
func TestCursorClampNoWrap(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("initial selection = %q, want aaaaaaaa", got)
	}
	// ↑ at the top clamps.
	_, handled := p.Update(key("up"))
	if !handled {
		t.Fatal("up not handled")
	}
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after up-at-top selection = %q, want aaaaaaaa (clamp)", got)
	}
	// Walk to the bottom (3 session rows; current is collapsed so no turn rows).
	p.Update(key("down"))
	if got := p.SelectedID(); got != "bbbbbbbb" {
		t.Fatalf("after 1 down = %q, want bbbbbbbb", got)
	}
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after 2 down = %q, want cccccccc", got)
	}
	// ↓ at the bottom clamps.
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after down-at-bottom = %q, want cccccccc (clamp)", got)
	}
}

// TestExpandInsertsTurnRowsAndSkipsThem: → expands a session, rebuild inserts
// its (non-selectable) turn rows, and ↓ skips over them to the next session.
func TestExpandCollapseRebuildAndCursorRepin(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	// Expand the root (it has 2 turns).
	_, handled := p.Update(key("right"))
	if !handled {
		t.Fatal("right not handled")
	}
	// Cursor must re-pin to the same session after the rebuild.
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after expand selection = %q, want aaaaaaaa (re-pin)", got)
	}
	// rows = root + 2 turns + child + sibling = 5.
	if len(p.rows) != 5 {
		t.Fatalf("rows after expand = %d, want 5", len(p.rows))
	}
	// ↓ must skip the two turn rows and land on the child session.
	p.Update(key("down"))
	if got := p.SelectedID(); got != "bbbbbbbb" {
		t.Fatalf("after down past turns = %q, want bbbbbbbb (turn rows skipped)", got)
	}
	// Move back up onto the root and collapse it.
	p.Update(key("up"))
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after up = %q, want aaaaaaaa", got)
	}
	_, _ = p.Update(key("left"))
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after collapse selection = %q, want aaaaaaaa (re-pin)", got)
	}
	if len(p.rows) != 3 {
		t.Fatalf("rows after collapse = %d, want 3", len(p.rows))
	}
}

// TestExpandNoTurnsIsNoop: a session with no turns can't be expanded.
func TestExpandNoTurnsIsNoop(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	// Move to the sibling (cccccccc), which has TurnCount 0.
	p.Update(key("down"))
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("selection = %q, want cccccccc", got)
	}
	before := len(p.rows)
	p.Update(key("right"))
	if len(p.rows) != before {
		t.Fatalf("rows after expanding turn-less node = %d, want %d (no-op)", len(p.rows), before)
	}
}

// TestHomeEnd: g/G jump to the first/last session row.
func TestHomeEnd(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("G"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after G = %q, want cccccccc", got)
	}
	p.Update(key("g"))
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after g = %q, want aaaaaaaa", got)
	}
}

// TestOpenSwitchEmission: Open with a current id selects it, and Enter over a
// switchable row emits a CommandSwitch the host drains via TakeAction.
func TestOpenEnterSwitchEmission(t *testing.T) {
	p := New()
	p.Open(sample(), "bbbbbbbb")
	if got := p.SelectedID(); got != "bbbbbbbb" {
		t.Fatalf("Open current selection = %q, want bbbbbbbb", got)
	}
	_, handled := p.Update(key("enter"))
	if !handled {
		t.Fatal("enter not handled")
	}
	action := p.TakeAction()
	if action.Type != CommandSwitch || action.ID != "bbbbbbbb" {
		t.Fatalf("switch action = %+v, want CommandSwitch bbbbbbbb", action)
	}
	// Outbox drained — a second take is empty.
	if again := p.TakeAction(); again.Type != CommandNone {
		t.Fatalf("second TakeAction = %+v, want CommandNone", again)
	}
}

// TestEnterDetachedNoSwitch: Enter over a detached row emits nothing.
func TestEnterDetachedNoSwitch(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("G")) // cccccccc is detached
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("selection = %q, want cccccccc", got)
	}
	p.Update(key("enter"))
	if action := p.TakeAction(); action.Type != CommandNone {
		t.Fatalf("detached enter action = %+v, want CommandNone", action)
	}
}

// TestEscCloses: Esc and Ctrl+C close the picker.
func TestEscCloses(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("esc"))
	if p.Visible {
		t.Fatal("esc should close the picker")
	}
	p.Open(sample(), "")
	p.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if p.Visible {
		t.Fatal("ctrl+c should close the picker")
	}
}

// --- reproduce-by-rendering: View width assertions (per the lipgloss memory) ---

// modalWidthFor mirrors View's modal-width formula so the test asserts the
// rendered frame matches the budget at a couple of screen widths.
func modalWidthFor(screenW int) int { return clampInt(screenW/2, 58, 98) }

func TestViewModalWidthAtScreenWidths(t *testing.T) {
	for _, sw := range []int{80, 120, 200} {
		p := New()
		p.Open(sample(), "bbbbbbbb")
		out := p.View(sw, 30)
		want := modalWidthFor(sw)
		// Every rendered line must fit within the modal frame width. lipgloss
		// .Width is the TOTAL box width in v2 (border + padding included), so
		// no rendered line should exceed `want`.
		for _, line := range strings.Split(out, "\n") {
			// lipgloss.Place centres the modal on the canvas, so each line
			// carries leading whitespace padding to the left of the frame.
			// Strip both sides so we measure the modal frame itself, not the
			// centering pad (which is what makes a line as wide as the screen).
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if w := lipgloss.Width(trimmed); w > want {
				t.Fatalf("screen=%d: line width %d exceeds modal width %d: %q", sw, w, want, trimmed)
			}
		}
		// The frame must actually be present (rounded border corners).
		if !strings.Contains(out, "╮") || !strings.Contains(out, "╰") {
			t.Fatalf("screen=%d: rounded border not rendered", sw)
		}
	}
}

// TestViewShowsGlyphsAndLabels: the rendered modal carries the design glyph
// language and the session labels.
func TestViewShowsGlyphsAndLabels(t *testing.T) {
	p := New()
	p.Open(sample(), "bbbbbbbb")
	// Expand root so a turn line renders too.
	p.Update(key("g"))
	p.Update(key("right"))
	out := p.View(120, 40)
	for _, want := range []string{
		"Session tree", // title
		glyphCurrent,   // ▸ you-are-here (bbbbbbbb is current)
		"child",        // current session label
		"root",         // root label
		glyphFork,      // ⑂ fork-origin marker on the child edge
		"turn 1",       // an expanded turn line
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered view missing %q\n%s", want, out)
		}
	}
}

// TestViewEmpty renders a placeholder when there are no sessions.
func TestViewEmpty(t *testing.T) {
	p := New()
	p.Open(nil, "")
	out := p.View(100, 30)
	if !strings.Contains(out, "no sessions") {
		t.Fatalf("empty view missing placeholder:\n%s", out)
	}
}
