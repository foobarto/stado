package retained

import (
	"context"
	"github.com/foobarto/stado/internal/broker/wal"
	"testing"
	"time"
)

func TestSupervisionRestartsOnlyBoundedTransientFailures(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := New(store)
	now := time.Now()
	r.now = func() time.Time { return now }
	a, err := r.Admit(context.Background(), Request{ParentSessionID: "p", ChildSessionID: "c", Purpose: "worker", Fork: ForkPoint{SourceSessionID: "p", ConversationDigest: "x", TreeCommit: "t", TraceCommit: "r", ResolvedAt: now}, CeilingDigest: "c", BudgetReservationID: "b", Principal: "a", Actor: "broker", IdempotencyKey: "admit"})
	if err != nil {
		t.Fatal(err)
	}
	p := RestartPolicy{Mode: "on_transient_failure", MaxRestarts: 2, Window: time.Minute, BaseBackoff: time.Second, MaxBackoff: 10 * time.Second}
	logical, err := r.DecideRestart(a.ID, FailureLogical, p, "a", "broker", "logical")
	if err != nil || logical.Restart {
		t.Fatalf("logical=%+v err=%v", logical, err)
	}
	one, err := r.DecideRestart(a.ID, FailureTransient, p, "a", "broker", "one")
	if err != nil || !one.Restart || one.Backoff != time.Second {
		t.Fatalf("one=%+v err=%v", one, err)
	}
	two, err := r.DecideRestart(a.ID, FailureTransient, p, "a", "broker", "two")
	if err != nil || !two.Restart || two.Backoff != 2*time.Second {
		t.Fatalf("two=%+v err=%v", two, err)
	}
	three, err := r.DecideRestart(a.ID, FailureTransient, p, "a", "broker", "three")
	if err != nil || three.Restart {
		t.Fatalf("three=%+v err=%v", three, err)
	}
}
