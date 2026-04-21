package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// costAwareProvider returns a fixed cost per turn and no tool calls,
// so each call to StreamTurn consumes the cap without tool-execution
// complications.
type costAwareProvider struct {
	costPerTurn float64
}

func (p costAwareProvider) Name() string { return "costaware" }

func (p costAwareProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p costAwareProvider) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "ok\n"}
	ch <- agent.Event{
		Kind:  agent.EvDone,
		Usage: &agent.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: p.costPerTurn},
	}
	close(ch)
	return ch, nil
}

// TestAgentLoop_CostCapAborts: after the cumulative cost crosses
// CostCapUSD at the first turn boundary, AgentLoop returns
// ErrCostCapExceeded and the partial conversation is still returned
// so callers can persist what's there. One turn costs $1.50 against
// a $1.00 cap — trip on turn 1.
func TestAgentLoop_CostCapAborts(t *testing.T) {
	msgs := []agent.Message{agent.Text(agent.RoleUser, "hi")}
	_, finalMsgs, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider:   costAwareProvider{costPerTurn: 1.50},
		Model:      "m",
		Messages:   msgs,
		MaxTurns:   5,
		CostCapUSD: 1.00,
	})
	if !errors.Is(err, ErrCostCapExceeded) {
		t.Fatalf("expected ErrCostCapExceeded, got %v", err)
	}
	if len(finalMsgs) == len(msgs) {
		t.Error("expected some assistant message in finalMsgs despite abort")
	}
}

// TestAgentLoop_CostCapDisabledByDefault: zero cap means no cap — the
// loop terminates naturally when the provider stops asking for tools
// (single turn in this fake since there are none).
func TestAgentLoop_CostCapDisabledByDefault(t *testing.T) {
	msgs := []agent.Message{agent.Text(agent.RoleUser, "hi")}
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: costAwareProvider{costPerTurn: 100.0},
		Model:    "m",
		Messages: msgs,
		MaxTurns: 5,
		// CostCapUSD left at 0.
	})
	if err != nil {
		t.Fatalf("expected natural completion with zero cap; got %v", err)
	}
}
