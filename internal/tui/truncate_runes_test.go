package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncate_RuneSafe reproduces a byte-vs-rune defect: truncate sliced
// s[:max-1] on a BYTE boundary, so a multi-byte UTF-8 rune straddling the
// boundary was cut in half, emitting invalid UTF-8 (a dangling lead byte).
// In the terminal that renders as a replacement glyph and corrupts the
// tool-call header / approval prompt / status modal. The sibling trimSeed
// is already rune-correct; truncate must be too.
func TestTruncate_RuneSafe(t *testing.T) {
	// "ééééé" is 5 runes / 10 bytes. max=4 truncates; the old byte-based
	// code sliced s[:3] = byte boundary mid-é, emitting a dangling lead byte.
	out := truncate("ééééé", 4)
	if !utf8.ValidString(out) {
		t.Fatalf("truncate produced invalid UTF-8: %q", out)
	}
	// Truncation must still happen (input is wider than the cap) and end
	// with the ellipsis marker.
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected ellipsis-terminated truncation, got %q", out)
	}
	// max-1 content runes + ellipsis = max display cells.
	if got := len([]rune(out)); got != 4 {
		t.Errorf("truncate cap is rune-counted: got %d runes, want 4 (%q)", got, out)
	}
	// The cap is in display cells / runes, not bytes: a 40-rune cap on a
	// 6-rune string must NOT truncate.
	if got := truncate("ééé", 40); got != "ééé" {
		t.Errorf("short string truncated against a byte count: got %q want %q", got, "ééé")
	}
}

// TestToolBlock_ArgsPreviewRuneSafe is the reproduce-by-rendering half: a
// tool call whose JSON args carry a long non-ASCII string must render a
// header that is valid UTF-8 — the ArgsPreview truncation can't leak a
// half-rune into the rendered conversation.
func TestToolBlock_ArgsPreviewRuneSafe(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// 60 accented runes (120 bytes) — well past the 40-cell ArgsPreview cap,
	// and crafted so a byte-boundary slice lands mid-rune.
	longArg := strings.Repeat("é", 60)
	blk := block{
		kind:     "tool",
		toolName: "bash",
		toolArgs: `{"cmd":"` + longArg + `"}`,
	}
	out, err := m.renderBlock(blk, 80)
	if err != nil {
		t.Fatalf("renderBlock: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("rendered tool block is not valid UTF-8 (mid-rune ArgsPreview slice):\n%q", out)
	}
}
