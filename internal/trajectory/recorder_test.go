package trajectory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

type captureWriter struct {
	objectives []string
	outcomes   []capturedOutcome
}

type capturedOutcome struct {
	turn   int
	call   agent.ToolUseBlock
	result agent.ToolResultBlock
}

func (w *captureWriter) EnsureTrajectoryObjective(_ context.Context, objective string) error {
	w.objectives = append(w.objectives, objective)
	return nil
}

func (w *captureWriter) RecordTrajectoryToolOutcome(_ context.Context, turn int, call agent.ToolUseBlock, result agent.ToolResultBlock) error {
	w.outcomes = append(w.outcomes, capturedOutcome{turn: turn, call: call, result: result})
	return nil
}

func TestRecorderForwardsFactsToWriter(t *testing.T) {
	writer := &captureWriter{}
	r := Recorder{Writer: writer}
	call := agent.ToolUseBlock{ID: "1", Name: "app", Input: json.RawMessage(`{"bad":true}`)}
	result := agent.ToolResultBlock{ToolUseID: "1", Content: "invalid args", IsError: true}

	r.EnsureObjective("ship safely")
	r.ToolOutcome(7, call, result)

	if len(writer.objectives) != 1 || writer.objectives[0] != "ship safely" {
		t.Fatalf("objectives=%v", writer.objectives)
	}
	if len(writer.outcomes) != 1 || writer.outcomes[0].turn != 7 || writer.outcomes[0].call.ID != call.ID || writer.outcomes[0].result.Content != result.Content {
		t.Fatalf("outcomes=%+v", writer.outcomes)
	}
}

func TestRecorderWithoutWriterIsInert(t *testing.T) {
	var r Recorder
	r.EnsureObjective("ignored")
	r.ToolOutcome(1, agent.ToolUseBlock{}, agent.ToolResultBlock{})
}
