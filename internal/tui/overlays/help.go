package overlays

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

func RenderHelp(reg *keys.Registry, width int) string {
	groups := reg.ActionsByGroup()
	
	// Define a strict order for the groups
	order := []string{
		"App",
		"Session",
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
			bindings := reg.Get(action)
			if len(bindings) == 0 {
				continue
			}
			
			// Join multiple keys for the same action
			var keyStrs []string
			for _, kb := range bindings {
				keyStrs = append(keyStrs, kb.Help().Key)
			}
			
			keyStr := strings.Join(keyStrs, ", ")
			desc := keys.ActionDescriptions[action]
			
			line := fmt.Sprintf("  %-25s %s\n", keyStr, lipgloss.NewStyle().Foreground(theme.TextDim).Render(desc))
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	content := strings.TrimRight(b.String(), "\n")
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Width(width - 4).
		Render(content)
}
