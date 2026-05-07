package overlays

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// CenterOver overlays a popup centred over a base render at total
// dimensions (width, height). Base rows ABOVE and BELOW the popup
// remain visible; rows the popup occupies are replaced entirely
// (padded with whitespace on either side of the popup so the row
// width stays exact). This is the "modal over chat" composite —
// nicer than lipgloss.Place's empty-canvas approach because the
// surrounding context stays visible.
//
// The popup is left-padded so it ends up horizontally centred. When
// the popup is taller than the canvas, the canvas is returned
// unchanged — caller should defensively clamp popup size.
//
// Both base and popup may contain ANSI escapes; strings.Split
// handles them per-line. We don't try to splice popup lines INTO
// base lines (which would require an ANSI-aware width parser);
// the whole row is replaced.
func CenterOver(base, popup string, width, height int) string {
	if width <= 0 || height <= 0 {
		return base
	}
	popupLines := strings.Split(strings.TrimRight(popup, "\n"), "\n")
	if len(popupLines) >= height {
		return popup
	}
	popupW := lipgloss.Width(popup)
	if popupW > width {
		popupW = width
	}
	leftPad := (width - popupW) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	rightPad := width - popupW - leftPad
	if rightPad < 0 {
		rightPad = 0
	}
	pad := strings.Repeat(" ", leftPad)
	rpad := strings.Repeat(" ", rightPad)
	for i, line := range popupLines {
		popupLines[i] = pad + line + rpad
	}

	baseLines := strings.Split(base, "\n")
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}
	if len(baseLines) > height {
		baseLines = baseLines[:height]
	}

	topPad := (height - len(popupLines)) / 2
	if topPad < 0 {
		topPad = 0
	}
	for i, popupLine := range popupLines {
		row := topPad + i
		if row >= height {
			break
		}
		baseLines[row] = popupLine
	}

	return strings.Join(baseLines, "\n")
}
