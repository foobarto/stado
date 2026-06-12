package treepicker

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/tui/overlays"
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
	glyphMutated  = "⟳"  // post_tool hook rewrote a tool result
	glyphDenied   = "⊘"  // pre_tool hook vetoed a call
	indentUnit    = "  " // two columns per depth level
)

// provenanceBadge renders the compact hook-mutation badge (spec
// hooks-audit-mutation-provenance STAGE 7b): `⟳N` when N post_tool mutations
// happened, `⊘M` when M pre_tool denials happened, space-joined when both.
// "" when neither — the common case, so an unaffected row carries no badge.
func provenanceBadge(mutated, denied int) string {
	var parts []string
	if mutated > 0 {
		parts = append(parts, glyphMutated+strconv.Itoa(mutated))
	}
	if denied > 0 {
		parts = append(parts, glyphDenied+strconv.Itoa(denied))
	}
	return strings.Join(parts, " ")
}

// View renders the modal centred on a screenWidth × screenHeight canvas,
// mirroring the sessionpicker/taskpicker layout (rounded border, padding,
// lipgloss.Place). The row list is windowed to the available height so a
// 159-session forest doesn't overflow the canvas.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 58, 98)
	// Height budget: total = list + border(2) + header+blank(2) +
	// blank+footer(2). Window the row list to what's left.
	listBudget := screenHeight - 8
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
	base := lipgloss.Place(screenWidth, screenHeight,
		lipgloss.Center, lipgloss.Center,
		modal)
	// Peek composes over the tree: the transcript box sits centred on top of
	// the tree, which stays visible behind it (the design's CenterOver layer).
	if m.peek != nil {
		return overlays.CenterOver(base, m.peek.box(screenWidth, screenHeight), screenWidth, screenHeight)
	}
	return base
}

func (m *Model) renderBody(innerW, maxListRows int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)

	titleText := "Session tree"
	hints := "enter switch/peek  b branch  →/← expand  g/G  esc"
	// Keep the header on ONE row: at common widths (<~127 cols) the inner
	// width can't fit title + 1-col gap + the full hint, and rowTwoColBg only
	// pads (never truncates), so the over-long span lipgloss-hard-wraps past
	// the box and pushes every row down. Ellipsize the hint to whatever space
	// the title leaves so it stays a single line (display-width aware).
	hintBudget := innerW - lipgloss.Width(titleText) - 1
	if hintBudget < 0 {
		hintBudget = 0
	}
	if lipgloss.Width(hints) > hintBudget {
		hints = ansi.Truncate(hints, hintBudget, "…")
	}
	title := bg.Foreground(theme.Text).Bold(true).Render(titleText)
	esc := bg.Foreground(theme.Muted).Render(hints)
	b.WriteString(rowTwoColBg(innerW, titleText, hints, title, esc, bg))
	b.WriteString("\n\n")

	if len(m.rows) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("no sessions"))
		b.WriteString("\n\n")
		b.WriteString(m.renderFooter(innerW, bg))
		return b.String()
	}

	// Render each row fresh so the selection highlight tracks the cursor
	// without a rebuild. innerW is the content width budget (the modal's
	// inner width); rows pad the two columns out to fill it. Turn rows are
	// landable now — a selected turn row paints its highlight too.
	lines := make([]string, len(m.rows))
	for i, r := range m.rows {
		if r.isTurn {
			text := r.turn.Text
			if badge := provenanceBadge(r.turn.MutatedCount, r.turn.DeniedCount); badge != "" {
				text += "  " + badge
			}
			lines[i] = m.renderTurnLine(text, i == m.cursor, innerW)
			continue
		}
		lines[i] = m.renderNodeLine(m.nodes[r.nodeIdx], i == m.cursor, innerW)
	}
	windowed := windowLines(lines, m.cursor, maxListRows)
	b.WriteString(strings.Join(windowed, "\n"))
	b.WriteString("\n\n")
	b.WriteString(m.renderFooter(innerW, bg))
	return b.String()
}

// renderFooter renders the bottom status line: a transient notice if one is
// pending, otherwise the session count + a "(capped)" warning when the forest
// hit MaxForestSessions. Stage-7 cap-footer surfacing.
func (m *Model) renderFooter(innerW int, bg lipgloss.Style) string {
	if m.notice != "" {
		return bg.Foreground(theme.Warning).Render(truncateVisible(m.notice, innerW))
	}
	count := strconv.Itoa(m.total) + " session"
	if m.total != 1 {
		count += "s"
	}
	if m.truncated {
		warn := bg.Foreground(theme.Warning).
			Render(truncateVisible(count+"  ⚠ capped at "+strconv.Itoa(maxForestCap)+" — more exist", innerW))
		return warn
	}
	return bg.Foreground(theme.Muted).Render(truncateVisible(count, innerW))
}

// maxForestCap mirrors runtime.MaxForestSessions for the footer message
// without pulling a runtime import into this package (the design's
// runtime-free treepicker rule). Kept in lockstep by the host's open path,
// which is the only place the real cap is enforced.
const maxForestCap = 5000

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
	// glyph is colourised separately on the non-selected path. The fork-origin
	// tag + provenance badge are a FIXED-cost suffix: they must survive
	// truncation (a deep node's long label must not slice off its "⑂ turn N"
	// origin), so the LABEL is truncated to fit the budget BEFORE the suffix is
	// appended, not the whole column truncated as a unit afterwards.
	prefix := indent + marker + glyphFor(n.Avail) + " "
	var suffix string
	if origin := forkOriginTag(n); origin != "" {
		suffix += "  " + origin
	}
	if badge := provenanceBadge(n.MutatedCount, n.DeniedCount); badge != "" {
		suffix += "  " + badge
	}
	right := n.Meta
	// Fit the label between prefix and the always-kept suffix. The full left
	// column must leave 1 col of gap before the right column.
	leftPlain := fitLeftColumn(prefix, nodeLabel(n), suffix, right, innerW)

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
	// Re-colourise the status glyph in place of the plain placeholder so a
	// live/idle/detached row keeps its coloured dot on the non-selected path.
	coloured := strings.Replace(leftPlain, glyphFor(n.Avail), statusGlyph(n.Avail), 1)
	pad := maxInt(innerW-lipgloss.Width(leftPlain)-lipgloss.Width(right), 1)
	return bg.Foreground(leftFg).Render(coloured) +
		bg.Render(strings.Repeat(" ", pad)) +
		bg.Foreground(theme.Muted).Render(right)
}

// renderTurnLine renders one expanded turn row beneath its session. Turn rows
// are landable (Enter peeks, b branches), so a selected turn row paints the
// full-width Primary highlight like a selected session row; an unselected one
// stays muted.
func (m *Model) renderTurnLine(text string, selected bool, innerW int) string {
	line := "    └ " + text
	if selected {
		padded := rowTwoCol(innerW, line, "")
		return lipgloss.NewStyle().
			Background(theme.Primary).
			Foreground(theme.Background).
			Render(padded)
	}
	bg := lipgloss.NewStyle().Background(theme.Background)
	return bg.Foreground(theme.Muted).Render(truncateVisible(line, innerW))
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

// fitLeftColumn assembles "prefix + label + suffix" so it fits the column
// budget (innerW minus the right column minus a 1-col gap), truncating ONLY the
// label when it overflows. prefix (indent/marker/glyph) and suffix (fork-origin
// tag + provenance badge) are fixed cost and always survive — a long label can
// never slice the "⑂ turn N" origin off a deep fork node (P3.1). Truncation is
// display-width + grapheme aware (ansi.Truncate). When even the fixed cost
// can't fit, the label collapses to its ellipsis and the suffix is kept.
func fitLeftColumn(prefix, label, suffix, right string, innerW int) string {
	full := prefix + label + suffix
	// Budget for the whole left column: leave the right column + 1-col gap.
	budget := innerW - lipgloss.Width(right) - 1
	if budget < 1 {
		budget = 1
	}
	if lipgloss.Width(full) <= budget {
		return full
	}
	// Overflow: shrink the label to whatever the fixed prefix+suffix leaves.
	labelBudget := budget - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	if labelBudget < 1 {
		labelBudget = 1
	}
	label = ansi.Truncate(label, labelBudget, "…")
	return prefix + label + suffix
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
