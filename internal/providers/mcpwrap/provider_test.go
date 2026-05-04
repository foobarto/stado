package mcpwrap

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/foobarto/stado/pkg/agent"
)

func TestNew_RequiresBinary(t *testing.T) {
	if _, err := New(Config{CallTool: "x"}); err == nil {
		t.Error("expected error for empty Binary")
	}
}

func TestNew_RequiresCallTool(t *testing.T) {
	if _, err := New(Config{Binary: "/bin/true"}); err == nil {
		t.Error("expected error for empty CallTool")
	}
}

func TestNew_DefaultsAreApplied(t *testing.T) {
	p, err := New(Config{Binary: "/bin/true", CallTool: "codex"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.cfg.PromptArgKey != "prompt" {
		t.Errorf("PromptArgKey default = %q, want %q", p.cfg.PromptArgKey, "prompt")
	}
	if p.cfg.ThreadIDArgKey != "threadId" {
		t.Errorf("ThreadIDArgKey default = %q", p.cfg.ThreadIDArgKey)
	}
	if p.cfg.ContentResultKey != "content" {
		t.Errorf("ContentResultKey default = %q", p.cfg.ContentResultKey)
	}
	if p.cfg.ThreadIDResultKey != "threadId" {
		t.Errorf("ThreadIDResultKey default = %q", p.cfg.ThreadIDResultKey)
	}
}

func TestNew_OverridesPreserved(t *testing.T) {
	// Operator can use this for non-codex MCP-wrapped agents that
	// use different field names (e.g. session_id, message).
	p, err := New(Config{
		Binary:            "/bin/true",
		CallTool:          "agent",
		PromptArgKey:      "message",
		ThreadIDArgKey:    "session_id",
		ContentResultKey:  "reply",
		ThreadIDResultKey: "session_id",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.cfg.PromptArgKey != "message" {
		t.Errorf("override lost: PromptArgKey = %q", p.cfg.PromptArgKey)
	}
	if p.cfg.ThreadIDArgKey != "session_id" {
		t.Errorf("override lost: ThreadIDArgKey = %q", p.cfg.ThreadIDArgKey)
	}
}

func TestProviderShape(t *testing.T) {
	var _ agent.Provider = &Provider{}
}

func TestLastUserText_PullsLastUser(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "first"}}}},
		{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "reply"}}}},
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "latest"}}}},
	}
	got, err := lastUserText(msgs)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "latest" {
		t.Errorf("got %q, want %q", got, "latest")
	}
}

func TestLastUserText_ErrsOnNoUserMessage(t *testing.T) {
	msgs := []agent.Message{{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "hi"}}}}}
	if _, err := lastUserText(msgs); err == nil {
		t.Error("expected error when no user message present")
	}
}

func TestExtractContentAndThread_StructuredContentPreferred(t *testing.T) {
	res := &mcp.CallToolResult{
		StructuredContent: map[string]any{
			"content":  "hello world",
			"threadId": "abc-123",
		},
	}
	text, thread := extractContentAndThread(res, "content", "threadId")
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
	if thread != "abc-123" {
		t.Errorf("thread = %q", thread)
	}
}

func TestExtractContentAndThread_FallbackToContentArray(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: "fallback text"},
		},
	}
	text, thread := extractContentAndThread(res, "content", "threadId")
	if text != "fallback text" {
		t.Errorf("text = %q", text)
	}
	if thread != "" {
		t.Errorf("thread should be empty in fallback, got %q", thread)
	}
}

func TestExtractContentAndThread_RoundTripStructuredContentJSON(t *testing.T) {
	// MCP servers in practice often emit structured content as
	// JSON-marshalled then unmarshalled into map[string]any. Verify
	// our marshal/unmarshal round-trip handles that.
	rawJSON := json.RawMessage(`{"content":"text","threadId":"t1"}`)
	var asAny any
	if err := json.Unmarshal(rawJSON, &asAny); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := &mcp.CallToolResult{StructuredContent: asAny}
	text, thread := extractContentAndThread(res, "content", "threadId")
	if text != "text" || thread != "t1" {
		t.Errorf("got (%q, %q), want (%q, %q)", text, thread, "text", "t1")
	}
}

func TestExtractErrText_PrefersFirstNonEmptyText(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: ""},
			mcp.TextContent{Type: "text", Text: "actual error"},
		},
	}
	got := extractErrText(res)
	if got != "actual error" {
		t.Errorf("got %q, want %q", got, "actual error")
	}
}
