package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// Cluster C1 follow-up (review-flagged sibling leak): the tool panel also
// renders the tool-call NAME (block.toolName) and ARGS (block.toolArgs →
// .ArgsPreview/.FullArgs in message_tool.tmpl) RAW. Both come from the
// model's streamed tool-call events (EvToolCallStart.ToolCall.Name,
// EvToolCallArgsDelta / EvToolCallEnd.ToolCall.Input) — the SAME
// attacker-controlled trust boundary as the tool result that C1 fixed. A
// hostile model emitting a tool name or invalid-JSON args carrying
// \x1b]0;…\x07 / \x1b]8;;url\x07 still rewrites the terminal title or
// injects a clickable hyperlink. prettyJSON() returns its input unchanged
// when the JSON won't parse (raw ESC makes it invalid), so the escapes
// reach the terminal.
//
// After fix toolName + toolArgs are sanitized at their set-sites in
// handleStreamEvent (mirroring the result/assistant/thinking seams).
func TestToolCallMeta_NameAndArgsSanitized(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming // handleStreamEvent gates non-Done/Error events on the live stream

	const namePayload = "bash\x1b]0;HIJACK\x07"
	const argsPayload = "{\"cmd\":\"x\x1b]8;;http://evil.example\x07y\x1b]8;;\x07\"}"

	m.handleStreamEvent(agent.Event{Kind: agent.EvToolCallStart, ToolCall: &agent.ToolUseBlock{ID: "t1", Name: namePayload}})
	// stream the args as a delta AND finalize via End (both set-sites).
	m.handleStreamEvent(agent.Event{Kind: agent.EvToolCallArgsDelta, ToolArgsDelta: argsPayload})
	m.handleStreamEvent(agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{ID: "t1", Name: namePayload, Input: json.RawMessage(argsPayload)}})

	var blk *block
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == "t1" {
			blk = &m.blocks[i]
			break
		}
	}
	if blk == nil {
		t.Fatal("tool block was not created")
	}

	// Stored display fields must carry no terminal escapes.
	assertNoEscapesIn(t, "block.toolName", blk.toolName)
	assertNoEscapesIn(t, "block.toolArgs", blk.toolArgs)

	// Legitimate content survives: the tool name keeps "bash", the args
	// keep the cmd value text around the stripped escapes.
	if !strings.HasPrefix(blk.toolName, "bash") {
		t.Errorf("legitimate tool name stripped: %q", blk.toolName)
	}
	if !strings.Contains(blk.toolArgs, "cmd") || !strings.Contains(blk.toolArgs, "x") {
		t.Errorf("legitimate args text stripped: %q", blk.toolArgs)
	}

	// And the rendered panel (collapsed + expanded) must be escape-free
	// for the OSC/BEL vectors (CSI/SGR from lipgloss styling is allowed).
	collapsed, err := m.renderBlock(*blk, 100)
	if err != nil {
		t.Fatalf("renderBlock (collapsed): %v", err)
	}
	blk.expanded = true
	expanded, err := m.renderBlock(*blk, 100)
	if err != nil {
		t.Fatalf("renderBlock (expanded): %v", err)
	}
	assertNoToolEscapesIn(t, "rendered collapsed tool meta", collapsed)
	assertNoToolEscapesIn(t, "rendered expanded tool meta", expanded)
}
