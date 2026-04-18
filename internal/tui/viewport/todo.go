package viewport

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tools/todo"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

type TodoModel struct {
	Viewport viewport.Model
	todos    []todo.Todo
	reg      *keys.Registry
}

func NewTodo(reg *keys.Registry) *TodoModel {
	vp := viewport.New(0, 0)
	return &TodoModel{
		Viewport: vp,
		reg:      reg,
	}
}

func (m *TodoModel) SetTodos(todos []todo.Todo) {
	m.todos = todos
	m.render()
}

func (m *TodoModel) SetSize(width, height int) {
	m.Viewport.Width = width
	m.Viewport.Height = height
	m.render() // re-render to wrap
}

func (m *TodoModel) HasTodos() bool {
	return len(m.todos) > 0
}

func (m *TodoModel) render() {
	if len(m.todos) == 0 {
		m.Viewport.SetContent("")
		return
	}

	var b strings.Builder
	b.WriteString(theme.Title.Render("📋 Todos") + "\n\n")

	for i, t := range m.todos {
		icon := " "
		color := theme.Text
		switch t.Status {
		case "completed":
			icon = "✓"
			color = theme.Success
		case "in_progress":
			icon = "▶"
			color = theme.Warning
		case "cancelled":
			icon = "✗"
			color = theme.TextDim
		default:
			icon = "○"
		}

		style := lipgloss.NewStyle().Foreground(color)

		priority := ""
		if t.Priority == "high" && t.Status != "completed" && t.Status != "cancelled" {
			priority = theme.ErrorStyle.Render(" !")
		}

		line := fmt.Sprintf("%s %s%s", style.Render(icon), style.Render(t.Content), priority)
		
		// Optional: wrap the line to viewport width if needed, but lipgloss usually handles that if we set widths
		b.WriteString(line + "\n")
		
		if i < len(m.todos)-1 {
			b.WriteString("\n")
		}
	}
	m.Viewport.SetContent(b.String())
}

func (m *TodoModel) View() string {
	return m.Viewport.View()
}
