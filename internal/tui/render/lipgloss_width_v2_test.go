package render

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// TestLipglossV2WidthIncludesBorder locks the lipgloss-v2 width invariant that
// the migration depends on: .Width(W) is the TOTAL rendered width — border AND
// padding included — not content+padding with the border added on top (the v1
// behavior). Every bordered modal in the TUI sizes itself against this; if a
// future lipgloss bump flips it back, the bordered panes would render two cols
// too wide and this test catches it before the UAT does.
func TestLipglossV2WidthIncludesBorder(t *testing.T) {
	const want = 20
	s := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(want)
	if got := lipgloss.Width(s.Render("x")); got != want {
		t.Fatalf("v2 .Width(%d) on a bordered+padded style rendered total width %d; want %d "+
			"(if this is %d the v1 'border adds 2 on top' behavior returned — bordered modals "+
			"that pass .Width(budget) now overflow by the border width)", want, got, want, want+2)
	}
}
