// Package agentpicker renders the in-TUI agent/mode selector.
package agentpicker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

const maxQueryBytes = 1024

type Item struct {
	ID      string
	Name    string
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
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch km.String() {
	case "up":
		m.moveCursor(-1)
		return nil, true
	case "down", "tab":
		m.moveCursor(1)
		return nil, true
	case "esc":
		m.Visible = false
		return nil, true
	case "backspace":
		if len(m.Query) > 0 {
			m.Query = textutil.TrimLastRune(m.Query)
			m.refresh()
		}
		return nil, true
	case "ctrl+u":
		m.Query = ""
		m.refresh()
		return nil, true
	case "space":
		m.Query = textutil.AppendWithinBytes(m.Query, " ", maxQueryBytes)
		m.refresh()
		return nil, true
	default:
		if km.Text != "" {
			m.Query = textutil.AppendWithinBytes(m.Query, km.Text, maxQueryBytes)
			m.refresh()
			return nil, true
		}
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
			words[i] = it.ID + " " + it.Name + " " + it.Desc
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
	modalW := clampInt(screenWidth/2, 48, 78)
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

	// Base style carrying the modal's background so every painted cell (and the
	// padding gaps between styled spans) fills its background, instead of
	// emitting a foreground-only reset that punches grey holes through the modal.
	bg := lipgloss.NewStyle().Background(theme.Background)

	title := bg.Foreground(theme.Text).Bold(true).Render("Select agent")
	esc := bg.Foreground(theme.Muted).Render("esc")
	b.WriteString(rowTwoCol(bg, innerW, title, esc))
	b.WriteString("\n\n")

	searchLabel := bg.Foreground(theme.Text).Render("Search")
	cursor := lipgloss.NewStyle().
		Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	queryDisplay := bg.Foreground(theme.Text).Render(m.Query)
	if m.Query == "" {
		b.WriteString(searchLabel + cursor)
	} else {
		b.WriteString(queryDisplay + cursor)
	}
	b.WriteString("\n\n")

	if len(m.Matches) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("no agents"))
		return b.String()
	}

	for i, it := range m.Matches {
		isSel := i == m.Cursor
		left := it.Name
		if it.Current {
			left = "* " + left
		}
		if isSel {
			// Build the selected line from plain text + plain padding and wrap
			// the whole line once so the highlight stays uniform across the gap.
			padded := plainTwoCol(innerW, left, it.Desc)
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(padded))
		} else {
			pad := maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(it.Desc), 1)
			b.WriteString(bg.Foreground(theme.Text).Render(left) +
				bg.Render(strings.Repeat(" ", pad)) +
				bg.Foreground(theme.Muted).Render(it.Desc))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rowTwoCol joins two already-styled spans with a background-painted padding
// gap so the modal background fills the space between the columns.
func rowTwoCol(bg lipgloss.Style, width int, left, right string) string {
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
	return left + bg.Render(strings.Repeat(" ", pad)) + right
}

// plainTwoCol joins two plain strings with plain padding; the caller wraps the
// whole result in a single highlight style (used for the selected row).
func plainTwoCol(width int, left, right string) string {
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

// truncateVisible bounds s to width DISPLAY columns (incl. the tail), via
// ansi.Truncate. A rune-count slice under-budgeted wide-CJK/emoji agent names
// — a name whose rune count fit width still had ~2x display width and
// hard-wrapped / overflowed the modal border. ansi.Truncate is display-width-
// and grapheme-aware.
func truncateVisible(s string, width int) string {
	if width <= 1 {
		return "."
	}
	return ansi.Truncate(s, width, ".")
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
