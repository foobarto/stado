package agentpicker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
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

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", maxQueryBytes+128))})
	if got := len(m.Query); got != maxQueryBytes {
		t.Fatalf("query length = %d, want %d", got, maxQueryBytes)
	}
	m.Query = strings.Repeat("x", maxQueryBytes-1)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("é")})
	if got := len(m.Query); got != maxQueryBytes-1 {
		t.Fatalf("query length after split rune = %d, want %d", got, maxQueryBytes-1)
	}
}

func TestEscapeCloses(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "")
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.Visible {
		t.Error("escape should close")
	}
	if m.Selected() != nil {
		t.Error("no selected item after close")
	}
}
