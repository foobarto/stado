package personapicker

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRowsNeverExceedInnerWidth: a persona whose title+description is
// wider than the modal inner width must be truncated to fit, not let
// the row overflow. An over-long row gets hard-wrapped by the outer
// lipgloss modal box (Width(modalW)), which turns one item into several
// physical rows, corrupting the one-line-per-item layout AND the
// maxRows windowing math. Mirror of the fleetpicker overflow fix.
func TestRowsNeverExceedInnerWidth(t *testing.T) {
	m := New()
	m.Open([]Item{
		{ID: "short", Title: "T", Description: "d", Origin: "bundled"},
		{
			ID:          "long-one",
			Title:       "A Very Long Persona Title That Goes On",
			Description: "with an even longer description that should overflow the modal inner width for sure yes indeed",
			Origin:      "project",
		},
	}, "short")

	const innerW = 52
	// Exercise both the non-selected path (cursor on short) and the
	// selected path (cursor on long).
	for _, cursor := range []int{0, 1} {
		m.Cursor = cursor
		body := m.renderBody(innerW, 20)
		for _, ln := range strings.Split(body, "\n") {
			if w := lipgloss.Width(ln); w > innerW {
				t.Errorf("cursor=%d: body row width %d exceeds innerW %d: %q",
					cursor, w, innerW, ln)
			}
		}
	}
}
