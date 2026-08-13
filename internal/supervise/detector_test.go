package supervise

import (
	"testing"
	"time"
)

func detectorAnchor(seq uint64) Anchor {
	return Anchor{RootSessionID: "root", SessionSequence: seq, PlanVersion: 1, ActiveStep: "build", TreeDigest: "tree"}
}

func TestEventDetectorStaysQuietThenCoalescesThrash(t *testing.T) {
	d := NewDetector(ModeEvent)
	base := time.Unix(1000, 0).UTC()
	for i := uint64(1); i <= 2; i++ {
		got := d.Observe(WorkerEvent{Kind: WorkerToolOutcome, Sequence: i, At: base.Add(time.Duration(i) * time.Second), Tool: "shell__exec", ArgsDigest: "args", ErrorFingerprint: "exit:1"}, detectorAnchor(i))
		if i == 1 && got != nil {
			t.Fatalf("one failure triggered: %+v", got)
		}
		if i == 2 && (got == nil || len(got.Signals) != 1 || got.Signals[0].Type != TriggerRepeatedFailure) {
			t.Fatalf("repeated failure trigger = %+v", got)
		}
	}
	got := d.Observe(WorkerEvent{Kind: WorkerToolOutcome, Sequence: 3, At: base.Add(3 * time.Second), Tool: "shell__exec", ArgsDigest: "args", ErrorFingerprint: "exit:1"}, detectorAnchor(3))
	if got == nil || len(got.Signals) != 1 || got.Signals[0].Type != TriggerRetryThrash {
		t.Fatalf("thrash trigger = %+v", got)
	}
}

func TestLiveDetectorReviewsEveryTurn(t *testing.T) {
	d := NewDetector(ModeLive)
	got := d.Observe(WorkerEvent{Kind: WorkerTurnCompleted, Sequence: 1, StepID: "build", CriteriaCompleted: 1}, detectorAnchor(1))
	if got == nil || len(got.Signals) != 1 || got.Signals[0].Type != TriggerLiveTurn {
		t.Fatalf("live turn trigger = %+v", got)
	}
}

func TestDetectorFindsRegressionScopeChildRiskAndCompletion(t *testing.T) {
	d := NewDetector(ModeEvent)
	pass, fail := true, false
	cases := []struct {
		event WorkerEvent
		want  TriggerType
	}{
		{WorkerEvent{Kind: WorkerVerification, Sequence: 1, VerificationPassed: &pass}, ""},
		{WorkerEvent{Kind: WorkerVerification, Sequence: 2, VerificationPassed: &fail}, TriggerVerificationRegress},
		{WorkerEvent{Kind: WorkerTreeChanged, Sequence: 3, OutOfScopePaths: []string{"unplanned.go"}}, TriggerScopeExpansion},
		{WorkerEvent{Kind: WorkerChildLifecycle, Sequence: 4, ChildID: "child", ChildStatus: "failed"}, TriggerChildFailure},
		{WorkerEvent{Kind: WorkerRiskBoundary, Sequence: 5, Boundary: "release"}, TriggerRisk},
		{WorkerEvent{Kind: WorkerCompletionClaimed, Sequence: 6}, TriggerCompletion},
	}
	for _, tc := range cases {
		got := d.Observe(tc.event, detectorAnchor(tc.event.Sequence))
		if tc.want == "" {
			if got != nil {
				t.Fatalf("unexpected trigger: %+v", got)
			}
			continue
		}
		if got == nil || len(got.Signals) == 0 || got.Signals[0].Type != tc.want {
			t.Fatalf("event %s trigger = %+v, want %s", tc.event.Kind, got, tc.want)
		}
	}
}

func TestDetectorSnapshotPreservesCooldown(t *testing.T) {
	d := NewDetector(ModeEvent)
	base := time.Unix(1000, 0).UTC()
	d.Observe(WorkerEvent{Kind: WorkerTreeChanged, Sequence: 1, At: base, OutOfScopePaths: []string{"x"}}, detectorAnchor(1))
	restored := RestoreDetector(d.Snapshot())
	got := restored.Observe(WorkerEvent{Kind: WorkerTreeChanged, Sequence: 2, At: base.Add(time.Second), OutOfScopePaths: []string{"x"}}, detectorAnchor(2))
	if got != nil {
		t.Fatalf("restored cooldown did not suppress duplicate: %+v", got)
	}
}

func TestDetectorFindsEditRevertAcrossInterleavedToolEvents(t *testing.T) {
	d := NewDetector(ModeEvent)
	events := []WorkerEvent{
		{Kind: WorkerTreeChanged, Sequence: 1, TreeDigest: "tree-a"},
		{Kind: WorkerToolOutcome, Sequence: 2, Tool: "fs.write", Succeeded: true},
		{Kind: WorkerTreeChanged, Sequence: 3, TreeDigest: "tree-b"},
		{Kind: WorkerToolOutcome, Sequence: 4, Tool: "fs.write", Succeeded: true},
	}
	for _, event := range events {
		if got := d.Observe(event, detectorAnchor(event.Sequence)); got != nil {
			t.Fatalf("early trigger for %s: %+v", event.Kind, got)
		}
	}
	got := d.Observe(WorkerEvent{Kind: WorkerTreeChanged, Sequence: 5, TreeDigest: "tree-a"}, detectorAnchor(5))
	if got == nil || len(got.Signals) != 1 || got.Signals[0].Type != TriggerEditRevert {
		t.Fatalf("interleaved edit/revert trigger = %+v", got)
	}
}

func TestDetectorFindsChangedPathCountExpansion(t *testing.T) {
	d := NewDetector(ModeEvent)
	d.Observe(WorkerEvent{Kind: WorkerTreeChanged, Sequence: 1, TreeDigest: "tree-a", ChangedPaths: []string{"a.go", "b.go"}}, detectorAnchor(1))
	got := d.Observe(WorkerEvent{Kind: WorkerTreeChanged, Sequence: 2, TreeDigest: "tree-b", ChangedPaths: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}}, detectorAnchor(2))
	if got == nil || len(got.Signals) != 1 || got.Signals[0].Type != TriggerScopeExpansion {
		t.Fatalf("changed-path expansion trigger = %+v", got)
	}
}

func TestDetectorFindsStepCorrectionNoProgressAndBudget(t *testing.T) {
	d := NewDetector(ModeEvent)
	base := time.Unix(2000, 0).UTC()
	if got := d.Observe(WorkerEvent{Kind: WorkerStepClaimed, Sequence: 1, At: base}, detectorAnchor(1)); got == nil || got.Signals[0].Type != TriggerStepCompletion {
		t.Fatalf("step trigger = %+v", got)
	}
	if got := d.Observe(WorkerEvent{Kind: WorkerCorrectionFollowup, Sequence: 2, At: base.Add(time.Second)}, detectorAnchor(2)); got == nil || got.Signals[0].Type != TriggerCorrectionFollowup {
		t.Fatalf("correction trigger = %+v", got)
	}
	for seq := uint64(3); seq <= 5; seq++ {
		if got := d.Observe(WorkerEvent{Kind: WorkerTurnCompleted, Sequence: seq, At: base.Add(time.Duration(seq) * time.Second), StepID: "build", CriteriaCompleted: 0}, detectorAnchor(seq)); got != nil {
			t.Fatalf("early no-progress trigger at %d = %+v", seq, got)
		}
	}
	got := d.Observe(WorkerEvent{Kind: WorkerTurnCompleted, Sequence: 6, At: base.Add(6 * time.Second), StepID: "build", CriteriaCompleted: 0, TokenUsage: 80, TokenBudget: 100}, detectorAnchor(6))
	if got == nil || len(got.Signals) != 2 || got.Signals[0].Type != TriggerBudgetBurn || got.Signals[1].Type != TriggerNoProgress {
		t.Fatalf("progress/budget trigger = %+v", got)
	}
}

func TestRestoreLegacyDetectorDefaultsToEvent(t *testing.T) {
	d := RestoreDetector(DetectorSnapshot{})
	if d.Snapshot().Mode != ModeEvent {
		t.Fatalf("mode = %q", d.Snapshot().Mode)
	}
}

func TestDetectorPrunesOldCooldownShapes(t *testing.T) {
	d := NewDetector(ModeEvent)
	base := time.Unix(3000, 0).UTC()
	for i := 0; i < maxDetectorCooldowns+20; i++ {
		d.Observe(WorkerEvent{Kind: WorkerRiskBoundary, Sequence: uint64(i + 1), At: base.Add(time.Duration(i) * time.Second), Boundary: string(rune('a' + i))}, detectorAnchor(uint64(i+1)))
	}
	snapshot := d.Snapshot()
	if len(snapshot.LastEmittedAt) != maxDetectorCooldowns || len(snapshot.LastEmittedSeq) != maxDetectorCooldowns {
		t.Fatalf("cooldown maps = %d/%d", len(snapshot.LastEmittedAt), len(snapshot.LastEmittedSeq))
	}
}

func TestDetectorTreatsRuntimeChildErrorAndTimeoutAsFailures(t *testing.T) {
	for _, status := range []string{"error", "timeout"} {
		d := NewDetector(ModeEvent)
		trigger := d.Observe(WorkerEvent{Kind: WorkerChildLifecycle, Sequence: 1, ChildID: "child-1", ChildStatus: status}, detectorAnchor(1))
		if trigger == nil || len(trigger.Signals) != 1 || trigger.Signals[0].Type != TriggerChildFailure {
			t.Fatalf("child status %q trigger = %+v", status, trigger)
		}
	}
}
