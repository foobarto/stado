package treepicker

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/foobarto/stado/internal/tui/theme"
)

// Glyph language (per the /tree design):
//
//	▸   you-are-here marker (current session)
//	●   live status, ○ idle, ◌ detached (dimmed, switch gated off)
//	⑂   fork-origin marker — "⑂ turn N" (edge) / "⑂ unlinked" (mid-turn) /
//	    "⑂ orphan" (parent refs gone)
//	│   indent spine, one level per depth
const (
	glyphCurrent  = "▸"  // ▸
	glyphLive     = "●"  // ●
	glyphIdle     = "○"  // ○
	glyphDetached = "◌"  // ◌
	glyphFork     = "⑂"  // ⑂
	indentUnit    = "  " // two columns per depth level
)

// View renders the modal centred on a screenWidth × screenHeight canvas,
// mirroring the sessionpicker/taskpicker layout (rounded border, padding,
// lipgloss.Place). The row list is windowed to the available height so a
// 159-session forest doesn't overflow the canvas.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 58, 98)
	// Height budget: total = list + border(2) + header/blank/blank chrome(4) +
	// footer(1). Window the row list to what's left.
	listBudget := screenHeight - 7
	if listBudget < 3 {
		listBudget = 3
	}
	body := m.renderBody(modalW-4, listBudget) // -4 for border + padding
	// lipgloss v2: .Width is the TOTAL rendered width (border + padding
	// included), so .Width(modalW) keeps the modal exactly modalW wide.
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

func (m *Model) renderBody(innerW, maxListRows int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)

	titleText := "Session tree"
	hints := "enter switch  ↑↓ move  →/← expand/collapse  g/G  esc"
	title := bg.Foreground(theme.Text).Bold(true).Render(titleText)
	esc := bg.Foreground(theme.Muted).Render(hints)
	b.WriteString(rowTwoColBg(innerW, titleText, hints, title, esc, bg))
	b.WriteString("\n\n")

	if len(m.rows) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("no sessions"))
		return b.String()
	}

	// Render each row fresh so the selection highlight tracks the cursor
	// without a rebuild. innerW is the content width budget (the modal's
	// inner width); rows pad the two columns out to fill it.
	lines := make([]string, len(m.rows))
	for i, r := range m.rows {
		if r.nodeIdx < 0 {
			lines[i] = m.renderTurnLine(r.turnText, innerW)
			continue
		}
		lines[i] = m.renderNodeLine(m.nodes[r.nodeIdx], i == m.cursor, innerW)
	}
	windowed := windowLines(lines, m.cursor, maxListRows)
	b.WriteString(strings.Join(windowed, "\n"))
	return b.String()
}

// renderNodeLine renders one session header row to a width of innerW: indent
// spine, you-are-here marker, status glyph, label, fork-origin tag, and a
// right-aligned meta column. A selected row paints the whole width in the
// Primary highlight (so the gap doesn't punch a hole through the selection).
func (m *Model) renderNodeLine(n Node, selected bool, innerW int) string {
	indent := strings.Repeat(indentUnit, maxInt(n.Depth, 0))
	marker := "  "
	if n.IsCurrent {
		marker = glyphCurrent + " "
	}

	// Left column is built as plain text first (for width math); the status
	// glyph is colourised separately on the non-selected path.
	leftPlain := indent + marker + glyphFor(n.Avail) + " " + nodeLabel(n)
	if origin := forkOriginTag(n); origin != "" {
		leftPlain += "  " + origin
	}
	right := n.Meta

	if selected {
		// Plain text + plain padding wrapped in one Primary span so every
		// cell (incl. the gap + glyph) is uniformly highlighted — the
		// palette/taskpicker selected-row idiom.
		line := rowTwoCol(innerW, leftPlain, right)
		return lipgloss.NewStyle().
			Background(theme.Primary).
			Foreground(theme.Background).
			Render(line)
	}

	bg := lipgloss.NewStyle().Background(theme.Background)
	leftFg := theme.Text
	if n.Avail == AvailDetached {
		leftFg = theme.Muted // dim the whole row for an unswitchable session
	}
	// Truncate the left column if the two columns would overflow innerW.
	left := leftPlain
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > innerW {
		budget := maxInt(innerW-lipgloss.Width(right)-2, 3)
		left = truncateVisible(left, budget)
	}
	// Re-colourise the status glyph in place of the plain placeholder so a
	// live/idle/detached row keeps its coloured dot on the non-selected path.
	coloured := strings.Replace(left, glyphFor(n.Avail), statusGlyph(n.Avail), 1)
	pad := maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return bg.Foreground(leftFg).Render(coloured) +
		bg.Render(strings.Repeat(" ", pad)) +
		bg.Foreground(theme.Muted).Render(right)
}

// renderTurnLine renders one expanded turn row beneath its session, muted
// (turn rows are context-only in this increment — not selectable).
func (m *Model) renderTurnLine(text string, innerW int) string {
	bg := lipgloss.NewStyle().Background(theme.Background)
	line := "    └ " + text
	line = truncateVisible(line, innerW)
	return bg.Foreground(theme.Muted).Render(line)
}

// glyphFor returns the PLAIN (uncoloured) status glyph used during left-column
// width math; statusGlyph wraps it in the availability colour for display.
func glyphFor(a Avail) string {
	switch a {
	case AvailLive:
		return glyphLive
	case AvailIdle:
		return glyphIdle
	default:
		return glyphDetached
	}
}

func statusGlyph(a Avail) string {
	switch a {
	case AvailLive:
		return lipgloss.NewStyle().Background(theme.Background).Foreground(theme.Success).Render(glyphLive)
	case AvailIdle:
		return lipgloss.NewStyle().Background(theme.Background).Foreground(theme.Text).Render(glyphIdle)
	default:
		return lipgloss.NewStyle().Background(theme.Background).Foreground(theme.Muted).Render(glyphDetached)
	}
}

func nodeLabel(n Node) string {
	if strings.TrimSpace(n.Label) != "" {
		return n.Label
	}
	return n.ID
}

// forkOriginTag renders the "⑂ turn N" / "⑂ unlinked" / "⑂ orphan" origin
// marker, or "" for a plain root.
func forkOriginTag(n Node) string {
	switch {
	case n.Orphan:
		return glyphFork + " orphan"
	case n.HasParent && n.ParentTurn >= 1:
		return glyphFork + " turn " + strconv.Itoa(n.ParentTurn)
	case n.HasParent:
		return glyphFork + " unlinked"
	default:
		return ""
	}
}

// windowLines returns at most budget lines, sliding the window so cursorLine
// stays visible (cursor sticks to the window bottom once it scrolls) — the
// palette's height-windowing behaviour.
func windowLines(lines []string, cursorLine, budget int) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	start := 0
	if cursorLine >= budget {
		start = cursorLine - budget + 1
	}
	if start+budget > len(lines) {
		start = len(lines) - budget
	}
	if start < 0 {
		start = 0
	}
	return lines[start : start+budget]
}

// rowTwoColBg lays out two already-styled spans across width, painting the
// inter-column padding gap with bg so it doesn't punch a hole in the modal
// background. Mirrors sessionpicker.rowTwoColBg.
func rowTwoColBg(width int, leftPlain, rightPlain, leftSpan, rightSpan string, bg lipgloss.Style) string {
	lw := lipgloss.Width(leftPlain)
	rw := lipgloss.Width(rightPlain)
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return leftSpan + bg.Render(strings.Repeat(" ", pad)) + rightSpan
}

// rowTwoCol produces a line of exactly width visible columns with left at the
// start and right at the end, padded between. Long left columns are truncated
// with an ellipsis. Mirrors sessionpicker.rowTwoCol; used for the selected
// row where the whole line is painted in one highlight span.
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
