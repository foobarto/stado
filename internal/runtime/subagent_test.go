package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestSubagentRunnerWorkerUsesScopedWriteTools(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	prov := &workerSubagentProvider{}

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  prov,
		Model:     "test-model",
		AgentName: "test-worker",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:     "Write the allowed file and report blocked attempts.",
		Role:       subagent.WorkerRole,
		Mode:       subagent.WorkspaceWriteMode,
		Ownership:  "allowed directory only",
		WriteScope: []string{"allowed/**"},
		MaxTurns:   2,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "completed" || res.Mode != subagent.WorkspaceWriteMode {
		t.Fatalf("bad result: %+v", res)
	}
	for _, want := range []string{"read", "write", "edit"} {
		if !containsString(prov.toolNames, want) {
			t.Fatalf("worker tools missing %q: %v", want, prov.toolNames)
		}
	}
	for _, forbidden := range []string{"bash", "ast_grep", "webfetch", subagent.ToolName} {
		if containsString(prov.toolNames, forbidden) {
			t.Fatalf("worker tools exposed %q: %v", forbidden, prov.toolNames)
		}
	}
	written := filepath.Join(res.Worktree, "allowed", "new.txt")
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read child write: %v", err)
	}
	if string(data) != "child write" {
		t.Fatalf("child write = %q", data)
	}
	if want := []string{"allowed/new.txt"}; !reflect.DeepEqual(res.ChangedFiles, want) {
		t.Fatalf("changed_files = %#v, want %#v", res.ChangedFiles, want)
	}
	if _, err := os.Stat(filepath.Join(parent.WorktreePath, "allowed", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent worktree was modified, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Worktree, "blocked", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write created file, stat err = %v", err)
	}
	if got := prov.toolResult("allow"); got == nil || got.IsError {
		t.Fatalf("allow tool result = %+v", got)
	}
	blocked := prov.toolResult("block")
	if blocked == nil || !blocked.IsError || !strings.Contains(blocked.Content, "outside write_scope") {
		t.Fatalf("blocked tool result = %+v", blocked)
	}
	if len(res.ScopeViolations) != 1 || !strings.Contains(res.ScopeViolations[0], "blocked/new.txt") {
		t.Fatalf("scope_violations = %#v, want blocked/new.txt", res.ScopeViolations)
	}
}

func TestSubagentRunnerRejectsWorkerWithoutScope(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	_, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  &subagentCaptureProvider{},
		Model:     "test-model",
		AgentName: "test-worker",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:    "Write files.",
		Role:      subagent.WorkerRole,
		Mode:      subagent.WorkspaceWriteMode,
		Ownership: "missing scope",
		MaxTurns:  1,
	})
	if err == nil {
		t.Fatal("expected missing write_scope error")
	}
	if !strings.Contains(err.Error(), "write_scope is required") {
		t.Fatalf("error = %v", err)
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

func TestSubagentRunnerPropagatesParentCancellation(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	var events []SubagentEvent

	done := make(chan error, 1)
	go func() {
		_, err := (SubagentRunner{
			Config:    cfg,
			Parent:    parent,
			Provider:  blockingSubagentProvider{},
			Model:     "test-model",
			AgentName: "test-subagent",
			OnEvent: func(ev SubagentEvent) {
				events = append(events, ev)
			},
		}).SpawnSubagent(ctx, subagent.Request{
			Prompt:         "This child waits for parent cancellation.",
			Role:           subagent.DefaultRole,
			Mode:           subagent.DefaultMode,
			MaxTurns:       1,
			TimeoutSeconds: 60,
		})
		done <- err
	}()

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want started+finished: %+v", len(events), events)
	}
	if events[1].Phase != "finished" || events[1].Status != "error" {
		t.Fatalf("finished event = %+v", events[1])
	}
	if !strings.Contains(events[1].Error, "context canceled") {
		t.Fatalf("finished event error = %q", events[1].Error)
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

type workerSubagentProvider struct {
	turn        int
	toolNames   []string
	toolResults []agent.ToolResultBlock
}

func (p *workerSubagentProvider) Name() string {
	return "worker-capture"
}

func (p *workerSubagentProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *workerSubagentProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if p.turn == 0 {
		p.toolNames = p.toolNames[:0]
		for _, def := range req.Tools {
			p.toolNames = append(p.toolNames, def.Name)
		}
		p.turn++
		ch := make(chan agent.Event, 3)
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID:    "allow",
			Name:  "write",
			Input: json.RawMessage(`{"path":"allowed/new.txt","content":"child write"}`),
		}}
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID:    "block",
			Name:  "write",
			Input: json.RawMessage(`{"path":"blocked/new.txt","content":"should not land"}`),
		}}
		ch <- agent.Event{Kind: agent.EvDone}
		close(ch)
		return ch, nil
	}
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.ToolResult != nil {
				p.toolResults = append(p.toolResults, *block.ToolResult)
			}
		}
	}
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "worker done"}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func (p *workerSubagentProvider) toolResult(id string) *agent.ToolResultBlock {
	for i := range p.toolResults {
		if p.toolResults[i].ToolUseID == id {
			return &p.toolResults[i]
		}
	}
	return nil
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
