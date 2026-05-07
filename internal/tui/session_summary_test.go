package tui

import (
	"bytes"
	"testing"
)

// TestEmitSessionSummary_NilSessionNoOps: the helper must not panic
// when called against a model that hasn't reached a session yet
// (e.g., quit during initial provider build). The TUI's quit path
// hits this any time the user presses Ctrl-D before the first turn.
func TestEmitSessionSummary_NilSessionNoOps(t *testing.T) {
	var buf bytes.Buffer
	emitSessionSummary(&Model{}, &buf)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nil session, got %q", buf.String())
	}
}

// TestEmitSessionSummary_NilModelNoOps: defensive against caller bugs
// (m.session.Sidecar nil etc.). Helper returns silently.
func TestEmitSessionSummary_NilModelNoOps(t *testing.T) {
	var buf bytes.Buffer
	emitSessionSummary(nil, &buf)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nil model, got %q", buf.String())
	}
}
