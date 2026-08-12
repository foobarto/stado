package authority

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

func TestGrantExactActionOneUseAndPersistence(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issuer, consumer := New(store)
	action := Action{Kind: "artifact.activate", Principal: "alice", ScopeDigest: "scope", PayloadDigest: "body", Version: 2}
	grant, err := issuer.Issue(context.Background(), action, "operator:alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.PrepareConsume(context.Background(), grant.ID, Action{Kind: action.Kind, Principal: "alice", ScopeDigest: "scope", PayloadDigest: "changed", Version: 2}, "use-1"); !errors.Is(err, ErrGrantMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	event, err := consumer.PrepareConsume(context.Background(), grant.ID, action, "use-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(wal.Transaction{ID: "approved-tx", IdempotencyKey: "approved", Principal: "alice", Actor: "broker", Events: []wal.Event{event, {Store: "test", Type: "approved", Data: []byte(`{}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.PrepareConsume(context.Background(), grant.ID, action, "different-use"); !errors.Is(err, ErrGrantConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if event, err := consumer.PrepareConsume(context.Background(), grant.ID, action, "use-1"); err != nil || event.Type != "" {
		t.Fatalf("idempotent retry: event=%+v err=%v", event, err)
	}
}

func TestExpiredGrant(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issuer, consumer := New(store)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	issuer.state.now = func() time.Time { return now }
	action := Action{Kind: "x", Principal: "alice"}
	grant, err := issuer.Issue(context.Background(), action, "operator", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	consumer.state.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := consumer.PrepareConsume(context.Background(), grant.ID, action, "use"); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("got %v", err)
	}
}
