package modelpicker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sampleItems() []Item {
	return []Item{
		{ID: "claude-opus-4-7", Origin: "anthropic", Note: "200K"},
		{ID: "claude-sonnet-4-5", Origin: "anthropic"},
		{ID: "gpt-5", Origin: "openai"},
		{ID: "llama-3.3-70b-versatile", Origin: "groq"},
	}
}

// TestOpenPreselectsCurrent ensures the cursor lands on the current
// model when it's in the list — Enter is a no-op confirm.
func TestOpenPreselectsCurrent(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "gpt-5")
	sel := m.Selected()
	if sel == nil || sel.ID != "gpt-5" {
		t.Fatalf("expected cursor on gpt-5, got %+v", sel)
	}
	if !sel.Current {
		t.Fatalf("selected current item should be marked Current")
	}
	if got := m.View(120, 40); !strings.Contains(got, "* gpt-5") {
		t.Fatalf("rendered picker missing current marker: %q", got)
	}
}

// TestOpenFallbackCursorZero covers the case where current isn't in
// the catalog — cursor defaults to 0.
func TestOpenFallbackCursorZero(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "some-unknown-model")
	sel := m.Selected()
	if sel == nil || sel.ID != "claude-opus-4-7" {
		t.Fatalf("expected cursor at [0], got %+v", sel)
	}
}

// TestFuzzyFiltersAndCursorClamps: typing narrows the match list; the
// cursor clamps into the new range.
func TestFuzzyFiltersAndCursorClamps(t *testing.T) {
	m := New()
	m.Open(sampleItems(), "claude-sonnet-4-5")
	m.Cursor = 3 // far-right in the full list

	// Type "sonnet" — only sonnet entry should match.
	for _, r := range "sonnet" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(m.Matches))
	}
	if m.Cursor != 0 {
		t.Errorf("cursor should clamp to 0, got %d", m.Cursor)
	}
}

// TestEscapeCloses covers dismiss-without-select.
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

// TestCatalogForKnownProviders returns something non-empty.
func TestCatalogForKnownProviders(t *testing.T) {
	for _, p := range []string{"anthropic", "openai", "google", "groq", "deepseek", "mistral", "xai", "cerebras"} {
		items := CatalogFor(p)
		if len(items) == 0 {
			t.Errorf("catalog empty for %q", p)
		}
		for _, it := range items {
			if it.ID == "" {
				t.Errorf("%s: item missing id", p)
			}
		}
	}
	if got := CatalogFor("completely-unknown"); got != nil {
		t.Errorf("unknown provider should return nil, got %v", got)
	}
}

// TestMergeLocalMergesAndOverrides checks that a detected local model
// present in the catalog gets its Origin rewritten + new ids are
// appended.
func TestMergeLocalMergesAndOverrides(t *testing.T) {
	catalog := []Item{
		{ID: "llama3.3-70b", Origin: "cerebras"},
		{ID: "qwen-32b", Origin: "cerebras"},
	}
	got := MergeLocal(catalog, "lmstudio", true, []string{"llama3.3-70b", "mistral-nemo"})

	// llama3.3-70b Origin rewritten.
	var found, brandNew bool
	for _, it := range got {
		if it.ID == "llama3.3-70b" && strings.Contains(it.Origin, "lmstudio · detected") {
			found = true
		}
		if it.ID == "mistral-nemo" && strings.Contains(it.Origin, "lmstudio · detected") {
			brandNew = true
		}
	}
	if !found {
		t.Errorf("llama3.3-70b should have been rewritten to detected")
	}
	if !brandNew {
		t.Errorf("mistral-nemo should have been added as new")
	}
}

// TestMergeLocalIgnoresUnreachable: a down runner shouldn't mutate the
// catalog.
func TestMergeLocalIgnoresUnreachable(t *testing.T) {
	catalog := []Item{{ID: "llama3", Origin: "cerebras"}}
	got := MergeLocal(catalog, "lmstudio", false, []string{"anything"})
	if len(got) != 1 || got[0].Origin != "cerebras" {
		t.Errorf("unreachable runner mutated the catalog: %v", got)
	}
}
