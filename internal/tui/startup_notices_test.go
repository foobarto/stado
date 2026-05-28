package tui

import (
	"strings"
	"testing"
)

// injectStartupNotices must surface the launch banner as a system block so
// the alt-screen TUI doesn't lose what the CLI entry points print to
// stderr before the program takes the screen. Regression test for the
// "startup messages disappear once the TUI appears" report.
func TestInjectStartupNotices_rendersSystemBlock(t *testing.T) {
	m := &Model{}
	notices := []string{
		"stado: warn: running without a process-containment sandbox.",
		"stado: sandbox=no-sandbox session=abc123 (broker-mediated)",
		"stado: writable: <cwd>, /tmp",
	}
	m.injectStartupNotices(notices)

	if len(m.blocks) != 1 {
		t.Fatalf("expected exactly 1 block, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "system" {
		t.Errorf("expected a system block, got kind=%q", m.blocks[0].kind)
	}
	body := m.lastSystemBlockBody()
	for _, want := range notices {
		if !strings.Contains(body, want) {
			t.Errorf("startup block missing line %q; body=%q", want, body)
		}
	}
	// All lines collapse into one block joined by newlines, not one block
	// per line.
	if !strings.Contains(body, "\n") {
		t.Errorf("expected multi-line body, got single line: %q", body)
	}
}

// Empty notices → no block. A sandboxed or suppressed launch must not get a
// stray empty system block at the top of the conversation.
func TestInjectStartupNotices_emptyIsNoOp(t *testing.T) {
	m := &Model{}
	m.injectStartupNotices(nil)
	m.injectStartupNotices([]string{})
	if len(m.blocks) != 0 {
		t.Errorf("expected no blocks for empty notices, got %d", len(m.blocks))
	}
}

// The banner lands after any already-present (replayed) blocks — a resumed
// session shows prior history first, then this launch's notice.
func TestInjectStartupNotices_appendsAfterExistingBlocks(t *testing.T) {
	m := &Model{}
	m.appendBlock(block{kind: "user", body: "earlier message"})
	m.injectStartupNotices([]string{"stado: warn: running without a process-containment sandbox."})
	if len(m.blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "user" || m.blocks[1].kind != "system" {
		t.Errorf("expected [user, system] order, got [%s, %s]", m.blocks[0].kind, m.blocks[1].kind)
	}
}

// Render-level guard: the injected banner must survive renderBlock into
// the visible output, not merely live in m.blocks. (Verify by rendering,
// not by inspecting state.)
func TestInjectStartupNotices_survivesRender(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.injectStartupNotices([]string{
		"stado: warn: running without a process-containment sandbox.",
		"stado: sandbox=no-sandbox session=deadbeef (broker-mediated)",
	})
	out, err := m.renderBlock(m.blocks[len(m.blocks)-1], 80)
	if err != nil {
		t.Fatalf("renderBlock: %v", err)
	}
	// Single tokens that word-wrap won't split, so the assertion is robust
	// to lipgloss re-wrapping at the given width.
	for _, want := range []string{"process-containment", "no-sandbox", "broker-mediated"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered startup block missing %q; out=%q", want, out)
		}
	}
}
