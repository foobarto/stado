package tui

// Plugin approval drawer — when a plugin calls stado_ui_approve, the
// host bridges the request through pluginApprovalRequestMsg, the
// runtime sets m.approval and switches state to stateApproval, and
// the next View() reserves vertical space for this drawer between the
// chat viewport and the input box. Y/N keys (or ←/→/Enter when the
// drawer has focus) resolve the request via the response channel.
//
// Per A1 audit: this is NOT an "overlay" in the modal-takeover sense.
// It's a layout-contributing component — approvalCardHeight is
// subtracted from main pane height before the conversation viewport
// is sized.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m *Model) approvalCardHeight(mainW int) int {
	card := m.renderApprovalCard(mainW)
	if card == "" {
		return 0
	}
	return lipgloss.Height(card) + 1
}

func (m *Model) renderApprovalCard(mainW int) string {
	if m.approval == nil {
		return ""
	}

	innerW := mainW - 8
	if innerW < 8 {
		innerW = 8
	}

	rawTitle := strings.TrimSpace(m.approval.title)
	if rawTitle == "" {
		rawTitle = "Approval required"
	}
	icon := m.theme.Fg("warning").Bold(true).Render("⚠ ")
	title := icon + m.theme.Fg("warning").Bold(true).Render(rawTitle)

	body := m.renderApprovalBody(strings.TrimSpace(m.approval.body), innerW)

	allow := m.renderApprovalButton("Allow", "Y", m.approvalAllowSelected, "success")
	deny := m.renderApprovalButton("Deny", "N", !m.approvalAllowSelected, "error")
	buttonsRow := lipgloss.JoinHorizontal(lipgloss.Top, allow, "  ", deny)

	var hint string
	if m.approvalFocused {
		hint = m.theme.Fg("warning").Render("← / →  switch · Enter confirm · ↓ return to input")
	} else {
		hint = m.theme.Fg("muted").Render("Y allow · N deny · ↑ focus drawer")
	}

	cardBody := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		body,
		"",
		buttonsRow,
		hint,
	)

	border := m.theme.Fg("border").GetForeground()
	if m.approvalFocused {
		border = m.theme.Fg("warning").GetForeground()
	}
	style := m.theme.Bg("surface").
		Border(m.theme.Border()).
		BorderForeground(border).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1)
	if mainW > 2 {
		style = style.Width(mainW - 2)
	}
	return style.Render(cardBody)
}

// renderApprovalBody styles the request body. When the body looks
// command-shaped (has a "$ " prefix, backticks, or is multi-line)
// it renders inside a faint bordered code-block frame so a long
// shell command stays scannable; otherwise it's plain text. Capped
// at innerW*3 chars so the drawer never balloons over a chatty body.
func (m *Model) renderApprovalBody(body string, innerW int) string {
	if body == "" {
		return ""
	}
	body = truncate(body, innerW*3)
	codeShaped := strings.Contains(body, "\n") ||
		strings.HasPrefix(body, "$ ") ||
		strings.HasPrefix(body, "$") ||
		strings.Contains(body, "`")
	if !codeShaped {
		return m.theme.Fg("text").Render(body)
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(m.theme.Fg("muted").GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		PaddingLeft(1)
	if innerW > 4 {
		style = style.Width(innerW - 2)
	}
	return style.Render(body)
}

// renderApprovalButton draws one of the two action buttons. The
// active one gets a tone-tinted border AND background fill so the
// selection contrast survives in low-contrast themes; the inactive
// one stays muted.
func (m *Model) renderApprovalButton(label, key string, active bool, tone string) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Fg("border").GetForeground()).
		Padding(0, 2).
		Foreground(m.theme.Fg("muted").GetForeground())
	if active {
		style = style.
			BorderForeground(m.theme.Fg(tone).GetForeground()).
			Foreground(m.theme.Fg(tone).GetForeground()).
			Bold(true)
	}
	keyChip := m.theme.Fg(tone).Bold(true).Render(key)
	if !active {
		keyChip = m.theme.Fg("muted").Render(key)
	}
	return style.Render(keyChip + "  " + label)
}

func (m *Model) handleApprovalKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if approvalChoice(msg, 'y') {
		return m.resolveApproval(true), true
	}
	if approvalChoice(msg, 'n') {
		return m.resolveApproval(false), true
	}

	switch msg.Type {
	case tea.KeyUp:
		if m.filePicker.Visible {
			return nil, false
		}
		if !m.approvalFocused {
			m.approvalFocused = true
			m.renderBlocks()
		}
		return nil, true
	case tea.KeyDown:
		if m.approvalFocused {
			m.approvalFocused = false
			m.renderBlocks()
			return nil, true
		}
		if m.filePicker.Visible {
			return nil, false
		}
	case tea.KeyLeft:
		if m.approvalFocused && !m.approvalAllowSelected {
			m.approvalAllowSelected = true
			m.renderBlocks()
			return nil, true
		}
	case tea.KeyRight:
		if m.approvalFocused && m.approvalAllowSelected {
			m.approvalAllowSelected = false
			m.renderBlocks()
			return nil, true
		}
	case tea.KeyEnter:
		if m.approvalFocused {
			return m.resolveApproval(m.approvalAllowSelected), true
		}
		if m.filePicker.Visible {
			return nil, false
		}
		// Keep the editor live while approval is pending, but don't
		// submit a new turn until the pending tool decision resolves.
		return nil, true
	}

	return nil, false
}

func approvalChoice(msg tea.KeyMsg, want rune) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	r := msg.Runes[0]
	return r == want || r == want-'a'+'A'
}
