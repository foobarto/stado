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

	"charm.land/lipgloss/v2"
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
	} else if hint := m.renderChordHint(mainW - 4); hint != "" {
		// A multi-chord prefix (e.g. ctrl+x) is primed — show the possible
		// second chords so the operator doesn't have to remember them.
		pickerPrefix = hint + "\n"
	}

	// The v2 textarea renders its filler rows (the empty space below the
	// content, up to its set height) with Inline-styled end-of-buffer
	// cells that leave background holes the surrounding box can't repaint
	// — so the waiting/short-input states showed grey filler rows. Keep
	// the painted content rows and swap the holey filler for clean empty
	// lines, which lipgloss fills with the box's surface background.
	taView := m.input.View()
	taLines := strings.Split(taView, "\n")
	if content := m.input.Model.LineCount(); content >= 1 && content < len(taLines) {
		clean := append([]string(nil), taLines[:content]...)
		for range taLines[content:] {
			clean = append(clean, "")
		}
		taView = strings.Join(clean, "\n")
	}

	// The inline status row ("Do · model · provider") is built from
	// foreground-only template spans and is shorter than the frame, so the
	// box can't fill the trailing area past its reset — leaving a grey
	// strip on the right of that row. Pad it to the content width with the
	// surface background so the row reads solid.
	inlineLine := strings.TrimRight(inline, "\n")
	if innerW := mainW - 3; innerW > 0 { // 1 border + 2 padding
		if w := lipgloss.Width(inlineLine); w < innerW {
			inlineLine += m.theme.Bg("surface").Render(strings.Repeat(" ", innerW-w))
		}
	}

	body := pickerPrefix + taView + "\n" + inlineLine

	style := m.theme.Bg("surface").
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(m.theme.Fg(m.inputBorderTone()).GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1).
		Width(mainW).
		// Hard-clamp to the allotted width so the box can never push past
		// the terminal edge. Needed since the textarea now paints its
		// trailing cells with the surface background (so they're no longer
		// trimmed as bare whitespace) — without this a 1-col trailing pad
		// can tip a narrow terminal's input box over budget.
		MaxWidth(mainW)
	return style.Render(body) + "\n"
}

func (m *Model) inputBorderTone() string {
	// While a multi-chord prefix is primed, dim the mode strip so the
	// operator's eye is drawn to the chord hint above the input.
	if _, ok := m.keys.PendingPrefix(); ok {
		return "muted"
	}
	switch m.mode {
	case modePlan:
		return "role_thinking"
	case modeBTW:
		return "accent"
	default:
		return "role_user"
	}
}

// renderChordHint draws the chord-continuation hint shown above the input
// while a multi-chord prefix (e.g. ctrl+x) is primed: the pressed prefix
// plus each possible next chord and the action it completes, e.g.
//
//	ctrl+x  ·  m model · a agents · l sessions · …
//
// Returns "" when no prefix is pending. Every span paints the surface
// background so the hint reads solid inside the input frame.
func (m *Model) renderChordHint(innerW int) string {
	pc, ok := m.keys.PendingPrefix()
	if !ok {
		return ""
	}
	bg := m.theme.Bg("surface")
	var b strings.Builder
	b.WriteString(bg.Foreground(m.theme.Fg("accent").GetForeground()).Bold(true).Render(prettyChord(pc.Prefix)))
	b.WriteString(bg.Foreground(m.theme.Fg("muted").GetForeground()).Render("  ·  "))
	for i, opt := range pc.Options {
		if i > 0 {
			b.WriteString(bg.Foreground(m.theme.Fg("muted").GetForeground()).Render("  "))
		}
		b.WriteString(bg.Foreground(m.theme.Fg("text_secondary").GetForeground()).Bold(true).Render(prettyChord(opt.Key)))
		b.WriteString(bg.Render(" "))
		b.WriteString(bg.Foreground(m.theme.Fg("muted").GetForeground()).Render(opt.Desc))
	}
	line := b.String()
	// Pad the trailing area so the hint row reads solid like the rest of
	// the frame. Overflow (a very long option list on a narrow terminal)
	// is clamped by the input box's MaxWidth.
	if w := lipgloss.Width(line); w < innerW {
		line += bg.Render(strings.Repeat(" ", innerW-w))
	}
	return line
}

// prettyChord renders "ctrl+x" as "C-x" and "ctrl+b" as "C-b" for a
// compact Emacs-style hint; non-ctrl chords (a single letter like "m")
// pass through unchanged.
func prettyChord(c string) string {
	c = strings.TrimSpace(c)
	if strings.HasPrefix(c, "ctrl+") {
		return "C-" + strings.TrimPrefix(c, "ctrl+")
	}
	return c
}
