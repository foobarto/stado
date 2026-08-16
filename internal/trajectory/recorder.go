package trajectory

import (
	"context"

	"github.com/foobarto/stado/pkg/agent"
)

// Writer is the authenticated broker-client seam for canonical trajectory
// state. Production implementations submit typed facts over broker RPC; they
// never open the broker WAL from the orchestrator process.
type Writer interface {
	EnsureTrajectoryObjective(context.Context, string) error
	RecordTrajectoryToolOutcome(context.Context, int, int, agent.ToolUseBlock, agent.ToolResultBlock) error
}

type Recorder struct{ Writer Writer }

// InvocationBase returns the transcript-order ordinal of the first tool call
// not yet present in messages. currentCalls accounts for a just-appended
// assistant response whose calls are already in the transcript. Providers do
// not universally supply unique call IDs, so trajectory identity uses this
// stable ordering instead.
func InvocationBase(messages []agent.Message, currentCalls int) int {
	total := 0
	for _, message := range messages {
		for _, block := range message.Content {
			if block.ToolUse != nil {
				total++
			}
		}
	}
	if currentCalls < 0 || total < currentCalls {
		return 0
	}
	return total - currentCalls
}

func (r Recorder) EnsureObjective(objective string) {
	if r.Writer == nil {
		return
	}
	_ = r.Writer.EnsureTrajectoryObjective(context.Background(), objective)
}

func (r Recorder) ToolOutcome(turn, invocation int, call agent.ToolUseBlock, result agent.ToolResultBlock) {
	if r.Writer == nil {
		return
	}
	_ = r.Writer.RecordTrajectoryToolOutcome(context.Background(), turn, invocation, call, result)
}
