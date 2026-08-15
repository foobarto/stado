package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

type inactiveApplicationBroker struct {
	recordingBrokerController
	publishes int
}

func (b *inactiveApplicationBroker) ApplicationEventContext() ApplicationEventContext {
	return ApplicationEventContext{SessionID: "inactive", Generation: 0}
}

func (b *inactiveApplicationBroker) PublishApplicationEvent(context.Context, HostApplicationEvent) (uint64, error) {
	b.publishes++
	return 0, errors.New("inactive application event scope must not be used")
}

func TestAgentLoopCreatesTurnBoundaryOnSession(t *testing.T) {
	root := t.TempDir()
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(root, "sessions.git"), root)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, filepath.Join(root, "worktrees"), "loop-turn", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	exec := &tools.Executor{Registry: tools.NewRegistry(), Session: sess}

	if _, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &systemCaptureProvider{},
		Executor: exec,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	}); err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if got := sess.Turn(); got != 1 {
		t.Fatalf("session turn = %d, want 1", got)
	}
	if _, err := sc.ResolveRef(stadogit.TurnTagRef(sess.ID, 1)); err != nil {
		t.Fatalf("turn ref missing: %v", err)
	}
}

func TestAgentLoopDoesNotPublishTurnsWithoutAdmittedApplicationGeneration(t *testing.T) {
	root := t.TempDir()
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(root, "sessions.git"), root)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, filepath.Join(root, "worktrees"), "inactive-application", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	broker := &inactiveApplicationBroker{}
	if _, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &systemCaptureProvider{},
		Executor: &tools.Executor{Registry: tools.NewRegistry(), Session: sess},
		Broker:   broker,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	}); err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if broker.publishes != 0 {
		t.Fatalf("inactive application publisher calls = %d, want 0", broker.publishes)
	}
}

type systemCaptureProvider struct {
	system string
}

func (p *systemCaptureProvider) Name() string {
	return "capture"
}

func (p *systemCaptureProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *systemCaptureProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.system = req.System
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}
