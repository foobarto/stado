package overlays

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/theme"
)

// RenderHelp paints the ? overlay. Sections: keybindings (grouped by
// action category) first, then an index of the slash-command palette
// so a user pressing ? sees both halves of the surface — previously
// the help overlay never mentioned /budget, /skill, /model, etc. and
// users had to remember to open the palette to discover them.
func RenderHelp(reg *keys.Registry, width int) string {
	groups := reg.ActionsByGroup()

	order := []string{
		"App",
		"Session",
		"Modes",
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
	// renders them, but as a compact name-→-description table so users
	// can skim what's available at a glance.
	b.WriteString(theme.Title.Render("Slash commands") + "\n")
	dim := lipgloss.NewStyle().Foreground(theme.TextDim)
	lastGroup := ""
	for _, cmd := range palette.Commands {
		if cmd.Group != lastGroup {
			if lastGroup != "" {
				b.WriteString("\n")
			}
			b.WriteString(dim.Render("  "+cmd.Group+":") + "\n")
			lastGroup = cmd.Group
		}
		b.WriteString(fmt.Sprintf("    %-15s %s\n", cmd.Name, dim.Render(cmd.Desc)))
	}

	content := strings.TrimRight(b.String(), "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Width(width - 4).
		Render(content)
}
