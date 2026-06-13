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
	// The extra (un-columned) cell is dropped, not leaked — guards against a
	// future break->continue regression that would still avoid the panic.
	if strings.Contains(out, "cc") {
		t.Errorf("extra cell beyond the declared columns should be dropped, got %q", out)
	}
}
