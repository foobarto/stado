// Package retained implements the durable admission and lifecycle projection
// for resumable child sessions.
package retained

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

const storeName = "retained_child"

var (
	ErrNotFound  = errors.New("retained child not found")
	ErrLifecycle = errors.New("invalid retained child lifecycle transition")
	ErrLease     = errors.New("stale retained child lease")
)

type Status string

const (
	StatusAdmitted  Status = "admitted"
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusIdle      Status = "idle"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusDown      Status = "down"
	StatusArchived  Status = "archived"
	StatusDeleted   Status = "deleted"
)

type ForkPoint struct {
	SourceSessionID    string    `json:"source_session_id"`
	SourceGeneration   uint64    `json:"source_generation"`
	CommittedTurn      int       `json:"committed_turn"`
	ConversationDigest string    `json:"conversation_digest"`
	CompactionLineage  string    `json:"compaction_lineage,omitempty"`
	TreeCommit         string    `json:"tree_commit"`
	TraceCommit        string    `json:"trace_commit"`
	EventSequence      uint64    `json:"event_sequence"`
	ResolvedAt         time.Time `json:"resolved_at"`
}
type Admission struct {
	ID                  string    `json:"id"`
	ParentSessionID     string    `json:"parent_session_id"`
	ChildSessionID      string    `json:"child_session_id"`
	Generation          uint64    `json:"generation"`
	BrokerEpoch         uint64    `json:"broker_epoch"`
	RuntimeNonce        string    `json:"runtime_nonce"`
	Purpose             string    `json:"purpose"`
	Fork                ForkPoint `json:"fork_point"`
	CeilingDigest       string    `json:"ceiling_digest"`
	Model               string    `json:"model"`
	ToolProfile         string    `json:"tool_profile"`
	BudgetReservationID string    `json:"budget_reservation_id"`
	Status              Status    `json:"status"`
	LeaseEpoch          uint64    `json:"lease_epoch"`
	LeaseUntil          time.Time `json:"lease_until,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}
type Request struct {
	AdmissionID, ParentSessionID, ChildSessionID, Purpose, CeilingDigest, Model, ToolProfile, BudgetReservationID, Principal, Actor, IdempotencyKey string
	Fork                                                                                                                                            ForkPoint
}
type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
	Epoch() uint64
}
type Registry struct {
	mu  sync.Mutex
	wal appender
	now func() time.Time
}

func New(w appender) *Registry { return &Registry{wal: w, now: time.Now} }
func (r *Registry) Admit(ctx context.Context, req Request) (Admission, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.ParentSessionID == "" || req.ChildSessionID == "" || req.Purpose == "" || req.CeilingDigest == "" || req.BudgetReservationID == "" {
		return Admission{}, errors.New("incomplete retained admission")
	}
	if err := ValidateForkPoint(req.Fork); err != nil {
		return Admission{}, err
	}
	if req.AdmissionID == "" {
		req.AdmissionID = mint("adm_")
	}
	if _, ok, err := foldOne(r.wal.Records(), req.AdmissionID); err != nil {
		return Admission{}, err
	} else if ok {
		return Admission{}, errors.New("admission exists")
	}
	now := r.now().UTC()
	a := Admission{ID: req.AdmissionID, ParentSessionID: req.ParentSessionID, ChildSessionID: req.ChildSessionID, Generation: 1, BrokerEpoch: r.wal.Epoch(), RuntimeNonce: mint("nonce_"), Purpose: req.Purpose, Fork: req.Fork, CeilingDigest: req.CeilingDigest, Model: req.Model, ToolProfile: req.ToolProfile, BudgetReservationID: req.BudgetReservationID, Status: StatusAdmitted, CreatedAt: now, UpdatedAt: now}
	if err := appendEvent(r.wal, a, req.Principal, req.Actor, req.IdempotencyKey, "child.admitted"); err != nil {
		return Admission{}, err
	}
	return a, nil
}
func (r *Registry) Transition(ctx context.Context, id string, from, to Status, leaseEpoch uint64, principal, actor, idem string) (Admission, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok, err := foldOne(r.wal.Records(), id)
	if err != nil {
		return Admission{}, err
	}
	if !ok {
		return Admission{}, ErrNotFound
	}
	if a.Status != from || !validTransition(from, to) {
		return Admission{}, ErrLifecycle
	}
	if leaseEpoch != 0 && leaseEpoch != a.LeaseEpoch {
		return Admission{}, ErrLease
	}
	a.Status = to
	a.UpdatedAt = r.now().UTC()
	if err := appendEvent(r.wal, a, principal, actor, idem, "child.transitioned"); err != nil {
		return Admission{}, err
	}
	return a, nil
}
func (r *Registry) AcquireLease(ctx context.Context, id, runtimeNonce, principal, actor, idem string, ttl time.Duration) (Admission, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if ttl <= 0 {
		return Admission{}, errors.New("lease ttl required")
	}
	a, ok, err := foldOne(r.wal.Records(), id)
	if err != nil {
		return Admission{}, err
	}
	if !ok {
		return Admission{}, ErrNotFound
	}
	now := r.now().UTC()
	if a.RuntimeNonce != runtimeNonce {
		return Admission{}, ErrLease
	}
	if !a.LeaseUntil.IsZero() && a.LeaseUntil.After(now) && a.BrokerEpoch == r.wal.Epoch() {
		return Admission{}, ErrLease
	}
	a.LeaseEpoch++
	a.BrokerEpoch = r.wal.Epoch()
	a.LeaseUntil = now.Add(ttl)
	a.UpdatedAt = now
	if err := appendEvent(r.wal, a, principal, actor, idem, "child.lease_acquired"); err != nil {
		return Admission{}, err
	}
	return a, nil
}

// RestartGeneration fences every prior runtime before a supervised relaunch.
func (r *Registry) RestartGeneration(ctx context.Context, id, principal, actor, idem string) (Admission, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok, err := foldOne(r.wal.Records(), id)
	if err != nil {
		return Admission{}, err
	}
	if !ok {
		return Admission{}, ErrNotFound
	}
	if a.Status != StatusDown {
		return Admission{}, ErrLifecycle
	}
	a.Generation++
	a.RuntimeNonce = mint("nonce_")
	a.LeaseEpoch = 0
	a.LeaseUntil = time.Time{}
	a.Status = StatusAdmitted
	a.UpdatedAt = r.now().UTC()
	if err := appendEvent(r.wal, a, principal, actor, idem, "child.generation_restarted"); err != nil {
		return Admission{}, err
	}
	return a, nil
}
func (r *Registry) Get(id string) (Admission, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return foldOne(r.wal.Records(), id)
}
func (r *Registry) List() ([]Admission, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := map[string]Admission{}
	for _, rec := range r.wal.Records() {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			var a Admission
			if err := json.Unmarshal(ev.Data, &a); err != nil {
				return nil, err
			}
			byID[a.ID] = a
		}
	}
	out := make([]Admission, 0, len(byID))
	for _, a := range byID {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func foldOne(records []wal.Record, id string) (Admission, bool, error) {
	var a Admission
	ok := false
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			var x Admission
			if err := json.Unmarshal(ev.Data, &x); err != nil {
				return Admission{}, false, err
			}
			if x.ID == id {
				a = x
				ok = true
			}
		}
	}
	return a, ok, nil
}
func appendEvent(w appender, a Admission, principal, actor, idem, typ string) error {
	b, _ := json.Marshal(a)
	_, err := w.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: storeName, Type: typ, Session: a.ChildSessionID, Data: b}}})
	return err
}
func validTransition(f, t Status) bool {
	allowed := map[Status][]Status{StatusAdmitted: {StatusStarting, StatusCancelled}, StatusStarting: {StatusRunning, StatusFailed, StatusCancelled}, StatusRunning: {StatusIdle, StatusCompleted, StatusFailed, StatusCancelled, StatusDown}, StatusIdle: {StatusRunning, StatusArchived, StatusDeleted}, StatusCompleted: {StatusRunning, StatusArchived, StatusDeleted}, StatusFailed: {StatusDown, StatusArchived}, StatusCancelled: {StatusDown, StatusArchived}, StatusDown: {StatusStarting, StatusArchived}, StatusArchived: {StatusDeleted}}
	for _, x := range allowed[f] {
		if x == t {
			return true
		}
	}
	return false
}
func mint(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
func ValidateForkPoint(f ForkPoint) error {
	if strings.TrimSpace(f.SourceSessionID) == "" || f.ConversationDigest == "" || f.TreeCommit == "" || f.TraceCommit == "" || f.ResolvedAt.IsZero() {
		return fmt.Errorf("incomplete immutable fork point")
	}
	return nil
}
