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
//
// F10: per-option `Input` fields render as a textfield prefix +
// label. The validation error (when set) sits between the rows
// and the hint so the operator sees it with their typing focus
// still in view. A single-option-with-input with no Label is the
// "bare-input shortcut" — renders as a plain TUI input prompt
// instead of a one-row chooser.
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

	// F10: bare-input shortcut — single option carrying only an
	// Input field (no Label, no other choices) renders as a plain
	// input prompt instead of a one-row chooser. Detect at the top
	// so the rest of the drawer logic doesn't have to special-case
	// the chooser scaffolding away.
	bareInput := !m.choice.multi &&
		len(m.choice.options) == 1 &&
		m.choice.options[0].Input != nil &&
		m.choice.options[0].Label == ""

	hasAnyInput := false
	for _, opt := range m.choice.options {
		if opt.Input != nil {
			hasAnyInput = true
			break
		}
	}

	parts := []string{title, ""}
	if bareInput {
		// One row, the input field; no checkbox, no chevron, no
		// truncation logic — the field gets the full width.
		opt := m.choice.options[0]
		parts = append(parts, "  "+m.renderChoiceInputRow(opt, 0, true, innerW))
	} else {
		const maxVisible = 8
		first, last := choiceWindow(m.choiceCursor, len(m.choice.options), maxVisible)
		rows := make([]string, 0, last-first)
		for i := first; i < last; i++ {
			opt := m.choice.options[i]
			row := m.renderChoiceRow(opt, i, i == m.choiceCursor, m.choiceMarked[opt.ID], m.choice.multi, innerW)
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
		parts = append(parts, rows...)
		if indicator != "" {
			parts = append(parts, indicator)
		}
	}

	// F10: validation error sits between the rows and the hint so
	// the operator sees it with their typing focus still in view.
	if m.choiceValidationErr != "" {
		parts = append(parts, "", m.theme.Fg("error").Render("  ✗ "+m.choiceValidationErr))
	}

	var hint string
	switch {
	case m.choice.multi:
		hint = m.theme.Fg("muted").Render("↑/↓ navigate · Space toggle · Enter confirm · Esc cancel")
	case bareInput:
		hint = m.theme.Fg("muted").Render("type input · Enter confirm · Esc cancel")
	case hasAnyInput:
		hint = m.theme.Fg("muted").Render("↑/↓ navigate · type input · Enter confirm · Esc cancel")
	default:
		hint = m.theme.Fg("muted").Render("↑/↓ navigate · Enter confirm · Esc cancel")
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
//
// F10: when opt.Input != nil, the row renders as
// `prefix [textfield] label` — the textfield holds the row's
// current input value (m.choiceInputs[index]) with a caret on the
// cursor row.
func (m *Model) renderChoiceRow(opt pluginRuntime.ChoiceOption, index int, isCursor, marked, multi bool, innerW int) string {
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
	// F10: input-bearing rows render the textfield instead of a
	// label. Multi mode keeps the checkbox but skips the textfield —
	// inputs don't combine with multi-select per the F10 spec.
	if opt.Input != nil && !multi {
		return cursor + m.renderChoiceInputRow(opt, index, isCursor, innerW)
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

// renderChoiceInputRow draws an option whose Input field is non-nil
// as `prefix [textfield] label`. The cursor row gets a caret at the
// end of the buffer; non-cursor rows render the value plain. F10.
func (m *Model) renderChoiceInputRow(opt pluginRuntime.ChoiceOption, index int, isCursor bool, innerW int) string {
	prefix := opt.Prefix
	if prefix != "" {
		prefix = m.theme.Fg("muted").Render(prefix) + " "
	}
	value := ""
	if index >= 0 && index < len(m.choiceInputs) {
		value = m.choiceInputs[index]
	}
	field := value
	if isCursor {
		field += m.theme.Fg("accent").Bold(true).Render("▏")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(m.theme.Fg("accent").GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		PaddingLeft(0).
		Render(field)
	label := opt.Label
	if label != "" {
		label = "  " + m.theme.Fg("muted").Render(truncate(label, innerW-lipgloss.Width(box)-lipgloss.Width(prefix)-4))
	}
	return prefix + box + label
}

// appendChoiceInputRune appends a rune to the cursor row's input
// buffer (clamped to the input default-size cap) and clears any
// previously displayed validation error so the user sees a fresh
// state as they edit. F10.
func (m *Model) appendChoiceInputRune(r rune) {
	if m.choiceCursor >= len(m.choiceInputs) {
		return
	}
	cur := m.choiceInputs[m.choiceCursor]
	// Same byte cap as the input default — gives the operator
	// roughly 4 KiB of typing room before the runtime truncates.
	if len(cur)+len(string(r)) > 4<<10 {
		return
	}
	m.choiceInputs[m.choiceCursor] = cur + string(r)
	m.choiceValidationErr = ""
	m.renderBlocks()
}

// popChoiceInputRune removes the last rune from the cursor row's
// input buffer. F10.
func (m *Model) popChoiceInputRune() {
	if m.choiceCursor >= len(m.choiceInputs) {
		return
	}
	cur := m.choiceInputs[m.choiceCursor]
	if cur == "" {
		return
	}
	runes := []rune(cur)
	m.choiceInputs[m.choiceCursor] = string(runes[:len(runes)-1])
	m.choiceValidationErr = ""
	m.renderBlocks()
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
// is open. ↑ / ↓ moves the cursor; Space toggles in multi mode (or
// inserts a literal space into the cursor row's input field when the
// cursor is on an input-bearing option in single mode); printable
// runes / Backspace edit the input buffer; Enter validates (when the
// option carries a validator) and confirms; Esc cancels
// (cancelled=true to plugin). F10.
func (m *Model) handleChoiceKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.choice == nil {
		return nil, false
	}
	cursorHasInput := m.choiceCursor >= 0 &&
		m.choiceCursor < len(m.choice.options) &&
		m.choice.options[m.choiceCursor].Input != nil
	switch msg.Type {
	case tea.KeyEsc:
		return m.resolveChoiceCancel(), true
	case tea.KeyUp:
		if m.choiceCursor > 0 {
			m.choiceCursor--
			m.choiceValidationErr = ""
			m.renderBlocks()
		}
		return nil, true
	case tea.KeyDown:
		if m.choiceCursor < len(m.choice.options)-1 {
			m.choiceCursor++
			m.choiceValidationErr = ""
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
			return nil, true
		}
		if cursorHasInput {
			m.appendChoiceInputRune(' ')
			return nil, true
		}
		return nil, true
	case tea.KeyBackspace:
		if cursorHasInput && !m.choice.multi {
			m.popChoiceInputRune()
			return nil, true
		}
		return nil, true
	case tea.KeyRunes:
		if cursorHasInput && !m.choice.multi {
			for _, r := range msg.Runes {
				m.appendChoiceInputRune(r)
			}
			return nil, true
		}
		return nil, true
	case tea.KeyEnter:
		if m.choice.multi {
			var selected []string
			for _, opt := range m.choice.options {
				if m.choiceMarked[opt.ID] {
					selected = append(selected, opt.ID)
				}
			}
			return m.resolveChoice(selected, "", false), true
		}
		opt := m.choice.options[m.choiceCursor]
		input := ""
		if cursorHasInput {
			input = m.choiceInputs[m.choiceCursor]
			if opt.Input.Validator != nil {
				if err := pluginRuntime.ValidateChoiceInput(input, opt.Input.Validator); err != nil {
					m.choiceValidationErr = err.Error()
					m.renderBlocks()
					return nil, true
				}
			}
		}
		return m.resolveChoice([]string{opt.ID}, input, false), true
	}
	return nil, false
}
