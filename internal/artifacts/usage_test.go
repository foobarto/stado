package artifacts

import (
	"context"
	"testing"
)

func TestUsageSeparatesMechanicalAndEvaluativeEvidence(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	a, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeGlobal, Summary: "x"}, "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordUsage(ctx, UsageObservation{ArtifactID: a.ID, ArtifactVersion: 1, Event: UsageHelped, SessionID: "s"}, "alice", "agent", "bad"); err == nil {
		t.Fatal("model-only helped accepted")
	}
	if _, err := svc.RecordUsage(ctx, UsageObservation{ArtifactID: a.ID, ArtifactVersion: 1, Event: UsageOpened, SessionID: "s", EvidenceRef: "trace:1"}, "alice", "host", "open"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordUsage(ctx, UsageObservation{ArtifactID: a.ID, ArtifactVersion: 1, Event: UsageHelped, SessionID: "s", EvidenceRef: "gate:1", Evaluator: "verification-gate"}, "alice", "host", "help"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Usage(a.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
