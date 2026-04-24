// Package sessionpicker renders the in-TUI session manager.
package sessionpicker

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

type Item struct {
	ID      string
	Label   string
	Meta    string
	Current bool
}

type mode int

const (
	modeSearch mode = iota
	modeRename
	modeDelete
)

type Model struct {
	Visible bool
	Query   string
	Items   []Item
	Matches []Item
	Cursor  int
	mode    mode
	target  Item

	Width  int
	Height int
}

func New() *Model { return &Model{} }

func (m *Model) Open(items []Item, current string) {
	m.Visible = true
	m.Query = ""
	m.mode = modeSearch
	m.target = Item{}
	m.Items = append([]Item(nil), items...)
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

func (m *Model) BeginRename() bool {
	sel := m.Selected()
	if sel == nil {
		return false
	}
	m.mode = modeRename
	m.target = *sel
	m.Query = ""
	if !strings.HasPrefix(sel.Label, sel.ID[:minInt(len(sel.ID), len(sel.Label))]) {
		m.Query = sel.Label
	}
	return true
}

func (m *Model) BeginDelete() bool {
	sel := m.Selected()
	if sel == nil {
		return false
	}
	m.mode = modeDelete
	m.target = *sel
	return true
}

func (m *Model) CancelAction() {
	m.mode = modeSearch
	m.target = Item{}
	m.Query = ""
	m.refresh()
}

func (m *Model) Renaming() bool { return m.Visible && m.mode == modeRename }
func (m *Model) Deleting() bool { return m.Visible && m.mode == modeDelete }
func (m *Model) Target() Item   { return m.target }
func (m *Model) RenameValue() string {
	return strings.TrimSpace(m.Query)
}

func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	if m.mode == modeRename {
		switch km.Type {
		case tea.KeyEsc:
			m.CancelAction()
			return nil, true
		case tea.KeyBackspace:
			if len(m.Query) > 0 {
				m.Query = trimLastRune(m.Query)
			}
			return nil, true
		case tea.KeyCtrlU:
			m.Query = ""
			return nil, true
		case tea.KeyRunes:
			m.Query += string(km.Runes)
			return nil, true
		case tea.KeySpace:
			m.Query += " "
			return nil, true
		case tea.KeyUp, tea.KeyDown, tea.KeyTab:
			return nil, true
		}
		return nil, false
	}
	if m.mode == modeDelete {
		switch km.Type {
		case tea.KeyEsc:
			m.CancelAction()
			return nil, true
		case tea.KeyUp, tea.KeyDown, tea.KeyTab, tea.KeyBackspace, tea.KeyCtrlU, tea.KeyRunes, tea.KeySpace:
			return nil, true
		}
		return nil, false
	}
	switch km.Type {
	case tea.KeyUp:
		m.moveCursor(-1)
		return nil, true
	case tea.KeyDown, tea.KeyTab:
		m.moveCursor(1)
		return nil, true
	case tea.KeyEsc:
		m.Visible = false
		return nil, true
	case tea.KeyBackspace:
		if len(m.Query) > 0 {
			m.Query = trimLastRune(m.Query)
			m.refresh()
		}
		return nil, true
	case tea.KeyCtrlU:
		m.Query = ""
		m.refresh()
		return nil, true
	case tea.KeyRunes:
		m.Query += string(km.Runes)
		m.refresh()
		return nil, true
	case tea.KeySpace:
		m.Query += " "
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
			words[i] = it.ID + " " + it.Label + " " + it.Meta
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
	modalW := clampInt(screenWidth/2, 56, 96)
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

	titleText := "Sessions"
	if m.mode == modeRename {
		titleText = "Rename session"
	}
	if m.mode == modeDelete {
		titleText = "Delete session"
	}
	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render(titleText)
	hints := "enter switch  ctrl+n new  ctrl+r rename  ctrl+f fork  ctrl+d delete  esc"
	if m.mode == modeRename {
		hints = "enter save  esc cancel"
	}
	if m.mode == modeDelete {
		hints = "enter/y delete  esc/n cancel"
		if m.target.Current {
			hints = "esc/n cancel"
		}
	}
	esc := lipgloss.NewStyle().Foreground(theme.Muted).Render(hints)
	b.WriteString(rowTwoCol(innerW, title, esc))
	b.WriteString("\n\n")

	if m.mode == modeDelete {
		label := m.target.Label
		if label == "" {
			label = m.target.ID
		}
		if m.target.Current {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Error).Bold(true).
				Render("Cannot delete the active session."))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
				Render("Switch to another session first, then delete this one."))
			return b.String()
		}
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Error).Bold(true).
			Render("Delete " + label + "?"))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render("This removes the session refs, worktree, and conversation log."))
		return b.String()
	}

	searchText := "Search"
	if m.mode == modeRename {
		searchText = "Name"
	}
	searchLabel := lipgloss.NewStyle().Foreground(theme.Text).Render(searchText)
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

	if m.mode == modeRename {
		target := shortLabel(m.target)
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render("target: " + target))
		return b.String()
	}

	if len(m.Matches) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("no sessions"))
		return b.String()
	}

	start := 0
	limit := 12
	if len(m.Matches) < limit {
		limit = len(m.Matches)
	}
	if m.Cursor >= limit {
		start = m.Cursor - limit + 1
	}
	for i, it := range m.Matches[start : start+limit] {
		idx := start + i
		isSel := idx == m.Cursor
		left := it.Label
		if left == "" {
			left = it.ID
		}
		if it.Current {
			left = "* " + left
		}
		padded := rowTwoCol(innerW, left, it.Meta)
		if isSel {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(padded))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(left) +
				strings.Repeat(" ", maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(it.Meta), 1)) +
				lipgloss.NewStyle().Foreground(theme.Muted).Render(it.Meta))
		}
		b.WriteString("\n")
	}
	if hidden := len(m.Matches) - limit; hidden > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render("+" + strconv.Itoa(hidden) + " more; keep typing to narrow"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortLabel(it Item) string {
	if it.Label != "" {
		return it.Label
	}
	return it.ID
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
