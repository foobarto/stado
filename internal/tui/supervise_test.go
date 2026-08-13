package tui

import (
	"context"
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
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git merge feature"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git -C repo push origin main"}`)}, "push"},
		{agent.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"git -c user.name=x merge feature"}`)}, "merge"},
		{agent.ToolUseBlock{Name: "exec_command", Input: []byte(`{"cmd":"gh issue comment 42 --body shipped"}`)}, "external commitment"},
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
	for _, status := range []supervise.Status{supervise.StatusPivotPending, supervise.StatusVerifying, supervise.StatusPaused, supervise.StatusCompleted} {
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
	if control.service != service || control.runID != st.ID {
		t.Fatalf("captured control tool identity = service:%p run:%q, want service:%p run:%q", control.service, control.runID, service, st.ID)
	}
}
