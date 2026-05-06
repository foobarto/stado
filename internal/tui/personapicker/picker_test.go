package personapicker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sampleItems() []Item {
	return []Item{
		{ID: "default", Title: "Default", Description: "Stado boot prompt", Origin: "bundled"},
		{ID: "software-engineer", Title: "Software Engineer", Description: "Coding agent", Origin: "bundled"},
		{ID: "qa-tester", Title: "QA Tester", Description: "Test design", Origin: "bundled"},
		{ID: "writer-custom", Title: "Custom Writer", Description: "User override", Origin: "custom"},
	}
}

func TestOpenPreselectsCurrent(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "qa-tester")
	sel := m.Selected()
	if sel == nil || sel.ID != "qa-tester" {
		t.Fatalf("expected cursor on qa-tester, got %+v", sel)
	}
	if !sel.Current {
		t.Fatalf("selected current item should be marked Current")
	}
	if got := m.View(120, 40); !strings.Contains(got, "* qa-tester") {
		t.Fatalf("rendered picker missing current marker: %q", got)
	}
}

func TestOpenFallbackCursorZero(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "unknown")
	sel := m.Selected()
	if sel == nil || sel.ID != "default" {
		t.Fatalf("expected cursor at [0], got %+v", sel)
	}
}

func TestFuzzyFilters(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "default")
	m.Cursor = 3

	for _, r := range "soft" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.Matches) != 1 || m.Matches[0].ID != "software-engineer" {
		t.Fatalf("expected single software-engineer match, got %+v", m.Matches)
	}
	if m.Cursor != 0 {
		t.Errorf("cursor should clamp into reduced match list, got %d", m.Cursor)
	}
}

func TestEscapeCloses(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "default")
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.Visible {
		t.Error("escape should close the picker")
	}
}

func TestClosedNoUpdate(t *testing.T) {
	m := New()
	if cmd, handled := m.Update(tea.KeyMsg{Type: tea.KeyDown}); handled || cmd != nil {
		t.Errorf("update on closed picker must be a no-op, got handled=%v cmd=%v", handled, cmd)
	}
	if got := m.View(80, 30); got != "" {
		t.Errorf("View on closed picker should be empty, got %q", got)
	}
}

func TestNoMatches(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "default")
	for _, r := range "zzzzz" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.View(80, 30); !strings.Contains(got, "no matches") {
		t.Errorf("expected 'no matches' line, got %q", got)
	}
	if sel := m.Selected(); sel != nil {
		t.Errorf("Selected should be nil with no matches, got %+v", sel)
	}
}
