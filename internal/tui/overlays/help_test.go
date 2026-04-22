package overlays

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
)

// TestRenderHelp_IncludesSlashCommands: before this test, pressing ?
// surfaced keybindings but not slash commands — users had to open
// the palette separately to learn about /budget, /skill, /model etc.
// The help overlay now appends the palette's Commands table so ?
// is a complete cheat-sheet for the TUI surface.
func TestRenderHelp_IncludesSlashCommands(t *testing.T) {
	reg := keys.NewRegistry()
	out := RenderHelp(reg, 200)

	if !strings.Contains(out, "Slash commands") {
		t.Error("expected 'Slash commands' section header")
	}
	// Sample a few high-value commands that would be easy to miss
	// without the overlay surfacing them.
	for _, needle := range []string{"/model", "/budget", "/skill", "/compact"} {
		if !strings.Contains(out, needle) {
			t.Errorf("help overlay missing %s", needle)
		}
	}
	// Sanity: keybindings section still renders. The whole point of
	// this change is additive, not a replacement.
	if !strings.Contains(out, "Input Editing") {
		t.Error("keybindings section disappeared")
	}
}

// TestRenderHelp_GroupsSlashCommands: slash commands inside the help
// overlay are grouped the same way the palette groups them (Quick /
// Session / View). Guards against a refactor that accidentally
// inlines them all into one flat list — the grouping is what makes
// the list scannable.
func TestRenderHelp_GroupsSlashCommands(t *testing.T) {
	reg := keys.NewRegistry()
	out := RenderHelp(reg, 200)
	groups := map[string]bool{}
	for _, cmd := range palette.Commands {
		groups[cmd.Group] = true
	}
	for g := range groups {
		if !strings.Contains(out, g+":") {
			t.Errorf("help overlay missing group header %q", g)
		}
	}
}

func TestRenderHelp_IncludesModeBindingsAndFullPrefixChords(t *testing.T) {
	reg := keys.NewRegistry()
	out := RenderHelp(reg, 200)

	for _, needle := range []string{"Modes", "ctrl+x ctrl+b", "ctrl+x ctrl+c"} {
		if !strings.Contains(out, needle) {
			t.Errorf("help overlay missing %q", needle)
		}
	}
}
