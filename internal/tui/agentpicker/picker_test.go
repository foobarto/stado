package agentpicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func sampleItems() []Item {
	return []Item{
		{ID: "do", Name: "Do", Desc: "all configured tools"},
		{ID: "plan", Name: "Plan", Desc: "read-only planning tools"},
		{ID: "btw", Name: "BTW", Desc: "off-band side question"},
	}
}

func TestOpenPreselectsCurrent(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "plan")

	sel := m.Selected()
	if sel == nil || sel.ID != "plan" {
		t.Fatalf("expected cursor on plan, got %+v", sel)
	}
	if !sel.Current {
		t.Fatalf("selected current item should be marked Current")
	}
	if got := m.View(120, 40); !strings.Contains(got, "* Plan") {
		t.Fatalf("rendered picker missing current marker: %q", got)
	}
}

func TestFuzzyFiltersAndCursorClamps(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "do")
	m.Cursor = 2

	for _, r := range "read-only" {
		m.Update(tea.KeyPressMsg{Text: string(r)})
	}
	if len(m.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(m.Matches))
	}
	if m.Matches[0].ID != "plan" {
		t.Fatalf("expected plan match, got %+v", m.Matches[0])
	}
	if m.Cursor != 0 {
		t.Errorf("cursor should clamp to 0, got %d", m.Cursor)
	}
}

func TestQueryCapsBytes(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "")

	m.Update(tea.KeyPressMsg{Text: strings.Repeat("x", maxQueryBytes+128)})
	if got := len(m.Query); got != maxQueryBytes {
		t.Fatalf("query length = %d, want %d", got, maxQueryBytes)
	}
	m.Query = strings.Repeat("x", maxQueryBytes-1)
	m.Update(tea.KeyPressMsg{Text: "é"})
	if got := len(m.Query); got != maxQueryBytes-1 {
		t.Fatalf("query length after split rune = %d, want %d", got, maxQueryBytes-1)
	}
}

func TestEscapeCloses(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Visible {
		t.Error("escape should close")
	}
	if m.Selected() != nil {
		t.Error("no selected item after close")
	}
}

// modalBoxWidth returns the display width of the centred modal BOX, trimming
// lipgloss.Place's plain-space padding from each line.
func modalBoxWidth(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.Trim(line, " ")
		if trimmed == "" {
			continue
		}
		if w := lipgloss.Width(trimmed); w > max {
			max = w
		}
	}
	return max
}

// marginBudget: cols the modal box must leave free at narrow terminals — a
// 2-col margin per side (4 total) so the centred box never touches or
// overflows the canvas edge (lipgloss.Place would clip overflow).
const marginBudget = 4

// TestModalLeavesMarginAtNarrowWidths is the sibling of providerpicker's /
// modelpicker's narrow-width cap. The agentpicker clamped modalW up to its 48
// floor with no upper cap tied to the screen, so at <=48-col terminals the
// bordered box filled (or overflowed) the canvas — touching/clipping the edge.
func TestModalLeavesMarginAtNarrowWidths(t *testing.T) {
	for _, screenW := range []int{52, 50, 48, 44, 40} {
		m := New()
		m.Open(sampleItems(), "")
		out := m.View(screenW, 30)
		if got := modalBoxWidth(out); got > screenW-marginBudget {
			t.Errorf("screenW=%d: modal box width %d leaves no breathing room (want <= %d)\n---\n%s",
				screenW, got, screenW-marginBudget, out)
		}
	}
}

// TestModalStillUsableWidthAtRoomyTerminal: the narrow-width cap must not
// shrink the modal at terminals wide enough for the design's half-screen
// width — at 120 cols the modal should keep its clamped 48..78 size.
func TestModalStillUsableWidthAtRoomyTerminal(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "")
	out := m.View(120, 40)
	if got := modalBoxWidth(out); got < 48 {
		t.Errorf("at 120 cols the modal should keep its half-screen width, got %d", got)
	}
	if got := modalBoxWidth(out); got > 120-2 {
		t.Errorf("modal width %d exceeds the margin budget at 120 cols", got)
	}
}
