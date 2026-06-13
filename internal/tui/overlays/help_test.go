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

// TestRenderHelp_SurfacesPreviouslyHiddenCommands pins the P2 defect from
// the /help angle: nine working commands dispatched fine but were absent
// from palette.Commands, so the ? overlay (which renders straight from
// palette.Commands) never mentioned them. Pressing ? must surface them.
func TestRenderHelp_SurfacesPreviouslyHiddenCommands(t *testing.T) {
	reg := keys.NewRegistry()
	out, _ := RenderHelp(reg, 200, 0, 0)
	for _, needle := range []string{
		"/stats", "/ps", "/config", "/sandbox", "/fleet",
		"/kill", "/spawn", "/cancel", "/supervisor",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("help overlay missing previously-hidden command %s", needle)
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

// #16/#17: the in-turn routing bindings (alt+enter queue, ctrl+enter
// interrupt) must be discoverable in the ? overlay — they're the
// universal fallback for terminals without enhanced-keyboard support.
func TestRenderHelp_IncludesInTurnRoutingBindings(t *testing.T) {
	reg := keys.NewRegistry()
	out, _ := RenderHelp(reg, 200, 0, 0)

	for _, needle := range []string{"In-turn routing", "alt+enter", "ctrl+enter"} {
		if !strings.Contains(out, needle) {
			t.Errorf("help overlay missing %q", needle)
		}
	}
}

// P2.6: plain Enter WHILE A TURN STREAMS injects a mid-turn STEER
// (handler_input.go #16), but the keybinding table only documented the
// alt+enter (Queue) and ctrl+enter (Interrupt) siblings — the steer was
// the one in-turn routing intent a user couldn't discover from ?. The
// "In-turn routing" group must surface that plain enter steers, so a
// user reading the keymap next to Queue/Interrupt learns what Enter does
// mid-turn. We assert on the KEYBINDINGS half of the overlay (above the
// "Slash commands" section) because the /steer palette row sits in the
// slash list, not the keymap a user scans for the Enter behavior.
func TestRenderHelp_DocumentsEnterWhileBusySteer(t *testing.T) {
	reg := keys.NewRegistry()
	out, _ := RenderHelp(reg, 200, 0, 0)

	keymap := out
	if si := strings.Index(out, "Slash commands"); si >= 0 {
		keymap = out[:si]
	}
	lower := strings.ToLower(keymap)
	if !strings.Contains(lower, "steer") {
		t.Errorf("In-turn routing keymap must document the Enter-while-busy steer; "+
			"'steer' absent from the keybindings section:\n%s", keymap)
	}
	// The steer row must sit in the In-turn routing group, alongside the
	// alt+enter / ctrl+enter siblings — not orphaned elsewhere.
	gi := strings.Index(out, "In-turn routing")
	if gi < 0 {
		t.Fatal("In-turn routing group header missing")
	}
	region := out[gi:]
	if si := strings.Index(region, "Slash commands"); si >= 0 {
		region = region[:si]
	}
	if !strings.Contains(strings.ToLower(region), "steer") {
		t.Errorf("steer not documented within the In-turn routing group:\n%s", region)
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
	// Width matters as much as height: the budget must count rendered
	// (post-wrap) rows, not logical lines. Narrow widths wrap the long
	// slash-command descriptions into multiple rows, and the bottom
	// window (scroll-to-end) is where those rows live — the original
	// fix windowed pre-wrap lines and still overflowed once scrolled.
	for _, width := range []int{120, 100, 80, 40} {
		for _, height := range []int{32, 24} {
			for _, scroll := range []int{0, 1 << 30} {
				out, _ := RenderHelp(reg, width, height, scroll)
				if got := len(strings.Split(out, "\n")); got > height {
					t.Errorf("%dx%d scroll=%d: rendered %d lines — overlay doesn't fit the canvas",
						width, height, scroll, got)
				}
			}
			out, scroll := RenderHelp(reg, width, height, 0)
			if scroll != 0 {
				t.Errorf("%dx%d: scroll 0 should stay 0, got %d", width, height, scroll)
			}
			if !strings.Contains(out, "scroll") {
				t.Errorf("%dx%d: windowed overlay missing the scroll footer hint", width, height)
			}
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

// stripANSI removes CSI ... m sequences so a line can be classified by its
// text content in tests.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// innerText strips ANSI, then the rounded-border left/right frame cells
// ("│"/"╮"…), yielding the box's inner content for a line so leading-space
// (hang-indent) can be measured.
func innerText(raw string) string {
	s := stripANSI(raw)
	// Drop a leading vertical border cell if present.
	s = strings.TrimPrefix(s, "│")
	s = strings.TrimRight(s, " ")
	s = strings.TrimSuffix(s, "│")
	return s
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

// TestRenderHelp_SlashCommandsHangIndent: a slash command whose description
// is wider than the overlay's inner width must hang-indent — the wrapped
// continuation lines start under the command name (past the command-row
// indent), not at the left margin. Pre-fix the overlay re-wrapped long
// descriptions back to column 0 via a whole-content lipgloss re-wrap. We
// render narrow so at least one description is forced to wrap, and assert
// on a specific command (/sidebar) whose description wraps.
func TestRenderHelp_SlashCommandsHangIndent(t *testing.T) {
	reg := keys.NewRegistry()
	// height 0 disables windowing so the full slash list is present.
	out, _ := RenderHelp(reg, 60, 0, 0)

	si := strings.Index(out, "Slash commands")
	if si < 0 {
		t.Fatal("Slash commands section missing")
	}
	lines := strings.Split(out[si:], "\n")

	// Locate /sidebar's command row and read its continuation line.
	cmdRow := -1
	for i, raw := range lines {
		inner := innerText(raw)
		if strings.Contains(strings.TrimSpace(inner), "/sidebar") {
			cmdRow = i
			break
		}
	}
	if cmdRow < 0 || cmdRow+1 >= len(lines) {
		t.Fatalf("could not find /sidebar command row with a following line:\n%s", out[si:])
	}

	nameInner := innerText(lines[cmdRow])
	contInner := innerText(lines[cmdRow+1])

	// The continuation carries the rest of /sidebar's description and must
	// not itself be a command row.
	contTrim := strings.TrimSpace(contInner)
	if strings.HasPrefix(contTrim, "/") {
		t.Fatalf("expected a wrapped continuation after /sidebar, got another command row: %q", contInner)
	}
	if contTrim == "" {
		t.Fatalf("expected /sidebar's description to wrap to a continuation line; got blank: %q", contInner)
	}

	// Hanging indent: the continuation begins to the RIGHT of where the
	// command name begins, and strictly past column 0.
	nameIndent := leadingSpaces(nameInner)
	contIndent := leadingSpaces(contInner)
	if contIndent == 0 {
		t.Errorf("wrapped /sidebar description flowed to column 0: %q", contInner)
	}
	if contIndent <= nameIndent {
		t.Errorf("continuation (%d) should hang past the name column (%d): name=%q cont=%q",
			contIndent, nameIndent, nameInner, contInner)
	}
	// No truncation: the description's tail word is present.
	if !strings.Contains(stripANSI(out), "ctrl+x") {
		t.Errorf("/sidebar description appears truncated (tail 'ctrl+x' missing)")
	}
}
