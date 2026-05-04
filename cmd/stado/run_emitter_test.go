package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestEmitter_QuietSuppressesToolCallPreview — dogfood note from
// htb-writeups workflow integration: `stado run --tools` interleaves
// agent text with "▸ tool(args)" preview lines, making stdout hard
// to script-parse. `--quiet` strips the previews while keeping text.
func TestEmitter_QuietSuppressesToolCallPreview(t *testing.T) {
	var buf bytes.Buffer
	emit := emitter(false /*jsonOut*/, true /*quiet*/, &buf)

	emit(agent.Event{Kind: agent.EvTextDelta, Text: "hello "})
	emit(agent.Event{
		Kind: agent.EvToolCallEnd,
		ToolCall: &agent.ToolUseBlock{
			Name:  "webfetch",
			Input: []byte(`{"url":"https://example.com"}`),
		},
	})
	emit(agent.Event{Kind: agent.EvTextDelta, Text: "world"})

	got := buf.String()
	if got != "hello world" {
		t.Fatalf("expected text-only stdout, got %q", got)
	}
	if strings.Contains(got, "webfetch") || strings.Contains(got, "▸") {
		t.Fatalf("tool-call preview leaked under --quiet: %q", got)
	}
}

func TestEmitter_NonQuiet_KeepsToolCallPreview(t *testing.T) {
	var buf bytes.Buffer
	emit := emitter(false /*jsonOut*/, false /*quiet*/, &buf)

	emit(agent.Event{Kind: agent.EvTextDelta, Text: "answer "})
	emit(agent.Event{
		Kind: agent.EvToolCallEnd,
		ToolCall: &agent.ToolUseBlock{
			Name:  "read_file",
			Input: []byte(`{"path":"foo.go"}`),
		},
	})

	got := buf.String()
	if !strings.Contains(got, "▸ read_file(") {
		t.Fatalf("expected tool-call preview in default mode, got %q", got)
	}
}

func TestEmitter_JSONIgnoresQuiet(t *testing.T) {
	// JSON mode is already structured + machine-parseable; --quiet has
	// no effect there. Tool calls still appear as `{"type":"tool_call"...}`.
	var buf bytes.Buffer
	emit := emitter(true /*jsonOut*/, true /*quiet*/, &buf)

	emit(agent.Event{
		Kind: agent.EvToolCallEnd,
		ToolCall: &agent.ToolUseBlock{
			Name:  "bash",
			Input: []byte(`{"cmd":"ls"}`),
		},
	})

	got := buf.String()
	if !strings.Contains(got, `"type":"tool_call"`) {
		t.Fatalf("expected JSON tool_call event under --json --quiet, got %q", got)
	}
	if !strings.Contains(got, `"name":"bash"`) {
		t.Fatalf("expected name field in JSON event, got %q", got)
	}
}
