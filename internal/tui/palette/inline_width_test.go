package palette

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestInlineViewFitsWidth pins the inline-palette wrap bug: InlineView(W) must
// produce a box no wider than W. It used to render W+2 (the rounded border was
// not subtracted from .Width), so when nested inside the input frame it
// overflowed and lipgloss word-wrapped each row, spilling the /name+keybinding
// onto the next line (the "junk in the palette" the tester saw).
func TestInlineViewFitsWidth(t *testing.T) {
	m := New()
	m.Open() // browse mode, all groups
	for _, W := range []int{24, 40, 60, 80, 110} {
		out := m.InlineView(W)
		for _, ln := range strings.Split(out, "\n") {
			if w := lipgloss.Width(ln); w > W {
				t.Errorf("InlineView(%d): line width %d exceeds budget: %q", W, w, ln)
			}
		}
	}
}

// TestInlineViewNestedInInputFrameDoesNotWrap reproduces the real compositing
// from input_box.go: InlineView(mainW-4) nested inside the outer input frame.
// The combined render must not exceed mainW columns (else the terminal wraps).
func TestInlineViewNestedInInputFrameDoesNotWrap(t *testing.T) {
	m := New()
	m.Open()
	for _, mainW := range []int{50, 64, 80, 120} {
		box := m.InlineView(mainW - 4)
		// Mirror input_box.go's outer frame: left border + padding(0,1),
		// content Width(mainW-1).
		framed := lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}, false, false, false, true).
			Padding(0, 1).
			Width(mainW - 1).
			Render(box)
		for _, ln := range strings.Split(framed, "\n") {
			if w := lipgloss.Width(ln); w > mainW {
				t.Errorf("mainW=%d: framed line width %d exceeds mainW: %q", mainW, w, ln)
			}
		}
	}
}

// TestInlineViewUncapped pins that the inline popup is no longer capped at 110
// (it was, which truncated long command descriptions on wide terminals). It
// should use the full available width — while still never exceeding it.
func TestInlineViewUncapped(t *testing.T) {
	m := New()
	m.Open()
	const W = 160
	out := m.InlineView(W)
	maxLine := 0
	for _, ln := range strings.Split(out, "\n") {
		if w := lipgloss.Width(ln); w > maxLine {
			maxLine = w
		}
	}
	if maxLine <= 112 { // old cap (110) + border
		t.Errorf("InlineView(%d) maxLine=%d — still near the old 110 cap; expected full width", W, maxLine)
	}
	if maxLine > W {
		t.Errorf("InlineView(%d) maxLine=%d exceeds budget", W, maxLine)
	}
}
