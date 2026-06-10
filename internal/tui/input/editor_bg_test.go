package input

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/theme"
)

// TestEditorTextAreaFillsSurfaceBackground guards a bubbletea-v2 migration
// regression: the textarea's per-cell styles only set foreground colours,
// so the editor area rendered with the terminal's default (grey) background
// while the surrounding input box used the theme's "surface" colour —
// leaving the chat input visibly grey instead of matching the dark frame.
//
// We render with a distinctive surface colour and assert the textarea's own
// output carries that background. (Rendered, not theorised — see the
// reproduce-by-rendering discipline.)
func TestEditorTextAreaFillsSurfaceBackground(t *testing.T) {
	th := theme.Default()
	th.Colors.Surface = "#123456" // RGB(18,52,86) → ANSI bg "48;2;18;52;86"
	theme.Apply(th)
	t.Cleanup(func() { theme.Apply(theme.Default()) })

	e := New(keys.NewRegistry())
	e.SetValue("hello")
	e.Model.SetWidth(40)

	out := e.View()
	if !strings.Contains(out, "48;2;18;52;86") {
		t.Errorf("textarea View does not paint the surface background (#123456); "+
			"the editor area renders with no background and shows through grey.\nrendered:\n%q", out)
	}
}

// TestEditorEmptyStateFillsSurfaceBackground guards the operator-reported
// follow-up: the WAITING (empty / placeholder) state still showed grey
// even after the typing state was fixed, because only the placeholder
// glyphs were painted while the rest of the editor area stayed bare. The
// textarea Base style must fill the whole area regardless of content.
func TestEditorEmptyStateFillsSurfaceBackground(t *testing.T) {
	th := theme.Default()
	th.Colors.Surface = "#123456" // RGB(18,52,86) → ANSI bg "48;2;18;52;86"
	theme.Apply(th)
	t.Cleanup(func() { theme.Apply(theme.Default()) })

	e := New(keys.NewRegistry())
	e.Model.SetWidth(40)
	e.SetValue("") // waiting / placeholder state — no text typed yet

	for i, line := range strings.Split(e.View(), "\n") {
		if strings.TrimSpace(stripANSI(line)) == "" {
			continue // a fully-blank line is fine — the box frame fills it
		}
		if !bgCoversFullWidth(line, "48;2;18;52;86") {
			t.Errorf("empty-state line %d has an unpainted (grey) gap — surface bg "+
				"doesn't cover the full width:\n%q", i, line)
		}
	}
}

// bgCoversFullWidth walks the SGR codes of a rendered line and reports
// whether the target background colour is active for every visible cell.
func bgCoversFullWidth(line, wantBG string) bool {
	bgActive := false
	i := 0
	for i < len(line) {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			end := strings.IndexByte(line[i:], 'm')
			if end < 0 {
				break
			}
			seq := line[i+2 : i+end]
			switch {
			case seq == "" || seq == "0": // reset clears bg
				bgActive = false
			case strings.Contains(seq, wantBG):
				bgActive = true
			case strings.Contains(seq, "48;2;") || strings.Contains(seq, "48;5;"):
				bgActive = false // a different background
			}
			i += end + 1
			continue
		}
		// A visible cell with no active target background = a grey gap.
		if !bgActive {
			return false
		}
		i++
	}
	return true
}

func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := strings.IndexByte(s[i:], 'm')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
