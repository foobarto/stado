package sessioncontext

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

func setup(t *testing.T) (*Service, *wal.Store) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(store), store
}

func TestStateSeparatesModelAndHostAuthority(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()
	state, err := s.PatchModel(ctx, "s1", "alice", "agent", "model-1", 0, StatePatch{CurrentTask: "debug", Blockers: []string{"test fails"}, NextStep: "inspect trace"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Assertions["current_task"] != "model_hypothesis" {
		t.Fatalf("assertions=%v", state.Assertions)
	}
	objective := "ship safely"
	caps := []string{"fs:read"}
	state, err = s.PatchHost(ctx, "s1", "alice", "broker", "host-1", 1, HostPatch{Objective: &objective, Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	if state.Objective != objective || len(state.Capabilities) != 1 || state.CurrentTask != "debug" {
		t.Fatalf("state=%+v", state)
	}
	if _, err := s.PatchModel(ctx, "s1", "alice", "agent", "stale", 1, StatePatch{}); !errors.Is(err, ErrVersion) {
		t.Fatalf("got %v", err)
	}
}

func TestEnsureObjectivePreservesFirstValue(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()

	state, err := s.EnsureObjective(ctx, "s1", "  ship safely  ", "alice", "broker", "objective:s1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Objective != "ship safely" || state.Version != 1 {
		t.Fatalf("first state=%+v", state)
	}
	state, err = s.EnsureObjective(ctx, "s1", "replace me", "mallory", "agent", "objective:s1:later")
	if err != nil {
		t.Fatal(err)
	}
	if state.Objective != "ship safely" || state.Version != 1 {
		t.Fatalf("later state=%+v", state)
	}
	if got := len(store.Records()); got != 1 {
		t.Fatalf("records=%d, want 1", got)
	}
}

func TestEnsureObjectiveRejectsInvalidInput(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()

	tests := []struct {
		name      string
		session   string
		objective string
	}{
		{name: "empty session", objective: "objective"},
		{name: "empty objective", session: "s1", objective: "  "},
		{name: "oversize objective", session: "s1", objective: string(make([]rune, 4097))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.EnsureObjective(ctx, test.session, test.objective, "alice", "broker", "objective:test"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDecisionLifecycleAndJournal(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()
	d, err := s.ProposeDecision(ctx, Decision{SessionID: "s1", Context: "index is derived", Choice: "rebuild on mismatch"}, "alice", "agent", "d1")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.SetDecisionStatus(ctx, "s1", d.ID, 1, DecisionAccepted, "alice", "operator", "d2")
	if err != nil || d.Version != 2 || d.Status != DecisionAccepted {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	journal, err := s.Journal("s1", 10)
	if err != nil || len(journal) != 2 {
		t.Fatalf("journal=%v err=%v", journal, err)
	}
}

func TestFiveDeterministicSignalShapes(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()
	observe := func(idem string, o Observation) []Signal {
		got, err := s.Observe(ctx, o, "alice", "broker", idem)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	base := func(kind ObservationKind, ref string) Observation {
		return Observation{SessionID: "s1", Kind: kind, EvidenceRef: ref}
	}
	o := base(ObservationTool, "trace:1")
	o.Tool = "app"
	o.ArgsDigest = "same"
	observe("o1", o)
	o.EvidenceRef = "trace:2"
	if got := observe("o2", o); len(got) != 1 || got[0].Type != SignalRepeatedToolFailure {
		t.Fatalf("repeated=%v", got)
	}
	o.EvidenceRef = "trace:3"
	o.ArgsDigest = "changed"
	o.Succeeded = true
	if got := observe("o3", o); len(got) != 1 || got[0].Type != SignalArgumentChangedSuccess {
		t.Fatalf("changed=%v", got)
	}
	v := base(ObservationVerification, "trace:4")
	observe("o4", v)
	v.EvidenceRef = "trace:5"
	v.Succeeded = true
	if got := observe("o5", v); len(got) != 1 || got[0].Type != SignalVerificationRecovered {
		t.Fatalf("verify=%v", got)
	}
	d := base(ObservationDenial, "trace:6")
	d.Attributes = map[string]string{"boundary": "fs:/private"}
	observe("o6", d)
	d.EvidenceRef = "trace:7"
	if got := observe("o7", d); len(got) != 1 || got[0].Type != SignalRecurringDenial {
		t.Fatalf("denial=%v", got)
	}
	c := base(ObservationCorrection, "conversation:turn:8")
	c.Attributes = map[string]string{"origin": "operator", "marker": "wrong assumption"}
	if got := observe("o8", c); len(got) != 1 || got[0].Type != SignalOperatorCorrection {
		t.Fatalf("correction=%v", got)
	}
	all, err := s.Signals("s1", false)
	if err != nil || len(all) != 5 {
		t.Fatalf("signals=%v err=%v", all, err)
	}
}

func TestOneOffFailureYieldsNoSignal(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	got, err := s.Observe(context.Background(), Observation{SessionID: "s", Kind: ObservationTool, Tool: "x", ArgsDigest: "a", EvidenceRef: "trace:1"}, "alice", "broker", "one")
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestObserveRetryReturnsCommittedResultBeforeRegeneratingMetadata(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()
	first := Observation{SessionID: "s", Kind: ObservationTool, Tool: "shell", ArgsDigest: "same", EvidenceRef: "trace:1"}
	second := Observation{SessionID: "s", Kind: ObservationTool, Tool: "shell", ArgsDigest: "same", EvidenceRef: "trace:2"}
	if _, err := s.Observe(ctx, first, "alice", "broker", "obs:1"); err != nil {
		t.Fatal(err)
	}
	signals, err := s.Observe(ctx, second, "alice", "broker", "obs:2")
	if err != nil || len(signals) != 1 {
		t.Fatalf("initial signals=%v err=%v", signals, err)
	}
	replayed, err := s.Observe(ctx, second, "alice", "broker", "obs:2")
	if err != nil || len(replayed) != 1 || replayed[0].ID != signals[0].ID {
		t.Fatalf("replayed signals=%v err=%v", replayed, err)
	}
	if got := len(store.Records()); got != 2 {
		t.Fatalf("records after retry=%d, want 2", got)
	}
	second.ArgsDigest = "changed"
	if _, err := s.Observe(ctx, second, "alice", "broker", "obs:2"); !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("changed retry error=%v", err)
	}
	if got := len(store.Records()); got != 2 {
		t.Fatalf("records after conflict=%d, want 2", got)
	}
}

func TestRepeatedSignalShapeAggregatesWhileActive(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_, err := s.Observe(ctx, Observation{SessionID: "s", Kind: ObservationTool, Tool: "x", ArgsDigest: "same", EvidenceRef: "trace:" + string(rune('a'+i))}, "alice", "broker", "obs"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	signals, err := s.Signals("s", false)
	if err != nil || len(signals) != 1 || signals[0].Type != SignalRepeatedToolFailure {
		t.Fatalf("signals=%v err=%v", signals, err)
	}
}

func TestRepeatedSignalShapeCanRecurAfterCooldown(t *testing.T) {
	s, store := setup(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	observe := func(id string) {
		_, err := s.Observe(ctx, Observation{SessionID: "s", Kind: ObservationTool, Tool: "x", ArgsDigest: "same", EvidenceRef: "trace:" + id}, "alice", "broker", "obs"+id)
		if err != nil {
			t.Fatal(err)
		}
	}
	observe("1")
	observe("2")
	now = now.Add(2 * time.Hour)
	observe("3")
	signals, err := s.Signals("s", false)
	if err != nil || len(signals) != 2 {
		t.Fatalf("signals=%v err=%v", signals, err)
	}
}
