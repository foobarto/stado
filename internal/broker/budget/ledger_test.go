package budget

import (
	"context"
	"errors"
	"github.com/foobarto/stado/internal/broker/wal"
	"testing"
)

func TestRecursiveReservationCannotEscapeRoot(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l := New(store)
	ctx := context.Background()
	root, err := l.CreateAccount(ctx, "root", "", Limits{Tokens: 100}, "alice", "broker", "root")
	if err != nil {
		t.Fatal(err)
	}
	child, err := l.CreateAccount(ctx, "child", root.ID, Limits{Tokens: 80}, "alice", "broker", "child")
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Reserve(ctx, child.ID, Limits{Tokens: 70}, "alice", "broker", "reserve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, root.ID, Limits{Tokens: 40}, "alice", "broker", "escape"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("got %v", err)
	}
	if _, err := l.Commit(ctx, r.ID, Limits{Tokens: 71}, "alice", "provider", "over"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("got %v", err)
	}
	if _, err := l.Commit(ctx, r.ID, Limits{Tokens: 60}, "alice", "provider", "commit"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Release(ctx, r.ID, "alice", "broker", "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, root.ID, Limits{Tokens: 100}, "alice", "broker", "after-release"); err != nil {
		t.Fatal(err)
	}
}
