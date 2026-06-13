package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// TestTruncateAroundWideCJK: the centered excerpt window for
// `stado session search` is cut on DISPLAY-COLUMN boundaries, never
// mid-rune. A byte-offset slice (s[lo:hi]) split wide-CJK / emoji
// graphemes, leaking invalid UTF-8 continuation bytes into the excerpt
// (StripControlChars downstream does not repair a broken rune). The
// windowed body must also stay within the column budget.
func TestTruncateAroundWideCJK(t *testing.T) {
	// 3-byte runes; widths chosen so byte offsets would have landed
	// mid-rune under the old implementation.
	s := strings.Repeat("你好世界", 10) // 40 runes, 80 display columns
	for _, width := range []int{32, 30, 28, 26, 20, 9, 1} {
		got := truncateAround(s, "", width)
		if !utf8.ValidString(got) {
			t.Errorf("width=%d: split a rune mid-byte: %q", width, got)
		}
		// Body (between the ellipses) must not exceed the width budget.
		body := strings.Trim(got, "…")
		if w := ansi.StringWidth(body); w > width {
			t.Errorf("width=%d: window body width = %d > budget: %q", width, w, body)
		}
	}
}

// TestTruncateAroundShortInputUnchanged: input that already fits is
// returned verbatim (no ellipses, no copy).
func TestTruncateAroundShortInputUnchanged(t *testing.T) {
	s := "你好世界" // 8 columns
	if got := truncateAround(s, "", 40); got != s {
		t.Fatalf("short input mutated: got %q want %q", got, s)
	}
}
