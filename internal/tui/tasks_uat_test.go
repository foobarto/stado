package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tasks"
)

func TestUAT_TasksSlashOpensTaskBrowser(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := scenarioModel(t)
	m.cfg = &config.Config{}

	typeString(m, "/tasks")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.taskPick.Visible {
		t.Fatal("/tasks should open the task browser")
	}
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "Tasks") || !strings.Contains(out, "ctrl+n new") {
		t.Fatalf("task browser did not render expected controls:\n%s", out)
	}
}

func TestUAT_TaskBrowserCRUD(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := scenarioModel(t)
	m.cfg = &config.Config{}

	if err := m.openTaskPicker(); err != nil {
		t.Fatalf("openTaskPicker: %v", err)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	typeString(m, "Review v1")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	store, err := m.taskStore()
	if err != nil {
		t.Fatalf("taskStore: %v", err)
	}
	list, err := store.List("")
	if err != nil {
		t.Fatalf("List after create: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Review v1" || list[0].Status != tasks.StatusOpen {
		t.Fatalf("created list = %+v", list)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeString(m, "Review v1 security")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	list, err = store.List("")
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Review v1 security" {
		t.Fatalf("updated list = %+v", list)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	list, err = store.List("")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("tasks after delete = %+v", list)
	}
}
