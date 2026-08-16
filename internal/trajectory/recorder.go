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
