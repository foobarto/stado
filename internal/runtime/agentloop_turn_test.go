package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

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
		Provider: costAwareProvider{},
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

func TestAgentLoopAddsMemoryContextToSystemPrompt(t *testing.T) {
	prov := &systemCaptureProvider{}
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider:      prov,
		Model:         "m",
		Messages:      []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns:      1,
		MemoryContext: "Memory snippets supplied by installed plugins.\n- [global/preference mem_1] Prefer focused tests.",
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if got := prov.system; got == "" || !strings.Contains(got, "Memory snippets supplied by installed plugins") {
		t.Fatalf("system prompt missing memory context:\n%s", got)
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
