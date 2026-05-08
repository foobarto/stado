package tui

// Plugin choice drawer — when a plugin calls stado_ui_choose, the
// host bridges the request through pluginChoiceRequestMsg, the runtime
// sets m.choice and switches state to stateChoice, and the next View()
// reserves vertical space for the drawer. ↑/↓ move the cursor; Space
// toggles in multi mode; Enter confirms; Esc cancels (sends
// cancelled=true to the plugin).
//
// Sibling shape to the approval drawer (approval.go) — a layout-
// adjusting component, not a modal overlay. choiceDrawerHeight is
// subtracted from main pane height before the conversation viewport
// is sized.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// choiceDrawerHeight reserves layout space for the choice drawer
// the same way approvalCardHeight does for the approval card —
// caller subtracts this from the main pane height.
func (m *Model) choiceDrawerHeight(mainW int) int {
	card := m.renderChoiceDrawer(mainW)
	if card == "" {
		return 0
	}
	return lipgloss.Height(card) + 1
}

// renderChoiceDrawer renders the bottom-pinned drawer for an
// in-flight stado_ui_choose request. Single mode shows ▸ at the
// cursor; multi mode shows [x] / [ ] checkboxes plus ▸. Long
// option lists scroll naturally — the renderer caps to a window
// around the cursor so the drawer never balloons over the chat.
// Q3.
func (m *Model) renderChoiceDrawer(mainW int) string {
	if m.choice == nil {
		return ""
	}
	innerW := mainW - 8
	if innerW < 8 {
		innerW = 8
	}

	icon := m.theme.Fg("accent").Bold(true).Render("? ")
	titleText := strings.TrimSpace(m.choice.prompt)
	if titleText == "" {
		titleText = "Plugin requests a choice"
	}
	title := icon + m.theme.Fg("accent").Bold(true).Render(truncate(titleText, innerW*2))

	const maxVisible = 8
	first, last := choiceWindow(m.choiceCursor, len(m.choice.options), maxVisible)
	rows := make([]string, 0, last-first)
	for i := first; i < last; i++ {
		opt := m.choice.options[i]
		row := m.renderChoiceRow(opt, i == m.choiceCursor, m.choiceMarked[opt.ID], m.choice.multi, innerW)
		rows = append(rows, row)
	}
	moreAbove := first > 0
	moreBelow := last < len(m.choice.options)
	indicator := ""
	switch {
	case moreAbove && moreBelow:
		indicator = m.theme.Fg("muted").Render(fmt.Sprintf("  ↕ %d more above / %d more below", first, len(m.choice.options)-last))
	case moreAbove:
		indicator = m.theme.Fg("muted").Render(fmt.Sprintf("  ↑ %d more above", first))
	case moreBelow:
		indicator = m.theme.Fg("muted").Render(fmt.Sprintf("  ↓ %d more below", len(m.choice.options)-last))
	}

	var hint string
	if m.choice.multi {
		hint = m.theme.Fg("muted").Render("↑/↓ navigate · Space toggle · Enter confirm · Esc cancel")
	} else {
		hint = m.theme.Fg("muted").Render("↑/↓ navigate · Enter confirm · Esc cancel")
	}

	parts := []string{title, ""}
	parts = append(parts, rows...)
	if indicator != "" {
		parts = append(parts, indicator)
	}
	parts = append(parts, "", hint)

	style := m.theme.Bg("surface").
		Border(m.theme.Border()).
		BorderForeground(m.theme.Fg("accent").GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1)
	if mainW > 2 {
		style = style.Width(mainW - 2)
	}
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// renderChoiceRow draws one option line. Cursor row is highlighted
// with a left chevron + accent fg; toggled multi-rows show [x],
// untoggled [ ]; single-mode rows have no checkbox.
func (m *Model) renderChoiceRow(opt pluginRuntime.ChoiceOption, isCursor, marked, multi bool, innerW int) string {
	cursor := "  "
	if isCursor {
		cursor = m.theme.Fg("accent").Bold(true).Render("▸ ")
	}
	checkbox := ""
	if multi {
		if marked {
			checkbox = m.theme.Fg("success").Render("[x] ")
		} else {
			checkbox = m.theme.Fg("muted").Render("[ ] ")
		}
	}
	label := opt.Label
	if label == "" {
		label = opt.ID
	}
	available := innerW - 8
	if available < 8 {
		available = 8
	}
	label = truncate(label, available)
	if isCursor {
		label = m.theme.Fg("accent").Bold(true).Render(label)
	} else {
		label = m.theme.Fg("text").Render(label)
	}
	return cursor + checkbox + label
}

// choiceWindow returns the [first, last) slice indexes to render
// when the option list is taller than maxVisible. Window slides so
// the cursor stays visible with a small leading margin.
func choiceWindow(cursor, total, maxVisible int) (first, last int) {
	if total <= maxVisible {
		return 0, total
	}
	half := maxVisible / 2
	first = cursor - half
	if first < 0 {
		first = 0
	}
	last = first + maxVisible
	if last > total {
		last = total
		first = last - maxVisible
		if first < 0 {
			first = 0
		}
	}
	return first, last
}

// handleChoiceKey routes keystrokes while the stado_ui_choose drawer
// is open. ↑ / ↓ moves the cursor; Space toggles in multi mode; Enter
// confirms (single mode = current cursor option, multi mode = sorted
// toggled ids); Esc cancels (cancelled=true to plugin). Q3.
func (m *Model) handleChoiceKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.choice == nil {
		return nil, false
	}
	switch msg.Type {
	case tea.KeyEsc:
		return m.resolveChoiceCancel(), true
	case tea.KeyUp:
		if m.choiceCursor > 0 {
			m.choiceCursor--
			m.renderBlocks()
		}
		return nil, true
	case tea.KeyDown:
		if m.choiceCursor < len(m.choice.options)-1 {
			m.choiceCursor++
			m.renderBlocks()
		}
		return nil, true
	case tea.KeySpace:
		if m.choice.multi {
			id := m.choice.options[m.choiceCursor].ID
			if m.choiceMarked == nil {
				m.choiceMarked = map[string]bool{}
			}
			m.choiceMarked[id] = !m.choiceMarked[id]
			m.renderBlocks()
		}
		return nil, true
	case tea.KeyEnter:
		var selected []string
		if m.choice.multi {
			for _, opt := range m.choice.options {
				if m.choiceMarked[opt.ID] {
					selected = append(selected, opt.ID)
				}
			}
		} else {
			selected = []string{m.choice.options[m.choiceCursor].ID}
		}
		return m.resolveChoice(selected, false), true
	}
	return nil, false
}
