// Package modelpicker is a modal "/model" picker: a popup with fuzzy
// search and a navigable list of model ids. Opened when the user runs
// `/model` with no args — selecting an item sets the active model for
// the current session.
//
// Rendering + keybindings mirror palette.Model so the UX is consistent:
// arrow keys / j k navigate, Esc closes, Enter resolves, typed runes
// feed the fuzzy matcher.
package modelpicker

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

const maxQueryBytes = 1024

// Item is one model entry surfaced in the picker. Origin is the
// human-readable tag ("lmstudio · detected"); ProviderName is the
// stado provider id the caller should switch to on select. Keeping
// them separate lets the display text be verbose without making the
// TUI re-parse it back into a provider name.
type Item struct {
	ID           string // model name, assigned to m.model on select
	Origin       string // human display — e.g. "anthropic", "lmstudio · detected"
	ProviderName string // stado provider id — set when selecting should swap providers
	Note         string // optional per-model hint (context window, etc.)
	Current      bool
	Recent       bool
	Favorite     bool
}

// Model is the modal picker. Open populates Items; Update handles the
// keypress loop while Visible is true.
type Model struct {
	Visible bool
	Query   string
	Items   []Item
	Matches []Item
	Cursor  int

	// Outer screen size so View can centre the modal.
	Width  int
	Height int
}

func New() *Model { return &Model{} }

// Open (items, current) shows the picker seeded with items. current is
// the active model id — the cursor lands on it if present so Enter is
// a no-op confirm.
func (m *Model) Open(items []Item, current string) {
	m.Visible = true
	m.Query = ""
	m.Items = append([]Item(nil), items...)
	for i := range m.Items {
		m.Items[i].Current = m.Items[i].Current || m.Items[i].ID == current
	}
	m.refresh()
	for i, it := range m.Matches {
		if it.ID == current {
			m.Cursor = i
			return
		}
	}
	m.Cursor = 0
}

// Close dismisses the picker without a selection.
func (m *Model) Close() { m.Visible = false }

// Selected returns the highlighted item, or nil when the picker is
// hidden / empty.
func (m *Model) Selected() *Item {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

func (m *Model) SetFavorite(id, providerName string, favorite bool) {
	m.updateFavorite(m.Items, id, providerName, favorite)
	m.updateFavorite(m.Matches, id, providerName, favorite)
}

func (m *Model) updateFavorite(items []Item, id, providerName string, favorite bool) {
	for i := range items {
		if items[i].ID == id && items[i].ProviderName == providerName {
			items[i].Favorite = favorite
		}
	}
}

// Update consumes a keypress while Visible. handled=true means the
// caller MUST NOT forward the key to the underlying text input.
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

// refresh recomputes Matches from Query. Empty query shows everything
// in Items order.
func (m *Model) refresh() {
	q := strings.TrimSpace(m.Query)
	if q == "" {
		m.Matches = append([]Item(nil), m.Items...)
	} else {
		words := make([]string, len(m.Items))
		for i, it := range m.Items {
			words[i] = it.ID + " " + it.Origin + " " + it.Note
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

// View renders the modal centred on the provided canvas size.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 56, 96)
	// Compute the row budget for the match list: total screen minus
	// modal chrome (border 2 + padding 0 + title row 1 + blank 1 +
	// search row 1 + blank 1 + truncation indicators ~2 = 8). Leave
	// at least 5 rows visible even on tiny terminals.
	maxRows := screenHeight - 8
	if maxRows < 5 {
		maxRows = 5
	}
	body := m.renderBody(modalW-4, maxRows)
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

func (m *Model) renderBody(innerW, maxRows int) string {
	var b strings.Builder

	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render("Select a model")
	hints := lipgloss.NewStyle().Foreground(theme.Muted).Render("ctrl+a setup  ctrl+f favorite  esc")
	b.WriteString(rowTwoCol(innerW, title, hints))
	b.WriteString("\n\n")

	// Search input line (same shape as palette).
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
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("no matches"))
		return b.String()
	}

	// Compute a scroll window around the cursor that fits in maxRows.
	// When matches > maxRows, show "↑ N more" / "↓ N more" indicators
	// so the user knows there's content off-screen.
	total := len(m.Matches)
	start, end := 0, total
	if maxRows > 0 && total > maxRows {
		// Center the cursor in the visible window where possible.
		half := maxRows / 2
		start = m.Cursor - half
		if start < 0 {
			start = 0
		}
		end = start + maxRows
		if end > total {
			end = total
			start = end - maxRows
			if start < 0 {
				start = 0
			}
		}
	}

	if start > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render(fmt.Sprintf("↑ %d more above", start)))
		b.WriteString("\n")
	}
	// Track when we cross from the favorites section into the rest
	// of the list so we can emit a horizontal separator. Favorites
	// are guaranteed to lead the matches list because
	// prependModelFavorites runs before the picker opens.
	prevWasFav := false
	for i := start; i < end; i++ {
		it := m.Matches[i]
		// Insert a horizontal rule the first time we transition from
		// favorite → non-favorite within the visible window.
		if prevWasFav && !it.Favorite {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
				Render(strings.Repeat("─", maxInt(innerW, 1))))
			b.WriteString("\n")
		}
		prevWasFav = it.Favorite

		isSel := i == m.Cursor
		left := it.ID
		if it.Current {
			left = "* " + left
		}
		right := it.Origin
		if it.Note != "" {
			right += "  " + it.Note
		}
		if it.Favorite {
			right += "  favorite"
		}
		if it.Recent {
			right += "  recent"
		}
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
	if end < total {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render(fmt.Sprintf("↓ %d more below", total-end)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rowTwoCol / clampInt / maxInt — lifted from palette; kept local to
// avoid a cross-package dependency on a sibling TUI package.
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
		return "…"
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
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

// Compile-time acknowledgement that fmt's used in comments only. Keeps
// the import list honest when future methods add format strings.
var _ = fmt.Sprintf
