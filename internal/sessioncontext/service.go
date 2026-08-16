package sessioncontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/broker/wal"
)

const sessionStore = "session_context"

var ErrVersion = errors.New("session context version conflict")

type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

type Service struct {
	mu  sync.Mutex
	wal appender
	now func() time.Time
}

func New(store appender) *Service { return &Service{wal: store, now: time.Now} }

func (s *Service) PatchModel(ctx context.Context, session, principal, actor, idem string, expected uint64, patch StatePatch) (State, error) {
	_ = ctx
	if err := validateModelPatch(patch); err != nil {
		return State{}, err
	}
	return s.patchState(session, principal, actor, idem, expected, "model", patch, HostPatch{})
}

func (s *Service) PatchHost(ctx context.Context, session, principal, actor, idem string, expected uint64, patch HostPatch) (State, error) {
	_ = ctx
	return s.patchState(session, principal, actor, idem, expected, "host", StatePatch{}, patch)
}

// EnsureObjective records the first non-empty host objective atomically. A
// retry or later caller observes the existing objective and does not replace
// it. This avoids a State/PatchHost read-modify-write race at broker RPC
// boundaries.
func (s *Service) EnsureObjective(ctx context.Context, session, objective, principal, actor, idem string) (State, error) {
	_ = ctx
	objective = strings.TrimSpace(objective)
	if strings.TrimSpace(session) == "" || objective == "" {
		return State{}, errors.New("session context session and objective required")
	}
	if err := bounded(objective, 4096); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := foldState(s.wal.Records(), session)
	if err != nil {
		return State{}, err
	}
	if state.Objective != "" {
		return state, nil
	}
	if state.SessionID == "" {
		state.SessionID = session
		state.Assertions = map[string]string{}
	}
	state.Objective = objective
	state.Version++
	state.UpdatedAt = s.now().UTC()
	b, _ := json.Marshal(state)
	_, err = s.wal.Append(tx(session, principal, actor, idem, "state.updated", b))
	return state, err
}

func (s *Service) patchState(session, principal, actor, idem string, expected uint64, writer string, model StatePatch, host HostPatch) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := foldState(s.wal.Records(), session)
	if err != nil {
		return State{}, err
	}
	if state.Version != expected {
		return State{}, ErrVersion
	}
	if state.SessionID == "" {
		state.SessionID = session
		state.Assertions = map[string]string{}
	}
	if writer == "model" {
		state.CurrentTask, state.Blockers, state.NextStep = model.CurrentTask, append([]string(nil), model.Blockers...), model.NextStep
		state.Assertions["current_task"] = "model_hypothesis"
		state.Assertions["blockers"] = "model_hypothesis"
		state.Assertions["next_step"] = "model_hypothesis"
	} else {
		if host.Objective != nil {
			state.Objective = *host.Objective
		}
		if host.Capabilities != nil {
			state.Capabilities = append([]string(nil), host.Capabilities...)
		}
		if host.ActiveChildren != nil {
			state.ActiveChildren = append([]string(nil), host.ActiveChildren...)
		}
		if host.Verification != nil {
			state.Verification = *host.Verification
		}
		if host.TokenUsage != nil {
			state.TokenUsage = *host.TokenUsage
		}
		if host.CostMicros != nil {
			state.CostMicros = *host.CostMicros
		}
	}
	state.Version++
	state.UpdatedAt = s.now().UTC()
	b, _ := json.Marshal(state)
	_, err = s.wal.Append(tx(session, principal, actor, idem, "state.updated", b))
	return state, err
}

func (s *Service) State(session string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return foldState(s.wal.Records(), session)
}

func foldState(records []wal.Record, session string) (State, error) {
	var state State
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != sessionStore || ev.Session != session || ev.Type != "state.updated" {
				continue
			}
			var next State
			if err := json.Unmarshal(ev.Data, &next); err != nil {
				return State{}, err
			}
			if next.Version != state.Version+1 {
				return State{}, ErrVersion
			}
			state = next
		}
	}
	return state, nil
}

func (s *Service) ProposeDecision(ctx context.Context, d Decision, principal, actor, idem string) (Decision, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.SessionID == "" || strings.TrimSpace(d.Choice) == "" || strings.TrimSpace(d.Context) == "" {
		return Decision{}, errors.New("decision session, context, and choice required")
	}
	if err := bounded(d.Context, 4096); err != nil {
		return Decision{}, err
	}
	if err := bounded(d.Choice, 4096); err != nil {
		return Decision{}, err
	}
	if d.ID == "" {
		d.ID = mint("dec_")
	}
	d.Version = 1
	d.Status = DecisionProposed
	d.CreatedBy = actor
	d.CreatedAt = s.now().UTC()
	d.UpdatedAt = d.CreatedAt
	if _, ok, _ := foldDecision(s.wal.Records(), d.SessionID, d.ID); ok {
		return Decision{}, errors.New("decision exists")
	}
	b, _ := json.Marshal(d)
	_, err := s.wal.Append(tx(d.SessionID, principal, actor, idem, "decision.proposed", b))
	return d, err
}

func (s *Service) SetDecisionStatus(ctx context.Context, session, id string, expected uint64, status DecisionStatus, principal, actor, idem string) (Decision, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok, err := foldDecision(s.wal.Records(), session, id)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return Decision{}, errors.New("decision not found")
	}
	if d.Version != expected {
		return Decision{}, ErrVersion
	}
	if status != DecisionAccepted && status != DecisionRejected && status != DecisionSuperseded {
		return Decision{}, errors.New("invalid decision status")
	}
	d.Version++
	d.Status = status
	d.UpdatedAt = s.now().UTC()
	b, _ := json.Marshal(d)
	_, err = s.wal.Append(tx(session, principal, actor, idem, "decision.status", b))
	return d, err
}

func foldDecision(records []wal.Record, session, id string) (Decision, bool, error) {
	var d Decision
	ok := false
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != sessionStore || ev.Session != session || !strings.HasPrefix(ev.Type, "decision.") {
				continue
			}
			var x Decision
			if err := json.Unmarshal(ev.Data, &x); err != nil {
				return Decision{}, false, err
			}
			if x.ID == id {
				if ok && x.Version != d.Version+1 {
					return Decision{}, false, ErrVersion
				}
				d = x
				ok = true
			}
		}
	}
	return d, ok, nil
}

func (s *Service) Journal(session string, max int) ([]JournalEntry, error) {
	if max <= 0 {
		max = 200
	}
	var out []JournalEntry
	for _, rec := range s.wal.Records() {
		for _, ev := range rec.Transaction.Events {
			if ev.Session != session {
				continue
			}
			t, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
			if err != nil {
				return nil, err
			}
			out = append(out, JournalEntry{Sequence: rec.Sequence, Time: t, Type: ev.Type, Actor: rec.Transaction.Actor, Ref: rec.Digest})
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

func validateModelPatch(p StatePatch) error {
	if err := bounded(p.CurrentTask, 1024); err != nil {
		return err
	}
	if err := bounded(p.NextStep, 1024); err != nil {
		return err
	}
	if len(p.Blockers) > 16 {
		return errors.New("too many blockers")
	}
	for _, b := range p.Blockers {
		if err := bounded(b, 512); err != nil {
			return err
		}
	}
	return nil
}
func bounded(v string, n int) error {
	if utf8.RuneCountInString(v) > n {
		return fmt.Errorf("field exceeds %d characters", n)
	}
	return nil
}
func tx(session, principal, actor, idem, typ string, data []byte) wal.Transaction {
	return wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: sessionStore, Type: typ, Session: session, Data: data}}}
}
func mint(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
