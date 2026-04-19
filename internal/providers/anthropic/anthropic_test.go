package anthropic

import (
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/foobarto/stado/pkg/agent"
)

// Compile-time assertion: Provider satisfies agent.TokenCounter. See
// DESIGN §"Token accounting": a backend missing CountTokens is a hard
// error on first turn, so every bundled provider must pass this check.
var _ agent.TokenCounter = (*Provider)(nil)

func TestBuildMessages_TextAndToolRoundTrip(t *testing.T) {
	req := agent.TurnRequest{
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, "list files"),
			{Role: agent.RoleAssistant, Content: []agent.Block{
				{Text: &agent.TextBlock{Text: "sure"}},
				{ToolUse: &agent.ToolUseBlock{
					ID:    "tool_1",
					Name:  "glob",
					Input: json.RawMessage(`{"pattern":"*.go"}`),
				}},
			}},
			{Role: agent.RoleTool, Content: []agent.Block{
				{ToolResult: &agent.ToolResultBlock{ToolUseID: "tool_1", Content: "main.go"}},
			}},
		},
	}
	out, err := buildMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 msgs, got %d", len(out))
	}
	if out[0].Role != sdk.MessageParamRoleUser {
		t.Errorf("msg0 role = %q, want user", out[0].Role)
	}
	if out[1].Role != sdk.MessageParamRoleAssistant {
		t.Errorf("msg1 role = %q, want assistant", out[1].Role)
	}
	// Assistant message should have text + tool_use blocks
	if len(out[1].Content) != 2 {
		t.Errorf("assistant content blocks = %d, want 2", len(out[1].Content))
	}
	// Tool result message is user-role per Anthropic schema
	if out[2].Role != sdk.MessageParamRoleUser {
		t.Errorf("msg2 (tool result) role = %q, want user", out[2].Role)
	}
}

func TestBuildMessages_ThinkingRoundTrip(t *testing.T) {
	req := agent.TurnRequest{
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, "think then answer"),
			{Role: agent.RoleAssistant, Content: []agent.Block{
				{Thinking: &agent.ThinkingBlock{
					Text:      "let me think...",
					Signature: "sig-abc-123",
				}},
				{Text: &agent.TextBlock{Text: "42"}},
			}},
		},
	}
	out, err := buildMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	// Assistant message keeps both thinking and text blocks
	if len(out[1].Content) != 2 {
		t.Fatalf("assistant content = %d, want 2 (thinking + text)", len(out[1].Content))
	}
	// The thinking block should preserve signature (otherwise extended-thinking
	// tool-use will fail on replay).
	th := out[1].Content[0].OfThinking
	if th == nil {
		t.Fatalf("first block not thinking: %+v", out[1].Content[0])
	}
	if th.Signature != "sig-abc-123" {
		t.Errorf("thinking signature = %q, want preserved", th.Signature)
	}
	if th.Thinking != "let me think..." {
		t.Errorf("thinking text = %q, want preserved", th.Thinking)
	}
}

func TestBuildMessages_CacheBreakpoint(t *testing.T) {
	req := agent.TurnRequest{
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, "big system preamble"),
			agent.Text(agent.RoleUser, "actual question"),
		},
		CacheHints: []agent.CachePoint{{MessageIndex: 0}},
	}
	out, err := buildMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	tb := out[0].Content[0].OfText
	if tb == nil {
		t.Fatalf("msg 0 block is not text: %+v", out[0].Content[0])
	}
	if tb.CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control not attached to msg 0")
	}
	// Second message must NOT have cache_control.
	tb2 := out[1].Content[0].OfText
	if tb2.CacheControl.Type != "" {
		t.Errorf("cache_control unexpectedly on msg 1: %q", tb2.CacheControl.Type)
	}
}

func TestBuildTools_SchemaPassthrough(t *testing.T) {
	defs := []agent.ToolDef{{
		Name:        "grep",
		Description: "ripgrep wrapper",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"pattern":{"type":"string"},"path":{"type":"string"}},
			"required":["pattern"]
		}`),
	}}
	tools, err := buildTools(defs)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	tp := tools[0].OfTool
	if tp == nil {
		t.Fatalf("tool variant not OfTool: %+v", tools[0])
	}
	if tp.Name != "grep" {
		t.Errorf("name = %q", tp.Name)
	}
	if len(tp.InputSchema.Required) != 1 || tp.InputSchema.Required[0] != "pattern" {
		t.Errorf("required = %v, want [pattern]", tp.InputSchema.Required)
	}
}

func TestCapabilities(t *testing.T) {
	p := &Provider{name: "anthropic"}
	c := p.Capabilities()
	if !c.SupportsPromptCache || !c.SupportsThinking || !c.SupportsVision {
		t.Errorf("capabilities missing flags: %+v", c)
	}
	if c.MaxContextTokens != 200_000 {
		t.Errorf("max ctx = %d", c.MaxContextTokens)
	}
}
