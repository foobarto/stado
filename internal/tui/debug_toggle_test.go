package tui

// Repro for C7-debug-toggle / P3.7: the /debug command printed
// "sidebar diagnostics: on/off", but it actually toggles operational-detail
// verbosity (Context/Budget/Sandbox/Logs lines) — NOT the LSP Diagnostics
// panel. The Diagnostics panel (sidebarDiagnosticsLines) renders whenever
// diagnostics exist and the section is enabled, independent of m.sidebarDebug.
// These tests lock the message to its real effect and prove the panel is
// unaffected by the toggle.

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/lsp"
)

// TestDebugSlash_MessageDescribesVerbosityNotDiagnostics asserts the /debug
// confirmation does not falsely claim to toggle the diagnostics panel.
func TestDebugSlash_MessageDescribesVerbosityNotDiagnostics(t *testing.T) {
	m := describeSlashModel(t)

	// Toggle on.
	prior := len(m.blocks)
	m.handleSlash("/debug")
	if !m.sidebarDebug {
		t.Fatalf("/debug should have set sidebarDebug=true")
	}
	if len(m.blocks) != prior+1 {
		t.Fatalf("expected exactly 1 new block, got %d", len(m.blocks)-prior)
	}
	onMsg := m.blocks[len(m.blocks)-1].body

	// Toggle off.
	prior = len(m.blocks)
	m.handleSlash("/debug")
	if m.sidebarDebug {
		t.Fatalf("/debug should have set sidebarDebug=false")
	}
	if len(m.blocks) != prior+1 {
		t.Fatalf("expected exactly 1 new block, got %d", len(m.blocks)-prior)
	}
	offMsg := m.blocks[len(m.blocks)-1].body

	for _, tc := range []struct {
		label string
		msg   string
		state string
	}{
		{"on", onMsg, "on"},
		{"off", offMsg, "off"},
	} {
		// The word "diagnostics" is misleading — the toggle does not gate
		// the LSP Diagnostics panel. The message must not claim it does.
		if strings.Contains(strings.ToLower(tc.msg), "diagnostic") {
			t.Errorf("[%s] /debug message must not claim to toggle diagnostics; got %q", tc.label, tc.msg)
		}
		// Must still report the new state so the operator sees what happened.
		if !strings.Contains(strings.ToLower(tc.msg), tc.state) {
			t.Errorf("[%s] /debug message should report state %q; got %q", tc.label, tc.state, tc.msg)
		}
	}
}

// TestDebugSlash_DiagnosticsPanelNotGatedByToggle proves the root-cause
// mismatch: the LSP Diagnostics panel renders identically whether
// sidebarDebug is on or off, so the old "sidebar diagnostics: on/off"
// message was describing an effect the toggle does not have.
func TestDebugSlash_DiagnosticsPanelNotGatedByToggle(t *testing.T) {
	m := describeSlashModel(t)
	m.lspDiagnostics.Set("main.go", []lsp.Diagnostic{
		{
			Range:    lsp.Range{Start: lsp.Position{Line: 9}},
			Severity: lsp.SeverityError,
			Message:  "undefined: frobnicate",
		},
	})

	// Diagnostics off-by-debug? Render with debug OFF.
	m.sidebarDebug = false
	off := m.renderSidebar(40)
	if !strings.Contains(off, "Diagnostics") {
		t.Fatalf("Diagnostics panel should render with debug OFF when diagnostics exist\n%s", off)
	}

	// Render with debug ON.
	m.sidebarDebug = true
	on := m.renderSidebar(40)
	if !strings.Contains(on, "Diagnostics") {
		t.Fatalf("Diagnostics panel should render with debug ON\n%s", on)
	}

	// The Diagnostics section is unchanged by the toggle — extract and compare.
	if diagSection(off) != diagSection(on) {
		t.Errorf("Diagnostics panel changed across /debug toggle but the message implies it gates the panel\nOFF:\n%s\nON:\n%s",
			diagSection(off), diagSection(on))
	}
}

// diagSection returns the Diagnostics panel header line plus the error row,
// so the comparison isn't perturbed by debug-only verbosity lines elsewhere
// in the sidebar (ctx/budget/sandbox).
func diagSection(sidebar string) string {
	lines := strings.Split(sidebar, "\n")
	var out []string
	capture := false
	for _, ln := range lines {
		if strings.Contains(ln, "Diagnostics") {
			capture = true
		}
		if capture {
			if strings.Contains(ln, "undefined: frobnicate") || strings.Contains(ln, "Diagnostics") || strings.Contains(ln, "error") {
				out = append(out, strings.TrimSpace(ln))
			}
		}
	}
	return strings.Join(out, "\n")
}
