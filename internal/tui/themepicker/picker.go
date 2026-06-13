// Package themepicker renders the in-TUI theme selector.
package themepicker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	// Base style carrying the modal background so every painted cell fills
	// solid. v2 lipgloss styles default to no background, so a bare
	// Foreground(...) span emits a reset that punches a grey hole through the
	// dark modal — between columns, after short text, on empty trailing.
	bg := lipgloss.NewStyle().Background(theme.Background)

	title := bg.Foreground(theme.Text).Bold(true).Render("Select theme")
	esc := bg.Foreground(theme.Muted).Render("esc")
	b.WriteString(rowTwoCol(innerW, title, esc))
	b.WriteString("\n\n")

	searchLabel := bg.Foreground(theme.Text).Render("Search")
	cursor := bg.Foreground(theme.Text).
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
		b.WriteString(bg.Foreground(theme.Muted).Render("no themes"))
		return b.String()
	}

	for i, it := range m.Matches {
		isSel := i == m.Cursor
		left := it.Name
		if it.Current {
			left = "* " + left
		}
		right := strings.TrimSpace(it.Mode + "  " + it.Desc)
		// Truncate both columns to innerW so a long custom theme name /
		// description can't overflow the row — an over-long row is
		// hard-wrapped by the outer modal box, turning one entry into
		// several physical rows and corrupting the layout.
		left, right, pad := fitTwoCol(innerW, left, right)
		if isSel {
			// Build the line from PLAIN text + plain padding and wrap the
			// whole thing in the Primary highlight, so every cell (including
			// the gap) is uniformly highlighted. Routing through rowTwoCol
			// here would paint the gap with the modal background — punching a
			// dark strip through the selection.
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(left + strings.Repeat(" ", pad) + right))
		} else {
			b.WriteString(bg.Foreground(theme.Text).Render(left) +
				bg.Render(strings.Repeat(" ", pad)) +
				bg.Foreground(theme.Muted).Render(right))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func rowTwoCol(width int, left, right string) string {
	left, right, pad := fitTwoCol(width, left, right)
	// Paint the padding gap with the modal background so the header / search
	// rows don't show a grey hole between the two columns.
	gap := lipgloss.NewStyle().Background(theme.Background).Render(strings.Repeat(" ", pad))
	return left + gap + right
}

// fitTwoCol truncates the plain-text columns so that
// width(left)+pad+width(right) == width with pad >= 1, guaranteeing the
// assembled row never exceeds width display columns. left is truncated
// first, then right to whatever space remains. Both truncations are
// display-width- and grapheme-aware via truncateVisible.
func fitTwoCol(width int, left, right string) (string, string, int) {
	if width < 1 {
		width = 1
	}
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		budget := width - rw - 2
		if budget < 3 {
			budget = 3
		}
		left = truncateVisible(left, budget)
		lw = lipgloss.Width(left)
		if rmax := width - lw - 1; rw > rmax {
			right = truncateVisible(right, rmax)
			rw = lipgloss.Width(right)
		}
	}
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left, right, pad
}

// truncateVisible clips s to width display columns, appending an ellipsis when
// it overflows. Display-width- and grapheme-aware (ansi.Truncate) so the
// result's lipgloss.Width is bounded by width — fitTwoCol's row math relies on
// that, and theme names (custom themes carry any name) can hold wide runes a
// naive rune-count slice would under-budget and split. "…" tail matches the
// other pickers.
func truncateVisible(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(s, width, "…")
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
