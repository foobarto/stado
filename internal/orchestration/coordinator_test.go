package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLauncher struct{ done chan retained.Admission }

func (f fakeLauncher) Launch(_ context.Context, a retained.Admission) (LaunchResult, error) {
	f.done <- a
	return LaunchResult{Usage: brokerbudget.Limits{Tokens: 10}}, nil
}

type transientThenSuccessLauncher struct{ calls atomic.Int32 }

func (l *transientThenSuccessLauncher) Launch(_ context.Context, _ retained.Admission) (LaunchResult, error) {
	if l.calls.Add(1) == 1 {
		return LaunchResult{Transient: true, Error: "temporary provider outage"}, errors.New("temporary provider outage")
	}
	return LaunchResult{FinalText: "recovered"}, nil
}

func TestCoordinatorRestartsOnlyBoundedHostClassifiedTransientFailure(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	budgets := brokerbudget.New(store)
	if _, err := budgets.CreateAccount(t.Context(), "root", "", brokerbudget.Limits{Tokens: 100, Turns: 10}, "alice", "broker", "account"); err != nil {
		t.Fatal(err)
	}
	policy := mailbox.NewDynamicRelationPolicy()
	mail := mailbox.New(store, policy)
	launcher := &transientThenSuccessLauncher{}
	c := New(retained.New(store), budgets, mail, nil)
	c.Policy = policy
	now := time.Now()
	h, err := c.SpawnRetained(t.Context(), LaunchRequest{AccountID: "root", Budget: brokerbudget.Limits{Tokens: 20, Turns: 4}, Principal: "alice", Actor: "parent", IdempotencyKey: "restart", Launcher: launcher, RestartPolicy: retained.RestartPolicy{Mode: "on_transient_failure", MaxRestarts: 1, Window: time.Minute, BaseBackoff: time.Millisecond}, Admission: retained.Request{ParentSessionID: "parent", ChildSessionID: "child", Purpose: "worker", Fork: retained.ForkPoint{SourceSessionID: "parent", ConversationDigest: "conv", TreeCommit: "tree", TraceCommit: "trace", ResolvedAt: now}, CeilingDigest: "ceiling", Principal: "alice", Actor: "parent", IdempotencyKey: "restart-admission"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a, ok, getErr := c.Registry.Get(h.AdmissionID)
		if getErr != nil || !ok {
			t.Fatal(getErr)
		}
		if a.Status == retained.StatusCompleted {
			if a.Generation != 2 || launcher.calls.Load() != 2 {
				t.Fatalf("admission=%#v calls=%d", a, launcher.calls.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("supervised child did not recover")
}
func TestRetainedCoordinatorComposesAdmissionBudgetAndFollowup(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	budgets := brokerbudget.New(store)
	_, err = budgets.CreateAccount(context.Background(), "root", "", brokerbudget.Limits{Tokens: 100}, "alice", "broker", "account")
	if err != nil {
		t.Fatal(err)
	}
	policy := mailbox.RelationPolicy{"parent": {"child": true}}
	done := make(chan retained.Admission, 1)
	c := New(retained.New(store), budgets, mailbox.New(store, policy), fakeLauncher{done})
	now := time.Now()
	h, err := c.SpawnRetained(context.Background(), LaunchRequest{AccountID: "root", Budget: brokerbudget.Limits{Tokens: 20}, Principal: "alice", Actor: "parent", IdempotencyKey: "spawn", Admission: retained.Request{ParentSessionID: "parent", ChildSessionID: "child", Purpose: "worker", Fork: retained.ForkPoint{SourceSessionID: "parent", ConversationDigest: "conv", TreeCommit: "tree", TraceCommit: "trace", ResolvedAt: now}, CeilingDigest: "ceiling", Principal: "alice", Actor: "parent", IdempotencyKey: "admission"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("child did not launch")
	}
	msg, err := c.FollowUp(context.Background(), "parent", h, json.RawMessage(`{"prompt":"continue"}`), "alice", "parent", "followup")
	if err != nil || msg.ReceiverSession != "child" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		a, ok, err := c.Registry.Get(h.AdmissionID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if a.Status == retained.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status=%s", a.Status)
		}
		time.Sleep(time.Millisecond)
	}
}
