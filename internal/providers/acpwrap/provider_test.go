package acpwrap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// Real-world payload shapes from gemini --acp + opencode acp. Each
// is a single session/update notification's `params.update` payload,
// which is what handleUpdate forwards to translateUpdate.
var translateCases = []struct {
	name      string
	payload   string
	wantKind  agent.EventKind
	wantText  string
	wantEmpty bool // true → translateUpdate should return (_, false)
}{
	{
		name:     "agent_message_chunk single-object content (gemini)",
		payload:  `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"HELLO"}}`,
		wantKind: agent.EvTextDelta,
		wantText: "HELLO",
	},
	{
		name:     "agent_message_chunk array content (canonical-spec)",
		payload:  `{"sessionUpdate":"agent_message_chunk","content":[{"type":"text","text":"first"},{"type":"text","text":" second"}]}`,
		wantKind: agent.EvTextDelta,
		wantText: "first second",
	},
	{
		name:     "agent_thought_chunk routes to thinking delta",
		payload:  `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hmm"}}`,
		wantKind: agent.EvThinkingDelta,
		wantText: "hmm",
	},
	{
		name:     "tool_call surfaces breadcrumb",
		payload:  `{"sessionUpdate":"tool_call","toolCall":{"name":"read_file","title":"Read file"}}`,
		wantKind: agent.EvTextDelta,
		wantText: "\n[tool: Read file]\n",
	},
	{
		name:      "available_commands_update is dropped (noise)",
		payload:   `{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"memory"}]}`,
		wantEmpty: true,
	},
	{
		name:      "unknown sessionUpdate is dropped",
		payload:   `{"sessionUpdate":"some_future_kind","content":"whatever"}`,
		wantEmpty: true,
	},
	{
		name:      "malformed JSON is dropped",
		payload:   `{not-json`,
		wantEmpty: true,
	},
	{
		name:      "agent_message_chunk with empty text is dropped",
		payload:   `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}`,
		wantEmpty: true,
	},
	{
		name:      "agent_message_chunk with non-text content is dropped (image, etc.)",
		payload:   `{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"..."}}`,
		wantEmpty: true,
	},
}

func TestTranslateUpdate(t *testing.T) {
	for _, tc := range translateCases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := translateUpdate(json.RawMessage(tc.payload))
			if tc.wantEmpty {
				if ok {
					t.Errorf("expected empty result for %q, got Kind=%d Text=%q", tc.name, ev.Kind, ev.Text)
				}
				return
			}
			if !ok {
				t.Fatalf("expected event for %q, got drop", tc.name)
			}
			if ev.Kind != tc.wantKind {
				t.Errorf("Kind = %d, want %d", ev.Kind, tc.wantKind)
			}
			if ev.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", ev.Text, tc.wantText)
			}
		})
	}
}

func TestExtractTextBlocks_SingleAndArray(t *testing.T) {
	// Single-object shape (gemini's actual emit).
	if got := extractTextBlocks(json.RawMessage(`{"type":"text","text":"hi"}`)); got != "hi" {
		t.Errorf("single: got %q, want %q", got, "hi")
	}
	// Array shape (canonical-spec multi-block).
	if got := extractTextBlocks(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "ab" {
		t.Errorf("array: got %q, want %q", got, "ab")
	}
	// Empty content.
	if got := extractTextBlocks(json.RawMessage(``)); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
	// Non-text blocks dropped.
	if got := extractTextBlocks(json.RawMessage(`[{"type":"image","data":"x"},{"type":"text","text":"keep"}]`)); got != "keep" {
		t.Errorf("mixed: got %q, want %q", got, "keep")
	}
}

func TestLastUserText_PullsFromLastUserMessage(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "first"}}}},
		{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "reply"}}}},
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "second"}}}},
	}
	got, err := lastUserText(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

func TestLastUserText_ErrsOnNoUserMessage(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "lonely"}}}},
	}
	_, err := lastUserText(msgs)
	if err == nil {
		t.Fatal("expected error for assistant-only history")
	}
	if !strings.Contains(err.Error(), "no user message") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_RequiresBinary(t *testing.T) {
	if _, err := New(Config{Name: "x"}); err == nil {
		t.Fatal("expected error when Binary is empty")
	}
}

func TestProviderShape(t *testing.T) {
	// Smoke test: Provider satisfies agent.Provider at compile-time.
	var _ agent.Provider = &Provider{}
}
