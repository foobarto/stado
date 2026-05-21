package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// toolOnlyProvider scripts a turn that emits one tool call and zero
// text deltas, so the F7 tool_summary notification is the only signal
// the ACP client gets for the turn. cancellation lets the test
// inspect both the tool_call notification AND the synthetic
// tool_summary at end-of-turn.
type toolOnlyProvider struct {
	toolName string
}

func (p toolOnlyProvider) Name() string                     { return "tool-only" }
func (p toolOnlyProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p toolOnlyProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 4)
	go func() {
		defer close(ch)
		// Detect whether this is the assistant's tool-call turn or the
		// follow-up turn (which would echo a tool result and finish).
		// A real provider walks history; for the test we drive one
		// tool-call turn then a clean exit.
		hasToolResult := false
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.ToolResult != nil {
					hasToolResult = true
				}
			}
		}
		if !hasToolResult {
			ch <- agent.Event{
				Kind: agent.EvToolCallEnd,
				ToolCall: &agent.ToolUseBlock{
					ID:    "call-1",
					Name:  p.toolName,
					Input: json.RawMessage(`{}`),
				},
			}
			ch <- agent.Event{Kind: agent.EvDone}
			return
		}
		// Second turn: nothing further; AgentLoop terminates because
		// no new tool calls were issued.
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

func TestToolSummary_FiresOnToolOnlyTurn(t *testing.T) {
	cfg := isolatedACPConfig(t)
	srv := NewServer(cfg, toolOnlyProvider{toolName: "fs__read"})
	srv.EnableTools = false // skip executor wiring; we only need the event stream

	captured := newWriterSync()
	srv.conn = NewConn(strings.NewReader(""), captured)

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID

	// Drive one prompt. With EnableTools=false there is no executor
	// to run the tool call, so AgentLoop hits its 1-turn cap after
	// the model emits the call. F7 says tool_summary fires anyway —
	// the tool work that the model intended IS visible on the wire,
	// so the client should still see the summary.
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		raw := json.RawMessage(`{"sessionId":"` + id + `","prompt":"do work"}`)
		out, err := srv.handleSessionPrompt(context.Background(), raw)
		if err != nil {
			done <- result{err: err}
			return
		}
		done <- result{text: out.(sessionPromptResult).Text}
	}()
	select {
	case <-done:
		// success or error — both acceptable; the contract is "summary
		// fires regardless." The notifications buffer is what we
		// assert against below.
	case <-time.After(5 * time.Second):
		t.Fatal("handleSessionPrompt deadlocked")
	}

	// Parse line-delimited JSON-RPC notifications out of the buffer.
	notifs := splitNotifications(t, captured.Bytes())

	var sawToolCall, sawToolSummary bool
	for _, n := range notifs {
		params, ok := n.Params.(map[string]any)
		if !ok {
			continue
		}
		switch params["kind"] {
		case "tool_call":
			sawToolCall = true
			if got := params["name"]; got != "fs__read" {
				t.Errorf("tool_call name = %v, want fs__read", got)
			}
		case "tool_summary":
			sawToolSummary = true
			if got, _ := params["toolCount"].(float64); int(got) != 1 {
				t.Errorf("tool_summary toolCount = %v, want 1", params["toolCount"])
			}
			if got := params["lastTool"]; got != "fs__read" {
				t.Errorf("tool_summary lastTool = %v, want fs__read", got)
			}
			// lastError's branches are covered separately by
			// TestLastToolErrorIn; the synthetic test environment
			// here has no executor, so whichever value AgentLoop
			// surfaces is environment-dependent. Verify it's at
			// least a bool, not a missing field.
			if _, ok := params["lastError"].(bool); !ok {
				t.Errorf("tool_summary lastError missing or wrong type: %v", params["lastError"])
			}
		}
	}
	if !sawToolCall {
		t.Error("kind=tool_call notification missing — provider should have driven one")
	}
	if !sawToolSummary {
		t.Error("kind=tool_summary notification missing — F7 contract")
	}
}

func TestToolSummary_DoesNotFireWhenTextEmitted(t *testing.T) {
	srv := NewServer(&config.Config{}, scriptedProvider{text: "hello"})
	captured := newWriterSync()
	srv.conn = NewConn(strings.NewReader(""), captured)

	res, err := srv.handleSessionNew(nil)
	if err != nil {
		t.Fatalf("handleSessionNew: %v", err)
	}
	id := res.(sessionNewResult).SessionID

	raw := json.RawMessage(`{"sessionId":"` + id + `","prompt":"hi"}`)
	if _, err := srv.handleSessionPrompt(context.Background(), raw); err != nil {
		t.Fatalf("handleSessionPrompt: %v", err)
	}

	notifs := splitNotifications(t, captured.Bytes())
	for _, n := range notifs {
		params, ok := n.Params.(map[string]any)
		if !ok {
			continue
		}
		if params["kind"] == "tool_summary" {
			t.Errorf("tool_summary fired despite text deltas (%q present)", "hello")
		}
	}
}

// TestLastToolErrorIn directly exercises the messages-walker so the
// happy and unhappy lastError branches are covered without needing
// a full provider/executor environment.
func TestLastToolErrorIn(t *testing.T) {
	cases := []struct {
		name string
		msgs []agent.Message
		want bool
	}{
		{
			name: "no messages",
			msgs: nil,
			want: false,
		},
		{
			name: "no tool-result blocks",
			msgs: []agent.Message{
				agent.Text(agent.RoleUser, "hi"),
				agent.Text(agent.RoleAssistant, "hello"),
			},
			want: false,
		},
		{
			name: "single clean tool result",
			msgs: []agent.Message{{
				Role: agent.RoleUser,
				Content: []agent.Block{{ToolResult: &agent.ToolResultBlock{
					ToolUseID: "x", Content: "ok", IsError: false,
				}}},
			}},
			want: false,
		},
		{
			name: "single errored tool result",
			msgs: []agent.Message{{
				Role: agent.RoleUser,
				Content: []agent.Block{{ToolResult: &agent.ToolResultBlock{
					ToolUseID: "x", Content: "boom", IsError: true,
				}}},
			}},
			want: true,
		},
		{
			name: "last result wins (clean follows error)",
			msgs: []agent.Message{{
				Role: agent.RoleUser,
				Content: []agent.Block{
					{ToolResult: &agent.ToolResultBlock{ToolUseID: "a", IsError: true}},
					{ToolResult: &agent.ToolResultBlock{ToolUseID: "b", IsError: false}},
				},
			}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastToolErrorIn(tc.msgs); got != tc.want {
				t.Errorf("lastToolErrorIn = %v, want %v", got, tc.want)
			}
		})
	}
}

// splitNotifications parses every line of the captured buffer as a
// Notification. Useful when the test exercises a path that emits
// multiple session/update messages.
func splitNotifications(t *testing.T, raw []byte) []Notification {
	t.Helper()
	var out []Notification
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var n Notification
		if err := json.Unmarshal(line, &n); err != nil {
			t.Fatalf("notification json (line %q): %v", line, err)
		}
		out = append(out, n)
	}
	return out
}
