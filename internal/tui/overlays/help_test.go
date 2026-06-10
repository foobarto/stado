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
	out, _ := RenderHelp(reg, 200, 0, 0)

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
	out, _ := RenderHelp(reg, 200, 0, 0)
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
	out, _ := RenderHelp(reg, 200, 0, 0)

	for _, needle := range []string{"Modes", "ctrl+x ctrl+b", "ctrl+x ctrl+c"} {
		if !strings.Contains(out, needle) {
			t.Errorf("help overlay missing %q", needle)
		}
	}
}

// TestRenderHelp_FitsHeight: the help body is ~70+ lines, far taller than
// a typical terminal. bubbletea v2's compositor clips the frame to the
// canvas (v1 scrolled the overflow, losing the top instead of the
// bottom), so RenderHelp must window its body to the height budget.
// Regression for the pty-bridge HelpOverlay failure after the v2
// migration.
func TestRenderHelp_FitsHeight(t *testing.T) {
	reg := keys.NewRegistry()
	for _, height := range []int{32, 24} {
		out, scroll := RenderHelp(reg, 120, height, 0)
		if got := len(strings.Split(out, "\n")); got > height {
			t.Errorf("height %d: rendered %d lines — overlay doesn't fit the canvas", height, got)
		}
		if scroll != 0 {
			t.Errorf("height %d: scroll 0 should stay 0, got %d", height, scroll)
		}
		if !strings.Contains(out, "scroll") {
			t.Errorf("height %d: windowed overlay missing the scroll footer hint", height)
		}
	}
}

// TestRenderHelp_ScrollReachesSlashCommands: scrolling to the bottom must
// reveal the slash-command section (it sits below the keybinding groups),
// and the returned scroll must be clamped to the last useful position.
func TestRenderHelp_ScrollReachesSlashCommands(t *testing.T) {
	reg := keys.NewRegistry()
	out, scroll := RenderHelp(reg, 120, 32, 1<<30)
	if scroll <= 0 {
		t.Fatalf("expected clamped bottom scroll > 0, got %d", scroll)
	}
	for _, needle := range []string{"/sidebar", "/theme", "/split"} {
		if !strings.Contains(out, needle) {
			t.Errorf("bottom of help overlay missing %q:\n%s", needle, out)
		}
	}
	// Idempotent: rendering again with the clamped value yields the same scroll.
	_, again := RenderHelp(reg, 120, 32, scroll)
	if again != scroll {
		t.Errorf("clamp not stable: %d → %d", scroll, again)
	}
}
