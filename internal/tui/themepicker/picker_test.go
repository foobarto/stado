package themepicker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOpenMarksCurrentTheme(t *testing.T) {
	p := New()
	p.Open([]Item{
		{ID: "stado-dark", Name: "Stado Dark"},
		{ID: "stado-light", Name: "Stado Light"},
	}, "stado-light")

	sel := p.Selected()
	if sel == nil || sel.ID != "stado-light" || !sel.Current {
		t.Fatalf("selected = %+v, want current stado-light", sel)
	}
}

func TestFilterMatchesModeAndDescription(t *testing.T) {
	p := New()
	p.Open([]Item{
		{ID: "stado-dark", Name: "Stado Dark", Mode: "dark", Desc: "Default"},
		{ID: "stado-light", Name: "Stado Light", Mode: "light", Desc: "Bright neutral"},
	}, "")

	_, handled := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bright")})
	if !handled {
		t.Fatal("query key should be handled")
	}
	if len(p.Matches) != 1 || p.Matches[0].ID != "stado-light" {
		t.Fatalf("matches = %+v, want only stado-light", p.Matches)
	}
	if !strings.Contains(p.View(100, 30), "Stado Light") {
		t.Fatal("view should include filtered theme")
	}
}
