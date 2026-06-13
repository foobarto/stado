package tui

import (
	"strings"
	"testing"
)

// TestWriteTableRow_ExtraCellsNoPanic: a plugin row with MORE cells than the
// table declared columns (widths) must not panic. writeTable's width loop is
// bounded to cols, but writeTableRow indexed widths[i] for every cell.
func TestWriteTableRow_ExtraCellsNoPanic(t *testing.T) {
	var b strings.Builder
	// 2 declared columns, a 3-cell row.
	writeTableRow(&b, 40, "", []int{5, 5}, []string{"aa", "bb", "cc"})
	out := b.String()
	if !strings.Contains(out, "aa") || !strings.Contains(out, "bb") {
		t.Errorf("expected the first two cells rendered, got %q", out)
	}
}
