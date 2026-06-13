package overlays

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
)

// groupCommands buckets palette commands by their Group, preserving the
// first-seen group order and each group's command order — matching how the
// palette renders them. Returns the ordered group names and the bucket map.
func groupCommands(cmds []palette.Command) ([]string, map[string][]palette.Command) {
	order := make([]string, 0)
	byGroup := make(map[string][]palette.Command)
	for _, cmd := range cmds {
		if _, ok := byGroup[cmd.Group]; !ok {
			order = append(order, cmd.Group)
		}
		byGroup[cmd.Group] = append(byGroup[cmd.Group], cmd)
	}
	return order, byGroup
}

// RenderHelp paints the ? overlay. Sections: keybindings (grouped by
// action category) first, then an index of the slash-command palette
// so a user pressing ? sees both halves of the surface — previously
// the help overlay never mentioned /budget, /skill, /model, etc. and
// users had to remember to open the palette to discover them.
//
// The full content is taller than most terminals, and bubbletea v2's
// compositor clips the frame to the canvas (v1 let the overflow scroll,
// which lost the top instead of the bottom). When height > 0 the body is
// windowed to fit: scroll picks the first visible content line and the
// clamped value is returned so the caller can store it (and keep ↑/↓
// from running past the ends). A height of 0 disables windowing.
func RenderHelp(reg *keys.Registry, width, height, scroll int) (string, int) {
	groups := reg.ActionsByGroup()

	order := []string{
		"App",
		"Session",
		"Modes",
		"In-turn routing",
		"Input Navigation",
		"Input Editing",
		"History",
		"Messages View",
	}

	var b strings.Builder
	for _, groupName := range order {
		actions := groups[groupName]
		if len(actions) == 0 {
			continue
		}

		b.WriteString(theme.Title.Render(groupName) + "\n")

		for _, action := range actions {
			keyStrs := reg.HelpKeys(action)
			if len(keyStrs) == 0 {
				continue
			}

			keyStr := strings.Join(keyStrs, ", ")
			desc := keys.ActionDescriptions[action]

			line := fmt.Sprintf("  %-25s %s\n", keyStr, lipgloss.NewStyle().Foreground(theme.TextDim).Render(desc))
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	// Slash commands section. Render grouped the same way the palette
	// renders them, but as a hanging-indented name→description list so a
	// long description wraps cleanly under its command name instead of
	// flowing back to the left margin (render.WrapDescList). The list is
	// rendered to the overlay's inner width so the windowing re-wrap below
	// is a no-op for these rows.
	b.WriteString(theme.Title.Render("Slash commands") + "\n")
	dim := lipgloss.NewStyle().Foreground(theme.TextDim)
	const groupIndent = 4
	// The list is indented by groupIndent under each group header and must
	// fit the box's inner content area (innerW = width-8). So its wrap
	// width is innerW-groupIndent. Floor at 1 (WrapDescList floors its own
	// inner descWidth at 1 and never panics) rather than clamping UP to a
	// fixed minimum: a too-wide floor overflows innerW, the box re-wraps
	// the pre-wrapped rows, and the rounded border gets pushed a column off
	// the canvas at narrow widths (seen at width 28-30).
	listWidth := width - 8 - groupIndent
	if listWidth < 1 {
		listWidth = 1
	}
	groupOrder, byGroup := groupCommands(palette.Commands)
	for gi, group := range groupOrder {
		if gi > 0 {
			b.WriteString("\n")
		}
		b.WriteString(dim.Render("  "+group+":") + "\n")
		rows := make([]render.DescRow, 0, len(byGroup[group]))
		for _, cmd := range byGroup[group] {
			rows = append(rows, render.DescRow{Name: cmd.Name, Desc: cmd.Desc})
		}
		list := render.WrapDescList(rows, listWidth)
		pad := strings.Repeat(" ", groupIndent)
		for _, ln := range strings.Split(list, "\n") {
			b.WriteString(dim.Render(pad+ln) + "\n")
		}
	}

	content := strings.TrimRight(b.String(), "\n")

	// Wrap to the box's inner content width BEFORE windowing, so the
	// height budget counts rendered terminal rows rather than logical
	// lines. The final box style re-wraps anything wider than its
	// content area (width-2 total − 2 border − 4 padding), and several
	// slash-command descriptions are wider than a typical terminal —
	// windowing pre-wrap lines let the rendered box blow the budget
	// and the v2 compositor clipped the footer + bottom border.
	if innerW := width - 8; innerW > 0 {
		content = lipgloss.NewStyle().Width(innerW).Render(content)
	}

	// Window the content when it can't fit the canvas. Box chrome:
	// border (2) + Padding(1, 2) vertical (2) = 4 rows; one more body
	// row is reserved for the scroll-position footer.
	clamped := 0
	lines := strings.Split(content, "\n")
	if budget := height - 4; height > 0 && len(lines) > budget {
		visible := max(budget-1, 1)
		clamped = max(min(scroll, len(lines)-visible), 0)
		footerText := fmt.Sprintf("↑/↓ pgup/pgdn scroll — %d-%d of %d",
			clamped+1, clamped+visible, len(lines))
		// The footer joins the pre-wrapped lines, so it must respect the
		// inner width itself or it wraps into a second row and blows the
		// budget by one (seen at width 40).
		if innerW := width - 8; innerW > 0 {
			if r := []rune(footerText); len(r) > innerW {
				footerText = string(r[:innerW])
			}
		}
		content = strings.Join(append(lines[clamped:clamped+visible], dim.Render(footerText)), "\n")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Width(width - 2).
		Render(content), clamped
}
