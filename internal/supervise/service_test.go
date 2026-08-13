package supervise

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker/wal"
)

func openTestService(t *testing.T) (*Service, *wal.Store) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store), store
}

func TestNormalizeConfigDefaultsAndHighAssurance(t *testing.T) {
	cfg, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Fatalf("zero config = %+v, want defaults %+v", cfg, DefaultConfig())
	}
	if cfg.WatchdogRequired || !cfg.VerifierRequired || !cfg.AllowAdvisoryFallback {
		t.Fatalf("standard fallback posture = %+v", cfg)
	}
	high := DefaultConfig()
	high.Profile = ProfileHighAssurance
	high.AllowAdvisoryFallback = true
	high.WatchdogBudget.TokenCap = 1
	high.VerifierBudget.TokenCap = 1
	high, err = NormalizeConfig(high)
	if err != nil {
		t.Fatal(err)
	}
	if high.AllowAdvisoryFallback || !high.WatchdogRequired || !high.VerifierRequired || high.WatchdogBudget.TokenCap < 64_000 || high.VerifierBudget.TokenCap < 96_000 {
		t.Fatalf("high-assurance config = %+v", high)
	}
}

func TestNormalizeConfigRejectsNonFiniteBudgetAndOversizedProviderIdentity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WatchdogBudget.CostCapUSD = math.NaN()
	if _, err := NormalizeConfig(cfg); err == nil {
		t.Fatal("NaN watchdog cost budget accepted")
	}
	cfg = DefaultConfig()
	cfg.Verifier.Provider = strings.Repeat("x", 513)
	if _, err := NormalizeConfig(cfg); err == nil {
		t.Fatal("oversized verifier provider accepted")
	}
}

func TestServicePersistsObjectiveDetectorAndInitialTree(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	st, err := svc.CreateWithSeed(ctx, "root-1", "Objective: durable work", DefaultConfig(), "user", "host", "create-seeded")
	if err != nil {
		t.Fatal(err)
	}
	anchor := Anchor{RootSessionID: "root-1", SessionSequence: 1, TreeDigest: "tree:initial"}
	st, err = svc.UpdateRuntimeAnchor(ctx, st.ID, st.Version, RoleHost, anchor, "user", "host", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	detector := NewDetector(ModeEvent)
	detector.Observe(WorkerEvent{Kind: WorkerToolOutcome, Sequence: 1, Tool: "bash", Succeeded: true}, anchor)
	st, err = svc.RecordDetectorSnapshot(ctx, st.ID, st.Version, RoleHost, detector.Snapshot(), "user", "host", "detector")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := svc.StateBySession("root-1")
	if err != nil {
		t.Fatal(err)
	}
	if restored.ObjectiveSeed != "Objective: durable work" || restored.InitialTreeDigest != "tree:initial" || len(restored.Detector.History) != 1 {
		t.Fatalf("restored state = %+v", restored)
	}
}

func TestReattachSessionPreservesRootAndInvalidatesParentAuthority(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	parentAnchor := st.Anchor()
	childAnchor := parentAnchor
	childAnchor.SessionSequence++
	childAnchor.TreeDigest = "sha256:child"

	var err error
	st, err = svc.ReattachSession(context.Background(), st.ID, st.Version, RoleHost, "child-1", childAnchor, "user", "host", "reattach-child")
	if err != nil {
		t.Fatal(err)
	}
	if st.RootSessionID != "root-1" || st.AttachedSessionID != "child-1" || st.SessionSequence != childAnchor.SessionSequence || st.TreeDigest != childAnchor.TreeDigest {
		t.Fatalf("reattached state = %+v", st)
	}
	byChild, err := svc.StateBySession("child-1")
	if err != nil || byChild.ID != st.ID {
		t.Fatalf("state by attached child = %+v, %v", byChild, err)
	}
	if _, err = svc.StateBySession("root-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old parent attachment error = %v, want not found", err)
	}

	trigger := &Trigger{ID: "parent-trigger", Anchor: parentAnchor, Signals: []TriggerSignal{{Type: TriggerLiveTurn, Severity: "info", EvidenceRefs: []string{"worker-event:parent"}}}}
	_, err = svc.RecordEventVerdict(context.Background(), st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewEvent, Decision: VerdictContinue, Anchor: parentAnchor, Rationale: "parent verdict"}, trigger, "user", "watchdog", "stale-parent-verdict")
	if !errors.Is(err, ErrStaleVerdict) {
		t.Fatalf("parent verdict error = %v", err)
	}
}

func TestReattachSessionIsHostOnlyAndRequiresSequenceAdvance(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	if _, err := svc.ReattachSession(context.Background(), st.ID, st.Version, RoleWatchdog, "child-1", st.Anchor(), "user", "watchdog", "bad-role"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog reattach error = %v", err)
	}
	if _, err := svc.ReattachSession(context.Background(), st.ID, st.Version, RoleHost, "child-1", st.Anchor(), "user", "host", "stale-sequence"); !errors.Is(err, ErrStaleVerdict) {
		t.Fatalf("stale reattach error = %v", err)
	}
}

func TestFollowupInboxIsDurableBoundedAndHostResolved(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	ctx := context.Background()

	st, followup, err := svc.EnqueueFollowup(ctx, st.ID, st.Version, RoleOperator, "look into a separate idea", "user", "operator", "followup-enqueue")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := svc.State(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.PendingFollowups) != 1 || restored.PendingFollowups[0] != followup {
		t.Fatalf("restored follow-ups = %+v, want %+v", restored.PendingFollowups, followup)
	}
	if _, err := svc.ResolveFollowup(ctx, st.ID, st.Version, RoleWatchdog, followup.ID, "user", "watchdog", "bad-resolve"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog resolve error = %v", err)
	}
	st, err = svc.ResolveFollowup(ctx, st.ID, st.Version, RoleHost, followup.ID, "user", "host", "followup-resolve")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PendingFollowups) != 0 {
		t.Fatalf("pending follow-ups after resolution = %+v", st.PendingFollowups)
	}
}

func testBaseline() Baseline {
	return Baseline{
		Objective:          "Ship supervised work",
		Constraints:        []string{"keep authority in the host"},
		NonGoals:           []string{"nested supervision"},
		AcceptanceCriteria: []string{"stale verdicts fail closed"},
		Plan: []Step{
			{ID: "design", Title: "Design", DoneWhen: "contract approved"},
			{ID: "build", Title: "Build", DoneWhen: "tests pass"},
		},
		DefinitionOfDone: []string{"criteria and documentation reconciled"},
		Verification:     []string{"go test ./..."},
		Risks:            []string{"model prose mistaken for authority"},
	}
}

func createRunning(t *testing.T, svc *Service, cfg Config) State {
	t.Helper()
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-1", cfg, "user", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := Anchor{RootSessionID: "root-1", SessionSequence: 7, TreeDigest: "sha256:tree"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testBaseline(), RoleWatchdog, anchor, "user", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, RoleOperator, "user", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func completePlanAsOperator(t *testing.T, svc *Service, st State) State {
	t.Helper()
	for st.ActiveStep != "" {
		var err error
		st, err = svc.AdvanceStep(context.Background(), st.ID, st.Version, RoleOperator, st.Anchor(), "user", "operator", "complete-plan:"+st.ActiveStep)
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestServiceRequiresHumanBaselineApprovalAndBindsVerdicts(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	st, err := svc.Create(ctx, "root-1", DefaultConfig(), "user", "host", "create")
	if err != nil {
		t.Fatal(err)
	}
	anchor := Anchor{RootSessionID: "root-1", SessionSequence: 4, TreeDigest: "tree:a"}
	st, err = svc.ProposeBaseline(ctx, st.ID, st.Version, testBaseline(), RoleWatchdog, anchor, "user", "watchdog", "propose")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApproveBaseline(ctx, st.ID, st.Version, RoleWatchdog, "user", "watchdog", "bad-approve"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog baseline approval error = %v", err)
	}
	st, err = svc.ApproveBaseline(ctx, st.ID, st.Version, RoleOperator, "user", "operator", "approve")
	if err != nil {
		t.Fatal(err)
	}
	old := st.Anchor()
	advanced := old
	advanced.SessionSequence++
	advanced.TreeDigest = "tree:b"
	st, err = svc.UpdateRuntimeAnchor(ctx, st.ID, st.Version, RoleHost, advanced, "user", "host", "advance")
	if err != nil {
		t.Fatal(err)
	}
	oldTrigger := &Trigger{ID: "old-trigger", Anchor: old, Signals: []TriggerSignal{{Type: TriggerLiveTurn, Severity: "info", EvidenceRefs: []string{"worker-event:old"}}}}
	_, err = svc.RecordEventVerdict(ctx, st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewEvent, Decision: VerdictContinue, Anchor: old, Rationale: "reviewed old evidence"}, oldTrigger, "user", "watchdog", "stale")
	if !errors.Is(err, ErrStaleVerdict) {
		t.Fatalf("stale verdict error = %v", err)
	}
}

func TestPlanPivotAuthorityAndContractPivotRemainHumanOnly(t *testing.T) {
	svc, _ := openTestService(t)
	cfg := DefaultConfig()
	cfg.PivotApproval = PivotByWatchdog
	st := createRunning(t, svc, cfg)
	ctx := context.Background()
	plan := []Step{{ID: "replan", Title: "Replan", DoneWhen: "new path verified"}}
	st, err := svc.RequestPivot(ctx, st.ID, st.Version, RoleWorker, PivotRequest{Kind: PivotPlan, Reason: "evidence invalidated the old plan", ProposedPlan: plan, Anchor: st.Anchor()}, "user", "worker", "plan-request")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ResolvePivot(ctx, st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewPivot, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "new plan preserves the approved contract", EvidenceRefs: []string{"worker-diff"}}, "user", "watchdog", "plan-approve")
	if err != nil {
		t.Fatal(err)
	}
	if st.PlanVersion != 2 || st.ActiveStep != "replan" {
		t.Fatalf("plan pivot state = %+v", st)
	}
	if st.ContractDigest != baselineDigest(st.Baseline) {
		t.Fatalf("plan pivot left stale contract digest: %s", st.ContractDigest)
	}

	updated := testBaseline()
	updated.Objective = "A materially different objective"
	st, err = svc.RequestPivot(ctx, st.ID, st.Version, RoleWorker, PivotRequest{Kind: PivotContract, Reason: "objective must change", ProposedBaseline: &updated, Anchor: st.Anchor()}, "user", "worker", "contract-request")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ResolvePivot(ctx, st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewPivot, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "proposed objective change"}, "user", "watchdog", "contract-bad")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog contract approval error = %v", err)
	}
}

func TestAuthorityBearingWatchdogAndVerifierApprovalsRequireEvidence(t *testing.T) {
	svc, _ := openTestService(t)
	cfg := DefaultConfig()
	cfg.PivotApproval = PivotByWatchdog
	st := createRunning(t, svc, cfg)
	ctx := context.Background()

	if _, err := svc.AdvanceStep(ctx, st.ID, st.Version, RoleWatchdog, st.Anchor(), "user", "watchdog", "advance-without-verdict"); err == nil || !strings.Contains(err.Error(), "evidence-backed") {
		t.Fatalf("step advance without verdict error = %v", err)
	}
	verdict := Verdict{Kind: ReviewEvent, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "step evidence is sufficient", EvidenceRefs: []string{"worker-event:claim"}}
	trigger := &Trigger{ID: "trigger-step", Anchor: st.Anchor(), Signals: []TriggerSignal{{Type: TriggerStepCompletion, Severity: "info", EvidenceRefs: []string{"worker-event:claim"}, Attributes: map[string]string{"step": "design"}}}}
	st, err := svc.RecordEventVerdict(ctx, st.ID, st.Version, RoleWatchdog, verdict, trigger, "user", "watchdog", "step-verdict")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.AdvanceStep(ctx, st.ID, st.Version, RoleWatchdog, st.Anchor(), "user", "watchdog", "advance-with-verdict")
	if err != nil || st.ActiveStep != "build" {
		t.Fatalf("evidence-backed step advance state=%+v err=%v", st, err)
	}

	plan := []Step{{ID: "replan", Title: "Replan", DoneWhen: "new path verified"}}
	st, err = svc.RequestPivot(ctx, st.ID, st.Version, RoleWorker, PivotRequest{Kind: PivotPlan, Reason: "the current plan is blocked", ProposedPlan: plan, Anchor: st.Anchor()}, "user", "worker", "pivot-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolvePivot(ctx, st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewPivot, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "approve without citation"}, "user", "watchdog", "pivot-no-evidence"); err == nil || !strings.Contains(err.Error(), "requires evidence references") {
		t.Fatalf("evidence-free pivot approval error = %v", err)
	}
	st, err = svc.ResolvePivot(ctx, st.ID, st.Version, RoleOperator, Verdict{Kind: ReviewPivot, Decision: VerdictReject, Anchor: st.Anchor(), Rationale: "keep the approved plan"}, "user", "operator", "pivot-reject")
	if err != nil {
		t.Fatal(err)
	}

	st = completePlanAsOperator(t, svc, st)
	evidence := Evidence{Kind: "test", Summary: "full suite passed", References: []string{"trace:test"}, Anchor: st.Anchor()}
	st, err = svc.RequestCompletion(ctx, st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "done", Evidence: []Evidence{evidence}, Anchor: st.Anchor()}, "user", "worker", "completion-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveCompletion(ctx, st.ID, st.Version, RoleVerifier, Verdict{Kind: ReviewCompletion, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "approve without citation"}, "user", "verifier", "completion-no-evidence"); err == nil || !strings.Contains(err.Error(), "requires evidence references") {
		t.Fatalf("evidence-free completion approval error = %v", err)
	}
}

func TestStepEvidenceForEarlierStepCannotAuthorizeCurrentStep(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	ctx := context.Background()
	st, err := svc.AdvanceStep(ctx, st.ID, st.Version, RoleOperator, st.Anchor(), "user", "operator", "operator-advance-design")
	if err != nil || st.ActiveStep != "build" {
		t.Fatalf("advance to build state=%+v err=%v", st, err)
	}
	verdict := Verdict{Kind: ReviewEvent, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "old claim was coalesced", EvidenceRefs: []string{"worker-event:design-claim"}}
	trigger := &Trigger{ID: "coalesced-old-step", Anchor: st.Anchor(), Signals: []TriggerSignal{{Type: TriggerStepCompletion, Severity: "info", EvidenceRefs: []string{"worker-event:design-claim"}, Attributes: map[string]string{"step": "design"}}}}
	st, err = svc.RecordEventVerdict(ctx, st.ID, st.Version, RoleWatchdog, verdict, trigger, "user", "watchdog", "old-step-verdict")
	if err != nil {
		t.Fatal(err)
	}
	if st.StepApproval != nil {
		t.Fatalf("old step evidence created current authority: %+v", st.StepApproval)
	}
	if _, err = svc.AdvanceStep(ctx, st.ID, st.Version, RoleWatchdog, st.Anchor(), "user", "watchdog", "old-step-advance"); err == nil || !strings.Contains(err.Error(), "evidence-backed") {
		t.Fatalf("old step evidence advance error = %v", err)
	}
}

func TestUnrelatedEventApprovalCannotAdvancePlanStep(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	ctx := context.Background()
	verdict := Verdict{Kind: ReviewEvent, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "retry tactic recovered", EvidenceRefs: []string{"worker-event:retry"}}
	trigger := &Trigger{ID: "trigger-retry", Anchor: st.Anchor(), Signals: []TriggerSignal{{Type: TriggerRetryThrash, Severity: "high", EvidenceRefs: []string{"worker-event:retry"}}}}
	st, err := svc.RecordEventVerdict(ctx, st.ID, st.Version, RoleWatchdog, verdict, trigger, "user", "watchdog", "retry-verdict")
	if err != nil {
		t.Fatal(err)
	}
	if st.StepApproval != nil {
		t.Fatalf("unrelated event created step authority: %+v", st.StepApproval)
	}
	if _, err := svc.AdvanceStep(ctx, st.ID, st.Version, RoleWatchdog, st.Anchor(), "user", "watchdog", "bad-advance"); err == nil || !strings.Contains(err.Error(), "evidence-backed") {
		t.Fatalf("unrelated event step advance error = %v", err)
	}
}

func TestCompletionRequiresEveryPlanStep(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	evidence := Evidence{Kind: "test", Summary: "one check passed", References: []string{"trace:test"}, Anchor: st.Anchor()}
	_, err := svc.RequestCompletion(context.Background(), st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "premature", Evidence: []Evidence{evidence}, Anchor: st.Anchor()}, "user", "worker", "premature-completion")
	if err == nil || !strings.Contains(err.Error(), "every approved plan step") {
		t.Fatalf("premature completion error = %v", err)
	}
}

func TestPauseResumePreservesVerificationPhaseAndInvalidatesChangedTree(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	st = completePlanAsOperator(t, svc, st)
	ctx := context.Background()
	anchor := st.Anchor()
	e := Evidence{Kind: "test", Summary: "tests passed", Anchor: anchor}
	st, err := svc.RequestCompletion(ctx, st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "done", Evidence: []Evidence{e}, Anchor: anchor}, "user", "worker", "completion")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.Pause(ctx, st.ID, st.Version, RoleHost, "verifier unavailable", "user", "host", "pause")
	if err != nil || st.ResumeStatus != StatusVerifying {
		t.Fatalf("paused verification state=%+v err=%v", st, err)
	}
	st, err = svc.Resume(ctx, st.ID, st.Version, RoleOperator, anchor, "user", "operator", "resume-same")
	if err != nil || st.Status != StatusVerifying || st.Completion == nil {
		t.Fatalf("same-tree resume state=%+v err=%v", st, err)
	}
	st, err = svc.Pause(ctx, st.ID, st.Version, RoleHost, "paused again", "user", "host", "pause-again")
	if err != nil {
		t.Fatal(err)
	}
	changed := anchor
	changed.TreeDigest = "tree:changed"
	st, err = svc.Resume(ctx, st.ID, st.Version, RoleOperator, changed, "user", "operator", "resume-changed")
	if err != nil || st.Status != StatusRunning || st.Completion != nil {
		t.Fatalf("changed-tree resume state=%+v err=%v", st, err)
	}
}

func TestFollowupCanQueueDuringVerificationButResolvesOnlyWhenRunning(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	st = completePlanAsOperator(t, svc, st)
	ctx := context.Background()
	anchor := st.Anchor()
	e := Evidence{Kind: "test", Summary: "tests passed", Anchor: anchor}
	st, err := svc.RequestCompletion(ctx, st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "done", Evidence: []Evidence{e}, Anchor: anchor}, "user", "worker", "completion")
	if err != nil {
		t.Fatal(err)
	}
	st, followup, err := svc.EnqueueFollowup(ctx, st.ID, st.Version, RoleOperator, "one more requirement", "user", "operator", "followup")
	if err != nil || len(st.PendingFollowups) != 1 {
		t.Fatalf("verification follow-up state=%+v err=%v", st, err)
	}
	if _, err := svc.ResolveFollowup(ctx, st.ID, st.Version, RoleHost, followup.ID, "user", "host", "resolve-early"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("early follow-up resolve error = %v", err)
	}
	st, err = svc.InvalidateCompletion(ctx, st.ID, st.Version, RoleHost, "route follow-up before completion", "user", "host", "invalidate")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.ResolveFollowup(ctx, st.ID, st.Version, RoleHost, followup.ID, "user", "host", "resolve")
	if err != nil || len(st.PendingFollowups) != 0 {
		t.Fatalf("resolved follow-up state=%+v err=%v", st, err)
	}
}

func TestIndependentVerifierOwnsCompletion(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	st = completePlanAsOperator(t, svc, st)
	ctx := context.Background()
	e := Evidence{Kind: "test", Summary: "full suite passed", References: []string{"trace:test"}, Anchor: st.Anchor()}
	st, err := svc.RequestCompletion(ctx, st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "all criteria satisfied", Evidence: []Evidence{e}, Anchor: st.Anchor()}, "user", "worker", "complete-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveCompletion(ctx, st.ID, st.Version, RoleWatchdog, Verdict{Kind: ReviewCompletion, Decision: VerdictApprove, Anchor: st.Anchor()}, "user", "watchdog", "bad-verifier"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog completion approval error = %v", err)
	}
	st, err = svc.ResolveCompletion(ctx, st.ID, st.Version, RoleVerifier, Verdict{Kind: ReviewCompletion, Decision: VerdictApprove, Anchor: st.Anchor(), Rationale: "evidence covers the contract", EvidenceRefs: []string{"recorded-evidence:0"}}, "user", "verifier", "verified")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusCompleted {
		t.Fatalf("status = %s", st.Status)
	}
}

func TestPassedVerificationGatePreservesCompletionVerdictAnchor(t *testing.T) {
	svc, _ := openTestService(t)
	st := createRunning(t, svc, DefaultConfig())
	st = completePlanAsOperator(t, svc, st)
	ctx := context.Background()
	anchor := st.Anchor()
	e := Evidence{Kind: "test", Summary: "full suite passed", References: []string{"trace:test"}, Anchor: anchor}
	st, err := svc.RequestCompletion(ctx, st.ID, st.Version, RoleWorker, CompletionRequest{Summary: "all criteria satisfied", Evidence: []Evidence{e}, Anchor: anchor}, "user", "worker", "complete-request")
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.RecordVerificationGate(ctx, st.ID, st.Version, RoleHost, true, "deterministic gates passed", []string{"verify-work:1"}, "user", "host", "gate-pass")
	if err != nil {
		t.Fatal(err)
	}
	if st.Anchor() != anchor || st.Completion == nil || st.Completion.Anchor != anchor {
		t.Fatalf("passed gate changed completion binding: state=%+v completion=%+v", st.Anchor(), st.Completion)
	}
	st, err = svc.ResolveCompletion(ctx, st.ID, st.Version, RoleVerifier, Verdict{Kind: ReviewCompletion, Decision: VerdictApprove, Anchor: anchor, Rationale: "verified", EvidenceRefs: []string{"verify-work:1"}}, "user", "verifier", "complete-approved")
	if err != nil || st.Status != StatusCompleted {
		t.Fatalf("verifier after gate: state=%+v err=%v", st, err)
	}
}

func TestWatchdogFailurePolicyPausesAtLimit(t *testing.T) {
	svc, _ := openTestService(t)
	cfg := DefaultConfig()
	cfg.FailedEventLimit = 2
	st := createRunning(t, svc, cfg)
	ctx := context.Background()
	st, _ = svc.RecordReviewFailure(ctx, st.ID, st.Version, RoleHost, "timeout", "user", "host", "failure-1")
	if st.Status != StatusRunning {
		t.Fatalf("first event failure status = %s", st.Status)
	}
	st, _ = svc.RecordReviewFailure(ctx, st.ID, st.Version, RoleHost, "timeout", "user", "host", "failure-2")
	if st.Status != StatusPaused {
		t.Fatalf("failure limit status = %s", st.Status)
	}
}

func TestLiveWatchdogFailurePausesImmediatelyAndResumeIsHumanOnly(t *testing.T) {
	svc, _ := openTestService(t)
	cfg := DefaultConfig()
	cfg.Mode = ModeLive
	st := createRunning(t, svc, cfg)
	ctx := context.Background()
	st, err := svc.RecordReviewFailure(ctx, st.ID, st.Version, RoleHost, "timeout", "user", "host", "failure")
	if err != nil || st.Status != StatusPaused {
		t.Fatalf("live failure state=%+v err=%v", st, err)
	}
	if _, err := svc.Resume(ctx, st.ID, st.Version, RoleWatchdog, st.Anchor(), "user", "watchdog", "bad-resume"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("watchdog resume error = %v", err)
	}
	st, err = svc.Resume(ctx, st.ID, st.Version, RoleOperator, st.Anchor(), "user", "operator", "resume")
	if err != nil || st.Status != StatusRunning {
		t.Fatalf("operator resume state=%+v err=%v", st, err)
	}
}
