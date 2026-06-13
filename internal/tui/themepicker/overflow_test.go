package themepicker

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRowsNeverExceedInnerWidth: a theme whose name+mode+description is
// wider than the modal inner width must be truncated, not overflow the
// row. An over-long row is hard-wrapped by the outer lipgloss modal box
// (Width(modalW)), turning one entry into several physical rows and
// corrupting the layout. A user-loaded custom theme.toml name plus the
// "loaded theme.toml" description can exceed the inner width.
func TestRowsNeverExceedInnerWidth(t *testing.T) {
	m := New()
	m.Open([]Item{
		{ID: "stado-dark", Name: "Stado Dark", Mode: "dark", Desc: "Default"},
		{
			ID:   "my-very-long-custom-theme-id",
			Name: "My Very Long Custom Theme Name That Keeps Going",
			Mode: "custom",
			Desc: "loaded from a user theme.toml with an exceedingly verbose description here",
		},
	}, "stado-dark")

	const innerW = 48
	for _, cursor := range []int{0, 1} {
		m.Cursor = cursor
		body := m.renderBody(innerW)
		for _, ln := range strings.Split(body, "\n") {
			if w := lipgloss.Width(ln); w > innerW {
				t.Errorf("cursor=%d: body row width %d exceeds innerW %d: %q",
					cursor, w, innerW, ln)
			}
		}
	}
}
