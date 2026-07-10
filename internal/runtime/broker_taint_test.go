package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

func TestAgentLoopResetsAndTaintsBrokerAtIngestionBoundaries(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(metricProbeTool{})
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"metric_probe"}
	controller := &recordingBrokerController{}

	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &metricProvider{},
		Executor: &tools.Executor{Registry: reg},
		Config:   cfg,
		Broker:   controller,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "run")},
		MaxTurns: 2,
		Workdir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	controller.mu.Lock()
	got := append([]ContextTaint(nil), controller.taints...)
	controller.mu.Unlock()
	if len(got) != 2 || got[0] != ContextClean || got[1] != ContextTainted {
		t.Fatalf("taint transitions = %v, want [clean tainted]", got)
	}
}

type recordingBrokerController struct {
	mu       sync.Mutex
	taints   []ContextTaint
	requests []BrokerSubagentRequest
	closed   int
	worktree string
}

func (b *recordingBrokerController) CreateSubagent(_ context.Context, req BrokerSubagentRequest) (BrokerController, error) {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	b.mu.Unlock()
	return b, nil
}
func (b *recordingBrokerController) SetTaint(_ context.Context, state ContextTaint) error {
	b.mu.Lock()
	b.taints = append(b.taints, state)
	b.mu.Unlock()
	return nil
}
func (*recordingBrokerController) Sandbox() ExecutorSandbox { return ExecutorSandbox{} }
func (b *recordingBrokerController) Worktree() string       { return b.worktree }
func (b *recordingBrokerController) Close() error {
	b.mu.Lock()
	b.closed++
	b.mu.Unlock()
	return nil
}
