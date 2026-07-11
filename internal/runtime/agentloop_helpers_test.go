package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/streambudget"
	"github.com/foobarto/stado/pkg/agent"
)

func TestCollectTurnRejectsOversizedAssistantText(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{
		Kind: agent.EvTextDelta,
		Text: strings.Repeat("x", streambudget.MaxAssistantTextBytes+1),
	}
	close(ch)

	_, _, _, err := collectTurn(ch, nil, nil)
	if err == nil {
		t.Fatal("expected oversized assistant text to fail")
	}
	if !strings.Contains(err.Error(), "assistant text exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectTurnStopsAggregatingAfterCallbackError(t *testing.T) {
	wantErr := errors.New("candidate budget exceeded")
	ch := make(chan agent.Event, 1025)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "rejected"}
	for i := 0; i < 1024; i++ {
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID: "flood", Name: "tool", Input: []byte(strings.Repeat("x", 1024)),
		}}
	}
	close(ch)
	cancelled := false
	text, calls, _, err := collectTurn(ch, func(agent.Event) error { return wantErr }, func() { cancelled = true })
	if !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("error=%v cancelled=%v", err, cancelled)
	}
	if text != "" || len(calls) != 0 {
		t.Fatalf("post-error aggregation text=%q calls=%d", text, len(calls))
	}
}
