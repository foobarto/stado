package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/lspfind"
)

// TestDiagnosticEntryText_CollapsesInteriorControlChars reproduces P2.7: a
// multi-line LSP diagnostic message (rust-analyzer, some gopls diagnostics)
// carried interior \n/\r/\t straight into the row, so one diagnostic spilled
// across several physical sidebar rows with mis-padded continuation lines.
// After the fix interior control chars collapse to a single space so each
// entry stays on one row.
func TestDiagnosticEntryText_CollapsesInteriorControlChars(t *testing.T) {
	out := diagnosticEntryText(lspfind.DiagnosticEntry{
		RelPath: "main.go",
		Line:    12,
		Message: "cannot use 42 (untyped int) as string value\n\thave: int\n\twant: string",
	})
	if strings.ContainsAny(out, "\n\r\t") {
		t.Errorf("diagnosticEntryText leaked interior control chars (phantom rows): %q", out)
	}
	// Locus + the start of the message survive.
	if !strings.HasPrefix(out, "main.go:12 ") || !strings.Contains(out, "cannot use 42") {
		t.Errorf("locus or message text lost: %q", out)
	}
}
