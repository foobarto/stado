package overlays

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
)

// TestRenderHelp_AliasRowDoesNotWrap pins the G9 defect: the /alias
// slash-command row was the longest description in the palette (~205
// rendered columns) and wrapped onto 2–3 lines in the ? help overlay
// at every realistic terminal width, with the continuation lines
// dropping back to the left margin and breaking the name/description
// column alignment.
//
// The help overlay renders each slash row as "    %-15s %s" and then
// re-wraps the whole content block to (width-8). A clean single-row
// /alias entry must fit within that inner width at a typical terminal
// width — i.e. the rendered "/alias" row must carry the full
// description on one line, not spill the tail onto a following,
// left-margin-anchored continuation line.
func TestRenderHelp_AliasRowDoesNotWrap(t *testing.T) {
	// Find the canonical /alias palette description so the test pins the
	// real copy, not a stale literal.
	var aliasDesc string
	for _, c := range palette.Commands {
		if c.Name == "/alias" {
			aliasDesc = c.Desc
		}
	}
	if aliasDesc == "" {
		t.Fatal("/alias not found in palette.Commands")
	}

	reg := keys.NewRegistry()
	// 100 columns is a comfortable, common terminal width. The help
	// overlay's inner content width here is width-8 = 92. The full
	// rendered row is "    " + "/alias" padded to 15 + " " + desc, i.e.
	// 4 + 15 + 1 = 20 columns of prefix before the description. For the
	// row to fit on ONE line the description must be <= 92 - 20 = 72
	// columns. The old long description blew past that and wrapped.
	const width = 100
	out, _ := RenderHelp(reg, width, 0, 0)
	lines := strings.Split(out, "\n")

	// Locate the line that begins the /alias row (the one carrying the
	// command name) and confirm the FULL description sits on that single
	// rendered line — no spill onto a following, left-margin-anchored
	// continuation line.
	aliasLineIdx := -1
	for i, ln := range lines {
		plain := ansi.Strip(ln)
		if strings.Contains(plain, "/alias") && strings.Contains(plain, aliasFirstWord(aliasDesc)) {
			aliasLineIdx = i
			break
		}
	}
	if aliasLineIdx < 0 {
		t.Fatalf("could not find the /alias row in the help overlay:\n%s", out)
	}

	plain := ansi.Strip(lines[aliasLineIdx])
	// The whole description must be present on this single line. If the
	// row wrapped, the tail of the description lands on the NEXT line and
	// this assertion fails (the last word won't be on the name line).
	last := aliasLastWord(aliasDesc)
	if !strings.Contains(plain, last) {
		t.Errorf("/alias description wraps in the help overlay at width=%d: "+
			"name row does not contain the description tail %q.\n"+
			"name row: %q\nnext row: %q",
			width, last, plain, nextLine(lines, aliasLineIdx))
	}
}

func aliasFirstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func aliasLastWord(s string) string {
	s = strings.TrimRight(s, " .")
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[i+1:]
	}
	return s
}

func nextLine(lines []string, i int) string {
	if i+1 < len(lines) {
		return ansi.Strip(lines[i+1])
	}
	return ""
}
