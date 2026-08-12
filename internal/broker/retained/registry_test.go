package retained

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

func TestDurableAdmissionLeaseAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := New(store)
	fork := ForkPoint{SourceSessionID: "parent", ConversationDigest: "conv", TreeCommit: "tree", TraceCommit: "trace", ResolvedAt: time.Now()}
	a, err := r.Admit(context.Background(), Request{ParentSessionID: "parent", ChildSessionID: "child", Purpose: "research", Fork: fork, CeilingDigest: "ceil", BudgetReservationID: "budget", Principal: "alice", Actor: "broker", IdempotencyKey: "admit"})
	if err != nil {
		t.Fatal(err)
	}
	a, err = r.AcquireLease(context.Background(), a.ID, a.RuntimeNonce, "alice", "runtime", "lease", time.Minute)
	if err != nil || a.LeaseEpoch != 1 {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	a, err = r.Transition(context.Background(), a.ID, StatusAdmitted, StatusStarting, a.LeaseEpoch, "alice", "runtime", "start")
	if err != nil {
		t.Fatal(err)
	}
	a, err = r.Transition(context.Background(), a.ID, StatusStarting, StatusRunning, a.LeaseEpoch, "alice", "runtime", "running")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transition(context.Background(), a.ID, StatusRunning, StatusDeleted, a.LeaseEpoch, "alice", "runtime", "bad"); !errors.Is(err, ErrLifecycle) {
		t.Fatalf("got %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r = New(store)
	got, ok, err := r.Get(a.ID)
	if err != nil || !ok || got.Status != StatusRunning {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	if _, err := r.AcquireLease(context.Background(), a.ID, "wrong", "alice", "runtime", "wrong", time.Minute); !errors.Is(err, ErrLease) {
		t.Fatalf("got %v", err)
	}
}

func TestAdmissionRequiresResolvedFork(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := New(store)
	_, err = r.Admit(context.Background(), Request{ParentSessionID: "p", ChildSessionID: "c", Purpose: "x", CeilingDigest: "c", BudgetReservationID: "b", Principal: "a", Actor: "b", IdempotencyKey: "i"})
	if err == nil {
		t.Fatal("unresolved source admitted")
	}
}
