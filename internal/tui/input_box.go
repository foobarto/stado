package tui

// Input-box rendering — the bordered chat-input area at the bottom of
// the conversation pane (and centred on the landing screen). Wraps a
// textarea, an inline status line ("Mode · Model · Provider"), and an
// optional popover (slash palette inline view OR @-trigger file
// picker) inside one surfaced rounded frame. Border tone adapts to
// the input mode (Do / Plan / BTW) so the operator can tell at a
// glance which pipeline a turn will route into.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderInputBox produces the bordered input area: a textarea stacked on
// top of an inline status line (Mode · Model · Provider), all inside one
// surfaced rounded frame.
func (m *Model) renderInputBox(mainW int) string {
	inline, err := m.renderer.Exec("input_status", map[string]any{
		"Mode":         m.mode.String(),
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Hint":         "", // reserved — "xhigh" effort-style badge lands when reasoning-effort config does
	})
	if err != nil {
		inline = "[input status render error: " + err.Error() + "]"
	}

	// File-picker popover (triggered by `@` in the buffer). Rendered
	// INSIDE the bordered input frame, above the textarea, so the
	// suggestion column stays visually anchored to the input cursor
	// instead of floating at the top of the screen.
	var pickerPrefix string
	if m.slash.Visible && m.slashInline {
		pickerPrefix = m.slash.InlineView(mainW-4) + "\n"
	} else if m.filePicker.Visible && len(m.filePicker.Matches) > 0 {
		// Leave 2 cols of breathing room inside the border + padding.
		pickerPrefix = m.filePicker.View(mainW-4) + "\n"
	}

	body := pickerPrefix + m.input.View() + "\n" + strings.TrimRight(inline, "\n")

	style := m.theme.Bg("surface").
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(m.theme.Fg(m.inputBorderTone()).GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1).
		Width(mainW - 1)
	return style.Render(body) + "\n"
}

func (m *Model) inputBorderTone() string {
	switch m.mode {
	case modePlan:
		return "role_thinking"
	case modeBTW:
		return "accent"
	default:
		return "role_user"
	}
}
