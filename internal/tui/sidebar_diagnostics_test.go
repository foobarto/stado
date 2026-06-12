package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/lsp"
)

// storeDiag is a tiny constructor for an lsp.Diagnostic at a 0-indexed line.
func storeDiag(line int, sev lsp.Severity, msg string) lsp.Diagnostic {
	return lsp.Diagnostic{
		Range:    lsp.Range{Start: lsp.Position{Line: line}},
		Severity: sev,
		Message:  msg,
	}
}

// TestSidebar_DiagnosticsPanelRenders: with diagnostics in the session
// store, the sidebar shows a Diagnostics panel with the count header,
// file:line loci, and the message, tone-coloured by severity.
func TestSidebar_DiagnosticsPanelRenders(t *testing.T) {
	m := describeSlashModel(t)
	m.lspDiagnostics.Set("internal/lsp/client.go", []lsp.Diagnostic{
		storeDiag(41, lsp.SeverityError, "undeclared name: foo"),
		storeDiag(9, lsp.SeverityWarning, "unused variable bar"),
	})

	// Render wide enough that the longest `locus message` line (46 cols)
	// isn't hard-wrapped mid-word, so the message stays a contiguous
	// substring. Inner width = passed width - 4.
	got := m.renderSidebar(60)
	for _, want := range []string{
		"Diagnostics",
		"1 error · 1 warning",
		"internal/lsp/client.go:42", // 0-indexed line 41 → displayed 42
		"undeclared name: foo",
		"internal/lsp/client.go:10",
		"unused variable bar",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics panel missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestSidebar_DiagnosticsPanelHiddenWhenEmpty: no diagnostics → no panel
// (the section renders nothing, so the header never appears).
func TestSidebar_DiagnosticsPanelHiddenWhenEmpty(t *testing.T) {
	m := describeSlashModel(t)
	got := m.renderSidebar(48)
	if strings.Contains(got, "Diagnostics") {
		t.Fatalf("empty store should hide the Diagnostics panel\nfull output:\n%s", got)
	}
}

// TestSidebar_DiagnosticsPanelTruncates: more than the cap yields a
// `+N more` line and the cap is respected (the header still totals all).
func TestSidebar_DiagnosticsPanelTruncates(t *testing.T) {
	m := describeSlashModel(t)
	many := make([]lsp.Diagnostic, 0, sidebarDiagnosticsMaxEntries+3)
	for i := 0; i < sidebarDiagnosticsMaxEntries+3; i++ {
		many = append(many, storeDiag(i, lsp.SeverityError, "err"))
	}
	m.lspDiagnostics.Set("big.go", many)

	got := m.renderSidebar(48)
	if !strings.Contains(got, "+3 more") {
		t.Fatalf("expected `+3 more` truncation line\nfull output:\n%s", got)
	}
	// Header counts ALL errors, not just the shown ones (itoa is the
	// package test helper in system_block_render_test.go).
	total := sidebarDiagnosticsMaxEntries + 3
	wantHeader := itoa(total) + " errors" // e.g. "9 errors"
	if !strings.Contains(got, wantHeader) {
		t.Fatalf("header should total all %d errors (%q)\nfull output:\n%s", total, wantHeader, got)
	}
}
