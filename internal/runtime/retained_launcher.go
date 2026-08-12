package runtime

import (
	"context"

	"github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/orchestration"
	"github.com/foobarto/stado/internal/subagent"
)

// RetainedLauncher adapts the existing isolated SubagentRunner to the durable
// orchestration coordinator. Admission chooses identity before process launch.
type RetainedLauncher struct {
	Runner  SubagentRunner
	Request subagent.Request
}

func (l RetainedLauncher) Launch(ctx context.Context, a retained.Admission) (orchestration.LaunchResult, error) {
	req := l.Request
	req.ChildSessionID = a.ChildSessionID
	req.Execution = "wait"
	res, err := l.Runner.SpawnSubagent(ctx, req)
	usage := budget.Limits{Turns: uint64(req.MaxTurns)}
	out := orchestration.LaunchResult{Usage: usage}
	if err != nil {
		out.Error = err.Error()
		return out, err
	}
	if res.Error != "" {
		out.Error = res.Error
	}
	out.FinalText = res.Text
	return out, nil
}
