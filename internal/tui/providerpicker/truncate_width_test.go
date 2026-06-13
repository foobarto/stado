package providerpicker

import (
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// TestTruncateVisibleWideCJK: a wide-CJK / emoji label whose RUNE count
// fits the width budget still has ~2x display width. The old rune-count
// slice (string([]rune(s)[:width-1])) returned a string wider than the
// budget that hard-wrapped onto a second row / overflowed the modal
// border; it could also split a wide rune. truncateVisible must bound
// the result to width DISPLAY columns and stay valid UTF-8.
func TestTruncateVisibleWideCJK(t *testing.T) {
	// 10 wide-CJK runes = 20 display columns, 10 runes.
	s := "你好世界一二三四五六"
	for _, width := range []int{8, 6, 4, 2} {
		got := truncateVisible(s, width)
		if !utf8.ValidString(got) {
			t.Errorf("width=%d: invalid UTF-8: %q", width, got)
		}
		if w := lipgloss.Width(got); w > width {
			t.Errorf("width=%d: overflowed to %d cols: %q", width, w, got)
		}
	}
	// A label that already fits is returned untouched.
	if got := truncateVisible("abc", 10); got != "abc" {
		t.Errorf("short ASCII mutated: %q", got)
	}
}
