package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/pkg/agent"
)

func TestSubagentRunnerForksReadOnlyChild(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	prov := &subagentCaptureProvider{}
	var events []SubagentEvent

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  prov,
		Model:     "test-model",
		AgentName: "test-subagent",
		OnEvent: func(ev SubagentEvent) {
			events = append(events, ev)
		},
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:   "Inspect runtime subagent code.",
		Role:     subagent.DefaultRole,
		Mode:     subagent.DefaultMode,
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "completed" || res.ChildSession == "" || res.ChildSession == parent.ID {
		t.Fatalf("bad result: %+v", res)
	}
	if !strings.Contains(res.Text, "child findings") {
		t.Fatalf("text = %q", res.Text)
	}
	if !containsString(prov.toolNames, "read") {
		t.Fatalf("child tools missing read: %v", prov.toolNames)
	}
	for _, forbidden := range []string{"write", "edit", "bash", subagent.ToolName} {
		if containsString(prov.toolNames, forbidden) {
			t.Fatalf("child tools exposed %q in read-only mode: %v", forbidden, prov.toolNames)
		}
	}

	msgs, err := LoadConversation(res.Worktree)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(msgs) != res.MessageCount {
		t.Fatalf("conversation messages = %d, result count = %d", len(msgs), res.MessageCount)
	}
	if len(msgs) == 0 || msgs[0].Role != agent.RoleUser {
		t.Fatalf("seed message missing: %+v", msgs)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want started+finished: %+v", len(events), events)
	}
	if events[0].Phase != "started" || events[0].Status != "running" {
		t.Fatalf("started event = %+v", events[0])
	}
	if events[1].Phase != "finished" || events[1].Status != "completed" {
		t.Fatalf("finished event = %+v", events[1])
	}
	if events[0].ChildSession != res.ChildSession || events[1].ChildSession != res.ChildSession {
		t.Fatalf("event child ids = %q/%q, result=%q", events[0].ChildSession, events[1].ChildSession, res.ChildSession)
	}
}

func TestSubagentRunnerReturnsTimeoutResult(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  blockingSubagentProvider{},
		Model:     "test-model",
		AgentName: "test-subagent",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:         "This child blocks.",
		Role:           subagent.DefaultRole,
		Mode:           subagent.DefaultMode,
		MaxTurns:       1,
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", res.Status)
	}
	if res.ChildSession == "" || res.Worktree == "" {
		t.Fatalf("timeout result missing child identity: %+v", res)
	}
	if !strings.Contains(res.Error, "timed out after 1") {
		t.Fatalf("error = %q", res.Error)
	}
}

type subagentCaptureProvider struct {
	toolNames []string
}

func (p *subagentCaptureProvider) Name() string {
	return "capture"
}

func (p *subagentCaptureProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *subagentCaptureProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.toolNames = p.toolNames[:0]
	for _, def := range req.Tools {
		p.toolNames = append(p.toolNames, def.Name)
	}
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "child findings"}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

type blockingSubagentProvider struct{}

func (blockingSubagentProvider) Name() string {
	return "blocking"
}

func (blockingSubagentProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (blockingSubagentProvider) StreamTurn(ctx context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- agent.Event{Kind: agent.EvError, Err: ctx.Err()}
	}()
	return ch, nil
}
