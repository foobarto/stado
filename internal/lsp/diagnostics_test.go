package lsp

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestHandlePublishDiagnostics_ParsesAndCaches: a publishDiagnostics
// notification is parsed, its URI mapped back to an absolute fs path, and
// the set retrievable via CachedDiagnostics. A second publish with an empty
// list clears the file (records []), not stale data.
func TestHandlePublishDiagnostics_ParsesAndCaches(t *testing.T) {
	c := &Client{}
	abs := filepath.Join(t.TempDir(), "a.go")

	c.handlePublishDiagnostics(json.RawMessage(`{
		"uri": "file://` + abs + `",
		"diagnostics": [
			{"range":{"start":{"line":3,"character":0},"end":{"line":3,"character":5}},"severity":1,"message":"boom","source":"gopls"},
			{"range":{"start":{"line":7,"character":2},"end":{"line":7,"character":9}},"severity":2,"message":"meh"}
		]
	}`))

	got := c.CachedDiagnostics(abs)
	if len(got) != 2 {
		t.Fatalf("cached diagnostics len=%d, want 2", len(got))
	}
	if got[0].Severity != SeverityError || got[0].Message != "boom" || got[0].Source != "gopls" {
		t.Fatalf("first diagnostic wrong: %+v", got[0])
	}
	if got[0].Range.Start.Line != 3 {
		t.Fatalf("first diagnostic line=%d, want 3 (0-indexed)", got[0].Range.Start.Line)
	}
	if got[1].Severity != SeverityWarning {
		t.Fatalf("second diagnostic severity=%v, want warning", got[1].Severity)
	}

	// Empty publish clears (records an explicit empty slice, not nil-drop).
	c.handlePublishDiagnostics(json.RawMessage(`{"uri":"file://` + abs + `","diagnostics":[]}`))
	cleared := c.CachedDiagnostics(abs)
	if len(cleared) != 0 {
		t.Fatalf("after empty publish, len=%d, want 0", len(cleared))
	}
}

// TestCachedDiagnostics_UnknownFile: a file the server never published for
// returns nil (distinct from a published-but-empty file).
func TestCachedDiagnostics_UnknownFile(t *testing.T) {
	c := &Client{}
	if got := c.CachedDiagnostics("/never/published.go"); got != nil {
		t.Fatalf("unknown file returned %v, want nil", got)
	}
}

// TestCachedDiagnostics_ReturnsCopy: the returned slice is a copy — a later
// publish must not mutate a slice the caller already holds.
func TestCachedDiagnostics_ReturnsCopy(t *testing.T) {
	c := &Client{}
	abs := filepath.Join(t.TempDir(), "a.go")
	c.handlePublishDiagnostics(json.RawMessage(`{"uri":"file://` + abs + `","diagnostics":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"severity":1,"message":"first"}]}`))

	held := c.CachedDiagnostics(abs)
	c.handlePublishDiagnostics(json.RawMessage(`{"uri":"file://` + abs + `","diagnostics":[{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":1}},"severity":2,"message":"second"}]}`))

	if len(held) != 1 || held[0].Message != "first" {
		t.Fatalf("retained slice was mutated by a later publish: %+v", held)
	}
}

// TestSeverityString: severity labels are stable (used in the TUI surface).
func TestSeverityString(t *testing.T) {
	cases := map[Severity]string{
		SeverityError:   "error",
		SeverityWarning: "warn",
		SeverityInfo:    "info",
		SeverityHint:    "hint",
		SeverityUnknown: "unknown",
		Severity(99):    "unknown",
	}
	for sev, want := range cases {
		if got := sev.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", sev, got, want)
		}
	}
}
