package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// Cluster C1 P1 regression: onToolResult stored the tool-supplied
// result content verbatim (m.blocks[i].toolResult = msg.result.Content)
// and the tool panel rendered it raw. Streaming assistant / thinking
// text is sanitized at the append boundary (model_stream.go EvTextDelta
// / EvThinkingDelta), but the tool-result seam was a sibling miss: a
// tool whose output carries crafted bytes could rewrite the terminal
// title (OSC 0), inject a clickable hyperlink to an attacker URL
// (OSC 8), ring the bell (BEL), and move the cursor (CSI) — the exact
// vectors sanitize.go's own doc names ("a hostile model, tool, plugin,
// or manifest").
//
// After fix the result is routed through textutil.SanitizeForTerminal
// at store-time (the same seam as assistant/thinking), so legitimate
// content (\n, normal text) survives while ESC / BEL / OSC / CSI are
// stripped before they ever reach a render path.
func TestOnToolResult_SanitizesResultContent(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = append(m.blocks, block{
		kind:     "tool",
		toolID:   "tool-1",
		toolName: "bash",
	})

	// OSC 0 (title rewrite) + OSC 8 (clickable hyperlink to evil URL) +
	// BEL terminators, wrapped in legitimate multi-line text.
	const payload = "safe\n\x1b]0;HIJACK\x07\x1b]8;;http://evil.example\x07click\x1b]8;;\x07\nmore"

	_, _ = m.Update(toolResultMsg{result: agent.ToolResultBlock{
		ToolUseID: "tool-1",
		Content:   payload,
	}})

	// 1. Stored result must NOT carry any escape — it's the bytes the
	// panel renders and it's reused for clip/collapse measurement.
	var stored string
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == "tool-1" {
			stored = m.blocks[i].toolResult
			break
		}
	}
	if stored == "" {
		t.Fatal("tool block result was not stored")
	}
	assertNoEscapesIn(t, "block.toolResult", stored)

	// 2. Legitimate content (newlines + normal text) must survive —
	// SanitizeForTerminal preserves \n / \t / \r for prose, mirroring
	// the assistant/thinking path exactly.
	if !strings.Contains(stored, "safe\n") || !strings.Contains(stored, "\nmore") {
		t.Errorf("legitimate result text/newlines were stripped — should be preserved: %q", stored)
	}
	if !strings.Contains(stored, "HIJACK") || !strings.Contains(stored, "click") {
		t.Errorf("legitimate printable bytes were dropped; got %q", stored)
	}

	// 3. The actually-rendered bytes must NOT carry the injected attack
	// vectors in BOTH the collapsed (default) and expanded render paths —
	// that's what reaches the operator's terminal via View(). The render
	// path legitimately emits CSI SGR (\x1b[...) for lipgloss styling, so
	// here we assert specifically on the tool-supplied injection vectors:
	// OSC 0 (title rewrite), OSC 8 (hyperlink), and BEL (\x07). None must
	// survive sanitization.
	collapsed, err := m.renderBlock(m.blocks[len(m.blocks)-1], m.width)
	if err != nil {
		t.Fatalf("renderBlock (collapsed) error: %v", err)
	}
	assertNoToolEscapesIn(t, "rendered tool block (collapsed)", collapsed)

	expandedBlk := m.blocks[len(m.blocks)-1]
	expandedBlk.override = overrideExpanded
	expanded, err := m.renderBlock(expandedBlk, m.width)
	if err != nil {
		t.Fatalf("renderBlock (expanded) error: %v", err)
	}
	assertNoToolEscapesIn(t, "rendered tool block (expanded)", expanded)
}

// assertNoToolEscapesIn checks for the OSC / BEL injection vectors a
// hostile tool result can carry. Unlike assertNoEscapesIn it does not
// flag a bare CSI (\x1b[), because the tool-panel render path legitimately
// emits CSI SGR for lipgloss styling; the title/hyperlink/bell vectors
// (OSC 0, OSC 8, BEL) have no legitimate place in rendered tool output.
func assertNoToolEscapesIn(t *testing.T, label, got string) {
	t.Helper()
	for _, esc := range []string{"\x1b]0", "\x1b]8", "\x1b]52", "\x07"} {
		if strings.Contains(got, esc) {
			t.Errorf("%s leaks %q escape: %q", label, esc, got)
		}
	}
}
