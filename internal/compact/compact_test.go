package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestBuildRequestShape pins the invariants other code depends on:
//   - System carries the summarisation instruction.
//   - Exactly one user-role message is sent.
//   - Tools are empty (summarisation must not request tool calls).
func TestBuildRequestShape(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "hello"),
		agent.Text(agent.RoleAssistant, "hi"),
	}
	req := BuildRequest("some-model", msgs)

	if req.Model != "some-model" {
		t.Errorf("model: %q", req.Model)
	}
	if req.System != SystemPrompt {
		t.Errorf("system prompt mismatch")
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != agent.RoleUser {
		t.Fatalf("expected 1 user message, got %+v", req.Messages)
	}
	if len(req.Tools) != 0 {
		t.Errorf("summarisation must not expose tools, got %d", len(req.Tools))
	}
}

// TestRenderConversationIncludesAllRoles covers user / assistant / tool
// roles + the three block kinds (text, tool_use, tool_result).
func TestRenderConversationIncludesAllRoles(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "read foo.go"),
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "sure"}},
			{ToolUse: &agent.ToolUseBlock{
				ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"foo.go"}`),
			}},
		}},
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "t1", Content: "package main"}},
		}},
	}
	req := BuildRequest("m", msgs)
	body := req.Messages[0].Content[0].Text.Text

	for _, want := range []string{
		"--- USER ---",
		"--- ASSISTANT ---",
		"--- TOOL RESULTS ---",
		"read foo.go",
		"[tool_use read:",
		"[tool_result t1]",
		"package main",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q\nfull:\n%s", want, body)
		}
	}
}

// TestRenderConversationTruncatesHugePayloads keeps the summariser's own
// context from blowing up when a tool result is enormous.
func TestRenderConversationTruncatesHugePayloads(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	msgs := []agent.Message{
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "t", Content: huge}},
		}},
	}
	req := BuildRequest("m", msgs)
	body := req.Messages[0].Content[0].Text.Text
	if strings.Count(body, "x") >= 5000 {
		t.Errorf("expected truncation, got %d x's", strings.Count(body, "x"))
	}
	if !strings.Contains(body, "…") {
		t.Errorf("expected ellipsis marker, got none")
	}
}

// TestReplaceMessagesShape pins the output contract of post-compaction
// msgs: exactly one user-role Message labelled so the model understands
// this is a compaction, followed by nothing else.
func TestReplaceMessagesShape(t *testing.T) {
	got := ReplaceMessages("we fixed the auth bug")
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Role != agent.RoleUser {
		t.Errorf("role = %q, want user", got[0].Role)
	}
	body := got[0].Content[0].Text.Text
	if !strings.Contains(body, "[compaction summary") {
		t.Errorf("body missing compaction marker: %q", body)
	}
	if !strings.Contains(body, "we fixed the auth bug") {
		t.Errorf("body missing summary text: %q", body)
	}
}

// TestSummariseHappyPath uses a scripted Provider that emits TextDelta
// events to confirm Summarise concatenates them and returns the trimmed
// result.
func TestSummariseHappyPath(t *testing.T) {
	p := &scriptedProvider{chunks: []string{"compacted ", "summary ", "here."}}
	got, err := Summarise(context.Background(), p, "m", []agent.Message{
		agent.Text(agent.RoleUser, "anything"),
	})
	if err != nil {
		t.Fatalf("Summarise: %v", err)
	}
	if got != "compacted summary here." {
		t.Errorf("got %q want 'compacted summary here.'", got)
	}
}

// TestSummariseSurfacesStreamError: EvError from the provider should
// short-circuit with a contextual error.
func TestSummariseSurfacesStreamError(t *testing.T) {
	p := &scriptedProvider{chunks: []string{"part 1 "}, errAfter: true}
	got, err := Summarise(context.Background(), p, "m", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "compact:") {
		t.Errorf("error missing package prefix: %v", err)
	}
	if got != "part 1 " {
		t.Errorf("partial result dropped: %q", got)
	}
}

// --- test fakes ---

type scriptedProvider struct {
	chunks   []string
	errAfter bool
}

func (scriptedProvider) Name() string                     { return "scripted" }
func (scriptedProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p *scriptedProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, len(p.chunks)+2)
	go func() {
		defer close(ch)
		for _, c := range p.chunks {
			ch <- agent.Event{Kind: agent.EvTextDelta, Text: c}
		}
		if p.errAfter {
			ch <- agent.Event{Kind: agent.EvError, Err: fmt.Errorf("scripted fail")}
			return
		}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}
