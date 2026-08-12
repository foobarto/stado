package sessioncontext

import (
	"context"
	"errors"
	"testing"

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
