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
