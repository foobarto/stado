package tui

import (
	"strings"
	"testing"
)

// TestInputBoxFillerRowsPainted guards the waiting-state background fix:
// the v2 textarea renders its empty filler rows (below the content) with
// Inline end-of-buffer styling that leaves background holes, so the input
// box showed grey filler rows until the user typed. renderInputBox now
// swaps the holey filler for clean empty lines the box fills, and pads the
// inline status row. Every box row must therefore carry a background SGR —
// not just the placeholder and "Do" rows.
func TestInputBoxFillerRowsPainted(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.model = "test-model"

	out := m.renderInputBox(60)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected the input box to render >=4 rows, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		// Every rendered box row should carry a background paint sequence;
		// a row with none is the grey hole the operator reported.
		if !strings.Contains(line, "48;2;") && !strings.Contains(line, "48;5;") {
			t.Errorf("input-box row %d has no background paint (grey hole):\n%q", i, line)
		}
	}
}
