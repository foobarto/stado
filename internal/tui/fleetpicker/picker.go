// Package fleetpicker is the modal "/fleet" picker — lists active +
// recent background agents with terminate / view actions.
//
// Mirrors the modelpicker shape (filter input + scrollable list +
// modal modal-overlay layout) but with simpler item state since
// fleet entries don't have favorites/recents.
package fleetpicker

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
)

// Action is the result the modal returns when the user picks a row +
// presses an action key. Caller (TUI Model) interprets and dispatches.
type Action string

const (
	ActionNone   Action = ""
	ActionView   Action = "view"   // enter — switch main session to entry
	ActionCancel Action = "cancel" // ctrl+x — cancel the entry
	ActionRemove Action = "remove" // ctrl+d — drop terminal entries from registry
)

// Result captures what the user selected. Caller acts on it.
type Result struct {
	Action  Action
	FleetID string
}

// Model is the modal picker state.
type Model struct {
	Visible bool
	Items   []runtime.FleetEntry
	Query   string
	Cursor  int
	Out     Result // populated when an action key fires; caller reads + clears
}

// New returns an empty picker.
func New() *Model { return &Model{} }

// Open populates the picker with the supplied entries and shows it.
// Caller passes a fresh snapshot from runtime.Fleet.List(); the picker
// re-sorts internally if needed.
func (m *Model) Open(entries []runtime.FleetEntry) {
	m.Visible = true
	m.Items = append([]runtime.FleetEntry(nil), entries...)
	m.Query = ""
	m.Cursor = 0
	m.Out = Result{}
}

// Close dismisses the picker.
func (m *Model) Close() { m.Visible = false }

// Refresh replaces the item list with a fresh snapshot, preserving
// cursor position when possible (by FleetID match).
func (m *Model) Refresh(entries []runtime.FleetEntry) {
	if !m.Visible {
		return
	}
	prevID := ""
	if m.Cursor < len(m.Items) {
		prevID = m.Items[m.Cursor].FleetID
	}
	m.Items = append([]runtime.FleetEntry(nil), entries...)
	if prevID != "" {
		for i, it := range m.Items {
			if it.FleetID == prevID {
				m.Cursor = i
				return
			}
		}
	}
	if m.Cursor >= len(m.Items) {
		m.Cursor = max0(len(m.Items) - 1)
	}
}

// Selected returns the currently-cursored entry or nil.
func (m *Model) Selected() *runtime.FleetEntry {
	if m.Cursor < 0 || m.Cursor >= len(m.Items) {
		return nil
	}
	return &m.Items[m.Cursor]
}

// Update consumes a keypress while Visible. handled=true → caller
// must NOT forward the key further. The Result lives in m.Out when
// the user fired an action; caller reads + clears.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch km.String() {
	case "up", "ctrl+p":
		m.Cursor = max0(m.Cursor - 1)
		return nil, true
	case "down", "ctrl+n":
		if m.Cursor < len(m.Items)-1 {
			m.Cursor++
		}
		return nil, true
	case "enter":
		if sel := m.Selected(); sel != nil {
			m.Out = Result{Action: ActionView, FleetID: sel.FleetID}
		}
		return nil, true
	case "ctrl+x":
		if sel := m.Selected(); sel != nil && sel.Status == runtime.FleetStatusRunning {
			m.Out = Result{Action: ActionCancel, FleetID: sel.FleetID}
		}
		return nil, true
	case "ctrl+d":
		if sel := m.Selected(); sel != nil && sel.Status != runtime.FleetStatusRunning {
			m.Out = Result{Action: ActionRemove, FleetID: sel.FleetID}
		}
		return nil, true
	}
	return nil, false
}

// View renders the modal centered on the screen. Same body / layout
// pattern as modelpicker — see internal/tui/modelpicker/picker.go.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth*3/4, 64, 120)
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
	bg := lipgloss.NewStyle().Background(theme.Background)

	title := bg.Foreground(theme.Text).Bold(true).
		Render("Background agents")
	hints := bg.Foreground(theme.Muted).
		Render("enter view  ctrl+x cancel  ctrl+d remove  esc close")
	b.WriteString(rowTwoCol(innerW, title, hints))
	b.WriteString("\n\n")

	if len(m.Items) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).
			Render("no background agents — `/spawn <prompt>` to start one"))
		return b.String()
	}

	// Scroll window — same approach as modelpicker.
	total := len(m.Items)
	start, end := 0, total
	if maxRows > 0 && total > maxRows {
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
		b.WriteString(bg.Foreground(theme.Muted).
			Render(fmt.Sprintf("↑ %d more above", start)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		it := m.Items[i]
		isSel := i == m.Cursor
		row := renderEntryRow(it, innerW)
		if isSel {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(row))
		} else {
			b.WriteString(bg.Foreground(theme.Text).Render(row))
		}
		b.WriteString("\n")
	}
	if end < total {
		b.WriteString(bg.Foreground(theme.Muted).
			Render(fmt.Sprintf("↓ %d more below", total-end)))
		b.WriteString("\n")
	}
	// Detail pane for the selected entry.
	if sel := m.Selected(); sel != nil {
		b.WriteString("\n")
		b.WriteString(bg.Foreground(theme.Muted).
			Render(strings.Repeat("─", maxInt(innerW, 1))))
		b.WriteString("\n")
		b.WriteString(renderEntryDetail(*sel, innerW))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FleetEntry fields come from agent runs (operator-typed prompts,
// model-emitted text + tool outputs, errors from agent execution).
// All untrusted from the terminal-escape perspective: an attacker-
// influenced prompt can inject OSC52 (clipboard hijack), OSC8
// (clickable-link injection), CSI cursor moves, etc. Both the row and
// detail-pane renderers display fields on a single line. The
// `singleLineSafe` helper collapses newlines into spaces FIRST
// (preserving word boundaries — Copilot review #62: dropping the old
// `strings.ReplaceAll(... "\n", " ")` would mash "two\nwords" into
// "twowords") and THEN strips remaining control runes (including the
// other whitespace controls that the row layout can't render). The
// truncate happens after, so a boundary landing inside a CSI lead
// can't smuggle the final byte through. Codex G4/J-b P0: PR #49
// sibling-miss in the fleetpicker render path.
func singleLineSafe(s string) string {
	// Collapse \n → " " and \r → " " before StripControlChars eats
	// them; the visible result reads with word boundaries instead
	// of joined words. Tabs likewise become spaces.
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	return textutil.StripControlChars(s)
}

// minPromptW is the floor for the prompt column so even when both the
// prompt and the "last:" column want space, the prompt stays readable.
const minPromptW = 8

func renderEntryRow(e runtime.FleetEntry, innerW int) string {
	statusPill := fmt.Sprintf("[%-9s]", e.Status)
	short := e.FleetID
	if len(short) >= 8 {
		short = short[:8]
	}
	last := singleLineSafe(e.LastTool)
	if last == "" {
		last = "—"
	}

	// Bound the right-hand "last:" column to the *remaining* row width so
	// it can never overflow the modal either. G8 sized the prompt column
	// against the right column's width but built `right` from the full,
	// untruncated LastTool — a long tool name/arg (untrusted: it comes
	// from headless agent runs) produced an unbounded right column that
	// shoved the row past the border at any innerW (a reviewer probe
	// measured a 234-column row). The fixed left columns are the status
	// pill, a space, the 8-char id, and a two-space gap; reserve those
	// plus a minimum prompt and a one-space gap, then cap at 40 columns
	// so a wide modal doesn't render an unboundedly long tool string.
	const lastPrefix = "last: "
	fixed := lipgloss.Width(statusPill) + 1 + lipgloss.Width(short) + 2
	lastBudget := innerW - fixed - minPromptW - 1 - lipgloss.Width(lastPrefix)
	if lastBudget < 4 {
		lastBudget = 4
	}
	if lastBudget > 40 {
		lastBudget = 40
	}
	last = truncate(last, lastBudget)
	right := lastPrefix + last

	// Size the prompt to the *remaining* row width so the row never
	// overflows the modal. Reserve the fixed left columns, the (now
	// bounded) right column, and a one-space gap before truncating. Cap
	// at 50 columns so a wide modal doesn't render an unboundedly long
	// prompt. Pre-fix this used a hardcoded budget of 50 regardless of
	// innerW, so a long prompt in a narrow (64-wide → innerW 60) modal
	// pushed the row to ~83 columns and shoved the "last:" column past
	// the border.
	promptBudget := innerW - fixed - lipgloss.Width(right) - 1
	if promptBudget < minPromptW {
		promptBudget = minPromptW
	}
	if promptBudget > 50 {
		promptBudget = 50
	}
	prompt := truncate(singleLineSafe(strings.TrimSpace(e.Prompt)), promptBudget)

	left := fmt.Sprintf("%s %s  %s", statusPill, short, prompt)
	pad := maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", pad) + right
}

func renderEntryDetail(e runtime.FleetEntry, innerW int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)
	b.WriteString(bg.Foreground(theme.Muted).Render("Prompt: "))
	b.WriteString(bg.Foreground(theme.Text).Render(truncate(singleLineSafe(e.Prompt), maxInt(innerW-8, 30))))
	b.WriteString("\n")
	if e.SessionID != "" {
		// "Session: " label is 9 columns. Bound the id to the remaining
		// width like every other detail field — SessionID comes from
		// headless agent runs and a long one overflowed the modal border
		// (sibling of the LastTool/prompt column-bounding fix).
		b.WriteString(bg.Foreground(theme.Muted).Render("Session: "))
		b.WriteString(bg.Foreground(theme.Text).Render(truncate(singleLineSafe(e.SessionID), maxInt(innerW-9, 30))))
		b.WriteString("\n")
	}
	if e.LastText != "" {
		b.WriteString(bg.Foreground(theme.Muted).Render("Last text: "))
		b.WriteString(bg.Foreground(theme.Text).Render(truncate(singleLineSafe(e.LastText), maxInt(innerW-12, 30))))
		b.WriteString("\n")
	}
	if e.Status == runtime.FleetStatusError && e.Error != "" {
		b.WriteString(bg.Foreground(theme.Muted).Render("Error: "))
		// Copilot review #62: pre-this-fix Error wasn't ReplaceAll'd
		// either, so a multi-line error already broke the row layout
		// before the security fix touched it. singleLineSafe now
		// applies uniformly to every field so words stay readable.
		b.WriteString(bg.Foreground(theme.Text).Render(truncate(singleLineSafe(e.Error), maxInt(innerW-8, 30))))
		b.WriteString("\n")
	}
	if e.Status == runtime.FleetStatusCompleted && e.Result != "" {
		b.WriteString(bg.Foreground(theme.Muted).Render("Result: "))
		b.WriteString(bg.Foreground(theme.Text).Render(truncate(textutil.StripControlChars(e.Result), maxInt(innerW-9, 30))))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rowTwoCol — same helper modelpicker uses, lifted here to avoid a
// cross-package dep. innerW is the modal's content width.
//
// At the floor modal width (64-wide screen → modalW 64 → innerW 60) the
// header's title + key-hints content (68 columns) exceeded innerW, and
// the old `maxInt(... , 1)` gap floor let the row render at its full
// natural width (69 cols) — punching the right border on every /fleet
// open, independent of entry content. Clamp the row to innerW: the right
// column (dismissable key hints) yields first, truncated display-width
// aware so a wide-rune hint can't slip a column through; if the left
// column alone still overruns, truncate it too. Both truncations are
// grapheme-safe (never split a rune, always valid UTF-8).
func rowTwoCol(innerW int, left, right string) string {
	if innerW < 1 {
		innerW = 1
	}
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	// At least one column gap between left and right.
	if lw+rw+1 > innerW {
		// Give the left column priority but leave room for a 1-col gap.
		rightBudget := innerW - lw - 1
		if rightBudget < 0 {
			rightBudget = 0
		}
		right = truncate(right, rightBudget)
		rw = lipgloss.Width(right)
		// Left column alone may still exceed innerW (long title, narrow
		// modal). Truncate it to whatever remains after the gap + right.
		if lw+rw+1 > innerW {
			leftBudget := innerW - rw - 1
			if leftBudget < 1 {
				leftBudget = 1
			}
			left = truncate(left, leftBudget)
			lw = lipgloss.Width(left)
		}
	}
	pad := maxInt(innerW-lw-rw, 1)
	// Paint the padding gap with the modal background so the header row
	// doesn't show a grey hole between the two columns.
	gap := lipgloss.NewStyle().Background(theme.Background).Render(strings.Repeat(" ", pad))
	return left + gap + right
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

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// truncate clips s to a budget of n *display columns*, appending an
// ellipsis when it overflows. The budget is a column budget because
// renderEntryRow lays the result out against lipgloss.Width; callers
// pass widths derived from the modal's inner width.
//
// FleetEntry strings come from headless agent runs and routinely
// carry wide runes (CJK, emoji) and other multi-byte UTF-8. A naive
// byte slice (`s[:n-1]`) sliced mid-rune for any such input longer
// than the budget, leaking raw continuation bytes (invalid UTF-8)
// into the terminal and returning a string whose display width bore
// no relation to n — which then threw off the lipgloss.Width padding
// math and overflowed the row. ansi.Truncate is display-width- and
// grapheme-aware: it never splits a rune, always returns valid UTF-8,
// and bounds the result (incl. the ellipsis) to n columns.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}
