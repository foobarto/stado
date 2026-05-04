package palette

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOpenShowsAllCommands(t *testing.T) {
	m := New()
	m.Open()
	if !m.Visible {
		t.Error("Open() should set Visible=true")
	}
	if len(m.Matches) != len(Commands) {
		t.Errorf("empty query should show all %d commands, got %d", len(Commands), len(m.Matches))
	}
	if m.Query != "" {
		t.Errorf("Open() should start with empty query, got %q", m.Query)
	}
}

func TestUpdate_RunesBuildQuery(t *testing.T) {
	m := New()
	m.Open()

	// Type "help" one rune at a time.
	for _, r := range "help" {
		_, handled := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if !handled {
			t.Errorf("rune %q should be handled", r)
		}
	}
	if m.Query != "help" {
		t.Errorf("Query = %q, want %q", m.Query, "help")
	}
	// /help should be the top match.
	if len(m.Matches) == 0 || m.Matches[0].Name != "/help" {
		t.Errorf("top match = %+v, want /help", m.Matches)
	}
}

func TestUpdate_QueryCapsBytes(t *testing.T) {
	m := New()
	m.Open()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", maxQueryBytes+128))})
	if got := len(m.Query); got != maxQueryBytes {
		t.Fatalf("query length = %d, want %d", got, maxQueryBytes)
	}
	m.Query = strings.Repeat("x", maxQueryBytes-1)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("é")})
	if got := len(m.Query); got != maxQueryBytes-1 {
		t.Fatalf("query length after split rune = %d, want %d", got, maxQueryBytes-1)
	}
}

func TestUpdate_BackspaceShrinksQuery(t *testing.T) {
	m := New()
	m.Open()
	m.Query = "hel"
	m.refresh()

	_, handled := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if !handled {
		t.Error("backspace should be handled")
	}
	if m.Query != "he" {
		t.Errorf("Query after backspace = %q, want 'he'", m.Query)
	}
}

func TestUpdate_CtrlUClearsQuery(t *testing.T) {
	m := New()
	m.Open()
	m.Query = "some-filter"
	m.refresh()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.Query != "" {
		t.Errorf("ctrl+u should clear Query, got %q", m.Query)
	}
}

func TestUpdate_EscapeCloses(t *testing.T) {
	m := New()
	m.Open()
	_, handled := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !handled {
		t.Error("escape should be handled")
	}
	if m.Visible {
		t.Error("escape should close the palette")
	}
}

func TestUpdate_UpDownWraps(t *testing.T) {
	m := New()
	m.Open()
	if m.Cursor != 0 {
		t.Fatalf("initial cursor = %d", m.Cursor)
	}
	// Up from top should wrap to last.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != len(m.Matches)-1 {
		t.Errorf("up-from-top cursor = %d, want %d", m.Cursor, len(m.Matches)-1)
	}
	// Down from last should wrap to first.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 0 {
		t.Errorf("down-from-last cursor = %d, want 0", m.Cursor)
	}
}

func TestUpdate_TabAdvancesCursor(t *testing.T) {
	m := New()
	m.Open()
	_, handled := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !handled {
		t.Error("tab should advance cursor")
	}
	if m.Cursor != 1 {
		t.Errorf("cursor after tab = %d, want 1", m.Cursor)
	}
}

func TestSelectedReturnsCurrent(t *testing.T) {
	m := New()
	m.Open()
	sel := m.Selected()
	if sel == nil {
		t.Fatal("Selected() nil")
	}
	if sel.Name != Commands[0].Name {
		t.Errorf("Selected.Name = %q, want %q", sel.Name, Commands[0].Name)
	}
}

func TestSelected_HiddenReturnsNil(t *testing.T) {
	m := New()
	// Not visible.
	if m.Selected() != nil {
		t.Error("Selected() on hidden palette should be nil")
	}
}

func TestView_IncludesHeaderAndSearchHint(t *testing.T) {
	m := New()
	m.Open()
	out := m.View(120, 40)
	for _, want := range []string{"Commands", "esc", "Search"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestView_HiddenReturnsEmpty(t *testing.T) {
	m := New()
	if got := m.View(80, 24); got != "" {
		t.Errorf("hidden view should be empty, got %q", got)
	}
}

func TestInlineViewRendersCompactSuggestions(t *testing.T) {
	m := New()
	m.Open()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("model")})

	got := m.InlineView(80)
	// While filtering, group headers ("Session") are dropped — they're
	// pure clutter when the user is searching. The flat list renders
	// only matching commands.
	for _, want := range []string{"Slash commands", "/model"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline view missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Session") || strings.Contains(got, "Quick") || strings.Contains(got, "View") {
		t.Errorf("inline view should NOT show group headers while filtering: %q", got)
	}
}

// TestInlineViewShowsGroupsWhenBrowsing — empty query (browse mode)
// keeps the categories visible because they help orient first-time
// users navigating the full list.
func TestInlineViewShowsGroupsWhenBrowsing(t *testing.T) {
	m := New()
	m.Open()
	// No query → browse mode.

	got := m.InlineView(80)
	for _, want := range []string{"Slash commands"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline view missing %q: %q", want, got)
		}
	}
	// At least one group label should appear in browse mode. We don't
	// assert WHICH group because the inline view shows a sliding window
	// (default 6 rows) — the visible group depends on the cursor.
	hasAnyGroup := strings.Contains(got, "Quick") ||
		strings.Contains(got, "Session") ||
		strings.Contains(got, "View")
	if !hasAnyGroup {
		t.Errorf("expected at least one group header in browse mode: %q", got)
	}
}

func TestInlineViewShowsCommandShortcutHints(t *testing.T) {
	m := New()
	m.Open()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("theme")})

	got := m.InlineView(80)
	for _, want := range []string{"/theme", "ctrl+x t"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline view missing shortcut hint %q: %q", want, got)
		}
	}
}

func TestGroupMatches_StableOrder(t *testing.T) {
	cmds := []Command{
		{Name: "/a", Group: "Quick"},
		{Name: "/b", Group: "View"},
		{Name: "/c", Group: "Quick"},
	}
	groups := groupMatches(cmds)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if groups[0].name != "Quick" || groups[1].name != "View" {
		t.Errorf("group order = %v, want Quick then View", []string{groups[0].name, groups[1].name})
	}
	if len(groups[0].items) != 2 {
		t.Errorf("Quick group should have 2 items, got %d", len(groups[0].items))
	}
}
