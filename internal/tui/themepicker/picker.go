// Package themepicker renders the in-TUI theme selector.
package themepicker

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
)

const maxQueryBytes = 1024

type Item struct {
	ID      string
	Name    string
	Mode    string
	Desc    string
	Current bool
}

type Model struct {
	Visible bool
	Query   string
	Items   []Item
	Matches []Item
	Cursor  int

	Width  int
	Height int
}

func New() *Model { return &Model{} }

func (m *Model) Open(items []Item, current string) {
	m.Visible = true
	m.Query = ""
	m.Items = append([]Item(nil), items...)
	for i := range m.Items {
		m.Items[i].Current = m.Items[i].Current || m.Items[i].ID == current
	}
	m.refresh()
	m.Cursor = 0
	for i, it := range m.Matches {
		if it.ID == current {
			m.Cursor = i
			return
		}
	}
}

func (m *Model) Close() { m.Visible = false }

func (m *Model) Selected() *Item {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch km.Type {
	case tea.KeyUp:
		m.moveCursor(-1)
		return nil, true
	case tea.KeyDown, tea.KeyTab:
		m.moveCursor(1)
		return nil, true
	case tea.KeyEscape:
		m.Visible = false
		return nil, true
	case tea.KeyBackspace:
		if len(m.Query) > 0 {
			m.Query = textutil.TrimLastRune(m.Query)
			m.refresh()
		}
		return nil, true
	case tea.KeyCtrlU:
		m.Query = ""
		m.refresh()
		return nil, true
	case tea.KeyRunes:
		m.Query = textutil.AppendWithinBytes(m.Query, string(km.Runes), maxQueryBytes)
		m.refresh()
		return nil, true
	case tea.KeySpace:
		m.Query = textutil.AppendWithinBytes(m.Query, " ", maxQueryBytes)
		m.refresh()
		return nil, true
	}
	return nil, false
}

func (m *Model) moveCursor(delta int) {
	if len(m.Matches) == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor = (m.Cursor + delta + len(m.Matches)) % len(m.Matches)
}

func (m *Model) refresh() {
	q := strings.TrimSpace(m.Query)
	if q == "" {
		m.Matches = append([]Item(nil), m.Items...)
	} else {
		words := make([]string, len(m.Items))
		for i, it := range m.Items {
			words[i] = it.ID + " " + it.Name + " " + it.Mode + " " + it.Desc
		}
		found := fuzzy.Find(q, words)
		m.Matches = nil
		for _, f := range found {
			m.Matches = append(m.Matches, m.Items[f.Index])
		}
	}
	if m.Cursor >= len(m.Matches) {
		m.Cursor = len(m.Matches) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 52, 82)
	body := m.renderBody(modalW - 4)
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		Width(modalW).
		Render(body)
	return lipgloss.Place(screenWidth, screenHeight,
		lipgloss.Center, lipgloss.Center,
		modal)
}

func (m *Model) renderBody(innerW int) string {
	var b strings.Builder

	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render("Select theme")
	esc := lipgloss.NewStyle().Foreground(theme.Muted).Render("esc")
	b.WriteString(rowTwoCol(innerW, title, esc))
	b.WriteString("\n\n")

	searchLabel := lipgloss.NewStyle().Foreground(theme.Text).Render("Search")
	cursor := lipgloss.NewStyle().
		Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	queryDisplay := lipgloss.NewStyle().Foreground(theme.Text).Render(m.Query)
	if m.Query == "" {
		b.WriteString(searchLabel + cursor)
	} else {
		b.WriteString(queryDisplay + cursor)
	}
	b.WriteString("\n\n")

	if len(m.Matches) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("no themes"))
		return b.String()
	}

	for i, it := range m.Matches {
		isSel := i == m.Cursor
		left := it.Name
		if it.Current {
			left = "* " + left
		}
		right := strings.TrimSpace(it.Mode + "  " + it.Desc)
		padded := rowTwoCol(innerW, left, right)
		if isSel {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(padded))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(left) +
				strings.Repeat(" ", maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)) +
				lipgloss.NewStyle().Foreground(theme.Muted).Render(right))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func rowTwoCol(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		budget := width - rw - 2
		if budget < 3 {
			budget = 3
		}
		left = truncateVisible(left, budget)
		lw = lipgloss.Width(left)
	}
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func truncateVisible(s string, width int) string {
	if width <= 1 {
		return "."
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "."
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
