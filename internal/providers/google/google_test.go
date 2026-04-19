package google

import (
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
	"github.com/google/generative-ai-go/genai"
)

// Compile-time assertion: Provider satisfies agent.TokenCounter.
var _ agent.TokenCounter = (*Provider)(nil)

func TestSplitMessages_HistoryAndCurrent(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "first"),
		agent.Text(agent.RoleAssistant, "first-reply"),
		agent.Text(agent.RoleUser, "second"),
	}
	hist, cur, err := splitMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2", len(hist))
	}
	if hist[0].Role != "user" || hist[1].Role != "model" {
		t.Errorf("history roles = %q/%q", hist[0].Role, hist[1].Role)
	}
	if len(cur) != 1 {
		t.Fatalf("current parts = %d, want 1", len(cur))
	}
	if txt, ok := cur[0].(genai.Text); !ok || string(txt) != "second" {
		t.Errorf("current = %+v", cur[0])
	}
}

func TestSplitMessages_RequiresTrailingUser(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "ask"),
		agent.Text(agent.RoleAssistant, "reply"),
	}
	if _, _, err := splitMessages(msgs); err == nil {
		t.Error("expected error when last message isn't user role")
	}
}

func TestConvertContent_FunctionCallAndResult(t *testing.T) {
	parts, err := convertContent([]agent.Block{
		{ToolUse: &agent.ToolUseBlock{Name: "search", Input: json.RawMessage(`{"q":"hi"}`)}},
	}, agent.RoleAssistant)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := parts[0].(genai.FunctionCall)
	if !ok {
		t.Fatalf("part[0] = %T, want FunctionCall", parts[0])
	}
	if fc.Name != "search" || fc.Args["q"] != "hi" {
		t.Errorf("function call = %+v", fc)
	}

	parts, err = convertContent([]agent.Block{
		{ToolResult: &agent.ToolResultBlock{ToolUseID: "search", Content: "result"}},
	}, agent.RoleTool)
	if err != nil {
		t.Fatal(err)
	}
	fr, ok := parts[0].(genai.FunctionResponse)
	if !ok {
		t.Fatalf("part[0] = %T, want FunctionResponse", parts[0])
	}
	if fr.Name != "search" || fr.Response["result"] != "result" {
		t.Errorf("function response = %+v", fr)
	}
}

func TestJSONSchemaToGenai_Nested(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"regex"},
			"files":{"type":"array","items":{"type":"string"}}
		},
		"required":["pattern"]
	}`)
	s, err := jsonSchemaToGenai(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != genai.TypeObject {
		t.Errorf("root type = %v", s.Type)
	}
	if len(s.Required) != 1 || s.Required[0] != "pattern" {
		t.Errorf("required = %v", s.Required)
	}
	if s.Properties["pattern"].Type != genai.TypeString {
		t.Errorf("pattern type wrong")
	}
	if s.Properties["files"].Items == nil || s.Properties["files"].Items.Type != genai.TypeString {
		t.Errorf("files.items not set correctly")
	}
}

func TestMediaSubtype(t *testing.T) {
	cases := map[string]string{
		"image/png":  "png",
		"image/jpeg": "jpeg",
		"plain":      "plain",
	}
	for in, want := range cases {
		if got := mediaSubtype(in); got != want {
			t.Errorf("mediaSubtype(%q) = %q, want %q", in, got, want)
		}
	}
}
