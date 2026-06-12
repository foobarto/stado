package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestHardWrap_NonASCIIStaysValidUTF8 reproduces P2.8: hardWrap byte-sliced
// ln[:width], splitting a multi-byte rune mid-sequence and emitting invalid
// UTF-8. A non-ASCII diagnostic locus (real non-English filename) longer than
// the wrap width hit this. After the fix hardWrap is display-width + grapheme
// aware, so every wrapped line is valid UTF-8.
func TestHardWrap_NonASCIIStaysValidUTF8(t *testing.T) {
	s := strings.Repeat("—", 25) // 25 em-dashes: 75 bytes, 25 runes
	out := hardWrap(s, 10)
	if !utf8.ValidString(out) {
		t.Fatalf("hardWrap emitted invalid UTF-8: %q", out)
	}
	for i, ln := range strings.Split(out, "\n") {
		if !utf8.ValidString(ln) {
			t.Errorf("wrapped line %d is invalid UTF-8: %q", i, ln)
		}
	}
	// Sanity: the em-dashes survive (no replacement chars / dropped runes).
	if got := strings.Count(out, "—"); got != 25 {
		t.Errorf("expected 25 em-dashes preserved, got %d in %q", got, out)
	}
}
