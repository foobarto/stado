package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// Helper: pad of escape sequences PR #49 documented as the real attack
// surface (OSC52 = clipboard hijack, OSC8 = clickable link, CSI =
// cursor move / clear). The three lead bytes the assertions check.
const sanitizeProbeOSC52 = "\x1b]52;c;ZXZpbA==\x07"
const sanitizeProbeOSC8 = "\x1b]8;;https://evil\x1b\\link\x1b]8;;\x1b\\"
const sanitizeProbeCSI = "\x1b[2K\x1b[1;1H"

func assertNoEscapesIn(t *testing.T, label, got string) {
	t.Helper()
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(got, esc) {
			t.Errorf("%s leaks %q escape: %q", label, esc, got)
		}
	}
}

// Codex G3/J-a P0 regression: EvThinkingDelta was the lone case in
// model_stream.go's event switch that appended `ev.Text` to both
// `m.turnThinking` and the rendered block body without
// `textutil.SanitizeForTerminal`. EvTextDelta (lines 632, 642 pre-fix)
// was sanitized as part of PR #49; the thinking case was a sibling
// miss. An attacker-influenced reasoning trace from the model could
// therefore plant OSC52 (clipboard hijack), OSC8 (clickable link), or
// CSI cursor moves and have them reach the operator's terminal as
// part of the live thinking-block render.
//
// After fix the sanitize happens at the append boundary; legitimate
// \n / \t / \r in reasoning prose survive (SanitizeForTerminal, not
// StripControlChars).
func TestHandleStreamEvent_ThinkingDeltaSanitized(t *testing.T) {
	m := scenarioModel(t)
	// handleStreamEvent drops everything except EvDone/EvError if
	// the model isn't actively streaming — exercising the stream
	// path requires the live-stream gate.
	m.state = stateStreaming

	// Trio of escapes that PR #49 documented as the real attack
	// surface — OSC52 (clipboard), OSC8 (link), CSI (cursor).
	const payload = "thinking step 1\n\twith legit whitespace " +
		"\x1b]52;c;ZXZpbA==\x07" +
		"\x1b]8;;https://evil\x1b\\link\x1b]8;;\x1b\\" +
		"\x1b[2K\x1b[1;1H"

	m.handleStreamEvent(agent.Event{Kind: agent.EvThinkingDelta, Text: payload})

	// 1. Per-turn accumulator must NOT carry any escape — it's
	// reused for the final agent.Message and replayed on resume.
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(m.turnThinking, esc) {
			t.Errorf("m.turnThinking leaks %q escape: %q", esc, m.turnThinking)
		}
	}

	// 2. The rendered block body (live thinking pane) must not carry
	// any escape either — that's what reaches the operator's terminal
	// via View().
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "thinking" {
		t.Fatalf("expected one thinking block, got %+v", m.blocks)
	}
	body := m.blocks[len(m.blocks)-1].body
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(body, esc) {
			t.Errorf("block.body leaks %q escape: %q", esc, body)
		}
	}

	// 3. Legitimate whitespace (newline + tab) must survive — the
	// rule from memory `feedback_repro_tui_bugs_by_rendering` is that
	// SanitizeForTerminal preserves \n / \t / \r for prose.
	if !strings.Contains(body, "thinking step 1\n\twith legit whitespace") {
		t.Errorf("legitimate prose whitespace was stripped — should be preserved: %q", body)
	}
}

// Codex C2/J-c P1 regression: stado_ui_approve flows plugin-supplied
// title + body straight into the approval drawer. Pre-fix
// onPluginApprovalRequest stored the strings verbatim, and the
// drawer's lipgloss.Render emitted them to the operator's terminal
// without scrubbing. Worst case: a malicious plugin's approval
// prompt could plant OSC52 to exfiltrate the operator's selection
// when they pressed 'y'/'n', OSC8 to overlay a clickable link on
// the "approve" word, or CSI to clear-screen-and-overwrite the
// preceding context.
//
// After fix the handler sanitizes title with StripControlChars
// (single bold header) and body with SanitizeForTerminal (multi-line
// description, preserves legit \n / \t formatting).
func TestOnPluginApprovalRequest_SanitizesTitleAndBody(t *testing.T) {
	m := scenarioModel(t)
	resp := make(chan bool, 1)

	titleProbe := "Plugin wants " + sanitizeProbeOSC52
	bodyProbe := "Allow?\n\nReason: " + sanitizeProbeOSC8 + "\nFooter " + sanitizeProbeCSI

	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    titleProbe,
		body:     bodyProbe,
		response: resp,
	})

	if m.approval == nil {
		t.Fatal("approval should have been stored")
	}
	assertNoEscapesIn(t, "m.approval.title", m.approval.title)
	assertNoEscapesIn(t, "m.approval.body", m.approval.body)

	// Title is StripControlChars — newline-free.
	if strings.Contains(m.approval.title, "\n") {
		t.Errorf("approval title must be single-line; got %q", m.approval.title)
	}
	// Body is SanitizeForTerminal — legit \n preserved for the
	// multi-line description layout the drawer renders.
	if !strings.Contains(m.approval.body, "Allow?\n\nReason:") {
		t.Errorf("approval body must preserve legit \\n formatting; got %q", m.approval.body)
	}

	// Drain the response channel so the test goroutine doesn't leak.
	// We don't care about y/n routing here — that's covered by
	// TestUAT_ApprovalStateRoutesYN.
	close(resp)
}
