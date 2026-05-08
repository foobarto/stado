package tui

// Quit-confirmation popup. Triggered when the user presses Ctrl+D or
// the AppExit chord; the keypress sets state=stateQuitConfirm and the
// next View() call wraps the underlying frame with a centred warning
// box. Esc / N cancels (handled in handler_input.go); Y / Enter
// confirms via tea.Quit.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/tui/overlays"
)

// renderQuitConfirm draws the centred quit-confirmation popup over
// the underlying frame. Q4 polish pass: title is action-oriented,
// keys render as keycap-styled chips, and an in-flight subline
// surfaces when there's an unfinished tool call so the user knows
// what they'd lose by quitting now. Esc / N cancels, Y / Enter
// confirms (handled by handler_input.go; this helper is render-only).
func (m *Model) renderQuitConfirm(base string) string {
	popupW := 44
	if popupW > m.width-4 {
		popupW = m.width - 4
	}
	if popupW < 28 {
		popupW = 28
	}

	title := m.theme.Fg("warning").Bold(true).Render("Quit stado?")

	var subline string
	if name, _, ok := m.sidebarRunningTool(); ok {
		subline = m.theme.Fg("muted").Render("an in-flight " + trimSeed(name, 16) + " call will be cancelled")
	} else if m.state == stateStreaming {
		subline = m.theme.Fg("muted").Render("the current response will stop streaming")
	}

	keycap := func(label string) string {
		return lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(m.theme.Fg("muted").GetForeground()).
			Foreground(m.theme.Fg("text").GetForeground()).
			Padding(0, 1).
			Render(label)
	}
	yesKey := keycap("Y")
	noKey := keycap("N")
	yesLabel := m.theme.Fg("text_secondary").Render("quit")
	noLabel := m.theme.Fg("text_secondary").Render("cancel")
	keysRow := lipgloss.JoinHorizontal(lipgloss.Center,
		yesKey, " ", yesLabel, "    ", noKey, " ", noLabel)
	hint := m.theme.Fg("muted").Render("Enter quits · Esc cancels")

	lines := []string{title}
	if subline != "" {
		lines = append(lines, "", subline)
	}
	lines = append(lines, "", keysRow, "", hint)
	content := strings.Join(lines, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Fg("warning").GetForeground()).
		Padding(1, 2).
		Width(popupW).
		Align(lipgloss.Center).
		Render(content)
	return overlays.CenterOver(base, box, m.width, m.height)
}
