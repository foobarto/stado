package main

import (
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// erroredCommit builds a trace commit whose parsed trailers carry an Error,
// for exercising printLogEntry's colour branches directly (the colour path
// is unreachable through RunE in tests — useColor(os.Stdout) is false on a
// captured pipe).
func erroredCommit() *object.Commit {
	return &object.Commit{
		Message: "mutating [error]\n\nTool: bash\nError: boom\n",
		Author:  object.Signature{When: time.Unix(0, 0)},
	}
}

// TestPrintLogEntry_DimmedErroredOriginalIsDeEmphasised reproduces the Copilot
// finding on PR #125: in printLogEntry the errMsg case preceded the dimmed
// case, so the ORIGINAL (parent) half of a mutation pair that itself carried
// an Error rendered red and never dimmed — losing the "dim parent / highlight
// tip" pairing the two-commit model relies on.
//
// Pre-fix this FAILS: an errored + dimmed line renders pure red (no ansiDim).
// After the fix dimmed takes precedence and combines with red, so the line is
// both faint (superseded) and red (errored).
func TestPrintLogEntry_DimmedErroredOriginalIsDeEmphasised(t *testing.T) {
	out := captureStdout(t, func() {
		printLogEntry(erroredCommit(), true /*colour*/, true /*dimmed*/)
	})
	if !strings.Contains(out, ansiDim) {
		t.Errorf("dimmed errored original must be faint (contain %q), got %q", ansiDim, out)
	}
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf("errored original should keep red as well, got %q", out)
	}
}

// TestPrintLogEntry_ErroredNonPairStillRed: a normal errored line that is NOT
// part of a mutation pair (dimmed=false) stays plain red — the fix must not
// dim every error, only the parent half of a pair.
func TestPrintLogEntry_ErroredNonPairStillRed(t *testing.T) {
	out := captureStdout(t, func() {
		printLogEntry(erroredCommit(), true /*colour*/, false /*dimmed*/)
	})
	if strings.Contains(out, ansiDim) {
		t.Errorf("non-pair errored line must NOT be dimmed, got %q", out)
	}
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf("non-pair errored line must be red, got %q", out)
	}
}
