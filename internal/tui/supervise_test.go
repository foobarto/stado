package tui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/supervise"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

func TestSuperviseRiskBoundaryIsHumanOnly(t *testing.T) {
	cases := []struct {
		call agent.ToolUseBlock
		want string
	}{
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"gh pr merge 42 --squash"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"gh -R owner/repo pr merge 42 --squash"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"gh pr --repo owner/repo merge 42 --squash"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git merge feature"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git -C repo push origin main"}`)}, "push"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git -c user.name=x merge feature"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh issue comment 42 --body shipped"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh --repo=owner/repo issue close 42"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh issue -Rowner/repo edit 42 --title fixed"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh -R owner/repo issue delete 42 --yes"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh --repo owner/repo repo delete owner/repo --yes"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh -R owner/repo pr review 42 --approve"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh release create v1.2.3"}`)}, "release"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh -R owner/repo issue view 42"}`)}, ""},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh pr -R owner/repo diff 42"}`)}, ""},
		{agent.ToolUseBlock{Name: "shell", Input: []byte(`{"command":"rm --recursive --force build"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "shell", Input: []byte(`{"command":"rm -r -f build"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"/bin/rm -R --force build"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"find build -type f -delete"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"bash -lc 'rm -r -f build'"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "github.pr.merge", Input: []byte(`{"number":42}`)}, "merge"},
		{agent.ToolUseBlock{Name: "fs.delete", Input: []byte(`{"path":"tmp.txt"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"kubectl apply -f deploy.yaml"}`)}, "deploy"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"kubectl --context prod apply -f deploy.yaml"}`)}, "deploy"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"kubectl --context prod delete deployment old"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"kubectl --context prod get deployments"}`)}, ""},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"terraform -chdir=infra apply"}`)}, "deploy"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"terraform -chdir infra destroy"}`)}, "destructive operation"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"terraform -chdir=infra plan"}`)}, ""},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"./deploy.sh production"}`)}, "deploy"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"curl -d '{\"state\":\"on\"}' https://api.example.test/state"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"curl --form file=@artifact https://api.example.test/upload"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"curl --upload-file artifact https://api.example.test/upload"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"curl -X GET https://api.example.test/state"}`)}, ""},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api repos/o/r/issues/1 -f state=closed"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api repos/o/r/issues/1 --raw-field state=closed"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api repos/o/r/rulesets --input payload.json"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api repos/o/r/issues/1 -X PATCH"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api -X GET search/issues -f q='repo:o/r is:open'"}`)}, ""},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api repos/o/r/issues/1"}`)}, ""},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api graphql -f query='query { viewer { login } }'"}`)}, ""},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh api graphql -f query='mutation { closeIssue(input: {}) { clientMutationId } }'"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "fs.read", Input: []byte(`{"path":"README.md"}`)}, ""},
		{agent.ToolUseBlock{Name: "fs.read", Input: []byte(`{"path":"docs/publishing-guide.md"}`)}, ""},
		{agent.ToolUseBlock{Name: "fs.write", Input: []byte(`{"path":"notes.txt","content":"publish this guide later"}`)}, ""},
		{agent.ToolUseBlock{Name: "fs.write", Input: []byte(`{"path":"notes.txt","content":"run git push after review"}`)}, ""},
		{agent.ToolUseBlock{Name: "github.issue.create", Input: []byte(`{"title":"announce commitment"}`)}, "external commitment"},
		{agent.ToolUseBlock{Name: "budget.update", Input: []byte(`{"tokens":100000}`)}, "permission or budget change"},
	}
	for _, tc := range cases {
		if got := superviseRiskBoundary(tc.call); got != tc.want {
			t.Fatalf("risk boundary for %s = %q, want %q", tc.call.Name, got, tc.want)
		}
	}
}

func TestSuperviseVerificationRejectionEventHasFailurePolarity(t *testing.T) {
	event := superviseVerificationEvent(false)
	if event.VerificationPassed == nil || *event.VerificationPassed {
		t.Fatalf("verification rejection event = %+v, want explicit false", event)
	}
}

func TestSuperviseVerificationExhaustionPausesWithoutRestartingWorker(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-exhaustion", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-exhaustion", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.AdvanceStep(ctx, st.ID, st.Version, supervise.RoleOperator, st.Anchor(), "test", "operator", "step")
	if err != nil {
		t.Fatal(err)
	}
	evidence := supervise.Evidence{Kind: "test", Summary: "verification attempted", Anchor: st.Anchor()}
	st, err = svc.RequestCompletion(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.CompletionRequest{
		Summary: "ready", Evidence: []supervise.Evidence{evidence}, Anchor: st.Anchor(),
	}, "test", "worker", "complete")
	if err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.verifyConfig = runtime.VerifyConfig{Commands: []string{"false"}, MaxRounds: 2}
	m.verifyEnabled, m.verifying, m.state = true, true, stateStreaming
	m.supervision = &superviseRuntime{
		service: svc, store: store, state: st, detector: supervise.RestoreDetector(st.Detector), gateActive: true,
	}
	model, cmd := onSuperviseGateResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{
		Status: runtime.VerifyFailed, Round: 2, Output: "still failing",
	}})
	got := model.(*Model)
	if cmd != nil {
		t.Fatal("verification exhaustion restarted worker or watchdog")
	}
	if got.state != stateIdle || got.supervision.state.Status != supervise.StatusPaused || got.supervision.state.ResumeStatus != supervise.StatusRunning {
		t.Fatalf("exhausted supervision state=%v durable=%+v", got.state, got.supervision.state)
	}
	if !strings.Contains(got.supervision.state.PauseReason, "exhausted") {
		t.Fatalf("pause reason = %q", got.supervision.state.PauseReason)
	}
}

func newSuperviseTaintTestRun(t *testing.T, root string) (*supervise.Service, supervise.State) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	st, err := svc.Create(context.Background(), root, supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: root, SessionSequence: 1, TreeDigest: "unavailable"}
	st, err = svc.ProposeBaseline(context.Background(), st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	return svc, st
}

func completeSupervisePlanForTaintTest(t *testing.T, svc *supervise.Service, st supervise.State) supervise.State {
	t.Helper()
	var err error
	st, err = svc.ApproveBaseline(context.Background(), st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.AdvanceStep(context.Background(), st.ID, st.Version, supervise.RoleOperator, st.Anchor(), "test", "operator", "step")
	if err != nil {
		t.Fatal(err)
	}
	evidence := supervise.Evidence{Kind: "test", Summary: "verification attempted", Anchor: st.Anchor()}
	st, err = svc.RequestCompletion(context.Background(), st.ID, st.Version, supervise.RoleWorker, supervise.CompletionRequest{
		Summary: "ready", Evidence: []supervise.Evidence{evidence}, Anchor: st.Anchor(),
	}, "test", "worker", "complete")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSuperviseTaintFailuresPauseDurablyAndRemainResumable(t *testing.T) {
	t.Run("baseline initialization", func(t *testing.T) {
		svc, st := newSuperviseTaintTestRun(t, "root-taint-baseline")
		m := scenarioModel(t)
		m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
		m.supervision = &superviseRuntime{service: svc, state: st, generation: 1}
		model, cmd := onSuperviseBaselineDecision(m, superviseBaselineDecisionMsg{generation: 1, approved: true})
		got := model.(*Model)
		if cmd != nil || got.state != stateIdle || got.supervision.state.Status != supervise.StatusPaused || got.supervision.state.ResumeStatus != supervise.StatusRunning {
			t.Fatalf("baseline taint failure state=%v cmd=%v durable=%+v", got.state, cmd != nil, got.supervision.state)
		}
		if !strings.Contains(got.supervision.state.PauseReason, "baseline initialization") {
			t.Fatalf("pause reason = %q", got.supervision.state.PauseReason)
		}
	})

	t.Run("deterministic gate rejection", func(t *testing.T) {
		svc, st := newSuperviseTaintTestRun(t, "root-taint-gate")
		st = completeSupervisePlanForTaintTest(t, svc, st)
		m := scenarioModel(t)
		m.session = nil
		m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
		m.verifyConfig = runtime.VerifyConfig{Commands: []string{"false"}, MaxRounds: 2}
		m.verifyEnabled, m.verifying, m.state = true, true, stateStreaming
		m.supervision = &superviseRuntime{service: svc, state: st, detector: supervise.RestoreDetector(st.Detector), gateActive: true}
		model, cmd := onSuperviseGateResult(m, verifyResultMsg{outcome: runtime.VerifyOutcome{Status: runtime.VerifyFailed, Round: 1, Output: "still failing"}})
		got := model.(*Model)
		if cmd != nil || got.state != stateIdle || got.supervision.state.Status != supervise.StatusPaused || !strings.Contains(got.supervision.state.PauseReason, "still failing") {
			t.Fatalf("gate taint failure state=%v cmd=%v durable=%+v", got.state, cmd != nil, got.supervision.state)
		}
		got.broker = nil
		if resume := got.resumeSupervision(); resume == nil {
			t.Fatal("durably paused gate failure was not resumable")
		}
		last := got.msgs[len(got.msgs)-1]
		if len(last.Content) == 0 || last.Content[0].Text == nil || !strings.Contains(last.Content[0].Text.Text, "still failing") {
			t.Fatalf("resume omitted durable pause reason: %+v", last)
		}
	})

	t.Run("independent verifier rejection", func(t *testing.T) {
		svc, st := newSuperviseTaintTestRun(t, "root-taint-verifier")
		st = completeSupervisePlanForTaintTest(t, svc, st)
		m := scenarioModel(t)
		m.session = nil
		m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
		m.supervision = &superviseRuntime{service: svc, state: st, verifierGeneration: 1}
		verdict := &supervise.Verdict{Kind: supervise.ReviewCompletion, Decision: supervise.VerdictReject, Anchor: st.Anchor(), Rationale: "missing release evidence"}
		model, cmd := onSuperviseVerifierResult(m, superviseVerifierResultMsg{generation: 1, result: supervise.ReviewResult{Verdict: verdict}})
		got := model.(*Model)
		if cmd != nil || got.state != stateIdle || got.supervision.state.Status != supervise.StatusPaused || !strings.Contains(got.supervision.state.PauseReason, "missing release evidence") {
			t.Fatalf("verifier taint failure state=%v cmd=%v durable=%+v", got.state, cmd != nil, got.supervision.state)
		}
	})

	t.Run("tool-result continuation", func(t *testing.T) {
		svc, st := newSuperviseTaintTestRun(t, "root-taint-tool-result")
		var err error
		st, err = svc.ApproveBaseline(context.Background(), st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
		if err != nil {
			t.Fatal(err)
		}
		m := scenarioModel(t)
		m.broker = failingTaintBroker{err: errors.New("broker unavailable")}
		m.state = stateStreaming
		m.supervision = &superviseRuntime{service: svc, state: st, detector: supervise.RestoreDetector(st.Detector)}
		model, cmd := onToolsExecuted(m, toolsExecutedMsg{results: []agent.ToolResultBlock{{ToolUseID: "tool-1", Content: "result"}}})
		got := model.(*Model)
		if cmd != nil || got.state != stateIdle || got.supervision.state.Status != supervise.StatusPaused || got.supervision.state.ResumeStatus != supervise.StatusRunning {
			t.Fatalf("tool-result taint failure state=%v cmd=%v durable=%+v", got.state, cmd != nil, got.supervision.state)
		}
		if !strings.Contains(got.supervision.state.PauseReason, "tool-result continuation") {
			t.Fatalf("pause reason = %q", got.supervision.state.PauseReason)
		}
	})
}

func TestSuperviseTriggersCoalesceWithoutDroppingEvidence(t *testing.T) {
	now := time.Now()
	first := &supervise.Trigger{ID: "first", CreatedAt: now, Signals: []supervise.TriggerSignal{{Type: supervise.TriggerRepeatedFailure, Severity: "warning", EvidenceRefs: []string{"event:1"}, Attributes: map[string]string{"tool": "bash"}}}}
	second := &supervise.Trigger{ID: "second", CreatedAt: now.Add(time.Second), Signals: []supervise.TriggerSignal{
		{Type: supervise.TriggerRepeatedFailure, Severity: "high", EvidenceRefs: []string{"event:1", "event:2"}, Attributes: map[string]string{"tool": "bash"}},
		{Type: supervise.TriggerScopeExpansion, Severity: "high", EvidenceRefs: []string{"event:3"}},
	}}
	got := coalesceSuperviseTriggers(first, second)
	if got == nil || len(got.Signals) != 2 || got.Signals[0].Severity != "high" {
		t.Fatalf("coalesced trigger = %+v", got)
	}
	if refs := got.Signals[0].EvidenceRefs; len(refs) != 2 || refs[0] != "event:1" || refs[1] != "event:2" {
		t.Fatalf("coalesced evidence refs = %v", refs)
	}
}

func TestSuperviseLiveRetryDelayIncreasesAndCaps(t *testing.T) {
	cfg := supervise.DefaultConfig()
	cfg.Mode = supervise.ModeLive
	cfg.LiveRetryBaseMillis = 100
	cfg.LiveRetryMaxMillis = 450
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 450 * time.Millisecond, 450 * time.Millisecond}
	for attempt, expected := range want {
		if got := superviseReviewRetryDelay(cfg, attempt); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", attempt, got, expected)
		}
	}
}

func TestSuperviseUnsupportedCompletionClaimMatcher(t *testing.T) {
	for _, claim := range []string{"Done.", "The implementation is complete and all criteria are met.", "All tests pass; ready for review."} {
		if !superviseClaimsCompletion(claim) {
			t.Fatalf("completion claim not detected: %q", claim)
		}
	}
	for _, progress := range []string{"The implementation is not complete.", "All tests pass, but remaining work includes documentation.", "I still need to run verification."} {
		if superviseClaimsCompletion(progress) {
			t.Fatalf("progress text misclassified as completion: %q", progress)
		}
	}
	if !superviseTurnUsesControl([]agent.ToolUseBlock{{Name: superviseCompletionTool}}, superviseCompletionTool) {
		t.Fatal("completion control call was not recognized")
	}
}

func TestSuperviseDeferredTaskIsBoundedAndMarkerDeduplicated(t *testing.T) {
	followup := supervise.Followup{ID: "followup-1", Text: strings.Repeat("ż", tasks.MaxBodyBytes)}
	state := supervise.State{ID: "run-1", Baseline: supervise.Baseline{Objective: strings.Repeat("objective ", 800)}}
	title, body := superviseDeferredTaskText(state, followup, "unrelated")
	if len(title) > tasks.MaxTitleBytes || len(body) > tasks.MaxBodyBytes || !strings.Contains(body, "[supervise-followup:followup-1]") {
		t.Fatalf("deferred task title=%d body=%d marker=%t", len(title), len(body), strings.Contains(body, "[supervise-followup:followup-1]"))
	}
	store := tasks.Store{Path: filepath.Join(t.TempDir(), "tasks.json")}
	created, err := store.Create(title, body, tasks.StatusOpen)
	if err != nil {
		t.Fatal(err)
	}
	got, err := existingSuperviseFollowupTask(store, "[supervise-followup:followup-1]")
	if err != nil || got.ID != created.ID {
		t.Fatalf("deduplicated task=%+v err=%v", got, err)
	}
}

func TestSuperviseDeferredTasksResumeOnlyMatchingOpenRunItems(t *testing.T) {
	store := tasks.Store{Path: filepath.Join(t.TempDir(), "tasks.json")}
	matching, err := store.Create("next matching item", "[supervise-followup:f1]\nDeferred by /supervise while run run-1 was focused on: ship", tasks.StatusOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("other run", "Deferred by /supervise while run run-2 was focused on: other", tasks.StatusOpen); err != nil {
		t.Fatal(err)
	}
	done, err := store.Create("already done", "Deferred by /supervise while run run-1 was focused on: ship", tasks.StatusOpen)
	if err != nil {
		t.Fatal(err)
	}
	doneStatus := tasks.StatusDone
	if _, err := store.Update(done.ID, tasks.Patch{Status: &doneStatus}); err != nil {
		t.Fatal(err)
	}
	got, err := superviseDeferredTasksForRun(store, "run-1")
	if err != nil || len(got) != 1 || got[0].ID != matching.ID {
		t.Fatalf("deferred tasks=%+v err=%v", got, err)
	}
	prompt := renderSuperviseDeferredContinuation("run-1", got)
	if !strings.Contains(prompt, matching.ID) || strings.Contains(prompt, done.ID) || !strings.Contains(prompt, "oldest still-open") {
		t.Fatalf("deferred continuation prompt = %q", prompt)
	}
}

func TestSuperviseFollowupAcceptanceCoversAuthorityPhases(t *testing.T) {
	for _, status := range []supervise.Status{supervise.StatusRunning, supervise.StatusPivotPending, supervise.StatusVerifying, supervise.StatusPaused} {
		if !superviseAcceptsFollowup(status) {
			t.Fatalf("status %s rejected durable follow-up", status)
		}
	}
	for _, status := range []supervise.Status{supervise.StatusSetup, supervise.StatusAwaitingApproval, supervise.StatusCompleted, supervise.StatusCancelled} {
		if superviseAcceptsFollowup(status) {
			t.Fatalf("status %s accepted follow-up", status)
		}
	}
}

func TestSuperviseSystemContextEnforcesSingleTaskAndAuthority(t *testing.T) {
	m := &Model{supervision: &superviseRuntime{state: supervise.State{ID: "sup", Status: supervise.StatusRunning, RootSessionID: "root", PlanVersion: 1, ActiveStep: "build", Config: supervise.DefaultConfig(), Baseline: testSuperviseBaseline()}}}
	got := m.superviseSystemContext()
	for _, want := range []string{"exactly one active step", "unrelated input is persisted", "human-only", "independent verifier", "nested supervision is unavailable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("context missing %q: %s", want, got)
		}
	}
}

func TestSupervisePendingAuthorityPhaseBlocksRemainingToolBatch(t *testing.T) {
	for _, status := range []supervise.Status{supervise.StatusPivotPending, supervise.StatusVerifying, supervise.StatusPaused} {
		t.Run(string(status), func(t *testing.T) {
			call := agent.ToolUseBlock{ID: "later-call", Name: "bash"}
			m := &Model{
				supervision:  &superviseRuntime{state: supervise.State{Status: status}},
				pendingCalls: []agent.ToolUseBlock{call},
				blocks:       []block{{kind: "tool", toolID: call.ID, toolName: call.Name, streaming: true}},
			}
			cmd := m.advanceToolQueue()
			if cmd == nil {
				t.Fatal("advanceToolQueue returned nil")
			}
			msg, ok := cmd().(toolsExecutedMsg)
			if !ok || len(msg.results) != 1 || !msg.results[0].IsError || !strings.Contains(msg.results[0].Content, string(status)) {
				t.Fatalf("blocked result = %#v", msg)
			}
			if len(m.pendingCalls) != 0 || m.blocks[0].streaming || !strings.Contains(m.blocks[0].toolResult, "host boundary") {
				t.Fatalf("blocked model state: calls=%d block=%+v", len(m.pendingCalls), m.blocks[0])
			}
		})
	}
}

func TestSuperviseTerminalRunAllowsOrdinaryToolQueue(t *testing.T) {
	for _, status := range []supervise.Status{supervise.StatusCompleted, supervise.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			call := agent.ToolUseBlock{ID: "ordinary-call", Name: "read"}
			m := &Model{
				supervision:  &superviseRuntime{state: supervise.State{Status: status}},
				pendingCalls: []agent.ToolUseBlock{call},
				turnAllowed:  map[string]struct{}{call.Name: {}},
				blocks:       []block{{kind: "tool", toolID: call.ID, toolName: call.Name, streaming: true}},
			}
			cmd := m.advanceToolQueue()
			if cmd == nil {
				t.Fatal("advanceToolQueue returned nil")
			}
			msg, ok := cmd().(toolResultMsg)
			if !ok || !strings.Contains(msg.result.Content, "tool execution unavailable") {
				t.Fatalf("terminal run did not reach ordinary execution path: %#v", msg)
			}
			if strings.Contains(m.blocks[0].toolResult, "host boundary") {
				t.Fatalf("terminal run blocked ordinary tool: %+v", m.blocks[0])
			}
		})
	}
}

func TestSuperviseControlsRemainAvailableInPlanMode(t *testing.T) {
	m := &Model{
		mode:        modePlan,
		executor:    &tools.Executor{Registry: tools.NewRegistry()},
		supervision: &superviseRuntime{state: supervise.State{Status: supervise.StatusRunning}},
	}
	m.registerSuperviseControlTools()
	got := map[string]bool{}
	for _, tl := range m.toolSurfaceForTurn() {
		got[tl.Name()] = true
	}
	for _, name := range []string{superviseProgressTool, supervisePivotTool, superviseCompletionTool} {
		if !got[name] {
			t.Errorf("Plan mode omitted required supervision control %q", name)
		}
	}
}

func TestSuperviseEvidenceContentIsByteBoundedAndUTF8Safe(t *testing.T) {
	got := boundedSuperviseEvidenceContent(strings.Repeat("ż", 100), 71)
	if len(got) > 71 || !utf8.ValidString(got) || !strings.HasSuffix(got, "[evidence truncated by host boundary]") {
		t.Fatalf("bounded evidence bytes=%d valid=%t content=%q", len(got), utf8.ValidString(got), got)
	}
}

func TestSuperviseEvidenceSummaryIsPageSafeAndUTF8Safe(t *testing.T) {
	got := boundedSuperviseEvidenceSummary(strings.Repeat("ż-path/", 1_000))
	if len(got) > 4096 || !utf8.ValidString(got) || !strings.HasSuffix(got, "[summary truncated by host boundary]") {
		t.Fatalf("bounded summary bytes=%d valid=%t suffix=%q", len(got), utf8.ValidString(got), got[max(0, len(got)-64):])
	}
}

func TestSuperviseRecordedEvidenceSummaryIsPageSafe(t *testing.T) {
	m := scenarioModel(t)
	m.session = nil
	snapshot := m.superviseEvidenceSnapshot(supervise.State{
		RootSessionID: "root-recorded-summary", TreeDigest: "unavailable",
		Evidence: []supervise.Evidence{{Kind: "test", Summary: strings.Repeat("ż", 2_048)}},
	})
	items := snapshot.bySection[supervise.EvidenceState]
	if len(items) != 2 || len(items[1].Summary) > 4096 || !utf8.ValidString(items[1].Summary) {
		t.Fatalf("recorded evidence summary is not page-safe: %+v", items)
	}
}

func TestSuperviseEvidenceStateRedactsQueuedFollowupText(t *testing.T) {
	m := scenarioModel(t)
	m.session = nil
	st := supervise.State{
		RootSessionID: "root-followup-evidence", SessionSequence: 1, TreeDigest: "unavailable",
		PendingFollowups: []supervise.Followup{{ID: "f1", Text: "secret unrelated operator request"}},
	}
	snapshot := m.superviseEvidenceSnapshot(st)
	page, err := snapshot.Read(context.Background(), supervise.EvidenceQuery{Section: supervise.EvidenceState})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || strings.Contains(page.Items[0].Content, "secret unrelated operator request") || !strings.Contains(page.Items[0].Content, `"pending_followup_count":1`) {
		t.Fatalf("state evidence leaked or lost queue metadata: %+v", page.Items)
	}
}

func TestSuperviseChildEvidenceIncludesOnlyOwnedSessionTree(t *testing.T) {
	st := supervise.State{RootSessionID: "root-owned", AttachedSessionID: "root-recovery"}
	fleet, recent := superviseOwnedChildEvidence(st, []runtime.FleetEntry{
		{FleetID: "owned", ParentSessionID: "root-owned", SessionID: "child-owned"},
		{FleetID: "owned-descendant", ParentSessionID: "child-owned", SessionID: "grandchild-owned"},
		{FleetID: "recovery", ParentSessionID: "root-recovery", SessionID: "child-recovery"},
		{FleetID: "foreign", ParentSessionID: "other-root", SessionID: "foreign-child"},
		{FleetID: "unknown", SessionID: "unattributed-child"},
	}, []subagentActivity{
		{ParentSession: "grandchild-owned", ChildSession: "recent-descendant"},
		{ParentSession: "foreign-child", ChildSession: "foreign-recent"},
	})
	if len(fleet) != 3 || fleet[0].FleetID != "owned" || fleet[1].FleetID != "owned-descendant" || fleet[2].FleetID != "recovery" {
		t.Fatalf("owned fleet projection = %+v", fleet)
	}
	if len(recent) != 1 || recent[0].ChildSession != "recent-descendant" {
		t.Fatalf("owned recent projection = %+v", recent)
	}
}

func TestSuperviseEvidenceSnapshotLazilyPagesImmutableBlocks(t *testing.T) {
	m := scenarioModel(t)
	m.session = nil
	m.blocks = []block{
		{kind: "assistant", body: "\x1b[31mfirst answer\x1b[0m"},
		{kind: "thinking", body: "hidden reasoning"},
		{kind: "tool", toolName: "read", toolArgs: `{"path":"README.md"}`, toolResult: "tool result"},
		{kind: "assistant", body: strings.Repeat("later ", 20_000)},
	}
	snapshot := m.superviseEvidenceSnapshot(supervise.State{RootSessionID: "root-evidence-lazy", SessionSequence: 1, TreeDigest: "unavailable"})
	if len(snapshot.bySection[supervise.EvidenceTranscript]) != 0 || len(snapshot.bySection[supervise.EvidenceTools]) != 0 || len(snapshot.blocks) != 3 {
		t.Fatalf("snapshot eagerly materialized blocks: transcript=%d tools=%d raw=%d", len(snapshot.bySection[supervise.EvidenceTranscript]), len(snapshot.bySection[supervise.EvidenceTools]), len(snapshot.blocks))
	}
	if !strings.Contains(snapshot.blocks[0].body, "\x1b[31m") {
		t.Fatal("snapshot eagerly sanitized transcript content")
	}
	m.blocks[0].body = "mutated after snapshot"
	page, err := snapshot.Read(context.Background(), supervise.EvidenceQuery{Section: supervise.EvidenceTranscript, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != "1" || strings.Contains(page.Items[0].Content, "\x1b") || !strings.Contains(page.Items[0].Content, "first answer") {
		t.Fatalf("first lazy transcript page = %+v", page)
	}
	toolPage, err := snapshot.Search(context.Background(), supervise.EvidenceQuery{Section: supervise.EvidenceTools, Pattern: "tool result", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(toolPage.Items) != 1 || toolPage.Items[0].Summary != "read" || !strings.Contains(toolPage.Items[0].Content, "tool result") {
		t.Fatalf("lazy tool search page = %+v", toolPage)
	}
}

func testSuperviseBaseline() supervise.Baseline {
	return supervise.Baseline{Objective: "ship", AcceptanceCriteria: []string{"works"}, Plan: []supervise.Step{{ID: "build", Title: "build", DoneWhen: "done"}}, DefinitionOfDone: []string{"docs"}, Verification: []string{"tests"}}
}

func TestSuperviseWizardStopsExistingLoopBeforeApproval(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.loop = &loopState{prompt: "poll deployment", interval: time.Minute}
	m.openSuperviseWizard("ship safely")
	if m.loop != nil {
		t.Fatal("supervision wizard left the existing loop active")
	}
	if m.supervisePick == nil || !m.supervisePick.Visible {
		t.Fatal("supervision wizard did not open")
	}
	if cmd := m.loopIterate(); cmd != nil {
		t.Fatal("a queued loop tick started work after supervision took ownership")
	}
}

func TestLoopCannotOwnTurnsDuringNonTerminalSupervision(t *testing.T) {
	for _, status := range []supervise.Status{
		supervise.StatusSetup,
		supervise.StatusAwaitingApproval,
		supervise.StatusRunning,
		supervise.StatusPivotPending,
		supervise.StatusVerifying,
		supervise.StatusPaused,
	} {
		t.Run(string(status), func(t *testing.T) {
			m := scenarioModel(t)
			m.state = stateIdle
			m.supervision = &superviseRuntime{state: supervise.State{Status: status}}
			if cmd := m.handleLoopCmd("poll deployment"); cmd != nil {
				t.Fatal("starting /loop under supervision returned a worker command")
			}
			if m.loop != nil {
				t.Fatal("/loop started while non-terminal supervision was attached")
			}

			m.loop = &loopState{prompt: "stale queued iteration"}
			if cmd := m.loopIterate(); cmd != nil {
				t.Fatal("stale loop iteration returned a worker command")
			}
			if m.loop != nil {
				t.Fatal("stale loop iteration survived supervised ownership")
			}
		})
	}
}

func TestSuperviseEventVerdictResyncsAfterAsyncEvidenceWrite(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-evidence-cas", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-evidence-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	staleRuntimeState := st
	durable, err := svc.RecordEvidence(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.Evidence{
		Kind: "test", Summary: "worker evidence arrived during review", Anchor: st.Anchor(),
	}, "test", "worker", "evidence")
	if err != nil {
		t.Fatal(err)
	}

	trigger := &supervise.Trigger{ID: "trigger-evidence-cas", Anchor: st.Anchor(), Signals: []supervise.TriggerSignal{{
		Type: supervise.TriggerNoProgress, Severity: "warning", EvidenceRefs: []string{"worker-event:1"},
	}}}
	verdict := &supervise.Verdict{Kind: supervise.ReviewEvent, Decision: supervise.VerdictContinue, Anchor: st.Anchor(), Rationale: "continue with the new evidence"}
	m := scenarioModel(t)
	m.rootCtx = ctx
	m.supervision = &superviseRuntime{
		service: svc, store: store, state: staleRuntimeState, reviewGeneration: 1, reviewing: true,
	}
	model, cmd := onSuperviseEventReview(m, superviseEventReviewMsg{
		generation: 1, trigger: trigger, result: supervise.ReviewResult{Verdict: verdict},
	})
	if cmd != nil {
		t.Fatal("resynchronized verdict unexpectedly scheduled another host action")
	}
	got := model.(*Model)
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastVerdict == nil || current.LastVerdict.Decision != supervise.VerdictContinue {
		t.Fatalf("verdict was dropped after evidence version %d: %+v", durable.Version, current.LastVerdict)
	}
	if got.supervision.state.Version != current.Version || current.Version != durable.Version+1 {
		t.Fatalf("runtime version=%d durable version=%d evidence version=%d", got.supervision.state.Version, current.Version, durable.Version)
	}
}

func TestStaleSuperviseVerdictsUseAsymmetricPolicy(t *testing.T) {
	newRun := func(t *testing.T, root string) (*supervise.Service, supervise.State, supervise.Anchor, *supervise.Trigger) {
		t.Helper()
		svc, st := newSuperviseTaintTestRun(t, root)
		var err error
		st, err = svc.ApproveBaseline(context.Background(), st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
		if err != nil {
			t.Fatal(err)
		}
		old := st.Anchor()
		current := old
		current.SessionSequence++
		current.TreeDigest = "tree-current"
		st, err = svc.UpdateRuntimeAnchor(context.Background(), st.ID, st.Version, supervise.RoleHost, current, "test", "host", "advance")
		if err != nil {
			t.Fatal(err)
		}
		trigger := &supervise.Trigger{ID: "trigger-old", Anchor: old, Signals: []supervise.TriggerSignal{{
			Type: supervise.TriggerNoProgress, Severity: "high", EvidenceRefs: []string{"worker-event:old"},
		}}}
		return svc, st, old, trigger
	}

	t.Run("positive is discarded", func(t *testing.T) {
		svc, st, old, trigger := newRun(t, "root-stale-positive")
		m := scenarioModel(t)
		m.state = stateIdle
		m.supervision = &superviseRuntime{service: svc, state: st, reviewing: true, reviewGeneration: 1}
		_, cmd := onSuperviseEventReview(m, superviseEventReviewMsg{generation: 1, trigger: trigger, result: supervise.ReviewResult{Verdict: &supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictContinue, Anchor: old, Rationale: "the old state looked fine",
		}}})
		if cmd != nil {
			t.Fatal("stale positive scheduled more work")
		}
		if m.supervision.pendingTrigger != nil || m.supervision.interventionHold || m.supervision.advisorySteering != "" {
			t.Fatalf("stale positive changed runtime policy state: %+v", m.supervision)
		}
		current, err := svc.State(st.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.LastVerdict != nil || current.PendingIntervention != nil {
			t.Fatalf("stale positive acquired durable meaning: %+v", current)
		}
	})

	t.Run("correction becomes advisory steering", func(t *testing.T) {
		svc, st, old, trigger := newRun(t, "root-stale-correction")
		m := scenarioModel(t)
		m.state = stateStreaming
		m.supervision = &superviseRuntime{service: svc, state: st, reviewing: true, reviewGeneration: 1}
		_, cmd := onSuperviseEventReview(m, superviseEventReviewMsg{generation: 1, trigger: trigger, result: supervise.ReviewResult{Verdict: &supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictCorrect, Anchor: old,
			Rationale: "the previous tactic was weak", Correction: "inspect the repository guidance before retrying", EvidenceRefs: []string{"worker-event:old"},
		}}})
		if cmd != nil {
			t.Fatal("streaming stale correction scheduled an immediate worker or review command")
		}
		if m.supervision.interventionHold || m.supervision.correctionPending || !strings.Contains(m.supervision.advisorySteering, "earlier worker anchor") {
			t.Fatalf("stale correction policy state: %+v", m.supervision)
		}
		if !m.drainSuperviseAdvisorySteering() || len(m.msgs) == 0 || !strings.Contains(m.msgs[len(m.msgs)-1].Content[0].Text.Text, "inspect the repository guidance") {
			t.Fatal("stale correction was not delivered as bounded worker steering")
		}
		current, err := svc.State(st.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.LastVerdict != nil || current.PendingIntervention != nil || current.Status != supervise.StatusRunning {
			t.Fatalf("stale correction changed durable authority: %+v", current)
		}
	})

	t.Run("pause holds worker for a fresh verdict", func(t *testing.T) {
		svc, st, old, trigger := newRun(t, "root-stale-pause")
		m := scenarioModel(t)
		m.state = stateIdle
		m.supervision = &superviseRuntime{service: svc, state: st, reviewing: true, reviewGeneration: 1}
		_, reviewCmd := onSuperviseEventReview(m, superviseEventReviewMsg{generation: 1, trigger: trigger, result: supervise.ReviewResult{Verdict: &supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictPause, Anchor: old,
			Rationale: "the old trajectory should pause", EvidenceRefs: []string{"worker-event:old"},
		}}})
		if reviewCmd == nil || !m.supervision.interventionHold || !m.supervision.reviewing {
			t.Fatalf("stale pause did not start a held fresh review: %+v", m.supervision)
		}
		held, err := svc.State(st.ID)
		if err != nil {
			t.Fatal(err)
		}
		if held.Status != supervise.StatusRunning || held.PendingIntervention == nil || held.LastVerdict != nil {
			t.Fatalf("stale pause was applied instead of held: %+v", held)
		}

		freshTrigger := superviseInterventionReviewTrigger(held)
		_, _ = onSuperviseEventReview(m, superviseEventReviewMsg{generation: m.supervision.reviewGeneration, trigger: freshTrigger, result: supervise.ReviewResult{Verdict: &supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictContinue, Anchor: held.Anchor(), Rationale: "current evidence does not support pausing",
		}}})
		current, err := svc.State(st.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.PendingIntervention != nil || current.Status != supervise.StatusRunning || m.supervision.interventionHold {
			t.Fatalf("fresh positive did not release held worker: runtime=%+v durable=%+v", m.supervision, current)
		}
		if len(m.msgs) == 0 || !strings.Contains(m.msgs[len(m.msgs)-1].Content[0].Text.Text, "did not confirm") {
			t.Fatal("released worker did not receive fresh-review context")
		}
	})

	t.Run("durable hold takes priority over operator follow-up classification", func(t *testing.T) {
		svc, st, old, trigger := newRun(t, "root-held-followup")
		var err error
		st, err = svc.HoldStaleIntervention(context.Background(), st.ID, st.Version, supervise.RoleHost, supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictStop, Anchor: old,
			Rationale: "the old trajectory should stop", EvidenceRefs: []string{"worker-event:old"},
		}, trigger, "test", "host", "hold-before-followup")
		if err != nil {
			t.Fatal(err)
		}
		m := scenarioModel(t)
		m.state = stateIdle
		m.supervision = &superviseRuntime{
			service: svc, state: st, interventionHold: true,
			pendingTrigger: superviseInterventionReviewTrigger(st),
		}
		cmd := m.enqueueSuperviseFollowup("also check the operator note")
		if cmd == nil || !m.supervision.reviewing || m.supervision.followupReviewing {
			t.Fatalf("held follow-up bypassed intervention review: %+v", m.supervision)
		}
		if len(m.supervision.followupQueue) != 1 {
			t.Fatalf("operator follow-up was not retained behind hold: %+v", m.supervision.followupQueue)
		}
	})

	t.Run("pause closes an in-flight tool approval before fresh review", func(t *testing.T) {
		svc, st, old, trigger := newRun(t, "root-stale-pause-approval")
		response := make(chan bool, 1)
		m := scenarioModel(t)
		m.state = stateApproval
		m.approval = &approvalRequest{response: response}
		m.supervision = &superviseRuntime{service: svc, state: st, reviewing: true, reviewGeneration: 1}
		_, cmd := onSuperviseEventReview(m, superviseEventReviewMsg{generation: 1, trigger: trigger, result: supervise.ReviewResult{Verdict: &supervise.Verdict{
			Kind: supervise.ReviewEvent, Decision: supervise.VerdictPause, Anchor: old,
			Rationale: "pause before approving more effects", EvidenceRefs: []string{"worker-event:old"},
		}}})
		if cmd != nil || m.approval != nil || m.state != stateStreaming || !m.supervision.interventionHold || m.supervision.reviewing {
			t.Fatalf("approval boundary was not closed before held review: state=%v runtime=%+v approval=%+v", m.state, m.supervision, m.approval)
		}
		select {
		case allowed := <-response:
			if allowed {
				t.Fatal("stale pause allowed an in-flight tool approval")
			}
		default:
			t.Fatal("tool approval did not receive denial")
		}
		m.state = stateIdle // emulates the cancelled tool result reaching its boundary
		if fresh := m.nextSuperviseHostAction(); fresh == nil || !m.supervision.reviewing {
			t.Fatal("fresh intervention review did not start after tool cleanup")
		}
	})
}

func TestSuperviseInterventionHoldRejectsQueuedTools(t *testing.T) {
	m := scenarioModel(t)
	m.supervision = &superviseRuntime{
		state:            supervise.State{Status: supervise.StatusRunning, PendingIntervention: &supervise.InterventionHold{}},
		interventionHold: true,
	}
	m.pendingCalls = []agent.ToolUseBlock{{ID: "one", Name: "bash"}, {ID: "two", Name: "fs.write"}}
	cmd := m.advanceToolQueue()
	if cmd == nil {
		t.Fatal("held tool queue did not emit blocked results")
	}
	msg, ok := cmd().(toolsExecutedMsg)
	if !ok || len(msg.results) != 2 {
		t.Fatalf("held tool results = %#v", msg)
	}
	for _, result := range msg.results {
		if !result.IsError || !strings.Contains(result.Content, "fresh watchdog review") {
			t.Fatalf("held tool result = %+v", result)
		}
	}
}

func TestSuperviseInterventionHoldGuardsDirectStreamStart(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.supervision = &superviseRuntime{
		state: supervise.State{Status: supervise.StatusRunning, PendingIntervention: &supervise.InterventionHold{ID: "intervention-test"}},
	}
	if cmd := m.startStream(); cmd != nil {
		t.Fatal("direct stream start bypassed intervention hold")
	}
	if m.state != stateIdle || !m.supervision.interventionHold {
		t.Fatalf("stream guard state=%v runtime=%+v", m.state, m.supervision)
	}
	toolCmd := m.executeCallAsync(agent.ToolUseBlock{ID: "direct-tool", Name: "bash"})
	toolMsg, ok := toolCmd().(toolResultMsg)
	if !ok || !toolMsg.result.IsError || toolMsg.result.ToolUseID != "direct-tool" || !strings.Contains(toolMsg.result.Content, "fresh watchdog review") {
		t.Fatalf("direct tool guard result = %#v", toolMsg)
	}
}

func TestRetrySuperviseMutationReloadsAfterConcurrentFollowup(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-control-cas", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-control-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	got, err := retrySuperviseMutation(
		func() (supervise.State, error) { return svc.State(st.ID) },
		func(current supervise.State) (supervise.State, error) {
			attempts++
			if attempts == 1 {
				if _, _, enqueueErr := svc.EnqueueFollowup(ctx, current.ID, current.Version, supervise.RoleOperator, "concurrent operator note", "test", "operator", "followup"); enqueueErr != nil {
					t.Fatal(enqueueErr)
				}
			}
			return svc.RecordEvidence(ctx, current.ID, current.Version, supervise.RoleWorker, supervise.Evidence{
				Kind: "test", Summary: "worker progress", Anchor: current.Anchor(),
			}, "test", "worker", "evidence:"+strconvVersion(current.Version))
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(got.PendingFollowups) != 1 || len(got.Evidence) != 1 {
		t.Fatalf("attempts=%d state=%+v", attempts, got)
	}
}

func TestObserveSupervisePreservesVersionOnlyConcurrentMutation(t *testing.T) {
	svc, st := newSuperviseTaintTestRun(t, "root-observe-version")
	var err error
	st, err = svc.ApproveBaseline(context.Background(), st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	concurrent, _, err := svc.EnqueueFollowup(context.Background(), st.ID, st.Version, supervise.RoleOperator, "concurrent note", "test", "operator", "followup")
	if err != nil {
		t.Fatal(err)
	}
	m := scenarioModel(t)
	m.session = nil
	m.supervision = &superviseRuntime{service: svc, state: st, sequence: st.SessionSequence, detector: supervise.RestoreDetector(st.Detector)}
	if cmd := m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerToolOutcome, Tool: "read", Succeeded: true}); cmd != nil {
		t.Fatal("ordinary event unexpectedly started a review")
	}
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != supervise.StatusRunning || current.Version != concurrent.Version+2 || current.SessionSequence != st.SessionSequence+1 || len(current.PendingFollowups) != 1 || len(current.Detector.History) != 1 {
		t.Fatalf("observation lost concurrent state or detector history: stale=%+v concurrent=%+v current=%+v", st, concurrent, current)
	}
}

func TestObserveSuperviseChangedAnchorFailsClosed(t *testing.T) {
	svc, st := newSuperviseTaintTestRun(t, "root-observe-stale")
	var err error
	st, err = svc.ApproveBaseline(context.Background(), st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	advancedAnchor := st.Anchor()
	advancedAnchor.SessionSequence++
	advanced, err := svc.UpdateRuntimeAnchor(context.Background(), st.ID, st.Version, supervise.RoleHost, advancedAnchor, "test", "host", "concurrent-anchor")
	if err != nil {
		t.Fatal(err)
	}
	m := scenarioModel(t)
	m.state = stateStreaming
	m.pendingCalls = []agent.ToolUseBlock{{ID: "queued-after-lost-observation", Name: "read"}}
	m.supervision = &superviseRuntime{service: svc, state: st, sequence: st.SessionSequence, detector: supervise.RestoreDetector(st.Detector)}
	if cmd := m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerToolOutcome, Tool: "read", Succeeded: true}); cmd != nil {
		t.Fatal("failed observation scheduled more work")
	}
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != supervise.StatusPaused || current.ResumeStatus != supervise.StatusRunning || current.Version != advanced.Version+1 || len(current.Detector.History) != 0 {
		t.Fatalf("changed-anchor observation did not pause durably: advanced=%+v current=%+v", advanced, current)
	}
	if m.state != stateIdle || len(m.pendingCalls) != 0 || !m.turnCancelled || !strings.Contains(current.PauseReason, "runtime anchor") {
		t.Fatalf("in-memory dispatch was not stopped: state=%v pending=%d cancelled=%t reason=%q", m.state, len(m.pendingCalls), m.turnCancelled, current.PauseReason)
	}
}

func TestSuperviseCorrectionResultReloadsAfterConcurrentMutation(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-correction-cas", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-correction-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	concurrent, _, err := svc.EnqueueFollowup(ctx, st.ID, st.Version, supervise.RoleOperator, "concurrent operator note", "test", "operator", "followup")
	if err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.supervision = &superviseRuntime{service: svc, store: store, state: st, correctionPending: true}
	if err := m.recordSuperviseCorrectionResult(false, st.Anchor()); err != nil {
		t.Fatal(err)
	}
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != concurrent.Version+1 || current.FailedCorrectionCount != 1 || len(current.PendingFollowups) != 1 {
		t.Fatalf("correction result lost concurrent mutation: stale=%d concurrent=%+v current=%+v", st.Version, concurrent, current)
	}
	if m.supervision.state.Version != current.Version {
		t.Fatalf("runtime version=%d durable=%d", m.supervision.state.Version, current.Version)
	}
	stale := current.Anchor()
	stale.TreeDigest = "tree-stale"
	if err := m.recordSuperviseCorrectionResult(true, stale); !errors.Is(err, supervise.ErrStaleVerdict) {
		t.Fatalf("changed-anchor correction result error = %v, want stale verdict", err)
	}
	unchanged, err := svc.State(st.ID)
	if err != nil || unchanged.Version != current.Version {
		t.Fatalf("stale correction mutated durable state: before=%d after=%+v err=%v", current.Version, unchanged, err)
	}
}

func TestSuperviseStepAdvanceReloadsAfterConcurrentEvidence(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-step-cas", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-step-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	trigger := &supervise.Trigger{ID: "step-trigger", Anchor: st.Anchor(), Signals: []supervise.TriggerSignal{{
		Type: supervise.TriggerStepCompletion, Severity: "info", EvidenceRefs: []string{"worker-event:step"}, Attributes: map[string]string{"step": st.ActiveStep},
	}}}
	verdict := supervise.Verdict{Kind: supervise.ReviewEvent, Decision: supervise.VerdictApprove, Anchor: st.Anchor(), Rationale: "step evidence is sufficient", EvidenceRefs: []string{"worker-event:step"}}
	st, err = svc.RecordEventVerdict(ctx, st.ID, st.Version, supervise.RoleWatchdog, verdict, trigger, "test", "watchdog", "verdict")
	if err != nil {
		t.Fatal(err)
	}
	concurrent, err := svc.RecordEvidence(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.Evidence{Kind: "test", Summary: "concurrent worker evidence", Anchor: st.Anchor()}, "test", "worker", "evidence")
	if err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.supervision = &superviseRuntime{service: svc, store: store, state: st}
	advanced, err := m.advanceSuperviseApprovedStep()
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Version != concurrent.Version+1 || len(advanced.Evidence) != 1 || len(advanced.CompletedSteps) != 1 || advanced.ActiveStep != "" {
		t.Fatalf("step advance lost concurrent evidence: stale=%d concurrent=%+v advanced=%+v", st.Version, concurrent, advanced)
	}
}

func TestSuperviseReviewFailureReloadsAfterConcurrentControlMutation(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	cfg := supervise.DefaultConfig()
	cfg.WatchdogRequired = true
	st, err := svc.Create(ctx, "root-review-failure-cas", cfg, "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-review-failure-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	durable, _, err := svc.EnqueueFollowup(ctx, st.ID, st.Version, supervise.RoleOperator, "concurrent operator note", "test", "operator", "followup")
	if err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.supervision = &superviseRuntime{service: svc, store: store, state: st, reviewing: true, reviewGeneration: 1}
	model, _ := onSuperviseEventReview(m, superviseEventReviewMsg{generation: 1, err: errors.New("watchdog unavailable")})
	got := model.(*Model)
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != supervise.StatusPaused || current.FailedEventStreak != 1 || len(current.PendingFollowups) != 1 {
		t.Fatalf("review failure was not recorded after version %d: %+v", durable.Version, current)
	}
	if got.supervision.state.Version != current.Version || got.supervision.state.Status != supervise.StatusPaused {
		t.Fatalf("runtime state=%+v durable=%+v", got.supervision.state, current)
	}
}

func TestEnqueueSuperviseFollowupReloadsStaleRuntimeState(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-followup-cas", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-followup-cas", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	durable, err := svc.RecordEvidence(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.Evidence{
		Kind: "test", Summary: "concurrent worker evidence", Anchor: st.Anchor(),
	}, "test", "worker", "evidence")
	if err != nil {
		t.Fatal(err)
	}

	m := scenarioModel(t)
	m.state = stateStreaming
	m.supervision = &superviseRuntime{service: svc, store: store, state: st}
	if cmd := m.enqueueSuperviseFollowup("remember this operator request"); cmd != nil {
		t.Fatal("streaming follow-up unexpectedly scheduled immediate review")
	}
	current, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.PendingFollowups) != 1 || current.PendingFollowups[0].Text != "remember this operator request" {
		t.Fatalf("durable follow-ups = %+v", current.PendingFollowups)
	}
	if current.Version != durable.Version+1 || m.supervision.state.Version != current.Version || len(m.supervision.followupQueue) != 1 {
		t.Fatalf("runtime=%+v durable=%+v queue=%+v", m.supervision.state, current, m.supervision.followupQueue)
	}
}

func TestRegisterSuperviseControlToolsCapturesImmutableRunIdentity(t *testing.T) {
	m := scenarioModel(t)
	m.executor = &tools.Executor{Registry: tools.NewRegistry()}
	svc, storeErr := wal.Open(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	t.Cleanup(func() { _ = svc.Close() })
	service := supervise.New(svc)
	st, err := service.Create(context.Background(), "root-tool-capture", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	m.supervision = &superviseRuntime{service: service, store: svc, state: st}
	m.registerSuperviseControlTools()
	registered, ok := m.executor.Registry.Get(superviseProgressTool)
	if !ok {
		t.Fatal("supervision progress tool was not registered")
	}
	control, ok := registered.(*superviseControlTool)
	if !ok {
		t.Fatalf("registered tool type = %T", registered)
	}
	m.supervision.state.ID = "mutated-runtime-id"
	if control.service != service || control.runID != st.ID || control.anchor != st.Anchor() {
		t.Fatalf("captured control tool identity = service:%p run:%q anchor:%+v, want service:%p run:%q anchor:%+v", control.service, control.runID, control.anchor, service, st.ID, st.Anchor())
	}
}

func TestSuperviseControlToolRetriesVersionOnlyMutationButRejectsNewAnchor(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := supervise.New(store)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-control-anchor", supervise.DefaultConfig(), "test", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := supervise.Anchor{RootSessionID: "root-control-anchor", SessionSequence: 1, TreeDigest: "tree-a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testSuperviseBaseline(), supervise.RoleWatchdog, anchor, "test", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, supervise.RoleOperator, "test", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}

	control := &superviseControlTool{name: superviseProgressTool, service: svc, runID: st.ID, anchor: st.Anchor()}
	concurrent, _, err := svc.EnqueueFollowup(ctx, st.ID, st.Version, supervise.RoleOperator, "version-only operator note", "test", "operator", "followup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Run(ctx, json.RawMessage(`{"kind":"test","summary":"anchored progress"}`), nil); err != nil {
		t.Fatalf("version-only retry failed: %v", err)
	}
	current, err := svc.State(st.ID)
	if err != nil || current.Version != concurrent.Version+1 || len(current.Evidence) != 1 || len(current.PendingFollowups) != 1 {
		t.Fatalf("version-only mutation was not preserved: state=%+v err=%v", current, err)
	}

	advanced := current.Anchor()
	advanced.SessionSequence++
	current, err = svc.UpdateRuntimeAnchor(ctx, current.ID, current.Version, supervise.RoleHost, advanced, "test", "host", "advance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Run(ctx, json.RawMessage(`{"kind":"test","summary":"must not move forward"}`), nil); !errors.Is(err, supervise.ErrStaleVerdict) {
		t.Fatalf("changed-anchor control error = %v, want stale verdict", err)
	}
	after, err := svc.State(st.ID)
	if err != nil || after.Version != current.Version || len(after.Evidence) != 1 {
		t.Fatalf("stale control mutated current state: before=%+v after=%+v err=%v", current, after, err)
	}
}
