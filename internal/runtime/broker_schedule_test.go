package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

type scheduleBrokerStub struct {
	status ScheduleStatus
	err    error
}

func TestAgentLoopTreatsApplicationCompletionAsSuccess(t *testing.T) {
	provider := &textProvider{text: "provider must not run"}
	final, messages, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: provider, Model: "test", MaxTurns: 1,
		Messages: []agent.Message{agent.Text(agent.RoleUser, "work")},
		Broker:   &scheduleBrokerStub{status: ScheduleStatus{State: ScheduleCompleted}},
	})
	if err != nil || final != "" || len(messages) != 1 {
		t.Fatalf("completed loop final=%q messages=%d err=%v", final, len(messages), err)
	}
	if provider.system != "" {
		t.Fatal("provider ran after durable successful completion")
	}
}

func (s *scheduleBrokerStub) CreateSubagent(context.Context, BrokerSubagentRequest) (BrokerController, error) {
	return s, nil
}
func (s *scheduleBrokerStub) SetTaint(context.Context, ContextTaint) error { return nil }
func (s *scheduleBrokerStub) Sandbox() ExecutorSandbox                     { return ExecutorSandbox{} }
func (s *scheduleBrokerStub) Worktree() string                             { return "" }
func (s *scheduleBrokerStub) Close() error                                 { return nil }
func (s *scheduleBrokerStub) CheckSchedule(context.Context) (ScheduleStatus, error) {
	return s.status, s.err
}

func TestCheckSchedulingMapsDurableStates(t *testing.T) {
	for state, target := range map[ScheduleState]error{
		ScheduleHeld: ErrScheduleHeld, SchedulePaused: ErrSchedulePaused, ScheduleStopped: ErrScheduleStopped,
	} {
		err := CheckScheduling(context.Background(), &scheduleBrokerStub{status: ScheduleStatus{State: state, ReasonCode: "watchdog"}})
		if !errors.Is(err, target) {
			t.Fatalf("state %s err=%v, want %v", state, err, target)
		}
	}
	if err := CheckScheduling(context.Background(), &scheduleBrokerStub{status: ScheduleStatus{State: ScheduleActive}}); err != nil {
		t.Fatalf("active schedule: %v", err)
	}
	if err := CheckScheduling(context.Background(), &scheduleBrokerStub{status: ScheduleStatus{State: ScheduleCompleted}}); err != nil {
		t.Fatalf("successful completion was treated as dispatch error: %v", err)
	}
	completed, err := WaitForApplicationScheduleStatus(context.Background(), &scheduleBrokerStub{status: ScheduleStatus{State: ScheduleCompleted}}, nil)
	if err != nil || !completed {
		t.Fatalf("completion barrier = completed=%v err=%v", completed, err)
	}
	private := errors.New("broker unavailable")
	if err := CheckScheduling(context.Background(), &scheduleBrokerStub{err: private}); !errors.Is(err, private) {
		t.Fatalf("broker failure did not fail closed: %v", err)
	}
}
