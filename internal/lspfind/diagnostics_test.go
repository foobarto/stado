package lspfind

import (
	"testing"

	"github.com/foobarto/stado/internal/lsp"
)

func diag(line int, sev lsp.Severity, msg string) lsp.Diagnostic {
	return lsp.Diagnostic{
		Range:    lsp.Range{Start: lsp.Position{Line: line}},
		Severity: sev,
		Message:  msg,
	}
}

// TestStore_SetCountsAndClears: Set computes severity counts, and an empty
// slice drops the file (a cleaned file leaves no zero-count residue).
func TestStore_SetCountsAndClears(t *testing.T) {
	s := NewDiagnosticsStore()
	s.Set("a.go", []lsp.Diagnostic{
		diag(0, lsp.SeverityError, "boom"),
		diag(2, lsp.SeverityWarning, "meh"),
		diag(3, lsp.SeverityWarning, "meh2"),
	})
	sum := s.Summarize(0)
	if sum.Errors != 1 || sum.Warnings != 2 {
		t.Fatalf("counts: errors=%d warnings=%d, want 1/2", sum.Errors, sum.Warnings)
	}
	if sum.Total != 3 {
		t.Fatalf("total=%d, want 3", sum.Total)
	}

	// Clearing the file with an empty publish removes it entirely.
	s.Set("a.go", nil)
	if got := s.Summarize(0).Total; got != 0 {
		t.Fatalf("after clear, total=%d, want 0", got)
	}
}

// TestStore_SummarizeRanksAndCaps: errors sort before warnings, and the
// top-N cap reports Truncated with the pre-cap Total preserved.
func TestStore_SummarizeRanksAndCaps(t *testing.T) {
	s := NewDiagnosticsStore()
	s.Set("z.go", []lsp.Diagnostic{
		diag(5, lsp.SeverityWarning, "w1"),
		diag(1, lsp.SeverityError, "e1"),
	})
	s.Set("a.go", []lsp.Diagnostic{
		diag(9, lsp.SeverityError, "e2"),
		diag(2, lsp.SeverityHint, "h1"),
	})

	sum := s.Summarize(2)
	if !sum.Truncated {
		t.Fatalf("expected truncation at cap 2")
	}
	if sum.Total != 4 {
		t.Fatalf("Total=%d, want 4 (pre-cap)", sum.Total)
	}
	if len(sum.Entries) != 2 {
		t.Fatalf("Entries len=%d, want 2", len(sum.Entries))
	}
	// Both surviving entries must be the errors (most severe first), and
	// within errors, file order (a.go before z.go).
	for i, e := range sum.Entries {
		if e.Severity != lsp.SeverityError {
			t.Fatalf("entry %d severity=%v, want error (errors must rank first)", i, e.Severity)
		}
	}
	if sum.Entries[0].RelPath != "a.go" || sum.Entries[1].RelPath != "z.go" {
		t.Fatalf("error file order = %q,%q, want a.go,z.go", sum.Entries[0].RelPath, sum.Entries[1].RelPath)
	}
}

// TestStore_ResetClearsAll: Reset (session switch) empties the store.
func TestStore_ResetClearsAll(t *testing.T) {
	s := NewDiagnosticsStore()
	s.Set("a.go", []lsp.Diagnostic{diag(0, lsp.SeverityError, "x")})
	s.Reset()
	if got := s.Summarize(0).Total; got != 0 {
		t.Fatalf("after Reset, total=%d, want 0", got)
	}
}

// TestStore_NilSafe: methods on a nil store don't panic.
func TestStore_NilSafe(t *testing.T) {
	var s *DiagnosticsStore
	s.Set("a.go", []lsp.Diagnostic{diag(0, lsp.SeverityError, "x")}) // must not panic
	s.Reset()                                                        // must not panic
	if got := s.Summarize(5); got.Total != 0 {
		t.Fatalf("nil store Summarize Total=%d, want 0", got.Total)
	}
}

// TestSummary_String: the header renders error/warning counts with correct
// pluralization, omitting a zero half.
func TestSummary_String(t *testing.T) {
	cases := []struct {
		errs, warns int
		want        string
	}{
		{0, 0, ""},
		{1, 0, "1 error"},
		{2, 0, "2 errors"},
		{0, 1, "1 warning"},
		{1, 3, "1 error · 3 warnings"},
	}
	for _, c := range cases {
		got := Summary{Errors: c.errs, Warnings: c.warns}.String()
		if got != c.want {
			t.Errorf("Summary{e=%d,w=%d}.String() = %q, want %q", c.errs, c.warns, got, c.want)
		}
	}
}
