package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/foobarto/stado/internal/broker/wal"
	"testing"
	"time"
)

func TestDurableDeliveryAndAtomicInputAck(t *testing.T) {
	dir := t.TempDir()
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	policy := RelationPolicy{"parent": {"child": true}, "child": {"parent": true}}
	b := New(store, policy)
	ctx := context.Background()
	m, err := b.Send(ctx, SendRequest{MessageID: "m1", SenderSession: "parent", SenderGeneration: 1, ReceiverSession: "child", Kind: KindRequest, Payload: json.RawMessage(`{"query":"work"}`), Principal: "alice", Actor: "parent", IdempotencyKey: "send"})
	if err != nil || m.SenderSequence != 1 {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	delivered, ok, err := b.Deliver(ctx, "child", "alice", "broker", "deliver")
	if err != nil || !ok || delivered.DeliveryGeneration != 1 {
		t.Fatalf("delivered=%+v ok=%v err=%v", delivered, ok, err)
	}
	acked, err := b.CommitReceiverInput(ctx, "child", m.ID, 1, "turn-input-1", "alice", "broker", "ack")
	if err != nil || acked.State != StateAcked {
		t.Fatalf("acked=%+v err=%v", acked, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b = New(store, policy)
	if _, ok, err := b.Deliver(ctx, "child", "alice", "broker", "again"); err != nil || ok {
		t.Fatalf("acked message redelivered ok=%v err=%v", ok, err)
	}
}

func TestMailboxAuthorizationBackpressureExpiryAndStaleAck(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	b := New(store, RelationPolicy{"p": {"c": true}})
	b.now = func() time.Time { return now }
	b.MaxPending = 1
	ctx := context.Background()
	if _, err := b.Send(ctx, SendRequest{SenderSession: "x", ReceiverSession: "c", Kind: KindRequest, Payload: json.RawMessage(`{}`), Principal: "a", Actor: "x", IdempotencyKey: "unauth"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	m, err := b.Send(ctx, SendRequest{SenderSession: "p", ReceiverSession: "c", Kind: KindRequest, Payload: json.RawMessage(`{}`), ExpiresAt: now.Add(-time.Second), Principal: "a", Actor: "p", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(ctx, SendRequest{SenderSession: "p", ReceiverSession: "c", Kind: KindRequest, Payload: json.RawMessage(`{}`), Principal: "a", Actor: "p", IdempotencyKey: "two"}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("got %v", err)
	}
	if _, ok, err := b.Deliver(ctx, "c", "a", "broker", "delivery"); err != nil || ok {
		t.Fatalf("expired delivery ok=%v err=%v", ok, err)
	}
	if _, err := b.CommitReceiverInput(ctx, "c", m.ID, 1, "input", "a", "broker", "ack"); !errors.Is(err, ErrStaleDelivery) {
		t.Fatalf("got %v", err)
	}
}

func TestMailboxReplayRequiresExactMessageAndDelivery(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker := New(store, RelationPolicy{"p": {"c": true}, "p2": {"c2": true}})
	request := SendRequest{
		MessageID: "stable", SenderSession: "p", SenderGeneration: 1,
		ReceiverSession: "c", Kind: KindRequest, Payload: json.RawMessage(`{"prompt":"one"}`),
		Principal: "alice", Actor: "parent", IdempotencyKey: "send",
	}
	first, err := broker.Send(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := broker.Send(t.Context(), request)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("send replay=%+v err=%v", replay, err)
	}
	request.Payload = json.RawMessage(`{"prompt":"changed"}`)
	if _, err := broker.Send(t.Context(), request); err == nil {
		t.Fatal("message ID accepted conflicting payload")
	}
	delivered, found, err := broker.DeliverFrom(t.Context(), "c", "p", "alice", "broker", "deliver-stable")
	if err != nil || !found {
		t.Fatalf("delivery=%+v found=%v err=%v", delivered, found, err)
	}
	replayed, found, err := broker.DeliverFrom(t.Context(), "c", "p", "alice", "broker", "deliver-stable")
	if err != nil || !found || replayed.DeliveryGeneration != delivered.DeliveryGeneration {
		t.Fatalf("delivery replay=%+v found=%v err=%v", replayed, found, err)
	}
	if _, _, err := broker.DeliverFrom(t.Context(), "c2", "p2", "alice", "broker", "deliver-stable"); err == nil {
		t.Fatal("delivery idempotency key accepted conflicting endpoints")
	}
}
