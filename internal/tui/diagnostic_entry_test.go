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

// TestDiagnosticEntryText_StripsControlChars reproduces an escape-injection
// leak: an LSP server's diagnostic message (and the file path it reports)
// is untrusted text — a hostile repo can seed a diagnostic whose message or
// path carries ESC / CSI / OSC / BEL bytes (OSC 52 clipboard hijack, OSC 0
// title-bar rewrite, CSI cursor manipulation). The single-line sidebar row
// must flatten those before render, matching the codebase posture for every
// other untrusted single-line surface (model picker, tool names, memory IDs
// all run StripControlChars). The whitespace-collapse alone (strings.Fields)
// does NOT remove ESC/BEL — they aren't whitespace — so they survived into
// the sidebar.
func TestDiagnosticEntryText_StripsControlChars(t *testing.T) {
	out := diagnosticEntryText(lspfind.DiagnosticEntry{
		// OSC 0 (title-bar rewrite) in the path, CSI colour + BEL in the message.
		RelPath: "foo\x1b]0;pwned\x07.go",
		Line:    3,
		Message: "bad \x1b[31mvalue\x1b[0m here\x07",
	})
	for _, bad := range []rune{0x1b, 0x07, 0x9b} { // ESC, BEL, C1 CSI
		if strings.ContainsRune(out, bad) {
			t.Errorf("control char %#x leaked into sidebar row: %q", bad, out)
		}
	}
	// The printable text still survives — only the control bytes are gone.
	if !strings.Contains(out, "bad") || !strings.Contains(out, "value") || !strings.Contains(out, "here") {
		t.Errorf("printable message text lost: %q", out)
	}
	if !strings.Contains(out, "foo") || !strings.Contains(out, ".go:3") {
		t.Errorf("printable path/locus lost: %q", out)
	}
}
