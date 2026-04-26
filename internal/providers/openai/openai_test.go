package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/pkg/agent"
)

// Compile-time assertion: Provider satisfies agent.TokenCounter. See
// anthropic_test.go for the rationale.
var _ agent.TokenCounter = (*Provider)(nil)

func TestConvertMessages_TextAndToolRoundTrip(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "read foo.go"),
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "sure"}},
			{ToolUse: &agent.ToolUseBlock{
				ID:    "call_1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"foo.go"}`),
			}},
		}},
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "call_1", Content: "package foo"}},
		}},
	}
	out, err := convertMessages("be a coder", msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 msgs (system+user+assistant+tool), got %d", len(out))
	}
	if out[0].OfSystem == nil {
		t.Errorf("msg 0 not system: %+v", out[0])
	}
	if out[1].OfUser == nil {
		t.Errorf("msg 1 not user: %+v", out[1])
	}
	if out[2].OfAssistant == nil {
		t.Fatalf("msg 2 not assistant: %+v", out[2])
	}
	if len(out[2].OfAssistant.ToolCalls) != 1 {
		t.Errorf("assistant tool_calls = %d, want 1", len(out[2].OfAssistant.ToolCalls))
	}
	if out[2].OfAssistant.ToolCalls[0].Function.Arguments != `{"path":"foo.go"}` {
		t.Errorf("tool call args = %q", out[2].OfAssistant.ToolCalls[0].Function.Arguments)
	}
	if out[3].OfTool == nil {
		t.Errorf("msg 3 not tool: %+v", out[3])
	}
}

func TestBuildParams_ToolsAndParallelFlag(t *testing.T) {
	req := agent.TurnRequest{
		Model:    "gpt-4o",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		Tools: []agent.ToolDef{{
			Name:        "search",
			Description: "search",
			Schema:      json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
		}},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(params.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(params.Tools))
	}
	if params.Tools[0].Function.Name != "search" {
		t.Errorf("tool name = %q", params.Tools[0].Function.Name)
	}
	if !params.ParallelToolCalls.Valid() || !params.ParallelToolCalls.Value {
		t.Errorf("parallel_tool_calls not enabled")
	}
}

func TestConvertMessages_RejectsOversizedToolInput(t *testing.T) {
	_, err := convertMessages("", []agent.Message{{
		Role: agent.RoleAssistant,
		Content: []agent.Block{{ToolUse: &agent.ToolUseBlock{
			ID:    "call_1",
			Name:  "read_file",
			Input: json.RawMessage(strings.Repeat("x", toolinput.MaxBytes+1)),
		}}},
	}})
	if err == nil {
		t.Fatal("expected oversized tool input error")
	}
}

func TestCapabilities(t *testing.T) {
	p := &Provider{name: "openai"}
	c := p.Capabilities()
	if c.SupportsThinking {
		t.Error("chat-completions openai provider should not advertise thinking support")
	}
	if c.MaxContextTokens != 128_000 {
		t.Errorf("ctx = %d", c.MaxContextTokens)
	}
}
