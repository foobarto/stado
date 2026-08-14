package runtime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/go-git/go-git/v5/plumbing"
)

type recordingApplicationEventPublisher struct {
	events []HostApplicationEvent
}

func (p *recordingApplicationEventPublisher) PublishApplicationEvent(_ context.Context, event HostApplicationEvent) (uint64, error) {
	p.events = append(p.events, event)
	return uint64(len(p.events)), nil
}

func applicationEventSession(t *testing.T) *stadogit.Session {
	t.Helper()
	root := t.TempDir()
	sidecar, err := stadogit.OpenOrInitSidecar(filepath.Join(root, "sessions.git"), root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := stadogit.CreateSession(sidecar, filepath.Join(root, "worktrees"), "application-events", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionTurnCommittedProjectsBoundedHostFacts(t *testing.T) {
	session := applicationEventSession(t)
	before, err := session.TreeHead()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.WorktreePath, "result.txt"), []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := session.BuildTreeFromDir(session.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := session.CommitToTree(tree, stadogit.CommitMeta{Tool: "test", Summary: "host-observed change"})
	if err != nil {
		t.Fatal(err)
	}

	secretArgs := json.RawMessage(`{"path":"result.txt","secret":"do-not-copy"}`)
	longText := strings.Repeat("x", maxTurnFactTextBytes+17)
	facts, refs, err := BuildSessionTurnCommitted(session, TurnCommitInput{
		Iteration: 2, TreeBefore: before, ProviderName: "test", Model: "model",
		Usage:            agent.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3},
		CumulativeTokens: 42, TokenLimit: 100, Text: longText,
		Calls:    []agent.ToolUseBlock{{ID: "call-1", Name: "files__write", Input: secretArgs}},
		Results:  []agent.ToolResultBlock{{ToolUseID: "call-1", Content: "ok"}},
		Duration: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Schema != SessionTurnCommittedSchemaV1 || facts.Anchor.SessionSequence != (uint64(1)<<32|3) || !strings.Contains(facts.Anchor.TurnRef, "@"+commit.String()+"#turn-1-iteration-3") {
		t.Fatalf("unexpected schema/anchor: %+v", facts)
	}
	if len(facts.Assistant.Excerpt) != maxTurnFactTextBytes {
		t.Fatalf("assistant bounds mismatch: %+v", facts.Assistant)
	}
	if len(facts.ToolOutcomes) != 1 || facts.ToolOutcomes[0].Outcome != "success" || facts.ToolOutcomes[0].ArgsDigest == "" {
		t.Fatalf("tool facts mismatch: %+v", facts.ToolOutcomes)
	}
	raw, _ := json.Marshal(facts)
	if strings.Contains(string(raw), "do-not-copy") {
		t.Fatal("raw tool arguments escaped the digest-only event projection")
	}
	if facts.TreeDiff == nil || len(facts.TreeDiff.ChangedPaths) != 1 || facts.TreeDiff.ChangedPaths[0] != "result.txt" || facts.TreeDiff.AfterDigest == "empty" {
		t.Fatalf("tree facts mismatch: %+v", facts.TreeDiff)
	}
	if len(refs) != 1 || !strings.Contains(refs[0], stadogit.TreeRef(session.ID).String()) {
		t.Fatalf("evidence refs = %v", refs)
	}
}

func TestSessionTurnCommittedV1CrossRepositoryFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session-turn-facts-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var facts SessionTurnCommittedV1
	if err := decoder.Decode(&facts); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("fixture has trailing JSON: %v", err)
	}
	wantTree := "0123456789abcdef0123456789abcdef01234567"
	if facts.Schema != SessionTurnCommittedSchemaV1 || facts.Anchor.SessionSequence != uint64(1)<<32|1 || facts.Anchor.TreeDigest != wantTree {
		t.Fatalf("fixture core contract = %+v", facts)
	}
	if facts.ProviderTokens.InputTokens != 120 || facts.ProviderTokens.OutputTokens != 30 || facts.ProviderTokens.CachedTokens != 40 || facts.ProviderTokens.BudgetTokens != 2000 {
		t.Fatalf("fixture token contract = %+v", facts.ProviderTokens)
	}
	if facts.Assistant.MessageRef == "" || facts.Assistant.Digest == "" {
		t.Fatalf("fixture assistant contract = %+v", facts.Assistant)
	}
}

func TestPublishSessionTurnCommittedUsesStableFactIdempotency(t *testing.T) {
	session := applicationEventSession(t)
	publisher := &recordingApplicationEventPublisher{}
	input := TurnCommitInput{Iteration: 0, Text: "candidate", Usage: agent.Usage{OutputTokens: 1}}
	for range 2 {
		if _, err := PublishSessionTurnCommitted(context.Background(), publisher, session, input); err != nil {
			t.Fatal(err)
		}
	}
	if len(publisher.events) != 2 {
		t.Fatalf("published %d events", len(publisher.events))
	}
	first, second := publisher.events[0], publisher.events[1]
	if first.ID != second.ID || first.IdempotencyKey != second.IdempotencyKey || first.IdempotencyKey == "" {
		t.Fatalf("unstable fact identity: %#v %#v", first, second)
	}
	if first.Kind != SessionTurnCommittedEvent || len(first.Data) == 0 {
		t.Fatalf("unexpected event: %#v", first)
	}
}

type heldThenActiveController struct {
	checks int
}

func (c *heldThenActiveController) CreateSubagent(context.Context, BrokerSubagentRequest) (BrokerController, error) {
	return nil, nil
}
func (c *heldThenActiveController) SetTaint(context.Context, ContextTaint) error { return nil }
func (c *heldThenActiveController) Sandbox() ExecutorSandbox                     { return ExecutorSandbox{} }
func (c *heldThenActiveController) Worktree() string                             { return "" }
func (c *heldThenActiveController) Close() error                                 { return nil }
func (c *heldThenActiveController) CheckSchedule(context.Context) (ScheduleStatus, error) {
	c.checks++
	if c.checks == 1 {
		return ScheduleStatus{State: ScheduleHeld, Until: time.Now().Add(-time.Millisecond)}, nil
	}
	return ScheduleStatus{State: ScheduleActive}, nil
}

func TestWaitForApplicationScheduleServicesExpiredHoldWithoutDispatch(t *testing.T) {
	controller := &heldThenActiveController{}
	if err := WaitForApplicationSchedule(context.Background(), controller, nil); err != nil {
		t.Fatal(err)
	}
	if controller.checks != 2 {
		t.Fatalf("schedule checks = %d, want 2", controller.checks)
	}
}
