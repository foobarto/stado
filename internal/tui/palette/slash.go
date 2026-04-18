package palette

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

type Command struct {
	Name string
	Desc string
}

var Commands = []Command{
	{"/help", "Show keyboard shortcuts and help"},
	{"/clear", "Clear message history"},
	{"/model", "Change model for current session"},
	{"/provider", "Show current provider"},
	{"/exit", "Quit application"},
}

type Model struct {
	Visible bool
	Filter  string
	Matches []Command
	Cursor  int
	Width   int
}

func New() *Model {
	return &Model{
		Matches: Commands,
	}
}

func (m *Model) UpdateFilter(text string) {
	if !strings.HasPrefix(text, "/") {
		m.Visible = false
		return
	}
	m.Visible = true
	m.Filter = text

	if text == "/" {
		m.Matches = Commands
		m.Cursor = 0
		return
	}

	search := text[1:]
	words := []string{}
	for _, c := range Commands {
		words = append(words, c.Name[1:]+" "+c.Desc)
	}

	matches := fuzzy.Find(search, words)
	m.Matches = nil
	for _, match := range matches {
		m.Matches = append(m.Matches, Commands[match.Index])
	}
	if m.Cursor >= len(m.Matches) {
		m.Cursor = len(m.Matches) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.Cursor > 0 {
				m.Cursor--
			} else {
				m.Cursor = len(m.Matches) - 1
			}
			return nil, true
		case tea.KeyDown:
			if m.Cursor < len(m.Matches)-1 {
				m.Cursor++
			} else {
				m.Cursor = 0
			}
			return nil, true
		case tea.KeyEscape:
			m.Visible = false
			return nil, true
		}
	}
	return nil, false
}

func (m *Model) Selected() *Command {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

func (m *Model) View() string {
	if !m.Visible || len(m.Matches) == 0 {
		return ""
	}

	var b strings.Builder
	for i, match := range m.Matches {
		if i > 5 {
			break // Show max 6
		}
		cursor := "  "
		style := lipgloss.NewStyle().Foreground(theme.TextDim)
		if i == m.Cursor {
			cursor = "▸ "
			style = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
		}
		line := fmt.Sprintf("%s%-10s %s", cursor, match.Name, match.Desc)
		b.WriteString(style.Render(line) + "\n")
	}
	content := strings.TrimRight(b.String(), "\n")
	maxW := m.Width - 4
	if maxW < 10 {
		maxW = 10
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		MaxWidth(maxW).
		Render(content)
}
