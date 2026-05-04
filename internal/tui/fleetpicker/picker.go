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

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/runtime"
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
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch km.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.Cursor = max0(m.Cursor - 1)
		return nil, true
	case tea.KeyDown, tea.KeyCtrlN:
		if m.Cursor < len(m.Items)-1 {
			m.Cursor++
		}
		return nil, true
	case tea.KeyEnter:
		if sel := m.Selected(); sel != nil {
			m.Out = Result{Action: ActionView, FleetID: sel.FleetID}
		}
		return nil, true
	case tea.KeyCtrlX:
		if sel := m.Selected(); sel != nil && sel.Status == runtime.FleetStatusRunning {
			m.Out = Result{Action: ActionCancel, FleetID: sel.FleetID}
		}
		return nil, true
	case tea.KeyCtrlD:
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

	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).
		Render("Background agents")
	hints := lipgloss.NewStyle().Foreground(theme.Muted).
		Render("enter view  ctrl+x cancel  ctrl+d remove  esc close")
	b.WriteString(rowTwoCol(innerW, title, hints))
	b.WriteString("\n\n")

	if len(m.Items) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
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
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
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
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(row))
		}
		b.WriteString("\n")
	}
	if end < total {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render(fmt.Sprintf("↓ %d more below", total-end)))
		b.WriteString("\n")
	}
	// Detail pane for the selected entry.
	if sel := m.Selected(); sel != nil {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render(strings.Repeat("─", maxInt(innerW, 1))))
		b.WriteString("\n")
		b.WriteString(renderEntryDetail(*sel, innerW))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderEntryRow(e runtime.FleetEntry, innerW int) string {
	statusPill := fmt.Sprintf("[%-9s]", e.Status)
	short := e.FleetID
	if len(short) >= 8 {
		short = short[:8]
	}
	prompt := truncate(strings.ReplaceAll(strings.TrimSpace(e.Prompt), "\n", " "), 50)
	last := e.LastTool
	if last == "" {
		last = "—"
	}
	left := fmt.Sprintf("%s %s  %s", statusPill, short, prompt)
	right := fmt.Sprintf("last: %s", last)
	pad := maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", pad) + right
}

func renderEntryDetail(e runtime.FleetEntry, innerW int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Prompt: "))
	b.WriteString(truncate(strings.ReplaceAll(e.Prompt, "\n", " "), maxInt(innerW-8, 30)))
	b.WriteString("\n")
	if e.SessionID != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Session: "))
		b.WriteString(e.SessionID)
		b.WriteString("\n")
	}
	if e.LastText != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Last text: "))
		b.WriteString(truncate(strings.ReplaceAll(e.LastText, "\n", " "), maxInt(innerW-12, 30)))
		b.WriteString("\n")
	}
	if e.Status == runtime.FleetStatusError && e.Error != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Error: "))
		b.WriteString(truncate(e.Error, maxInt(innerW-8, 30)))
		b.WriteString("\n")
	}
	if e.Status == runtime.FleetStatusCompleted && e.Result != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Result: "))
		b.WriteString(truncate(strings.ReplaceAll(e.Result, "\n", " "), maxInt(innerW-9, 30)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rowTwoCol — same helper modelpicker uses, lifted here to avoid a
// cross-package dep. innerW is the modal's content width.
func rowTwoCol(innerW int, left, right string) string {
	pad := maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", pad) + right
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

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
