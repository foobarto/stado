package sessionpicker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenameModeEditsSelectedLabel(t *testing.T) {
	p := New()
	p.Open([]Item{{ID: "s1", Label: "first"}, {ID: "s2", Label: "second"}}, "s2")

	if !p.BeginRename() {
		t.Fatal("BeginRename returned false")
	}
	if !p.Renaming() || p.Target().ID != "s2" {
		t.Fatalf("rename mode target = %+v", p.Target())
	}
	_, _ = p.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	for _, r := range "renamed" {
		_, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := p.RenameValue(); got != "renamed" {
		t.Fatalf("rename value = %q, want renamed", got)
	}
	if !strings.Contains(p.View(100, 30), "target: second") {
		t.Fatal("rename view should show target label")
	}
}

func TestDeleteModeShowsConfirmation(t *testing.T) {
	p := New()
	p.Open([]Item{{ID: "s1", Label: "first"}}, "s1")

	if !p.BeginDelete() {
		t.Fatal("BeginDelete returned false")
	}
	if !p.Deleting() || p.Target().ID != "s1" {
		t.Fatalf("delete mode target = %+v", p.Target())
	}
	out := p.View(100, 30)
	if !strings.Contains(out, "Delete first?") || !strings.Contains(out, "refs") {
		t.Fatalf("delete confirmation not rendered: %q", out)
	}
}

func TestDeleteModeBlocksCurrentSessionInView(t *testing.T) {
	p := New()
	p.Open([]Item{{ID: "s1", Label: "first", Current: true}}, "s1")

	if !p.BeginDelete() {
		t.Fatal("BeginDelete returned false")
	}
	out := p.View(100, 30)
	if !strings.Contains(out, "Cannot delete the active session") {
		t.Fatalf("active delete warning not rendered: %q", out)
	}
	if strings.Contains(out, "enter/y delete") {
		t.Fatalf("active delete view should not offer delete confirmation: %q", out)
	}
}

func TestBackspaceRemovesOneRune(t *testing.T) {
	p := New()
	p.Open([]Item{{ID: "s1", Label: "zażółć"}}, "s1")

	for _, r := range "zaż" {
		_, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := p.Query; got != "za" {
		t.Fatalf("query = %q, want za", got)
	}
}
