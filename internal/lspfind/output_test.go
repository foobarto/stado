package lspfind

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools/budget"
)

func TestTruncateLSPOutputCapsOversizedText(t *testing.T) {
	input := strings.Repeat("x", budget.LSPBytes+2048)
	got := truncateLSPOutput(input)
	if len(got) >= len(input) {
		t.Fatalf("output was not capped: got %d bytes for %d-byte input", len(got), len(input))
	}
	if !strings.Contains(got, "[truncated:") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-128:])
	}
}

func TestTruncateLSPOutputTrimsTrailingNewlines(t *testing.T) {
	got := truncateLSPOutput("a\n\n")
	if got != "a" {
		t.Fatalf("output = %q, want trailing newlines trimmed", got)
	}
}
