package taskpicker

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tasks"
)

func TestPickerCreateCommand(t *testing.T) {
	m := New()
	m.Open(nil, "")

	cmd, handled := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !handled || cmd.Type != CommandNone {
		t.Fatalf("ctrl+n handled=%v cmd=%+v", handled, cmd)
	}
	for _, r := range "Ship tasks" {
		cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd.Type != CommandCreate || cmd.Title != "Ship tasks" || cmd.Status != tasks.StatusOpen {
		t.Fatalf("create cmd = %+v", cmd)
	}
}

func TestPickerDetailAndDeleteCommand(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	m := New()
	m.Open([]tasks.Task{{ID: "task-1", Title: "Review", Status: tasks.StatusOpen, UpdatedAt: now}}, "")

	cmd, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd.Type != CommandNone {
		t.Fatalf("detail cmd = %+v", cmd)
	}
	cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd.Type != CommandNone {
		t.Fatalf("delete prompt cmd = %+v", cmd)
	}
	cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd.Type != CommandDelete || cmd.ID != "task-1" {
		t.Fatalf("delete cmd = %+v", cmd)
	}
}

func TestPickerCapsSearchQuery(t *testing.T) {
	m := New()
	m.Open(nil, "")

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", maxQueryBytes+128))})
	if len(m.Query) != maxQueryBytes {
		t.Fatalf("query length = %d, want %d", len(m.Query), maxQueryBytes)
	}
}

func TestPickerCapsFormTitleAndBody(t *testing.T) {
	m := New()
	m.Open(nil, "")

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", tasks.MaxTitleBytes+128))})
	if len(m.formTitle) != tasks.MaxTitleBytes {
		t.Fatalf("title length = %d, want %d", len(m.formTitle), tasks.MaxTitleBytes)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", tasks.MaxBodyBytes+128))})
	if len(m.formBody) != tasks.MaxBodyBytes {
		t.Fatalf("body length = %d, want %d", len(m.formBody), tasks.MaxBodyBytes)
	}
}
