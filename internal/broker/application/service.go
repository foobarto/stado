// Package application implements broker-owned durable state primitives for
// WASM lifecycle applications (EP-0064). It owns storage and deterministic
// projection only; plugin admission, capability checks, RPC, and agent-loop
// enforcement remain at their respective broker/runtime boundaries.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/broker/wal"
)

const (
	storeName    = "lifecycle_application"
	eventSchema  = 1
	defaultLimit = 100
)

var (
	ErrInvalid             = errors.New("lifecycle application: invalid input")
	ErrNotFound            = errors.New("lifecycle application: record not found")
	ErrScope               = errors.New("lifecycle application: authority scope mismatch")
	ErrVersion             = errors.New("lifecycle application: version conflict")
	ErrTerminal            = errors.New("lifecycle application: record is terminal")
	ErrNotDue              = errors.New("lifecycle application: record is not due")
	ErrLeaseExpired        = errors.New("lifecycle application: hold lease expired")
	ErrLimit               = errors.New("lifecycle application: bounded state limit reached")
	ErrIdempotencyConflict = errors.New("lifecycle application: idempotency key reused with different request")
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// Authority is resolved and authenticated before this package is called. It is
// deliberately separate from every guest-controlled request shape.
type Authority struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	PluginID   string `json:"plugin_id"`
	Principal  string `json:"principal"`
	Actor      string `json:"actor"`
}

type Limits struct {
	MaxIDBytes               int
	MaxTextBytes             int
	MaxDataBytes             int
	MaxEvidenceRefs          int
	MaxEvidenceRefBytes      int
	MaxJournalEntries        int
	MaxHoldRecords           int
	MaxActiveHolds           int
	MaxHoldTTL               time.Duration
	MaxControlRequests       int
	MaxCompletions           int
	MaxWorkerRuns            int
	MaxWorkerPromptBytes     int
	MaxOperatorInputs        int
	MaxPendingOperatorInputs int
	MaxOperatorInputBytes    int
	MaxTimerRecords          int
	MaxActiveTimers          int
	MaxTimerPayloadBytes     int
	MaxTimerHorizon          time.Duration
	MaxEventRecords          int
	MaxProjectionItems       int
}

func DefaultLimits() Limits {
	return Limits{
		MaxIDBytes:               256,
		MaxTextBytes:             4 << 10,
		MaxDataBytes:             64 << 10,
		MaxEvidenceRefs:          32,
		MaxEvidenceRefBytes:      1024,
		MaxJournalEntries:        4096,
		MaxHoldRecords:           1024,
		MaxActiveHolds:           32,
		MaxHoldTTL:               time.Hour,
		MaxControlRequests:       1024,
		MaxCompletions:           1024,
		MaxWorkerRuns:            1024,
		MaxWorkerPromptBytes:     16 << 10,
		MaxOperatorInputs:        256,
		MaxPendingOperatorInputs: MaxPendingOperatorInputs,
		MaxOperatorInputBytes:    MaxOperatorInputBytes,
		MaxTimerRecords:          4096,
		MaxActiveTimers:          256,
		MaxTimerPayloadBytes:     64 << 10,
		MaxTimerHorizon:          30 * 24 * time.Hour,
		MaxEventRecords:          16384,
		MaxProjectionItems:       256,
	}
}

// JournalAppend is plugin application data. Session, generation, plugin
// namespace, principal, actor, sequence, and timestamps come from Authority.
type JournalAppend struct {
	ID           string          `json:"id,omitempty"`
	RunID        string          `json:"run_id"`
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	Data         json.RawMessage `json:"data,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
}

type JournalEntry struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	Generation   uint64          `json:"generation"`
	PluginID     string          `json:"plugin_id"`
	RunID        string          `json:"run_id"`
	Sequence     uint64          `json:"sequence"`
	WALSequence  uint64          `json:"wal_sequence"`
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	Data         json.RawMessage `json:"data,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type HoldStatus string

const (
	HoldActive   HoldStatus = "active"
	HoldReleased HoldStatus = "released"
	HoldExpired  HoldStatus = "expired"
)

type HoldAcquire struct {
	ID              string        `json:"id,omitempty"`
	RunID           string        `json:"run_id"`
	ExpectedVersion uint64        `json:"expected_version"`
	ReasonCode      string        `json:"reason_code"`
	Reason          string        `json:"reason,omitempty"`
	TTL             time.Duration `json:"ttl"`
}

type HoldCAS struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

type Hold struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	Generation  uint64     `json:"generation"`
	PluginID    string     `json:"plugin_id"`
	Owner       string     `json:"owner"`
	RunID       string     `json:"run_id"`
	Version     uint64     `json:"version"`
	WALSequence uint64     `json:"wal_sequence"`
	ReasonCode  string     `json:"reason_code"`
	Reason      string     `json:"reason,omitempty"`
	Status      HoldStatus `json:"status"`
	LeaseUntil  time.Time  `json:"lease_until"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type ControlKind string

const (
	ControlPause ControlKind = "pause"
	ControlStop  ControlKind = "stop"
)

type ControlInput struct {
	ID           string   `json:"id,omitempty"`
	RunID        string   `json:"run_id"`
	ReasonCode   string   `json:"reason_code"`
	Reason       string   `json:"reason,omitempty"`
	HoldID       string   `json:"hold_id,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

type ControlRequest struct {
	ID           string      `json:"id"`
	SessionID    string      `json:"session_id"`
	Generation   uint64      `json:"generation"`
	PluginID     string      `json:"plugin_id"`
	RunID        string      `json:"run_id"`
	Kind         ControlKind `json:"kind"`
	ReasonCode   string      `json:"reason_code"`
	Reason       string      `json:"reason,omitempty"`
	HoldID       string      `json:"hold_id,omitempty"`
	EvidenceRefs []string    `json:"evidence_refs,omitempty"`
	WALSequence  uint64      `json:"wal_sequence"`
	CreatedAt    time.Time   `json:"created_at"`
}

// CompletionInput is a plugin-owned successful handoff for one application
// run. It is deliberately distinct from pause/stop proposals: accepting it
// records successful completion, never an error or a request to suspend.
type CompletionInput struct {
	ID                   string   `json:"id,omitempty"`
	RunID                string   `json:"run_id"`
	Summary              string   `json:"summary,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
	ContinuationInputIDs []string `json:"continuation_input_ids,omitempty"`
}

type Completion struct {
	ID                     string    `json:"id"`
	SessionID              string    `json:"session_id"`
	Generation             uint64    `json:"generation"`
	PluginID               string    `json:"plugin_id"`
	RunID                  string    `json:"run_id"`
	Summary                string    `json:"summary,omitempty"`
	EvidenceRefs           []string  `json:"evidence_refs,omitempty"`
	ContinuationInputIDs   []string  `json:"continuation_input_ids,omitempty"`
	ContinuationDeliveryID string    `json:"continuation_delivery_id,omitempty"`
	ContinuationDelivered  bool      `json:"continuation_delivered,omitempty"`
	WALSequence            uint64    `json:"wal_sequence"`
	CreatedAt              time.Time `json:"created_at"`
}

type WorkerRunStatus string

const (
	WorkerRunRequested       WorkerRunStatus = "requested"
	WorkerRunResumeRequested WorkerRunStatus = "resume_requested"
	WorkerRunActive          WorkerRunStatus = "active"
	WorkerRunCancelled       WorkerRunStatus = "cancelled"
	WorkerRunCompleted       WorkerRunStatus = "completed"
	WorkerRunInterrupted     WorkerRunStatus = "interrupted"
	WorkerRunStopped         WorkerRunStatus = "stopped"
)

type WorkerRunConflict string

const (
	WorkerRunRejectOperatorLoop  WorkerRunConflict = "reject"
	WorkerRunReplaceOperatorLoop WorkerRunConflict = "replace_operator_loop"
)

// WorkerRunRequest is guest-controlled application policy. Authority and
// ownership are supplied exclusively by the admitted application binding.
type WorkerRunRequest struct {
	RunID     string            `json:"run_id"`
	Objective string            `json:"objective"`
	Prompt    string            `json:"prompt"`
	Conflict  WorkerRunConflict `json:"conflict"`
}

// WorkerRunCAS is used by an application to request resume or self-cancel and
// by the native command host to activate a request after a successful callback.
// ExpectedVersion prevents stale command/replay transitions.
type WorkerRunCAS struct {
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

// WorkerRunTerminal is broker-native scheduling consumption input. It is not
// reachable through a guest host import.
type WorkerRunTerminal struct {
	RunID           string          `json:"run_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Status          WorkerRunStatus `json:"status"`
	Reason          string          `json:"reason"`
	ControlSequence uint64          `json:"control_sequence"`
}

type WorkerRun struct {
	SessionID        string            `json:"session_id"`
	Generation       uint64            `json:"generation"`
	PluginID         string            `json:"plugin_id"`
	Owner            string            `json:"owner"`
	RunID            string            `json:"run_id"`
	Version          uint64            `json:"version"`
	WALSequence      uint64            `json:"wal_sequence"`
	Objective        string            `json:"objective"`
	Prompt           string            `json:"prompt"`
	Conflict         WorkerRunConflict `json:"conflict"`
	Status           WorkerRunStatus   `json:"status"`
	TerminalReason   string            `json:"terminal_reason,omitempty"`
	TerminalSequence uint64            `json:"terminal_sequence,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type TimerStatus string

const (
	TimerScheduled TimerStatus = "scheduled"
	TimerCancelled TimerStatus = "cancelled"
	TimerDue       TimerStatus = "due"
)

type TimerSchedule struct {
	ID              string          `json:"id,omitempty"`
	RunID           string          `json:"run_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Name            string          `json:"name"`
	DueAt           time.Time       `json:"due_at"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type TimerCAS struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

type Timer struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Generation  uint64          `json:"generation"`
	PluginID    string          `json:"plugin_id"`
	Owner       string          `json:"owner"`
	RunID       string          `json:"run_id"`
	Version     uint64          `json:"version"`
	WALSequence uint64          `json:"wal_sequence"`
	Name        string          `json:"name"`
	DueAt       time.Time       `json:"due_at"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Status      TimerStatus     `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ProjectionOptions struct {
	JournalLimit             int
	ControlLimit             int
	CompletionLimit          int
	WorkerLimit              int
	DeferredTaskLimit        int
	DeferredTaskAfterOrdinal uint64
	IncludeTerminal          bool
}

type Projection struct {
	SessionID               string           `json:"session_id"`
	Generation              uint64           `json:"generation"`
	PluginID                string           `json:"plugin_id"`
	AsOfSequence            uint64           `json:"as_of_sequence"`
	Journal                 []JournalEntry   `json:"journal"`
	JournalTruncated        bool             `json:"journal_truncated"`
	Holds                   []Hold           `json:"holds"`
	ControlRequests         []ControlRequest `json:"control_requests"`
	ControlsTruncated       bool             `json:"controls_truncated"`
	Completions             []Completion     `json:"completions"`
	CompletionsTruncated    bool             `json:"completions_truncated"`
	DeferredTasks           []DeferredTask   `json:"deferred_tasks"`
	DeferredTasksTruncated  bool             `json:"deferred_tasks_truncated"`
	DeferredTaskNextOrdinal uint64           `json:"deferred_task_next_ordinal,omitempty"`
	ReviewingInputs         []OperatorInput  `json:"reviewing_inputs,omitempty"`
	WorkerRuns              []WorkerRun      `json:"worker_runs"`
	WorkerRunsTruncated     bool             `json:"worker_runs_truncated"`
	Timers                  []Timer          `json:"timers"`
}

// SessionScope is a broker-authenticated session incarnation. Unlike
// Authority, it deliberately has no plugin identity: it is used only by the
// broker enforcement path to recover cross-plugin scheduling state.
type SessionScope struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
}

// EventInput is a bounded host fact submitted through a native-only broker
// method. The broker supplies session, generation, sequence, timestamp, and
// publisher. WASM cannot call the publish method or choose those fields.
type EventInput struct {
	ID           string          `json:"id,omitempty"`
	Kind         string          `json:"kind"`
	Data         json.RawMessage `json:"data"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
}

// Event is the durable, broker-stamped delivery unit consumed by a lifecycle
// application. TargetPlugin is broker-private routing metadata and is omitted
// for session-broadcast facts; it never grants authority.
type Event struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	Generation   uint64          `json:"generation"`
	Kind         string          `json:"kind"`
	Data         json.RawMessage `json:"data"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	TargetPlugin string          `json:"target_plugin,omitempty"`
	WALSequence  uint64          `json:"wal_sequence"`
	CreatedAt    time.Time       `json:"created_at"`
	// SubjectID is broker-private linkage for event kinds whose acknowledgement
	// depends on a durable entity transition. It is never exposed to WASM.
	SubjectID string `json:"-"`
}

type EventCursor struct {
	SessionID   string    `json:"session_id"`
	Generation  uint64    `json:"generation"`
	PluginID    string    `json:"plugin_id"`
	Sequence    uint64    `json:"sequence"`
	WALSequence uint64    `json:"wal_sequence"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type EventAck struct {
	Sequence uint64 `json:"sequence"`
}

// EnforcementProjection is broker-only recovery state. It must never be
// exposed to a guest because it aggregates otherwise isolated plugin
// namespaces. Pause and stop remain requests for host policy to evaluate.
type EnforcementProjection struct {
	SessionID               string                `json:"session_id"`
	Generation              uint64                `json:"generation"`
	AsOfSequence            uint64                `json:"as_of_sequence"`
	ActiveHolds             []Hold                `json:"active_holds"`
	LatestPause             *ControlRequest       `json:"latest_pause,omitempty"`
	LatestStop              *ControlRequest       `json:"latest_stop,omitempty"`
	LatestCompletion        *Completion           `json:"latest_completion,omitempty"`
	ActiveWorkerRun         *WorkerRun            `json:"active_worker_run,omitempty"`
	LatestWorkerRun         *WorkerRun            `json:"latest_worker_run,omitempty"`
	ReadyOperatorInputs     []OperatorInput       `json:"ready_operator_inputs,omitempty"`
	ReviewingOperatorInputs []OperatorInput       `json:"reviewing_operator_inputs,omitempty"`
	RecoveryOperatorInputs  []OperatorInput       `json:"recovery_operator_inputs,omitempty"`
	OpenDeferredTaskCount   int                   `json:"open_deferred_task_count,omitempty"`
	OpenDeferredTasks       []DeferredTaskSummary `json:"open_deferred_tasks,omitempty"`
	OpenDeferredTruncated   bool                  `json:"open_deferred_tasks_truncated,omitempty"`
	PendingContinuation     *Continuation         `json:"pending_continuation,omitempty"`
}

type walStore interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

type Service struct {
	mu     sync.Mutex
	wal    walStore
	limits Limits
	now    func() time.Time
}

func New(store walStore) *Service {
	service, err := NewWithLimits(store, DefaultLimits())
	if err != nil {
		panic(err)
	}
	return service
}

func NewWithLimits(store walStore, limits Limits) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: WAL store required", ErrInvalid)
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	return &Service{wal: store, limits: limits, now: time.Now}, nil
}

func (s *Service) AppendJournal(ctx context.Context, auth Authority, input JournalAppend, idem string) (JournalEntry, error) {
	if err := checkContext(ctx); err != nil {
		return JournalEntry{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return JournalEntry{}, err
	}
	if err := s.validateJournal(&input); err != nil {
		return JournalEntry{}, err
	}
	return withMutation(s, auth, "journal.append", input, idem, func(state foldedState, now time.Time) (JournalEntry, string, eventEnvelope, error) {
		entries := scopedJournal(state, auth)
		if len(entries) >= s.limits.MaxJournalEntries {
			return JournalEntry{}, "", eventEnvelope{}, ErrLimit
		}
		if input.ID == "" {
			id, err := mint("journal_")
			if err != nil {
				return JournalEntry{}, "", eventEnvelope{}, err
			}
			input.ID = id
		}
		for _, entry := range entries {
			if entry.ID == input.ID {
				return JournalEntry{}, "", eventEnvelope{}, fmt.Errorf("%w: journal ID exists", ErrVersion)
			}
		}
		entry := JournalEntry{
			ID: input.ID, SessionID: auth.SessionID, Generation: auth.Generation,
			PluginID: auth.PluginID, RunID: input.RunID, Sequence: uint64(len(entries) + 1),
			Kind: input.Kind, Summary: input.Summary, Data: cloneJSON(input.Data),
			EvidenceRefs: cloneStrings(input.EvidenceRefs), CreatedAt: now,
		}
		return entry, "journal.appended", eventEnvelope{Journal: &entry}, nil
	})
}

func (s *Service) AcquireHold(ctx context.Context, auth Authority, input HoldAcquire, idem string) (Hold, error) {
	if err := checkContext(ctx); err != nil {
		return Hold{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Hold{}, err
	}
	if err := s.validateHoldAcquire(input); err != nil {
		return Hold{}, err
	}
	return withMutation(s, auth, "hold.acquire", input, idem, func(state foldedState, now time.Time) (Hold, string, eventEnvelope, error) {
		id := input.ID
		if id == "" {
			if input.ExpectedVersion != 0 {
				return Hold{}, "", eventEnvelope{}, ErrVersion
			}
			var err error
			id, err = mint("hold_")
			if err != nil {
				return Hold{}, "", eventEnvelope{}, err
			}
		}
		current, exists := state.holds[entityKey(auth, id)]
		if !exists {
			if input.ExpectedVersion != 0 {
				return Hold{}, "", eventEnvelope{}, ErrVersion
			}
			if scopedHoldCount(state, auth, false) >= s.limits.MaxHoldRecords || scopedHoldCount(state, auth, true) >= s.limits.MaxActiveHolds {
				return Hold{}, "", eventEnvelope{}, ErrLimit
			}
			hold := Hold{ID: id, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, Owner: auth.PluginID, RunID: input.RunID, Version: 1, ReasonCode: input.ReasonCode, Reason: input.Reason, Status: HoldActive, LeaseUntil: now.Add(input.TTL), CreatedAt: now, UpdatedAt: now}
			return hold, "hold.acquired", eventEnvelope{Hold: &hold}, nil
		}
		if err := checkHoldScope(current, auth, input.RunID); err != nil {
			return Hold{}, "", eventEnvelope{}, err
		}
		if current.Status != HoldActive {
			return Hold{}, "", eventEnvelope{}, ErrTerminal
		}
		if !current.LeaseUntil.After(now) {
			return Hold{}, "", eventEnvelope{}, ErrLeaseExpired
		}
		if input.ExpectedVersion == 0 || current.Version != input.ExpectedVersion {
			return Hold{}, "", eventEnvelope{}, ErrVersion
		}
		current.Version++
		current.ReasonCode = input.ReasonCode
		current.Reason = input.Reason
		current.LeaseUntil = now.Add(input.TTL)
		current.UpdatedAt = now
		return current, "hold.renewed", eventEnvelope{Hold: &current}, nil
	})
}

func (s *Service) ReleaseHold(ctx context.Context, auth Authority, input HoldCAS, idem string) (Hold, error) {
	return s.mutateHold(ctx, auth, input, idem, "hold.release", func(current Hold, now time.Time) (Hold, string, error) {
		if !current.LeaseUntil.After(now) {
			return Hold{}, "", ErrLeaseExpired
		}
		current.Status = HoldReleased
		return current, "hold.released", nil
	})
}

func (s *Service) ExpireHold(ctx context.Context, auth Authority, input HoldCAS, idem string) (Hold, error) {
	return s.mutateHold(ctx, auth, input, idem, "hold.expire", func(current Hold, now time.Time) (Hold, string, error) {
		if current.LeaseUntil.After(now) {
			return Hold{}, "", ErrNotDue
		}
		current.Status = HoldExpired
		return current, "hold.expired", nil
	})
}

func (s *Service) mutateHold(ctx context.Context, auth Authority, input HoldCAS, idem, op string, mutate func(Hold, time.Time) (Hold, string, error)) (Hold, error) {
	if err := checkContext(ctx); err != nil {
		return Hold{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Hold{}, err
	}
	if err := s.validateHoldCAS(input); err != nil {
		return Hold{}, err
	}
	return withMutation(s, auth, op, input, idem, func(state foldedState, now time.Time) (Hold, string, eventEnvelope, error) {
		current, ok := state.holds[entityKey(auth, input.ID)]
		if !ok {
			return Hold{}, "", eventEnvelope{}, ErrNotFound
		}
		if err := checkHoldScope(current, auth, input.RunID); err != nil {
			return Hold{}, "", eventEnvelope{}, err
		}
		if current.Status != HoldActive {
			return Hold{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != input.ExpectedVersion {
			return Hold{}, "", eventEnvelope{}, ErrVersion
		}
		next, eventType, err := mutate(current, now)
		if err != nil {
			return Hold{}, "", eventEnvelope{}, err
		}
		next.Version++
		next.UpdatedAt = now
		return next, eventType, eventEnvelope{Hold: &next}, nil
	})
}

func (s *Service) RequestPause(ctx context.Context, auth Authority, input ControlInput, idem string) (ControlRequest, error) {
	return s.requestControl(ctx, auth, ControlPause, input, idem)
}

func (s *Service) RequestStop(ctx context.Context, auth Authority, input ControlInput, idem string) (ControlRequest, error) {
	return s.requestControl(ctx, auth, ControlStop, input, idem)
}

// CompleteSession durably records that an admitted application run completed
// its work successfully. A run may transition once; retries replay through the
// ordinary idempotency key, while a second logical completion is terminal.
func (s *Service) CompleteSession(ctx context.Context, auth Authority, input CompletionInput, idem string) (Completion, error) {
	if err := checkContext(ctx); err != nil {
		return Completion{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Completion{}, err
	}
	if err := s.validateCompletion(input); err != nil {
		return Completion{}, err
	}
	return withMutation(s, auth, "session.complete", input, idem, func(state foldedState, now time.Time) (Completion, string, eventEnvelope, error) {
		if err := validateCompletionContinuation(state, auth, input, s.limits.MaxPendingOperatorInputs); err != nil {
			return Completion{}, "", eventEnvelope{}, err
		}
		completions := scopedCompletions(state, auth)
		if len(completions) >= s.limits.MaxCompletions {
			return Completion{}, "", eventEnvelope{}, ErrLimit
		}
		for _, completion := range completions {
			if completion.RunID == input.RunID {
				return Completion{}, "", eventEnvelope{}, ErrTerminal
			}
			if input.ID != "" && completion.ID == input.ID {
				return Completion{}, "", eventEnvelope{}, fmt.Errorf("%w: completion ID exists", ErrVersion)
			}
		}
		if input.ID == "" {
			id, err := mint("completion_")
			if err != nil {
				return Completion{}, "", eventEnvelope{}, err
			}
			input.ID = id
		}
		continuationDeliveryID := ""
		if len(input.ContinuationInputIDs) > 0 {
			var err error
			continuationDeliveryID, err = mint("continuation_delivery_")
			if err != nil {
				return Completion{}, "", eventEnvelope{}, err
			}
		}
		completion := Completion{
			ID: input.ID, SessionID: auth.SessionID, Generation: auth.Generation,
			PluginID: auth.PluginID, RunID: input.RunID, Summary: input.Summary,
			EvidenceRefs:         cloneStrings(input.EvidenceRefs),
			ContinuationInputIDs: cloneStrings(input.ContinuationInputIDs), ContinuationDeliveryID: continuationDeliveryID, CreatedAt: now,
		}
		return completion, "session.completed", eventEnvelope{Completion: &completion}, nil
	})
}

func (s *Service) RequestWorkerRun(ctx context.Context, auth Authority, input WorkerRunRequest, idem string) (WorkerRun, error) {
	if err := checkContext(ctx); err != nil {
		return WorkerRun{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return WorkerRun{}, err
	}
	if err := s.validateWorkerRunRequest(input); err != nil {
		return WorkerRun{}, err
	}
	return withMutation(s, auth, "worker.request", input, idem, func(state foldedState, now time.Time) (WorkerRun, string, eventEnvelope, error) {
		if scopedWorkerRunCount(state, auth) >= s.limits.MaxWorkerRuns {
			return WorkerRun{}, "", eventEnvelope{}, ErrLimit
		}
		key := entityKey(auth, input.RunID)
		if _, exists := state.workerRuns[key]; exists {
			return WorkerRun{}, "", eventEnvelope{}, fmt.Errorf("%w: worker run exists", ErrVersion)
		}
		for _, completion := range state.completions {
			if completion.SessionID == auth.SessionID && completion.Generation == auth.Generation && completion.PluginID == auth.PluginID && completion.RunID == input.RunID {
				return WorkerRun{}, "", eventEnvelope{}, fmt.Errorf("%w: worker run was already completed", ErrTerminal)
			}
		}
		run := WorkerRun{
			SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID,
			Owner: auth.PluginID, RunID: input.RunID, Version: 1,
			Objective: input.Objective, Prompt: input.Prompt, Conflict: input.Conflict,
			Status: WorkerRunRequested, CreatedAt: now, UpdatedAt: now,
		}
		return run, "worker.requested", eventEnvelope{WorkerRun: &run}, nil
	})
}

// WorkerRunByID is a native command-host read under the exact application
// binding. It is intentionally not a WASM import: the guest already receives
// its request result, while the host uses this read to consume only a request
// named by a successfully validated command callback.
func (s *Service) WorkerRunByID(ctx context.Context, auth Authority, runID string) (WorkerRun, error) {
	if err := checkContext(ctx); err != nil {
		return WorkerRun{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return WorkerRun{}, err
	}
	if invalidIdentifier(runID, s.limits.MaxIDBytes, true) {
		return WorkerRun{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return WorkerRun{}, err
	}
	run, ok := state.workerRuns[entityKey(auth, runID)]
	if !ok {
		return WorkerRun{}, ErrNotFound
	}
	return projectWorkerRun(state, run), nil
}

func (s *Service) ActivateWorkerRun(ctx context.Context, auth Authority, input WorkerRunCAS, idem string) (WorkerRun, error) {
	return s.mutateWorkerRun(ctx, auth, input, idem, "worker.activate", WorkerRunActive, func(state foldedState, current WorkerRun, _ time.Time) error {
		if effectiveWorkerRunStatus(state, current) != WorkerRunRequested {
			return ErrTerminal
		}
		for _, other := range state.workerRuns {
			if other.SessionID == auth.SessionID && other.Generation == auth.Generation && effectiveWorkerRunStatus(state, other) == WorkerRunActive && (other.PluginID != current.PluginID || other.RunID != current.RunID) {
				return fmt.Errorf("%w: another application worker run is active", ErrVersion)
			}
		}
		return nil
	})
}

// RequestWorkerRunResume records application policy to continue an exact
// interrupted run. It does not activate recurrence: the native command host
// must independently fetch and CAS-activate the request after the signed
// command callback succeeds. A repeated request may refresh an unresolved
// resume request after a newer pause, but it can never change run identity or
// revive a stopped, cancelled, or completed run.
func (s *Service) RequestWorkerRunResume(ctx context.Context, auth Authority, input WorkerRunCAS, idem string) (WorkerRun, error) {
	return s.mutateWorkerRun(ctx, auth, input, idem, "worker.resume.request", WorkerRunResumeRequested, func(state foldedState, current WorkerRun, _ time.Time) error {
		status := effectiveWorkerRunStatus(state, current)
		if status != WorkerRunInterrupted && (status != WorkerRunResumeRequested || !workerRunHasLaterPause(state, current)) {
			return ErrTerminal
		}
		if workerRunHasLaterStop(state, current) || workerRunHasLaterCompletion(state, current) {
			return ErrTerminal
		}
		return nil
	})
}

// ActivateResumedWorkerRun is the controller-authenticated half of resume.
// The request's WAL position acknowledges pauses already visible to the signed
// command; a pause racing after that request or any post-interruption stop
// prevents activation. Any unexpired hold in the exact session/generation
// keeps the run resume-requested with a retryable conflict; the native
// controller may retry the same CAS after that hold is released or expires.
func (s *Service) ActivateResumedWorkerRun(ctx context.Context, auth Authority, input WorkerRunCAS, idem string) (WorkerRun, error) {
	return s.mutateWorkerRun(ctx, auth, input, idem, "worker.resume.activate", WorkerRunActive, func(state foldedState, current WorkerRun, now time.Time) error {
		if effectiveWorkerRunStatus(state, current) != WorkerRunResumeRequested {
			return ErrTerminal
		}
		if workerRunHasLaterStop(state, current) || workerRunHasLaterCompletion(state, current) {
			return ErrTerminal
		}
		for _, control := range state.controls {
			if control.SessionID == current.SessionID && control.Generation == current.Generation &&
				control.Kind == ControlPause && control.WALSequence > current.WALSequence {
				return fmt.Errorf("%w: pause raced worker resume request", ErrVersion)
			}
		}
		if workerRunHasActiveHold(state, current, now) {
			return fmt.Errorf("%w: active scheduling hold blocks worker resume", ErrVersion)
		}
		for _, other := range state.workerRuns {
			if other.SessionID == auth.SessionID && other.Generation == auth.Generation && effectiveWorkerRunStatus(state, other) == WorkerRunActive && (other.PluginID != current.PluginID || other.RunID != current.RunID) {
				return fmt.Errorf("%w: another application worker run is active", ErrVersion)
			}
		}
		return nil
	})
}

func (s *Service) CancelWorkerRun(ctx context.Context, auth Authority, input WorkerRunCAS, idem string) (WorkerRun, error) {
	if invalidText(input.Reason, s.limits.MaxTextBytes, true) {
		return WorkerRun{}, fmt.Errorf("%w: worker cancellation reason required", ErrInvalid)
	}
	return s.mutateWorkerRun(ctx, auth, input, idem, "worker.cancel", WorkerRunCancelled, func(state foldedState, current WorkerRun, _ time.Time) error {
		status := effectiveWorkerRunStatus(state, current)
		if status != WorkerRunRequested && status != WorkerRunResumeRequested && status != WorkerRunActive {
			return ErrTerminal
		}
		return nil
	})
}

// TerminalizeWorkerRun is called only after an authenticated native scheduler
// consumes pause/stop. Persisting this transition before returning the control
// result prevents restart/rebind from resurrecting recurrence.
func (s *Service) TerminalizeWorkerRun(ctx context.Context, auth Authority, input WorkerRunTerminal, idem string) (WorkerRun, error) {
	if input.Status != WorkerRunInterrupted && input.Status != WorkerRunStopped || input.ControlSequence == 0 || invalidText(input.Reason, s.limits.MaxTextBytes, true) {
		return WorkerRun{}, fmt.Errorf("%w: invalid worker terminal transition", ErrInvalid)
	}
	cas := WorkerRunCAS{RunID: input.RunID, ExpectedVersion: input.ExpectedVersion, Reason: input.Reason}
	return s.mutateWorkerRun(ctx, auth, cas, idem, "worker."+string(input.Status), input.Status, func(state foldedState, current WorkerRun, _ time.Time) error {
		if effectiveWorkerRunStatus(state, current) != WorkerRunActive {
			return ErrTerminal
		}
		return nil
	}, input.ControlSequence)
}

type workerRunGuard func(foldedState, WorkerRun, time.Time) error

func (s *Service) mutateWorkerRun(ctx context.Context, auth Authority, input WorkerRunCAS, idem, op string, status WorkerRunStatus, guard workerRunGuard, terminalSequence ...uint64) (WorkerRun, error) {
	if err := checkContext(ctx); err != nil {
		return WorkerRun{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return WorkerRun{}, err
	}
	if err := s.validateWorkerRunCAS(input); err != nil {
		return WorkerRun{}, err
	}
	request := any(input)
	if len(terminalSequence) > 0 {
		request = WorkerRunTerminal{RunID: input.RunID, ExpectedVersion: input.ExpectedVersion, Status: status, Reason: input.Reason, ControlSequence: terminalSequence[0]}
	}
	return withMutation(s, auth, op, request, idem, func(state foldedState, now time.Time) (WorkerRun, string, eventEnvelope, error) {
		current, ok := state.workerRuns[entityKey(auth, input.RunID)]
		if !ok {
			return WorkerRun{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.Owner != auth.PluginID || current.Version != input.ExpectedVersion {
			return WorkerRun{}, "", eventEnvelope{}, ErrVersion
		}
		if err := guard(state, current, now); err != nil {
			return WorkerRun{}, "", eventEnvelope{}, err
		}
		current.Version++
		current.Status = status
		current.UpdatedAt = now
		if status == WorkerRunActive {
			current.TerminalReason = ""
			current.TerminalSequence = 0
		}
		if status == WorkerRunCancelled || status == WorkerRunInterrupted || status == WorkerRunStopped {
			current.TerminalReason = input.Reason
			if len(terminalSequence) > 0 {
				current.TerminalSequence = terminalSequence[0]
			}
		}
		return current, "worker." + string(status), eventEnvelope{WorkerRun: &current}, nil
	})
}

func (s *Service) requestControl(ctx context.Context, auth Authority, kind ControlKind, input ControlInput, idem string) (ControlRequest, error) {
	if err := checkContext(ctx); err != nil {
		return ControlRequest{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return ControlRequest{}, err
	}
	if err := s.validateControl(input); err != nil {
		return ControlRequest{}, err
	}
	op := "control." + string(kind)
	return withMutation(s, auth, op, input, idem, func(state foldedState, now time.Time) (ControlRequest, string, eventEnvelope, error) {
		requests := scopedControls(state, auth)
		if len(requests) >= s.limits.MaxControlRequests {
			return ControlRequest{}, "", eventEnvelope{}, ErrLimit
		}
		if input.ID == "" {
			id, err := mint("control_")
			if err != nil {
				return ControlRequest{}, "", eventEnvelope{}, err
			}
			input.ID = id
		}
		for _, request := range requests {
			if request.ID == input.ID {
				return ControlRequest{}, "", eventEnvelope{}, fmt.Errorf("%w: control request ID exists", ErrVersion)
			}
		}
		if input.HoldID != "" {
			hold, ok := state.holds[entityKey(auth, input.HoldID)]
			if !ok {
				return ControlRequest{}, "", eventEnvelope{}, ErrNotFound
			}
			if err := checkHoldScope(hold, auth, input.RunID); err != nil {
				return ControlRequest{}, "", eventEnvelope{}, err
			}
			if hold.Status != HoldActive || !hold.LeaseUntil.After(now) {
				return ControlRequest{}, "", eventEnvelope{}, ErrLeaseExpired
			}
		}
		request := ControlRequest{ID: input.ID, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RunID: input.RunID, Kind: kind, ReasonCode: input.ReasonCode, Reason: input.Reason, HoldID: input.HoldID, EvidenceRefs: cloneStrings(input.EvidenceRefs), CreatedAt: now}
		return request, "control." + string(kind) + "_requested", eventEnvelope{Control: &request}, nil
	})
}

func (s *Service) ScheduleTimer(ctx context.Context, auth Authority, input TimerSchedule, idem string) (Timer, error) {
	if err := checkContext(ctx); err != nil {
		return Timer{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Timer{}, err
	}
	if err := s.validateTimerSchedule(&input); err != nil {
		return Timer{}, err
	}
	return withMutation(s, auth, "timer.schedule", input, idem, func(state foldedState, now time.Time) (Timer, string, eventEnvelope, error) {
		if input.DueAt.Before(now) || input.DueAt.After(now.Add(s.limits.MaxTimerHorizon)) {
			return Timer{}, "", eventEnvelope{}, fmt.Errorf("%w: timer due_at outside bounded horizon", ErrInvalid)
		}
		id := input.ID
		if id == "" {
			if input.ExpectedVersion != 0 {
				return Timer{}, "", eventEnvelope{}, ErrVersion
			}
			var err error
			id, err = mint("timer_")
			if err != nil {
				return Timer{}, "", eventEnvelope{}, err
			}
		}
		current, exists := state.timers[entityKey(auth, id)]
		if !exists {
			if input.ExpectedVersion != 0 {
				return Timer{}, "", eventEnvelope{}, ErrVersion
			}
			if scopedTimerCount(state, auth, false) >= s.limits.MaxTimerRecords || scopedTimerCount(state, auth, true) >= s.limits.MaxActiveTimers {
				return Timer{}, "", eventEnvelope{}, ErrLimit
			}
			timer := Timer{ID: id, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, Owner: auth.PluginID, RunID: input.RunID, Version: 1, Name: input.Name, DueAt: input.DueAt.UTC(), Payload: cloneJSON(input.Payload), Status: TimerScheduled, CreatedAt: now, UpdatedAt: now}
			return timer, "timer.scheduled", eventEnvelope{Timer: &timer}, nil
		}
		if err := checkTimerScope(current, auth, input.RunID); err != nil {
			return Timer{}, "", eventEnvelope{}, err
		}
		if current.Status != TimerScheduled {
			return Timer{}, "", eventEnvelope{}, ErrTerminal
		}
		if input.ExpectedVersion == 0 || current.Version != input.ExpectedVersion {
			return Timer{}, "", eventEnvelope{}, ErrVersion
		}
		current.Version++
		current.Name = input.Name
		current.DueAt = input.DueAt.UTC()
		current.Payload = cloneJSON(input.Payload)
		current.UpdatedAt = now
		return current, "timer.rescheduled", eventEnvelope{Timer: &current}, nil
	})
}

func (s *Service) CancelTimer(ctx context.Context, auth Authority, input TimerCAS, idem string) (Timer, error) {
	return s.mutateTimer(ctx, auth, input, idem, "timer.cancel", false, TimerCancelled, "timer.cancelled")
}

func (s *Service) MarkTimerDue(ctx context.Context, auth Authority, input TimerCAS, idem string) (Timer, error) {
	return s.mutateTimer(ctx, auth, input, idem, "timer.due", true, TimerDue, "timer.due")
}

func (s *Service) mutateTimer(ctx context.Context, auth Authority, input TimerCAS, idem, op string, requireDue bool, status TimerStatus, eventType string) (Timer, error) {
	if err := checkContext(ctx); err != nil {
		return Timer{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Timer{}, err
	}
	if err := s.validateTimerCAS(input); err != nil {
		return Timer{}, err
	}
	return withMutation(s, auth, op, input, idem, func(state foldedState, now time.Time) (Timer, string, eventEnvelope, error) {
		current, ok := state.timers[entityKey(auth, input.ID)]
		if !ok {
			return Timer{}, "", eventEnvelope{}, ErrNotFound
		}
		if err := checkTimerScope(current, auth, input.RunID); err != nil {
			return Timer{}, "", eventEnvelope{}, err
		}
		if current.Status != TimerScheduled {
			return Timer{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != input.ExpectedVersion {
			return Timer{}, "", eventEnvelope{}, ErrVersion
		}
		if requireDue && current.DueAt.After(now) {
			return Timer{}, "", eventEnvelope{}, ErrNotDue
		}
		current.Version++
		current.Status = status
		current.UpdatedAt = now
		return current, eventType, eventEnvelope{Timer: &current}, nil
	})
}

func (s *Service) Project(ctx context.Context, auth Authority, options ProjectionOptions) (Projection, error) {
	if err := checkContext(ctx); err != nil {
		return Projection{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Projection{}, err
	}
	journalLimit, err := s.projectionLimit(options.JournalLimit)
	if err != nil {
		return Projection{}, err
	}
	controlLimit, err := s.projectionLimit(options.ControlLimit)
	if err != nil {
		return Projection{}, err
	}
	completionLimit, err := s.projectionLimit(options.CompletionLimit)
	if err != nil {
		return Projection{}, err
	}
	workerLimit, err := s.projectionLimit(options.WorkerLimit)
	if err != nil {
		return Projection{}, err
	}
	deferredTaskLimitRequest := options.DeferredTaskLimit
	if deferredTaskLimitRequest == 0 {
		deferredTaskLimitRequest = defaultDeferredTaskLimit
	}
	deferredTaskLimit, err := s.projectionLimit(deferredTaskLimitRequest)
	if err != nil {
		return Projection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return Projection{}, err
	}
	journal := scopedJournal(state, auth)
	controls := scopedControls(state, auth)
	completions := scopedCompletions(state, auth)
	projection := Projection{SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID}
	projection.Journal, projection.JournalTruncated = tailJournal(journal, journalLimit)
	projection.ControlRequests, projection.ControlsTruncated = tailControls(controls, controlLimit)
	projection.Completions, projection.CompletionsTruncated = tailCompletions(completions, completionLimit)
	for i := range projection.Completions {
		_, projection.Completions[i].ContinuationDelivered = state.continuationDeliveries[continuationKey(projection.Completions[i])]
	}
	deferred := deferredTasksFor(state, auth)
	for _, task := range deferred {
		if task.Ordinal <= options.DeferredTaskAfterOrdinal {
			continue
		}
		if len(projection.DeferredTasks) == deferredTaskLimit {
			projection.DeferredTasksTruncated = true
			break
		}
		projection.DeferredTasks = append(projection.DeferredTasks, task)
		projection.DeferredTaskNextOrdinal = task.Ordinal
	}
	for _, entry := range journal {
		projection.AsOfSequence = max(projection.AsOfSequence, entry.WALSequence)
	}
	for _, request := range controls {
		projection.AsOfSequence = max(projection.AsOfSequence, request.WALSequence)
	}
	for _, completion := range completions {
		projection.AsOfSequence = max(projection.AsOfSequence, completion.WALSequence)
	}
	for _, input := range state.operatorInputs {
		if sameScope(input.SessionID, input.Generation, input.PluginID, auth) {
			projection.AsOfSequence = max(projection.AsOfSequence, input.WALSequence)
			if input.Status == OperatorInputReviewing {
				projection.ReviewingInputs = append(projection.ReviewingInputs, input)
			}
		}
	}
	sort.Slice(projection.ReviewingInputs, func(i, j int) bool {
		return projection.ReviewingInputs[i].Ordinal < projection.ReviewingInputs[j].Ordinal
	})
	for _, delivery := range state.continuationDeliveries {
		if sameScope(delivery.SessionID, delivery.Generation, delivery.PluginID, auth) {
			projection.AsOfSequence = max(projection.AsOfSequence, delivery.WALSequence)
		}
	}
	for _, run := range state.workerRuns {
		if !sameScope(run.SessionID, run.Generation, run.PluginID, auth) {
			continue
		}
		run = projectWorkerRun(state, run)
		projection.AsOfSequence = max(projection.AsOfSequence, run.WALSequence)
		projection.AsOfSequence = max(projection.AsOfSequence, run.TerminalSequence)
		if options.IncludeTerminal || run.Status == WorkerRunRequested || run.Status == WorkerRunResumeRequested || run.Status == WorkerRunActive {
			projection.WorkerRuns = append(projection.WorkerRuns, run)
		}
	}
	for _, hold := range state.holds {
		if !sameScope(hold.SessionID, hold.Generation, hold.PluginID, auth) {
			continue
		}
		projection.AsOfSequence = max(projection.AsOfSequence, hold.WALSequence)
		if options.IncludeTerminal || hold.Status == HoldActive {
			projection.Holds = append(projection.Holds, hold)
		}
	}
	for _, timer := range state.timers {
		if !sameScope(timer.SessionID, timer.Generation, timer.PluginID, auth) {
			continue
		}
		projection.AsOfSequence = max(projection.AsOfSequence, timer.WALSequence)
		if options.IncludeTerminal || timer.Status == TimerScheduled {
			projection.Timers = append(projection.Timers, timer)
		}
	}
	sort.Slice(projection.Holds, func(i, j int) bool { return projection.Holds[i].ID < projection.Holds[j].ID })
	sort.Slice(projection.WorkerRuns, func(i, j int) bool {
		if projection.WorkerRuns[i].CreatedAt.Equal(projection.WorkerRuns[j].CreatedAt) {
			return projection.WorkerRuns[i].RunID < projection.WorkerRuns[j].RunID
		}
		return projection.WorkerRuns[i].CreatedAt.Before(projection.WorkerRuns[j].CreatedAt)
	})
	if len(projection.WorkerRuns) > workerLimit {
		projection.WorkerRunsTruncated = true
		projection.WorkerRuns = append([]WorkerRun(nil), projection.WorkerRuns[len(projection.WorkerRuns)-workerLimit:]...)
	}
	sort.Slice(projection.Timers, func(i, j int) bool {
		if projection.Timers[i].DueAt.Equal(projection.Timers[j].DueAt) {
			return projection.Timers[i].ID < projection.Timers[j].ID
		}
		return projection.Timers[i].DueAt.Before(projection.Timers[j].DueAt)
	})
	return projection, nil
}

// ProjectEnforcement recovers host enforcement inputs for one authenticated
// session incarnation. The caller is the broker, never a plugin RPC handler.
func (s *Service) ProjectEnforcement(ctx context.Context, scope SessionScope) (EnforcementProjection, error) {
	if err := checkContext(ctx); err != nil {
		return EnforcementProjection{}, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return EnforcementProjection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return EnforcementProjection{}, err
	}
	now := s.now().UTC()
	out := EnforcementProjection{SessionID: scope.SessionID, Generation: scope.Generation}
	for _, hold := range state.holds {
		if hold.SessionID != scope.SessionID || hold.Generation != scope.Generation || hold.Status != HoldActive || !hold.LeaseUntil.After(now) {
			continue
		}
		out.ActiveHolds = append(out.ActiveHolds, hold)
		out.AsOfSequence = max(out.AsOfSequence, hold.WALSequence)
	}
	for i := range state.controls {
		request := state.controls[i]
		if request.SessionID != scope.SessionID || request.Generation != scope.Generation {
			continue
		}
		out.AsOfSequence = max(out.AsOfSequence, request.WALSequence)
		switch request.Kind {
		case ControlPause:
			if out.LatestPause == nil || request.WALSequence > out.LatestPause.WALSequence {
				copy := request
				out.LatestPause = &copy
			}
		case ControlStop:
			if out.LatestStop == nil || request.WALSequence > out.LatestStop.WALSequence {
				copy := request
				out.LatestStop = &copy
			}
		}
	}
	for i := range state.completions {
		completion := state.completions[i]
		if completion.SessionID != scope.SessionID || completion.Generation != scope.Generation {
			continue
		}
		out.AsOfSequence = max(out.AsOfSequence, completion.WALSequence)
		_, completion.ContinuationDelivered = state.continuationDeliveries[continuationKey(completion)]
		if out.LatestCompletion == nil || completion.WALSequence > out.LatestCompletion.WALSequence {
			copy := completion
			out.LatestCompletion = &copy
		}
		if len(completion.ContinuationInputIDs) > 0 {
			delivery, delivered := state.continuationDeliveries[continuationKey(completion)]
			if !delivered && (out.PendingContinuation == nil || completion.WALSequence < out.PendingContinuation.WALSequence) {
				continuation := continuationFrom(completion, nil)
				out.PendingContinuation = &continuation
			} else if delivered {
				out.AsOfSequence = max(out.AsOfSequence, delivery.WALSequence)
			}
		}
	}
	for _, stored := range state.workerRuns {
		if stored.SessionID != scope.SessionID || stored.Generation != scope.Generation {
			continue
		}
		run := projectWorkerRun(state, stored)
		sequence := max(run.WALSequence, run.TerminalSequence)
		out.AsOfSequence = max(out.AsOfSequence, sequence)
		if out.LatestWorkerRun == nil || sequence > max(out.LatestWorkerRun.WALSequence, out.LatestWorkerRun.TerminalSequence) {
			copy := run
			out.LatestWorkerRun = &copy
		}
		if run.Status != WorkerRunActive {
			continue
		}
		if out.ActiveWorkerRun != nil {
			return EnforcementProjection{}, errors.New("lifecycle application projection: multiple active worker runs")
		}
		copy := run
		out.ActiveWorkerRun = &copy
	}
	for _, input := range state.operatorInputs {
		if input.SessionID != scope.SessionID || input.Generation != scope.Generation {
			continue
		}
		out.AsOfSequence = max(out.AsOfSequence, input.WALSequence)
		run, exists := state.workerRuns[scopeKey(input.SessionID, input.Generation, input.PluginID)+"\x00"+input.RunID]
		if !exists {
			return EnforcementProjection{}, errors.New("lifecycle application projection: operator input has no worker run")
		}
		if input.Status == OperatorInputReady && effectiveWorkerRunStatus(state, run) == WorkerRunActive {
			out.ReadyOperatorInputs = append(out.ReadyOperatorInputs, input)
		}
		if input.Status == OperatorInputReviewing && effectiveWorkerRunStatus(state, run) == WorkerRunActive {
			out.ReviewingOperatorInputs = append(out.ReviewingOperatorInputs, input)
		}
		if (input.Status == OperatorInputQueued || input.Status == OperatorInputReviewing || input.Status == OperatorInputReady) && workerRunTerminal(state, run) {
			out.RecoveryOperatorInputs = append(out.RecoveryOperatorInputs, input)
		}
		if input.Status == OperatorInputDeferred && deferredTaskStatus(state, input) == DeferredTaskOpen {
			out.OpenDeferredTasks = append(out.OpenDeferredTasks, deferredTaskSummaryFromInput(input, DeferredTaskOpen))
		}
	}
	sort.Slice(out.ReadyOperatorInputs, func(i, j int) bool { return out.ReadyOperatorInputs[i].Ordinal < out.ReadyOperatorInputs[j].Ordinal })
	sort.Slice(out.ReviewingOperatorInputs, func(i, j int) bool {
		return out.ReviewingOperatorInputs[i].Ordinal < out.ReviewingOperatorInputs[j].Ordinal
	})
	sort.Slice(out.RecoveryOperatorInputs, func(i, j int) bool {
		return out.RecoveryOperatorInputs[i].Ordinal < out.RecoveryOperatorInputs[j].Ordinal
	})
	sort.Slice(out.OpenDeferredTasks, func(i, j int) bool {
		if out.OpenDeferredTasks[i].RunID == out.OpenDeferredTasks[j].RunID {
			return out.OpenDeferredTasks[i].Ordinal < out.OpenDeferredTasks[j].Ordinal
		}
		return out.OpenDeferredTasks[i].RunID < out.OpenDeferredTasks[j].RunID
	})
	out.OpenDeferredTaskCount = len(out.OpenDeferredTasks)
	if len(out.OpenDeferredTasks) > maxNativeDeferredSummaries {
		out.OpenDeferredTasks = out.OpenDeferredTasks[:maxNativeDeferredSummaries]
		out.OpenDeferredTruncated = true
	}
	sort.Slice(out.ActiveHolds, func(i, j int) bool {
		if out.ActiveHolds[i].LeaseUntil.Equal(out.ActiveHolds[j].LeaseUntil) {
			if out.ActiveHolds[i].PluginID == out.ActiveHolds[j].PluginID {
				return out.ActiveHolds[i].ID < out.ActiveHolds[j].ID
			}
			return out.ActiveHolds[i].PluginID < out.ActiveHolds[j].PluginID
		}
		return out.ActiveHolds[i].LeaseUntil.Before(out.ActiveHolds[j].LeaseUntil)
	})
	return out, nil
}

func (s *Service) DueHolds(ctx context.Context, auth Authority, limit int) ([]Hold, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return nil, err
	}
	limit, err := s.projectionLimit(limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	var out []Hold
	for _, hold := range state.holds {
		if sameScope(hold.SessionID, hold.Generation, hold.PluginID, auth) && hold.Status == HoldActive && !hold.LeaseUntil.After(now) {
			out = append(out, hold)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LeaseUntil.Equal(out[j].LeaseUntil) {
			return out[i].ID < out[j].ID
		}
		return out[i].LeaseUntil.Before(out[j].LeaseUntil)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Service) DueTimers(ctx context.Context, auth Authority, limit int) ([]Timer, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return nil, err
	}
	limit, err := s.projectionLimit(limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	var out []Timer
	for _, timer := range state.timers {
		if sameScope(timer.SessionID, timer.Generation, timer.PluginID, auth) && timer.Status == TimerScheduled && !timer.DueAt.After(now) {
			out = append(out, timer)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DueAt.Equal(out[j].DueAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].DueAt.Before(out[j].DueAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PublishEvent appends a native host fact to the canonical broker WAL. The
// publisher namespace is reserved and cannot collide with an admitted plugin.
// This is a quality/audit fact seam, not a claim that a compromised
// orchestrator is a security oracle; the important boundary is that a WASM
// guest cannot forge session/generation/order or publish directly.
func (s *Service) PublishEvent(ctx context.Context, scope SessionScope, input EventInput, idem string) (Event, error) {
	if err := checkContext(ctx); err != nil {
		return Event{}, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return Event{}, err
	}
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.Kind, s.limits.MaxIDBytes, true) || len(input.Data) == 0 || len(input.Data) > s.limits.MaxDataBytes || !json.Valid(input.Data) || validateRefs(input.EvidenceRefs, s.limits) != nil {
		return Event{}, ErrInvalid
	}
	auth := Authority{SessionID: scope.SessionID, Generation: scope.Generation, PluginID: "stado.dev/broker", Principal: "broker", Actor: "broker:event-publisher"}
	return withMutation(s, auth, "event.publish", input, idem, func(state foldedState, now time.Time) (Event, string, eventEnvelope, error) {
		count := 0
		for _, event := range state.events {
			if event.SessionID == scope.SessionID && event.Generation == scope.Generation {
				count++
			}
		}
		if count >= s.limits.MaxEventRecords {
			return Event{}, "", eventEnvelope{}, ErrLimit
		}
		if input.ID == "" {
			id, err := mint("event_")
			if err != nil {
				return Event{}, "", eventEnvelope{}, err
			}
			input.ID = id
		}
		for _, event := range state.events {
			if event.SessionID == scope.SessionID && event.Generation == scope.Generation && event.ID == input.ID {
				return Event{}, "", eventEnvelope{}, fmt.Errorf("%w: event ID exists", ErrVersion)
			}
		}
		event := Event{ID: input.ID, SessionID: scope.SessionID, Generation: scope.Generation, Kind: input.Kind, Data: cloneJSON(input.Data), EvidenceRefs: cloneStrings(input.EvidenceRefs), CreatedAt: now}
		return event, "event.published", eventEnvelope{Event: &event}, nil
	})
}

// PromoteDueTimers atomically marks each due timer through its existing CAS
// transition. The resulting timer.due WAL record is itself projected as a
// durable application event, avoiding a crash window between timer state and a
// second notification write.
func (s *Service) PromoteDueTimers(ctx context.Context, auth Authority, limit int) error {
	due, err := s.DueTimers(ctx, auth, limit)
	if err != nil {
		return err
	}
	for _, timer := range due {
		idem := "timer-due:" + timer.ID + ":" + strconv.FormatUint(timer.Version, 10)
		if _, err := s.MarkTimerDue(ctx, auth, TimerCAS{ID: timer.ID, RunID: timer.RunID, ExpectedVersion: timer.Version}, idem); err != nil && !errors.Is(err, ErrTerminal) && !errors.Is(err, ErrVersion) {
			return err
		}
	}
	return nil
}

// PendingEvents returns at-least-once deliveries after the durable cursor for
// this exact plugin/session/generation. Event kinds are the signed manifest
// subscription, supplied by the native dispatcher rather than guest JSON.
func (s *Service) PendingEvents(ctx context.Context, auth Authority, kinds []string, limit int) ([]Event, EventCursor, error) {
	if err := checkContext(ctx); err != nil {
		return nil, EventCursor{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return nil, EventCursor{}, err
	}
	limit, err := s.projectionLimit(limit)
	if err != nil {
		return nil, EventCursor{}, err
	}
	if len(kinds) == 0 || len(kinds) > 64 {
		return nil, EventCursor{}, ErrInvalid
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		if invalidIdentifier(kind, s.limits.MaxIDBytes, true) {
			return nil, EventCursor{}, ErrInvalid
		}
		allowed[kind] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return nil, EventCursor{}, err
	}
	cursor := state.cursors[scopeKey(auth.SessionID, auth.Generation, auth.PluginID)]
	if cursor.SessionID == "" {
		cursor = EventCursor{SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID}
	}
	out := make([]Event, 0, limit)
	for _, event := range state.events {
		if event.SessionID != auth.SessionID || event.Generation != auth.Generation || event.WALSequence <= cursor.Sequence || (event.TargetPlugin != "" && event.TargetPlugin != auth.PluginID) {
			continue
		}
		if _, ok := allowed[event.Kind]; !ok {
			continue
		}
		// A durable asynchronous claim, final route, or native terminal recovery
		// settles delivery of this mandatory-action event even when the process
		// crashed before the cursor ack. A claim does not settle the input itself:
		// reviewing remains visible in projection and fenced below scheduling.
		// Do not replay stale input prose into a fresh application instance; later
		// subscribed events may still advance the monotonic cursor past it.
		if event.Kind == OperatorInputQueuedEvent {
			input, exists := state.operatorInputs[operatorInputKey(auth.SessionID, auth.Generation, auth.PluginID, event.SubjectID)]
			if !exists || input.Status != OperatorInputQueued {
				continue
			}
		}
		out = append(out, event)
		if len(out) == limit {
			break
		}
	}
	return out, cursor, nil
}

// EventKindAtSequence returns the kind of an event visible to this exact
// application identity. Native dispatch uses it only to prove that an
// already-settled mandatory-action event was part of the binding's signed
// subscription before advancing the durable cursor. It does not expose event
// payload or turn capture provenance as authority.
func (s *Service) EventKindAtSequence(ctx context.Context, auth Authority, sequence uint64) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	if err := s.validateAuthority(auth); err != nil {
		return "", err
	}
	if sequence == 0 {
		return "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return "", err
	}
	for _, event := range state.events {
		if event.WALSequence == sequence && event.SessionID == auth.SessionID && event.Generation == auth.Generation && (event.TargetPlugin == "" || event.TargetPlugin == auth.PluginID) {
			return event.Kind, nil
		}
	}
	return "", ErrNotFound
}

// AcknowledgeEvent advances the cursor monotonically after a valid application
// callback result. This method is invoked by the native dispatcher; no guest
// host import exposes cursor mutation.
func (s *Service) AcknowledgeEvent(ctx context.Context, auth Authority, input EventAck, idem string) (EventCursor, error) {
	if err := checkContext(ctx); err != nil {
		return EventCursor{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return EventCursor{}, err
	}
	if input.Sequence == 0 {
		return EventCursor{}, ErrInvalid
	}
	return withMutation(s, auth, "event.ack", input, idem, func(state foldedState, now time.Time) (EventCursor, string, eventEnvelope, error) {
		current := state.cursors[scopeKey(auth.SessionID, auth.Generation, auth.PluginID)]
		if current.Sequence >= input.Sequence {
			return EventCursor{}, "", eventEnvelope{}, ErrVersion
		}
		visible := false
		for _, event := range state.events {
			if event.WALSequence == input.Sequence && event.SessionID == auth.SessionID && event.Generation == auth.Generation && (event.TargetPlugin == "" || event.TargetPlugin == auth.PluginID) {
				if event.Kind == OperatorInputQueuedEvent {
					operatorInput, ok := state.operatorInputs[operatorInputKey(auth.SessionID, auth.Generation, auth.PluginID, event.SubjectID)]
					if !ok {
						return EventCursor{}, "", eventEnvelope{}, ErrNotFound
					}
					if operatorInput.Status == OperatorInputQueued {
						return EventCursor{}, "", eventEnvelope{}, ErrOperatorInputQueued
					}
				}
				visible = true
				break
			}
		}
		if !visible {
			return EventCursor{}, "", eventEnvelope{}, ErrNotFound
		}
		cursor := EventCursor{SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, Sequence: input.Sequence, UpdatedAt: now}
		return cursor, "event.acknowledged", eventEnvelope{Cursor: &cursor}, nil
	})
}

type eventMeta struct {
	Schema        int    `json:"schema"`
	SessionID     string `json:"session_id"`
	Generation    uint64 `json:"generation"`
	PluginID      string `json:"plugin_id"`
	RequestDigest string `json:"request_digest"`
}

type eventEnvelope struct {
	Meta          eventMeta       `json:"meta"`
	Journal       *JournalEntry   `json:"journal,omitempty"`
	Hold          *Hold           `json:"hold,omitempty"`
	Control       *ControlRequest `json:"control,omitempty"`
	Completion    *Completion     `json:"completion,omitempty"`
	WorkerRun     *WorkerRun      `json:"worker_run,omitempty"`
	OperatorInput *OperatorInput  `json:"operator_input,omitempty"`
	Continuation  *Continuation   `json:"continuation,omitempty"`
	Timer         *Timer          `json:"timer,omitempty"`
	Event         *Event          `json:"event,omitempty"`
	Cursor        *EventCursor    `json:"cursor,omitempty"`
}

type foldedState struct {
	asOf                   uint64
	journal                []JournalEntry
	holds                  map[string]Hold
	controls               []ControlRequest
	completions            []Completion
	workerRuns             map[string]WorkerRun
	operatorInputs         map[string]OperatorInput
	continuationDeliveries map[string]continuationDelivery
	timers                 map[string]Timer
	events                 []Event
	cursors                map[string]EventCursor
}

type mutationBuilder[T any] func(foldedState, time.Time) (T, string, eventEnvelope, error)

func withMutation[T any](s *Service, auth Authority, op string, request any, idem string, build mutationBuilder[T]) (T, error) {
	var zero T
	if err := validateIdempotencyKey(idem); err != nil {
		return zero, err
	}
	digest, err := requestDigest(auth, op, request)
	if err != nil {
		return zero, err
	}
	key := effectiveIdempotencyKey(auth, op, idem)
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.wal.Records()
	state, err := fold(records)
	if err != nil {
		return zero, err
	}
	if previous, ok, err := replayEvent(records, key, digest); err != nil {
		return zero, err
	} else if ok {
		return eventResult[T](previous)
	}
	now := s.now().UTC()
	result, eventType, event, err := build(state, now)
	if err != nil {
		return zero, err
	}
	event.Meta = eventMeta{Schema: eventSchema, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RequestDigest: digest}
	data, err := json.Marshal(event)
	if err != nil {
		return zero, err
	}
	appendResult, err := s.wal.Append(wal.Transaction{
		ID: deterministicTransactionID(key), IdempotencyKey: key,
		Principal: auth.Principal, Actor: auth.Actor,
		Events: []wal.Event{{Store: storeName, Type: eventType, Session: auth.SessionID, Data: data}},
	})
	if errors.Is(err, wal.ErrConflict) {
		previous, ok, replayErr := replayEvent(s.wal.Records(), key, digest)
		if replayErr != nil {
			return zero, replayErr
		}
		if ok {
			return eventResult[T](previous)
		}
	}
	if err != nil {
		return zero, err
	}
	stampResult(&result, appendResult.Record.Sequence)
	return result, nil
}

func replayEvent(records []wal.Record, key, digest string) (eventEnvelope, bool, error) {
	for _, record := range records {
		if record.Transaction.IdempotencyKey != key {
			continue
		}
		if len(record.Transaction.Events) != 1 || record.Transaction.Events[0].Store != storeName {
			return eventEnvelope{}, false, ErrIdempotencyConflict
		}
		var event eventEnvelope
		if err := json.Unmarshal(record.Transaction.Events[0].Data, &event); err != nil {
			return eventEnvelope{}, false, err
		}
		if event.Meta.RequestDigest != digest {
			return eventEnvelope{}, false, ErrIdempotencyConflict
		}
		stampEnvelope(&event, record.Sequence)
		return event, true, nil
	}
	return eventEnvelope{}, false, nil
}

func eventResult[T any](event eventEnvelope) (T, error) {
	var zero T
	switch any(zero).(type) {
	case JournalEntry:
		if event.Journal == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Journal).(T), nil
	case Hold:
		if event.Hold == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Hold).(T), nil
	case ControlRequest:
		if event.Control == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Control).(T), nil
	case Completion:
		if event.Completion == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Completion).(T), nil
	case WorkerRun:
		if event.WorkerRun == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.WorkerRun).(T), nil
	case OperatorInput:
		if event.OperatorInput == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.OperatorInput).(T), nil
	case Continuation:
		if event.Continuation == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Continuation).(T), nil
	case Timer:
		if event.Timer == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Timer).(T), nil
	case Event:
		if event.Event == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Event).(T), nil
	case EventCursor:
		if event.Cursor == nil {
			return zero, ErrIdempotencyConflict
		}
		return any(*event.Cursor).(T), nil
	default:
		return zero, errors.New("lifecycle application: unsupported replay result")
	}
}

func stampResult[T any](result *T, sequence uint64) {
	switch value := any(result).(type) {
	case *JournalEntry:
		value.WALSequence = sequence
	case *ControlRequest:
		value.WALSequence = sequence
	case *Completion:
		value.WALSequence = sequence
	case *WorkerRun:
		value.WALSequence = sequence
	case *OperatorInput:
		value.WALSequence = sequence
	case *Continuation:
		value.WALSequence = sequence
	case *Hold:
		value.WALSequence = sequence
	case *Timer:
		value.WALSequence = sequence
	case *Event:
		value.WALSequence = sequence
	case *EventCursor:
		value.WALSequence = sequence
	}
}

func stampEnvelope(event *eventEnvelope, sequence uint64) {
	if event.Journal != nil {
		event.Journal.WALSequence = sequence
	}
	if event.Control != nil {
		event.Control.WALSequence = sequence
	}
	if event.Completion != nil {
		event.Completion.WALSequence = sequence
	}
	if event.WorkerRun != nil {
		event.WorkerRun.WALSequence = sequence
	}
	if event.OperatorInput != nil {
		event.OperatorInput.WALSequence = sequence
	}
	if event.Continuation != nil {
		event.Continuation.WALSequence = sequence
	}
	if event.Hold != nil {
		event.Hold.WALSequence = sequence
	}
	if event.Timer != nil {
		event.Timer.WALSequence = sequence
	}
	if event.Event != nil {
		event.Event.WALSequence = sequence
	}
	if event.Cursor != nil {
		event.Cursor.WALSequence = sequence
	}
}

func fold(records []wal.Record) (foldedState, error) {
	state := foldedState{
		holds: map[string]Hold{}, workerRuns: map[string]WorkerRun{},
		operatorInputs: map[string]OperatorInput{}, continuationDeliveries: map[string]continuationDelivery{},
		timers: map[string]Timer{}, cursors: map[string]EventCursor{},
	}
	journalIDs := map[string]bool{}
	controlIDs := map[string]bool{}
	completionIDs := map[string]bool{}
	completionRuns := map[string]bool{}
	journalSequence := map[string]uint64{}
	for _, record := range records {
		if record.Sequence > state.asOf {
			state.asOf = record.Sequence
		}
		for _, raw := range record.Transaction.Events {
			if raw.Store != storeName {
				continue
			}
			var event eventEnvelope
			if err := json.Unmarshal(raw.Data, &event); err != nil {
				return foldedState{}, fmt.Errorf("lifecycle application fold: %w", err)
			}
			if event.Meta.Schema != eventSchema || event.Meta.SessionID == "" || event.Meta.Generation == 0 || event.Meta.PluginID == "" || event.Meta.RequestDigest == "" || raw.Session != event.Meta.SessionID {
				return foldedState{}, errors.New("lifecycle application fold: invalid event metadata")
			}
			if envelopePayloads(event) != 1 {
				return foldedState{}, errors.New("lifecycle application fold: event must contain exactly one payload")
			}
			stampEnvelope(&event, record.Sequence)
			scope := scopeKey(event.Meta.SessionID, event.Meta.Generation, event.Meta.PluginID)
			if handled, err := foldOperatorInputEvent(&state, raw.Type, event.Meta, event.OperatorInput, event.Continuation); err != nil {
				return foldedState{}, err
			} else if handled {
				continue
			}
			switch raw.Type {
			case "journal.appended":
				if event.Journal == nil || !entityScopeMatches(event.Meta, event.Journal.SessionID, event.Journal.Generation, event.Journal.PluginID) || event.Journal.ID == "" || event.Journal.RunID == "" || event.Journal.Kind == "" || event.Journal.CreatedAt.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed journal event")
				}
				key := scope + "\x00" + event.Journal.ID
				if journalIDs[key] || event.Journal.Sequence != journalSequence[scope]+1 {
					return foldedState{}, errors.New("lifecycle application fold: journal sequence or ID conflict")
				}
				journalIDs[key] = true
				journalSequence[scope]++
				state.journal = append(state.journal, *event.Journal)
			case "hold.acquired", "hold.renewed", "hold.released", "hold.expired":
				if event.Hold == nil || !entityScopeMatches(event.Meta, event.Hold.SessionID, event.Hold.Generation, event.Hold.PluginID) || event.Hold.Owner != event.Meta.PluginID || event.Hold.ID == "" || event.Hold.RunID == "" || event.Hold.ReasonCode == "" || event.Hold.CreatedAt.IsZero() || event.Hold.UpdatedAt.IsZero() || event.Hold.LeaseUntil.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed hold event")
				}
				recordKey := scope + "\x00" + event.Hold.ID
				old, exists := state.holds[recordKey]
				if err := validateHoldTransition(raw.Type, old, exists, *event.Hold); err != nil {
					return foldedState{}, err
				}
				state.holds[recordKey] = *event.Hold
			case "control.pause_requested", "control.stop_requested":
				wantKind := ControlPause
				if raw.Type == "control.stop_requested" {
					wantKind = ControlStop
				}
				if event.Control == nil || !entityScopeMatches(event.Meta, event.Control.SessionID, event.Control.Generation, event.Control.PluginID) || event.Control.ID == "" || event.Control.RunID == "" || event.Control.ReasonCode == "" || event.Control.CreatedAt.IsZero() || event.Control.Kind != wantKind {
					return foldedState{}, errors.New("lifecycle application fold: malformed control event")
				}
				if event.Control.HoldID != "" {
					hold, ok := state.holds[scope+"\x00"+event.Control.HoldID]
					if !ok || hold.RunID != event.Control.RunID || hold.Status != HoldActive || !hold.LeaseUntil.After(event.Control.CreatedAt) {
						return foldedState{}, errors.New("lifecycle application fold: control references invalid hold")
					}
				}
				key := scope + "\x00" + event.Control.ID
				if controlIDs[key] {
					return foldedState{}, errors.New("lifecycle application fold: control ID conflict")
				}
				controlIDs[key] = true
				state.controls = append(state.controls, *event.Control)
			case "session.completed":
				if event.Completion == nil || !entityScopeMatches(event.Meta, event.Completion.SessionID, event.Completion.Generation, event.Completion.PluginID) || event.Completion.ID == "" || event.Completion.RunID == "" || event.Completion.CreatedAt.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed completion event")
				}
				if event.Completion.ContinuationDelivered || (len(event.Completion.ContinuationInputIDs) > 0) != (event.Completion.ContinuationDeliveryID != "") || validateCompletionContinuation(state, Authority{SessionID: event.Meta.SessionID, Generation: event.Meta.Generation, PluginID: event.Meta.PluginID, Principal: "fold", Actor: "fold"}, CompletionInput{RunID: event.Completion.RunID, ContinuationInputIDs: event.Completion.ContinuationInputIDs}, MaxPendingOperatorInputs) != nil {
					return foldedState{}, errors.New("lifecycle application fold: completion has invalid deferred-input continuation")
				}
				idKey := scope + "\x00" + event.Completion.ID
				runKey := scope + "\x00" + event.Completion.RunID
				if completionIDs[idKey] || completionRuns[runKey] {
					return foldedState{}, errors.New("lifecycle application fold: completion ID or run conflict")
				}
				completionIDs[idKey] = true
				completionRuns[runKey] = true
				state.completions = append(state.completions, *event.Completion)
			case "worker.requested", "worker.resume_requested", "worker.active", "worker.cancelled", "worker.interrupted", "worker.stopped":
				if event.WorkerRun == nil || !entityScopeMatches(event.Meta, event.WorkerRun.SessionID, event.WorkerRun.Generation, event.WorkerRun.PluginID) || event.WorkerRun.Owner != event.Meta.PluginID || event.WorkerRun.RunID == "" || event.WorkerRun.Objective == "" || event.WorkerRun.Prompt == "" || event.WorkerRun.CreatedAt.IsZero() || event.WorkerRun.UpdatedAt.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed worker run event")
				}
				recordKey := scope + "\x00" + event.WorkerRun.RunID
				old, exists := state.workerRuns[recordKey]
				if err := validateWorkerRunTransition(raw.Type, old, exists, *event.WorkerRun); err != nil {
					return foldedState{}, err
				}
				if raw.Type == "worker.active" {
					for key, other := range state.workerRuns {
						if key != recordKey && other.SessionID == event.WorkerRun.SessionID && other.Generation == event.WorkerRun.Generation && effectiveWorkerRunStatus(state, other) == WorkerRunActive {
							return foldedState{}, errors.New("lifecycle application fold: multiple active worker runs")
						}
					}
				}
				state.workerRuns[recordKey] = *event.WorkerRun
			case "timer.scheduled", "timer.rescheduled", "timer.cancelled", "timer.due":
				if event.Timer == nil || !entityScopeMatches(event.Meta, event.Timer.SessionID, event.Timer.Generation, event.Timer.PluginID) || event.Timer.Owner != event.Meta.PluginID || event.Timer.ID == "" || event.Timer.RunID == "" || event.Timer.Name == "" || event.Timer.DueAt.IsZero() || event.Timer.CreatedAt.IsZero() || event.Timer.UpdatedAt.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed timer event")
				}
				recordKey := scope + "\x00" + event.Timer.ID
				old, exists := state.timers[recordKey]
				if err := validateTimerTransition(raw.Type, old, exists, *event.Timer); err != nil {
					return foldedState{}, err
				}
				state.timers[recordKey] = *event.Timer
				if raw.Type == "timer.due" {
					data, err := json.Marshal(event.Timer)
					if err != nil {
						return foldedState{}, err
					}
					state.events = append(state.events, Event{
						ID:        "timer:" + event.Timer.ID + ":" + strconv.FormatUint(event.Timer.Version, 10),
						SessionID: event.Timer.SessionID, Generation: event.Timer.Generation,
						Kind: "timer.due", Data: data, TargetPlugin: event.Timer.PluginID,
						WALSequence: record.Sequence, CreatedAt: event.Timer.UpdatedAt,
					})
				}
			case "event.published":
				if event.Event == nil || event.Event.ID == "" || event.Event.Kind == "" || len(event.Event.Data) == 0 || !json.Valid(event.Event.Data) || event.Event.CreatedAt.IsZero() || event.Event.SessionID != event.Meta.SessionID || event.Event.Generation != event.Meta.Generation {
					return foldedState{}, errors.New("lifecycle application fold: malformed published event")
				}
				state.events = append(state.events, *event.Event)
			case "event.acknowledged":
				if event.Cursor == nil || !entityScopeMatches(event.Meta, event.Cursor.SessionID, event.Cursor.Generation, event.Cursor.PluginID) || event.Cursor.Sequence == 0 || event.Cursor.UpdatedAt.IsZero() {
					return foldedState{}, errors.New("lifecycle application fold: malformed event cursor")
				}
				key := scopeKey(event.Cursor.SessionID, event.Cursor.Generation, event.Cursor.PluginID)
				previous := state.cursors[key]
				if previous.Sequence >= event.Cursor.Sequence {
					return foldedState{}, errors.New("lifecycle application fold: event cursor is not monotonic")
				}
				state.cursors[key] = *event.Cursor
			default:
				return foldedState{}, fmt.Errorf("lifecycle application fold: unknown event type %q", raw.Type)
			}
		}
	}
	return state, nil
}

func envelopePayloads(event eventEnvelope) int {
	count := 0
	for _, present := range []bool{event.Journal != nil, event.Hold != nil, event.Control != nil, event.Completion != nil, event.WorkerRun != nil, event.OperatorInput != nil, event.Continuation != nil, event.Timer != nil, event.Event != nil, event.Cursor != nil} {
		if present {
			count++
		}
	}
	return count
}

func validateHoldTransition(eventType string, old Hold, exists bool, next Hold) error {
	if !exists {
		if eventType != "hold.acquired" || next.Version != 1 || next.Status != HoldActive || !next.CreatedAt.Equal(next.UpdatedAt) || !next.LeaseUntil.After(next.UpdatedAt) {
			return errors.New("lifecycle application fold: invalid initial hold transition")
		}
		return nil
	}
	if eventType == "hold.acquired" || old.Status != HoldActive || old.Version+1 != next.Version || old.Owner != next.Owner || old.RunID != next.RunID || !old.CreatedAt.Equal(next.CreatedAt) || !sameEntityScope(old.SessionID, old.Generation, old.PluginID, next.SessionID, next.Generation, next.PluginID) || next.UpdatedAt.Before(old.UpdatedAt) {
		return errors.New("lifecycle application fold: hold version, owner, or state conflict")
	}
	want := HoldActive
	switch eventType {
	case "hold.renewed":
		if !next.LeaseUntil.After(next.UpdatedAt) {
			return errors.New("lifecycle application fold: renewed hold has expired lease")
		}
	case "hold.released":
		want = HoldReleased
		if !next.LeaseUntil.After(next.UpdatedAt) {
			return errors.New("lifecycle application fold: released hold lease was expired")
		}
	case "hold.expired":
		want = HoldExpired
		if next.LeaseUntil.After(next.UpdatedAt) {
			return errors.New("lifecycle application fold: hold expired before lease deadline")
		}
	default:
		return errors.New("lifecycle application fold: unknown hold transition")
	}
	if next.Status != want {
		return errors.New("lifecycle application fold: hold event/status mismatch")
	}
	return nil
}

func validateWorkerRunTransition(eventType string, old WorkerRun, exists bool, next WorkerRun) error {
	if !exists {
		if eventType != "worker.requested" || next.Version != 1 || next.Status != WorkerRunRequested || !next.CreatedAt.Equal(next.UpdatedAt) || next.TerminalReason != "" || next.TerminalSequence != 0 {
			return errors.New("lifecycle application fold: invalid initial worker run transition")
		}
		return nil
	}
	if eventType == "worker.requested" || old.Version+1 != next.Version || old.Owner != next.Owner || old.RunID != next.RunID || old.Objective != next.Objective || old.Prompt != next.Prompt || old.Conflict != next.Conflict || !old.CreatedAt.Equal(next.CreatedAt) || !sameEntityScope(old.SessionID, old.Generation, old.PluginID, next.SessionID, next.Generation, next.PluginID) || next.UpdatedAt.Before(old.UpdatedAt) {
		return errors.New("lifecycle application fold: worker run version, owner, or state conflict")
	}
	want := WorkerRunStatus("")
	switch eventType {
	case "worker.active":
		if old.Status != WorkerRunRequested && old.Status != WorkerRunResumeRequested {
			return errors.New("lifecycle application fold: only requested worker run may activate")
		}
		want = WorkerRunActive
	case "worker.resume_requested":
		if old.Status != WorkerRunInterrupted && old.Status != WorkerRunResumeRequested || old.TerminalSequence == 0 || strings.TrimSpace(old.TerminalReason) == "" {
			return errors.New("lifecycle application fold: only interrupted worker run may request resume")
		}
		want = WorkerRunResumeRequested
	case "worker.cancelled":
		if old.Status != WorkerRunRequested && old.Status != WorkerRunResumeRequested && old.Status != WorkerRunActive {
			return errors.New("lifecycle application fold: terminal worker run cannot cancel")
		}
		want = WorkerRunCancelled
	case "worker.interrupted":
		if old.Status != WorkerRunActive || next.TerminalSequence == 0 {
			return errors.New("lifecycle application fold: invalid interrupted worker run")
		}
		want = WorkerRunInterrupted
	case "worker.stopped":
		if old.Status != WorkerRunActive || next.TerminalSequence == 0 {
			return errors.New("lifecycle application fold: invalid stopped worker run")
		}
		want = WorkerRunStopped
	default:
		return errors.New("lifecycle application fold: unknown worker run transition")
	}
	if next.Status != want {
		return errors.New("lifecycle application fold: worker run event/status mismatch")
	}
	if want == WorkerRunActive && (next.TerminalReason != "" || next.TerminalSequence != 0) {
		return errors.New("lifecycle application fold: active worker run has terminal metadata")
	}
	if want != WorkerRunActive && want != WorkerRunResumeRequested && strings.TrimSpace(next.TerminalReason) == "" {
		return errors.New("lifecycle application fold: terminal worker run lacks reason")
	}
	if want == WorkerRunResumeRequested && (next.TerminalSequence != old.TerminalSequence || next.TerminalReason != old.TerminalReason) {
		return errors.New("lifecycle application fold: resume request changed interruption provenance")
	}
	return nil
}

func validateTimerTransition(eventType string, old Timer, exists bool, next Timer) error {
	if !exists {
		if eventType != "timer.scheduled" || next.Version != 1 || next.Status != TimerScheduled || !next.CreatedAt.Equal(next.UpdatedAt) || next.DueAt.Before(next.UpdatedAt) {
			return errors.New("lifecycle application fold: invalid initial timer transition")
		}
		return nil
	}
	if eventType == "timer.scheduled" || old.Status != TimerScheduled || old.Version+1 != next.Version || old.Owner != next.Owner || old.RunID != next.RunID || !old.CreatedAt.Equal(next.CreatedAt) || !sameEntityScope(old.SessionID, old.Generation, old.PluginID, next.SessionID, next.Generation, next.PluginID) || next.UpdatedAt.Before(old.UpdatedAt) {
		return errors.New("lifecycle application fold: timer version, owner, or state conflict")
	}
	want := TimerScheduled
	switch eventType {
	case "timer.rescheduled":
		if next.DueAt.Before(next.UpdatedAt) {
			return errors.New("lifecycle application fold: timer rescheduled in the past")
		}
	case "timer.cancelled":
		want = TimerCancelled
	case "timer.due":
		want = TimerDue
		if next.DueAt.After(next.UpdatedAt) {
			return errors.New("lifecycle application fold: timer marked due before deadline")
		}
	default:
		return errors.New("lifecycle application fold: unknown timer transition")
	}
	if next.Status != want {
		return errors.New("lifecycle application fold: timer event/status mismatch")
	}
	return nil
}

func (s *Service) validateAuthority(auth Authority) error {
	if auth.Generation == 0 || invalidAuthorityPart(auth.SessionID, s.limits.MaxIDBytes) || invalidAuthorityPart(auth.PluginID, s.limits.MaxIDBytes*2) || invalidAuthorityPart(auth.Principal, s.limits.MaxIDBytes) || invalidAuthorityPart(auth.Actor, s.limits.MaxIDBytes) {
		return fmt.Errorf("%w: incomplete or oversized authenticated authority", ErrInvalid)
	}
	return nil
}

func (s *Service) validateSessionScope(scope SessionScope) error {
	if scope.Generation == 0 || invalidAuthorityPart(scope.SessionID, s.limits.MaxIDBytes) {
		return fmt.Errorf("%w: incomplete or oversized authenticated session scope", ErrInvalid)
	}
	return nil
}

func (s *Service) validateJournal(input *JournalAppend) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.Kind, 128, true) || invalidText(input.Summary, s.limits.MaxTextBytes, true) || len(input.Data) > s.limits.MaxDataBytes || !validOptionalJSON(input.Data) || validateRefs(input.EvidenceRefs, s.limits) != nil {
		return fmt.Errorf("%w: invalid or oversized journal entry", ErrInvalid)
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	return nil
}

func (s *Service) validateHoldAcquire(input HoldAcquire) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.ReasonCode, 128, true) || invalidText(input.Reason, s.limits.MaxTextBytes, false) || input.TTL <= 0 || input.TTL > s.limits.MaxHoldTTL {
		return fmt.Errorf("%w: invalid hold request", ErrInvalid)
	}
	return nil
}

func (s *Service) validateHoldCAS(input HoldCAS) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || input.ExpectedVersion == 0 {
		return fmt.Errorf("%w: incomplete hold CAS", ErrInvalid)
	}
	return nil
}

func (s *Service) validateControl(input ControlInput) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.ReasonCode, 128, true) || invalidIdentifier(input.HoldID, s.limits.MaxIDBytes, false) || invalidText(input.Reason, s.limits.MaxTextBytes, false) || validateRefs(input.EvidenceRefs, s.limits) != nil {
		return fmt.Errorf("%w: invalid control request", ErrInvalid)
	}
	return nil
}

func (s *Service) validateCompletion(input CompletionInput) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidText(input.Summary, s.limits.MaxTextBytes, false) || validateRefs(input.EvidenceRefs, s.limits) != nil || len(input.ContinuationInputIDs) > s.limits.MaxPendingOperatorInputs {
		return fmt.Errorf("%w: invalid completion transition", ErrInvalid)
	}
	for _, id := range input.ContinuationInputIDs {
		if invalidIdentifier(id, s.limits.MaxIDBytes, true) {
			return fmt.Errorf("%w: invalid completion continuation input", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) validateWorkerRunRequest(input WorkerRunRequest) error {
	if invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidText(input.Objective, s.limits.MaxTextBytes, true) || invalidText(input.Prompt, s.limits.MaxWorkerPromptBytes, true) || input.Conflict != WorkerRunRejectOperatorLoop && input.Conflict != WorkerRunReplaceOperatorLoop {
		return fmt.Errorf("%w: invalid worker run request", ErrInvalid)
	}
	return nil
}

func (s *Service) validateWorkerRunCAS(input WorkerRunCAS) error {
	if invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || input.ExpectedVersion == 0 || invalidText(input.Reason, s.limits.MaxTextBytes, false) {
		return fmt.Errorf("%w: incomplete worker run CAS", ErrInvalid)
	}
	return nil
}

func (s *Service) validateTimerSchedule(input *TimerSchedule) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, false) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.Name, 128, true) || input.DueAt.IsZero() || len(input.Payload) > s.limits.MaxTimerPayloadBytes || !validOptionalJSON(input.Payload) {
		return fmt.Errorf("%w: invalid timer request", ErrInvalid)
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	input.DueAt = input.DueAt.UTC()
	return nil
}

func (s *Service) validateTimerCAS(input TimerCAS) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, true) || invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || input.ExpectedVersion == 0 {
		return fmt.Errorf("%w: incomplete timer CAS", ErrInvalid)
	}
	return nil
}

func (s *Service) projectionLimit(limit int) (int, error) {
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > s.limits.MaxProjectionItems {
		return 0, fmt.Errorf("%w: projection limit", ErrInvalid)
	}
	return limit, nil
}

func validateLimits(l Limits) error {
	if l.MaxIDBytes < 16 || l.MaxTextBytes < 1 || l.MaxDataBytes < 2 || l.MaxEvidenceRefs < 1 || l.MaxEvidenceRefBytes < 1 || l.MaxJournalEntries < 1 || l.MaxHoldRecords < 1 || l.MaxActiveHolds < 1 || l.MaxHoldTTL <= 0 || l.MaxControlRequests < 1 || l.MaxCompletions < 1 || l.MaxWorkerRuns < 1 || l.MaxWorkerPromptBytes < 1 || l.MaxOperatorInputs < 1 || l.MaxPendingOperatorInputs < 1 || l.MaxPendingOperatorInputs > l.MaxOperatorInputs || l.MaxOperatorInputBytes < 1 || l.MaxOperatorInputBytes > MaxOperatorInputBytes || l.MaxTimerRecords < 1 || l.MaxActiveTimers < 1 || l.MaxTimerPayloadBytes < 2 || l.MaxTimerHorizon <= 0 || l.MaxEventRecords < 1 || l.MaxProjectionItems < 1 || l.MaxActiveHolds > l.MaxHoldRecords || l.MaxActiveTimers > l.MaxTimerRecords {
		return fmt.Errorf("%w: invalid lifecycle application limits", ErrInvalid)
	}
	return nil
}

func validateRefs(refs []string, limits Limits) error {
	if len(refs) > limits.MaxEvidenceRefs {
		return ErrInvalid
	}
	for _, ref := range refs {
		if invalidBounded(ref, limits.MaxEvidenceRefBytes, true) {
			return ErrInvalid
		}
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if invalidBounded(key, 256, true) {
		return fmt.Errorf("%w: idempotency key required", ErrInvalid)
	}
	return nil
}

func invalidIdentifier(value string, max int, required bool) bool {
	if value == "" {
		return required
	}
	return len(value) > max || !utf8.ValidString(value) || !namePattern.MatchString(value)
}

func invalidText(value string, max int, required bool) bool {
	if value == "" {
		return required
	}
	return strings.TrimSpace(value) == "" || len(value) > max || !utf8.ValidString(value) || hasDisallowedControl(value)
}

func invalidBounded(value string, max int, required bool) bool {
	if value == "" {
		return required
	}
	return strings.TrimSpace(value) == "" || len(value) > max || !utf8.ValidString(value) || hasDisallowedControl(value)
}

func invalidAuthorityPart(value string, max int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func hasDisallowedControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
	}
	return false
}

func validOptionalJSON(value json.RawMessage) bool { return len(value) == 0 || json.Valid(value) }

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context required", ErrInvalid)
	}
	return ctx.Err()
}

func checkHoldScope(hold Hold, auth Authority, runID string) error {
	if !sameScope(hold.SessionID, hold.Generation, hold.PluginID, auth) || hold.Owner != auth.PluginID || hold.RunID != runID {
		return ErrScope
	}
	return nil
}

func checkTimerScope(timer Timer, auth Authority, runID string) error {
	if !sameScope(timer.SessionID, timer.Generation, timer.PluginID, auth) || timer.Owner != auth.PluginID || timer.RunID != runID {
		return ErrScope
	}
	return nil
}

func scopedJournal(state foldedState, auth Authority) []JournalEntry {
	var out []JournalEntry
	for _, entry := range state.journal {
		if sameScope(entry.SessionID, entry.Generation, entry.PluginID, auth) {
			out = append(out, entry)
		}
	}
	return out
}

func scopedControls(state foldedState, auth Authority) []ControlRequest {
	var out []ControlRequest
	for _, request := range state.controls {
		if sameScope(request.SessionID, request.Generation, request.PluginID, auth) {
			out = append(out, request)
		}
	}
	return out
}

func scopedCompletions(state foldedState, auth Authority) []Completion {
	var out []Completion
	for _, completion := range state.completions {
		if sameScope(completion.SessionID, completion.Generation, completion.PluginID, auth) {
			out = append(out, completion)
		}
	}
	return out
}

func scopedWorkerRunCount(state foldedState, auth Authority) int {
	count := 0
	for _, run := range state.workerRuns {
		if sameScope(run.SessionID, run.Generation, run.PluginID, auth) {
			count++
		}
	}
	return count
}

func effectiveWorkerRunStatus(state foldedState, run WorkerRun) WorkerRunStatus {
	if run.Status == WorkerRunCancelled || run.Status == WorkerRunCompleted || run.Status == WorkerRunStopped {
		return run.Status
	}
	for _, completion := range state.completions {
		if completion.SessionID == run.SessionID && completion.Generation == run.Generation && completion.PluginID == run.PluginID && completion.RunID == run.RunID {
			return WorkerRunCompleted
		}
	}
	return run.Status
}

func workerRunHasLaterStop(state foldedState, run WorkerRun) bool {
	for _, control := range state.controls {
		if control.SessionID == run.SessionID && control.Generation == run.Generation &&
			control.Kind == ControlStop && control.WALSequence > run.TerminalSequence {
			return true
		}
	}
	return false
}

func workerRunHasLaterPause(state foldedState, run WorkerRun) bool {
	for _, control := range state.controls {
		if control.SessionID == run.SessionID && control.Generation == run.Generation &&
			control.Kind == ControlPause && control.WALSequence > run.WALSequence {
			return true
		}
	}
	return false
}

func workerRunHasLaterCompletion(state foldedState, run WorkerRun) bool {
	for _, completion := range state.completions {
		if completion.SessionID == run.SessionID && completion.Generation == run.Generation && completion.WALSequence > run.TerminalSequence {
			return true
		}
	}
	return false
}

func workerRunHasActiveHold(state foldedState, run WorkerRun, now time.Time) bool {
	for _, hold := range state.holds {
		if hold.SessionID == run.SessionID && hold.Generation == run.Generation &&
			hold.Status == HoldActive && hold.LeaseUntil.After(now) {
			return true
		}
	}
	return false
}

func projectWorkerRun(state foldedState, run WorkerRun) WorkerRun {
	for _, completion := range state.completions {
		if completion.SessionID == run.SessionID && completion.Generation == run.Generation && completion.PluginID == run.PluginID && completion.RunID == run.RunID {
			run.Status = WorkerRunCompleted
			run.TerminalReason = completion.Summary
			if run.TerminalReason == "" {
				run.TerminalReason = "application completed worker run"
			}
			run.TerminalSequence = completion.WALSequence
			run.UpdatedAt = completion.CreatedAt
			break
		}
	}
	return run
}

func scopedHoldCount(state foldedState, auth Authority, activeOnly bool) int {
	count := 0
	for _, hold := range state.holds {
		if sameScope(hold.SessionID, hold.Generation, hold.PluginID, auth) && (!activeOnly || hold.Status == HoldActive) {
			count++
		}
	}
	return count
}

func scopedTimerCount(state foldedState, auth Authority, activeOnly bool) int {
	count := 0
	for _, timer := range state.timers {
		if sameScope(timer.SessionID, timer.Generation, timer.PluginID, auth) && (!activeOnly || timer.Status == TimerScheduled) {
			count++
		}
	}
	return count
}

func sameScope(session string, generation uint64, plugin string, auth Authority) bool {
	return session == auth.SessionID && generation == auth.Generation && plugin == auth.PluginID
}

func sameEntityScope(aSession string, aGeneration uint64, aPlugin, bSession string, bGeneration uint64, bPlugin string) bool {
	return aSession == bSession && aGeneration == bGeneration && aPlugin == bPlugin
}

func entityScopeMatches(meta eventMeta, session string, generation uint64, plugin string) bool {
	return meta.SessionID == session && meta.Generation == generation && meta.PluginID == plugin
}

func scopeKey(session string, generation uint64, plugin string) string {
	return session + "\x00" + strconv.FormatUint(generation, 10) + "\x00" + plugin
}

func entityKey(auth Authority, id string) string {
	return scopeKey(auth.SessionID, auth.Generation, auth.PluginID) + "\x00" + id
}

func effectiveIdempotencyKey(auth Authority, op, key string) string {
	sum := sha256.Sum256([]byte(scopeKey(auth.SessionID, auth.Generation, auth.PluginID) + "\x00" + op + "\x00" + key))
	return "application:" + hex.EncodeToString(sum[:])
}

func deterministicTransactionID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "tx_application_" + hex.EncodeToString(sum[:12])
}

func requestDigest(auth Authority, op string, request any) (string, error) {
	data, err := json.Marshal(struct {
		Authority Authority `json:"authority"`
		Operation string    `json:"operation"`
		Request   any       `json:"request"`
	}{auth, op, request})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func tailJournal(all []JournalEntry, limit int) ([]JournalEntry, bool) {
	if len(all) <= limit {
		return append([]JournalEntry(nil), all...), false
	}
	return append([]JournalEntry(nil), all[len(all)-limit:]...), true
}

func tailControls(all []ControlRequest, limit int) ([]ControlRequest, bool) {
	if len(all) <= limit {
		return append([]ControlRequest(nil), all...), false
	}
	return append([]ControlRequest(nil), all[len(all)-limit:]...), true
}

func tailCompletions(all []Completion, limit int) ([]Completion, bool) {
	if len(all) <= limit {
		return append([]Completion(nil), all...), false
	}
	return append([]Completion(nil), all[len(all)-limit:]...), true
}

func cloneJSON(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }
func cloneStrings(value []string) []string            { return append([]string(nil), value...) }

func mint(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("lifecycle application: mint ID: %w", err)
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
