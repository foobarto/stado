// Package mailbox implements durable, per-message agent delivery state.
package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/foobarto/stado/internal/broker/wal"
	"sort"
	"sync"
	"time"
)

const storeName = "agent_mailbox"

var (
	ErrBackpressure  = errors.New("mailbox backpressure")
	ErrUnauthorized  = errors.New("mailbox address unauthorized")
	ErrStaleDelivery = errors.New("stale mailbox delivery")
)

type Kind string

const (
	KindRequest  Kind = "request"
	KindReply    Kind = "reply"
	KindProgress Kind = "progress"
)

type State string

const (
	StateAvailable     State = "available"
	StateDelivered     State = "delivered"
	StateAcked         State = "acked"
	StateExpired       State = "expired"
	StateDeadLetter    State = "dead_letter"
	StateUndeliverable State = "undeliverable"
)

type Message struct {
	ID                 string          `json:"id"`
	SenderSession      string          `json:"sender_session"`
	SenderGeneration   uint64          `json:"sender_generation"`
	ReceiverSession    string          `json:"receiver_session"`
	SenderSequence     uint64          `json:"sender_sequence"`
	Kind               Kind            `json:"kind"`
	CorrelationID      string          `json:"correlation_id,omitempty"`
	CausationID        string          `json:"causation_id,omitempty"`
	ContentType        string          `json:"content_type"`
	Payload            json.RawMessage `json:"payload"`
	Provenance         string          `json:"provenance"`
	CreatedAt          time.Time       `json:"created_at"`
	ExpiresAt          time.Time       `json:"expires_at,omitempty"`
	State              State           `json:"state"`
	DeliveryGeneration uint64          `json:"delivery_generation"`
	Attempts           int             `json:"attempts"`
	ReceiverInputID    string          `json:"receiver_input_id,omitempty"`
}
type SendRequest struct {
	MessageID, SenderSession, ReceiverSession string
	SenderGeneration                          uint64
	Kind                                      Kind
	CorrelationID, CausationID, ContentType   string
	Payload                                   json.RawMessage
	ExpiresAt                                 time.Time
	Principal, Actor, IdempotencyKey          string
}
type Policy interface {
	MayAddress(sender, receiver string) bool
}
type RelationPolicy map[string]map[string]bool

func (p RelationPolicy) MayAddress(s, r string) bool { return p[s] != nil && p[s][r] }

// DynamicRelationPolicy is broker-owned topology state for retained children.
type DynamicRelationPolicy struct {
	mu    sync.RWMutex
	edges map[string]map[string]bool
}

func NewDynamicRelationPolicy() *DynamicRelationPolicy {
	return &DynamicRelationPolicy{edges: map[string]map[string]bool{}}
}
func (p *DynamicRelationPolicy) Allow(a, b string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.edges[a] == nil {
		p.edges[a] = map[string]bool{}
	}
	p.edges[a][b] = true
}
func (p *DynamicRelationPolicy) MayAddress(a, b string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.edges[a] != nil && p.edges[a][b]
}

type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}
type Broker struct {
	mu              sync.Mutex
	wal             appender
	policy          Policy
	now             func() time.Time
	MaxPending      int
	MaxPayloadBytes int
	MaxAttempts     int
}

func New(w appender, p Policy) *Broker {
	return &Broker{wal: w, policy: p, now: time.Now, MaxPending: 64, MaxPayloadBytes: 64 << 10, MaxAttempts: 3}
}
func (b *Broker) Send(ctx context.Context, req SendRequest) (Message, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.policy == nil || !b.policy.MayAddress(req.SenderSession, req.ReceiverSession) {
		return Message{}, ErrUnauthorized
	}
	if req.Kind != KindRequest && req.Kind != KindReply && req.Kind != KindProgress {
		return Message{}, errors.New("invalid data message kind")
	}
	if len(req.Payload) == 0 || !json.Valid(req.Payload) || len(req.Payload) > b.MaxPayloadBytes {
		return Message{}, errors.New("invalid or oversized message payload")
	}
	messages, err := fold(b.wal.Records())
	if err != nil {
		return Message{}, err
	}
	pending := 0
	var maxSeq uint64
	for _, m := range messages {
		if m.ReceiverSession == req.ReceiverSession && (m.State == StateAvailable || m.State == StateDelivered) {
			pending++
		}
		if m.SenderSession == req.SenderSession && m.SenderGeneration == req.SenderGeneration && m.ReceiverSession == req.ReceiverSession && m.SenderSequence > maxSeq {
			maxSeq = m.SenderSequence
		}
	}
	if pending >= b.MaxPending {
		return Message{}, ErrBackpressure
	}
	if req.MessageID == "" {
		req.MessageID = mint("msg_")
	}
	if old, ok := messages[req.MessageID]; ok {
		return old, nil
	}
	m := Message{ID: req.MessageID, SenderSession: req.SenderSession, SenderGeneration: req.SenderGeneration, ReceiverSession: req.ReceiverSession, SenderSequence: maxSeq + 1, Kind: req.Kind, CorrelationID: req.CorrelationID, CausationID: req.CausationID, ContentType: req.ContentType, Payload: append(json.RawMessage(nil), req.Payload...), Provenance: "untrusted", CreatedAt: b.now().UTC(), ExpiresAt: req.ExpiresAt, State: StateAvailable}
	if m.ContentType == "" {
		m.ContentType = "application/json"
	}
	if err := appendEvent(b.wal, m, req.Principal, req.Actor, req.IdempotencyKey, "message.accepted"); err != nil {
		return Message{}, err
	}
	return m, nil
}
func (b *Broker) Deliver(ctx context.Context, receiver, principal, actor, idem string) (Message, bool, error) {
	return b.DeliverFrom(ctx, receiver, "", principal, actor, idem)
}

// DeliverFrom selects one sender when a parent is monitoring several retained
// children. Empty sender preserves ordinary receiver-wide delivery.
func (b *Broker) DeliverFrom(ctx context.Context, receiver, sender, principal, actor, idem string) (Message, bool, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	messages, err := fold(b.wal.Records())
	if err != nil {
		return Message{}, false, err
	}
	now := b.now().UTC()
	var candidates []Message
	for _, m := range messages {
		if m.ReceiverSession == receiver && (sender == "" || m.SenderSession == sender) && m.State == StateAvailable {
			candidates = append(candidates, m)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	for _, m := range candidates {
		if !m.ExpiresAt.IsZero() && !m.ExpiresAt.After(now) {
			m.State = StateExpired
			if err := appendEvent(b.wal, m, principal, actor, idem+":"+m.ID+":expire", "message.expired"); err != nil {
				return Message{}, false, err
			}
			continue
		}
		m.State = StateDelivered
		m.DeliveryGeneration++
		m.Attempts++
		if m.Attempts > b.MaxAttempts {
			m.State = StateDeadLetter
			if err := appendEvent(b.wal, m, principal, actor, idem+":"+m.ID+":dead", "message.dead_lettered"); err != nil {
				return Message{}, false, err
			}
			continue
		}
		if err := appendEvent(b.wal, m, principal, actor, idem+":"+m.ID+":deliver", "message.delivered"); err != nil {
			return Message{}, false, err
		}
		return m, true, nil
	}
	return Message{}, false, nil
}

// CommitReceiverInput atomically creates the unique input-turn record and ACKs.
func (b *Broker) CommitReceiverInput(ctx context.Context, receiver, messageID string, generation uint64, inputID, principal, actor, idem string) (Message, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	messages, err := fold(b.wal.Records())
	if err != nil {
		return Message{}, err
	}
	m, ok := messages[messageID]
	if !ok || m.ReceiverSession != receiver {
		return Message{}, errors.New("message not found")
	}
	if m.State == StateAcked && m.ReceiverInputID == inputID {
		return m, nil
	}
	if m.State != StateDelivered || m.DeliveryGeneration != generation {
		return Message{}, ErrStaleDelivery
	}
	m.State = StateAcked
	m.ReceiverInputID = inputID
	data, _ := json.Marshal(m)
	input, _ := json.Marshal(struct {
		MessageID          string `json:"message_id"`
		DeliveryGeneration uint64 `json:"delivery_generation"`
		InputID            string `json:"input_id"`
	}{m.ID, generation, inputID})
	_, err = b.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: "session_input", Type: "receiver_input.committed", Session: receiver, Data: input}, {Store: storeName, Type: "message.acked", Session: receiver, Data: data}}})
	return m, err
}
func fold(records []wal.Record) (map[string]Message, error) {
	out := map[string]Message{}
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			var m Message
			if err := json.Unmarshal(ev.Data, &m); err != nil {
				return nil, err
			}
			out[m.ID] = m
		}
	}
	return out, nil
}

// PendingCount reports available or delivered data messages without exposing
// their untrusted payloads. It is used by bounded application facts and
// operator status; recommendation policy remains outside the broker.
func PendingCount(w interface{ Records() []wal.Record }, receiver string) (int, error) {
	messages, err := fold(w.Records())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range messages {
		if m.ReceiverSession == receiver && (m.State == StateAvailable || m.State == StateDelivered) {
			n++
		}
	}
	return n, nil
}
func appendEvent(w appender, m Message, principal, actor, idem, typ string) error {
	data, _ := json.Marshal(m)
	_, err := w.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: storeName, Type: typ, Session: m.ReceiverSession, Data: data}}})
	return err
}
func mint(prefix string) string {
	var x [12]byte
	_, _ = rand.Read(x[:])
	return prefix + hex.EncodeToString(x[:])
}
