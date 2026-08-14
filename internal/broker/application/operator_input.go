package application

// Generic durable operator-input routing for application-owned worker runs.
//
// The broker records what the native session controller observed: immutable
// input bytes, the exact active worker-run attachment, and durable order. A
// signed application may classify that input as ready for the worker or as a
// deferred task, but it cannot replace, discard, or use the input as authority.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxOperatorInputBytes       = 48 << 10
	MaxPendingOperatorInputs    = 64
	OperatorInputQueuedEvent    = "operator.input.queued"
	operatorInputEventSchemaV1  = "stado.dev/operator-input/v1"
	maxOperatorInputLabelBytes  = 256
	maxOperatorInputReasonBytes = 4 << 10
	defaultDeferredTaskLimit    = 8
	maxNativeDeferredSummaries  = 16
)

var (
	ErrNoInputOwner            = errors.New("lifecycle application: no active worker run owns operator input")
	ErrOperatorInputQueued     = errors.New("lifecycle application: operator input still requires application routing")
	ErrOperatorInputUnresolved = errors.New("lifecycle application: operator input is unresolved")
)

type OperatorInputStatus string

const (
	OperatorInputQueued    OperatorInputStatus = "queued"
	OperatorInputReviewing OperatorInputStatus = "reviewing"
	OperatorInputReady     OperatorInputStatus = "ready"
	OperatorInputDeferred  OperatorInputStatus = "deferred"
	OperatorInputDelivered OperatorInputStatus = "delivered"
)

type OperatorInputDisposition string

const (
	OperatorInputDeliver OperatorInputDisposition = "deliver"
	OperatorInputDefer   OperatorInputDisposition = "defer"
)

// OperatorInput is broker-owned. Text, digest, owner, run, and ordinal are
// immutable after capture. Label and rationale are bounded application prose
// and never become host facts or authorization inputs.
type OperatorInput struct {
	ID              string              `json:"id"`
	SessionID       string              `json:"session_id"`
	Generation      uint64              `json:"generation"`
	PluginID        string              `json:"plugin_id"`
	RunID           string              `json:"run_id"`
	Ordinal         uint64              `json:"ordinal"`
	Version         uint64              `json:"version"`
	WALSequence     uint64              `json:"wal_sequence"`
	Text            string              `json:"text"`
	Digest          string              `json:"digest"`
	Status          OperatorInputStatus `json:"status"`
	Label           string              `json:"label,omitempty"`
	Rationale       string              `json:"rationale,omitempty"`
	ReviewID        string              `json:"review_id,omitempty"`
	TaskID          string              `json:"task_id,omitempty"`
	ReceiverInputID string              `json:"receiver_input_id,omitempty"`
	Recovered       bool                `json:"recovered,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

// DeferredTask is a projection of one deferred OperatorInput, not a second
// writer or marker-linked task store. The original text remains immutable and
// can be paged through the application projection.
type DeferredTask struct {
	ID        string    `json:"id"`
	InputID   string    `json:"input_id"`
	RunID     string    `json:"run_id"`
	Ordinal   uint64    `json:"ordinal"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Rationale string    `json:"rationale,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// DeferredTaskSummary is the bounded native recovery shape. It deliberately
// omits original text and rationale: the immutable input remains available to
// its owning application, while native UI only needs count and bounded titles
// to keep orphaned work discoverable.
type DeferredTaskSummary struct {
	ID      string `json:"id"`
	InputID string `json:"input_id"`
	RunID   string `json:"run_id"`
	Ordinal uint64 `json:"ordinal"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}

const (
	DeferredTaskOpen                = "open"
	DeferredTaskPendingContinuation = "pending_continuation"
	DeferredTaskContinued           = "continued"
)

type OperatorInputRoute struct {
	InputID         string                   `json:"input_id"`
	RunID           string                   `json:"run_id"`
	ExpectedVersion uint64                   `json:"expected_version"`
	Disposition     OperatorInputDisposition `json:"disposition"`
	ReviewID        string                   `json:"review_id,omitempty"`
	Label           string                   `json:"label,omitempty"`
	Rationale       string                   `json:"rationale,omitempty"`
}

// OperatorInputClaim durably accepts responsibility for asynchronous
// classification. ReviewID is bounded application metadata used only for
// crash-safe correlation; it is not an agent identity or authority claim.
type OperatorInputClaim struct {
	InputID         string `json:"input_id"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	ReviewID        string `json:"review_id"`
}

// OperatorInputCommit is native-only. Recovery may commit queued input only
// after its owning worker run is terminal; ordinary related delivery requires
// an application-routed ready input.
type OperatorInputCommit struct {
	InputID         string `json:"input_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	ReceiverInputID string `json:"receiver_input_id"`
	Recovery        bool   `json:"recovery,omitempty"`
}

type ContinuationStatus string

const (
	ContinuationPending   ContinuationStatus = "pending"
	ContinuationDelivered ContinuationStatus = "delivered"
)

// Continuation is derived from an immutable completion plus its optional
// native delivery record. InputIDs retain application-selected order, while
// the broker validates that they are exactly the run's deferred input set.
type Continuation struct {
	CompletionID    string             `json:"completion_id"`
	DeliveryID      string             `json:"delivery_id"`
	SessionID       string             `json:"session_id"`
	Generation      uint64             `json:"generation"`
	PluginID        string             `json:"plugin_id"`
	RunID           string             `json:"run_id"`
	InputIDs        []string           `json:"input_ids"`
	Status          ContinuationStatus `json:"status"`
	ReceiverInputID string             `json:"receiver_input_id,omitempty"`
	WALSequence     uint64             `json:"wal_sequence"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// ContinuationInput carries each immutable original independently. Native
// delivery can therefore append the exact user messages in application-chosen
// order without constructing new policy prose or truncating task content.
type ContinuationInput struct {
	ID      string `json:"id"`
	Ordinal uint64 `json:"ordinal"`
	Text    string `json:"text"`
	Digest  string `json:"digest"`
}

type ContinuationCommit struct {
	CompletionID    string `json:"completion_id"`
	RunID           string `json:"run_id"`
	ReceiverInputID string `json:"receiver_input_id"`
}

type continuationDelivery struct {
	CompletionID    string    `json:"completion_id"`
	SessionID       string    `json:"session_id"`
	Generation      uint64    `json:"generation"`
	PluginID        string    `json:"plugin_id"`
	RunID           string    `json:"run_id"`
	ReceiverInputID string    `json:"receiver_input_id"`
	WALSequence     uint64    `json:"wal_sequence"`
	CreatedAt       time.Time `json:"created_at"`
}

type operatorInputCaptureRequest struct {
	Text string `json:"text"`
}

type operatorInputQueuedV1 struct {
	Schema  string `json:"schema"`
	InputID string `json:"input_id"`
	RunID   string `json:"run_id"`
	Version uint64 `json:"version"`
	Ordinal uint64 `json:"ordinal"`
	Text    string `json:"text"`
	Digest  string `json:"digest"`
}

// CaptureOperatorInput is native-only. The active worker run is resolved under
// the same broker serialization boundary as the append, so a caller cannot
// select a plugin/run or race completion into a differently owned record.
func (s *Service) CaptureOperatorInput(ctx context.Context, scope SessionScope, text, idem string) (OperatorInput, error) {
	if err := checkContext(ctx); err != nil {
		return OperatorInput{}, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return OperatorInput{}, err
	}
	if invalidOperatorInputText(text, s.limits.MaxOperatorInputBytes) {
		return OperatorInput{}, fmt.Errorf("%w: operator input must be valid UTF-8 and at most %d bytes", ErrInvalid, s.limits.MaxOperatorInputBytes)
	}
	auth := Authority{SessionID: scope.SessionID, Generation: scope.Generation, PluginID: "stado.dev/broker", Principal: "broker", Actor: "broker:operator-input"}
	request := operatorInputCaptureRequest{Text: text}
	return withMutation(s, auth, "operator_input.capture", request, idem, func(state foldedState, now time.Time) (OperatorInput, string, eventEnvelope, error) {
		run, ok := activeWorkerRunForScope(state, scope)
		if !ok {
			return OperatorInput{}, "", eventEnvelope{}, ErrNoInputOwner
		}
		retained, pending, ordinal := 0, 0, uint64(0)
		for _, input := range state.operatorInputs {
			if !operatorInputRunMatches(input, run) {
				continue
			}
			retained++
			if input.Status == OperatorInputQueued || input.Status == OperatorInputReviewing || input.Status == OperatorInputReady {
				pending++
			}
			if input.Ordinal > ordinal {
				ordinal = input.Ordinal
			}
		}
		if retained >= s.limits.MaxOperatorInputs || pending >= s.limits.MaxPendingOperatorInputs {
			return OperatorInput{}, "", eventEnvelope{}, ErrLimit
		}
		id, err := mint("operator_input_")
		if err != nil {
			return OperatorInput{}, "", eventEnvelope{}, err
		}
		input := OperatorInput{
			ID: id, SessionID: scope.SessionID, Generation: scope.Generation,
			PluginID: run.PluginID, RunID: run.RunID, Ordinal: ordinal + 1,
			Version: 1, Text: text, Digest: operatorInputDigest(text),
			Status: OperatorInputQueued, CreatedAt: now, UpdatedAt: now,
		}
		return input, "operator_input.queued", eventEnvelope{OperatorInput: &input}, nil
	})
}

// ClaimOperatorInput moves an exact queued record to reviewing so the
// application can acknowledge the mandatory event while an asynchronous job
// classifies it. The original remains unresolved for scheduling, completion,
// cancellation, and terminal recovery until an exact route lands or the
// terminal native recovery path delivers it unchanged.
func (s *Service) ClaimOperatorInput(ctx context.Context, auth Authority, claim OperatorInputClaim, idem string) (OperatorInput, error) {
	if err := checkContext(ctx); err != nil {
		return OperatorInput{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return OperatorInput{}, err
	}
	if invalidIdentifier(claim.InputID, s.limits.MaxIDBytes, true) || invalidIdentifier(claim.RunID, s.limits.MaxIDBytes, true) || claim.ExpectedVersion == 0 || invalidIdentifier(claim.ReviewID, s.limits.MaxIDBytes, true) {
		return OperatorInput{}, ErrInvalid
	}
	return withMutation(s, auth, "operator_input.claim", claim, idem, func(state foldedState, now time.Time) (OperatorInput, string, eventEnvelope, error) {
		key := operatorInputKey(auth.SessionID, auth.Generation, auth.PluginID, claim.InputID)
		current, ok := state.operatorInputs[key]
		if !ok {
			return OperatorInput{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.RunID != claim.RunID || current.PluginID != auth.PluginID {
			return OperatorInput{}, "", eventEnvelope{}, ErrScope
		}
		run, exists := state.workerRuns[scopeKey(current.SessionID, current.Generation, current.PluginID)+"\x00"+current.RunID]
		if !exists || effectiveWorkerRunStatus(state, run) != WorkerRunActive {
			return OperatorInput{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != claim.ExpectedVersion {
			return OperatorInput{}, "", eventEnvelope{}, ErrVersion
		}
		if current.Status != OperatorInputQueued {
			return OperatorInput{}, "", eventEnvelope{}, ErrTerminal
		}
		current.Version++
		current.Status = OperatorInputReviewing
		current.ReviewID = claim.ReviewID
		current.UpdatedAt = now
		return current, "operator_input.reviewing", eventEnvelope{OperatorInput: &current}, nil
	})
}

// RouteOperatorInput lets the owning signed application classify one exact
// queued or claimed input. It has no discard operation and cannot alter the
// original. A claimed record requires the exact review correlation ID.
func (s *Service) RouteOperatorInput(ctx context.Context, auth Authority, route OperatorInputRoute, idem string) (OperatorInput, error) {
	if err := checkContext(ctx); err != nil {
		return OperatorInput{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return OperatorInput{}, err
	}
	if err := s.validateOperatorInputRoute(route); err != nil {
		return OperatorInput{}, err
	}
	return withMutation(s, auth, "operator_input.route", route, idem, func(state foldedState, now time.Time) (OperatorInput, string, eventEnvelope, error) {
		key := operatorInputKey(auth.SessionID, auth.Generation, auth.PluginID, route.InputID)
		current, ok := state.operatorInputs[key]
		if !ok {
			return OperatorInput{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.RunID != route.RunID || current.PluginID != auth.PluginID {
			return OperatorInput{}, "", eventEnvelope{}, ErrScope
		}
		run, exists := state.workerRuns[scopeKey(current.SessionID, current.Generation, current.PluginID)+"\x00"+current.RunID]
		if !exists || effectiveWorkerRunStatus(state, run) != WorkerRunActive {
			return OperatorInput{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != route.ExpectedVersion {
			return OperatorInput{}, "", eventEnvelope{}, ErrVersion
		}
		if current.Status != OperatorInputQueued && current.Status != OperatorInputReviewing {
			return OperatorInput{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Status == OperatorInputReviewing {
			if route.ReviewID == "" || route.ReviewID != current.ReviewID {
				return OperatorInput{}, "", eventEnvelope{}, ErrScope
			}
		} else if route.ReviewID != "" {
			return OperatorInput{}, "", eventEnvelope{}, ErrInvalid
		}
		current.Version++
		current.Label = strings.TrimSpace(route.Label)
		current.Rationale = strings.TrimSpace(route.Rationale)
		current.UpdatedAt = now
		eventType := "operator_input.ready"
		if route.Disposition == OperatorInputDefer {
			taskID, err := mint("deferred_task_")
			if err != nil {
				return OperatorInput{}, "", eventEnvelope{}, err
			}
			current.Status = OperatorInputDeferred
			current.TaskID = taskID
			eventType = "operator_input.deferred"
		} else {
			current.Status = OperatorInputReady
		}
		return current, eventType, eventEnvelope{OperatorInput: &current}, nil
	})
}

func (s *Service) CommitOperatorInput(ctx context.Context, scope SessionScope, commit OperatorInputCommit, idem string) (OperatorInput, error) {
	if err := checkContext(ctx); err != nil {
		return OperatorInput{}, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return OperatorInput{}, err
	}
	if invalidIdentifier(commit.InputID, s.limits.MaxIDBytes, true) || commit.ExpectedVersion == 0 || invalidIdentifier(commit.ReceiverInputID, s.limits.MaxIDBytes, true) {
		return OperatorInput{}, ErrInvalid
	}
	auth := Authority{SessionID: scope.SessionID, Generation: scope.Generation, PluginID: "stado.dev/broker", Principal: "broker", Actor: "broker:operator-input"}
	return withMutation(s, auth, "operator_input.commit", commit, idem, func(state foldedState, now time.Time) (OperatorInput, string, eventEnvelope, error) {
		current, ok := operatorInputByNativeScope(state, scope, commit.InputID)
		if !ok {
			return OperatorInput{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.Version != commit.ExpectedVersion {
			return OperatorInput{}, "", eventEnvelope{}, ErrVersion
		}
		if commit.ReceiverInputID != current.ID {
			return OperatorInput{}, "", eventEnvelope{}, ErrScope
		}
		if commit.Recovery {
			run, exists := state.workerRuns[scopeKey(current.SessionID, current.Generation, current.PluginID)+"\x00"+current.RunID]
			if !exists || !workerRunTerminal(state, run) || (current.Status != OperatorInputQueued && current.Status != OperatorInputReviewing && current.Status != OperatorInputReady) {
				return OperatorInput{}, "", eventEnvelope{}, ErrInvalid
			}
		} else if current.Status != OperatorInputReady {
			return OperatorInput{}, "", eventEnvelope{}, ErrInvalid
		}
		current.Version++
		current.Status = OperatorInputDelivered
		current.ReceiverInputID = commit.ReceiverInputID
		current.Recovered = commit.Recovery
		current.UpdatedAt = now
		return current, "operator_input.delivered", eventEnvelope{OperatorInput: &current}, nil
	})
}

func (s *Service) CommitContinuation(ctx context.Context, scope SessionScope, commit ContinuationCommit, idem string) (Continuation, error) {
	if err := checkContext(ctx); err != nil {
		return Continuation{}, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return Continuation{}, err
	}
	if invalidIdentifier(commit.CompletionID, s.limits.MaxIDBytes, true) || invalidIdentifier(commit.RunID, s.limits.MaxIDBytes, true) || invalidIdentifier(commit.ReceiverInputID, s.limits.MaxIDBytes, true) {
		return Continuation{}, ErrInvalid
	}
	auth := Authority{SessionID: scope.SessionID, Generation: scope.Generation, PluginID: "stado.dev/broker", Principal: "broker", Actor: "broker:operator-input"}
	return withMutation(s, auth, "continuation.commit", commit, idem, func(state foldedState, now time.Time) (Continuation, string, eventEnvelope, error) {
		completion, ok := completionByNativeScope(state, scope, commit.CompletionID)
		if !ok || completion.RunID != commit.RunID {
			return Continuation{}, "", eventEnvelope{}, ErrNotFound
		}
		if len(completion.ContinuationInputIDs) == 0 {
			return Continuation{}, "", eventEnvelope{}, ErrInvalid
		}
		if completion.ContinuationDeliveryID == "" || commit.ReceiverInputID != completion.ContinuationDeliveryID {
			return Continuation{}, "", eventEnvelope{}, ErrScope
		}
		if _, exists := state.continuationDeliveries[continuationKey(completion)]; exists {
			return Continuation{}, "", eventEnvelope{}, ErrTerminal
		}
		delivery := continuationDelivery{
			CompletionID: completion.ID, SessionID: completion.SessionID,
			Generation: completion.Generation, PluginID: completion.PluginID,
			RunID: completion.RunID, ReceiverInputID: commit.ReceiverInputID,
			CreatedAt: now,
		}
		continuation := continuationFrom(completion, &delivery)
		return continuation, "continuation.delivered", eventEnvelope{Continuation: &continuation}, nil
	})
}

// PendingContinuationInputs returns immutable originals for one native
// receiver projection. It refuses delivered or mismatched completions; WASM
// sees only the ordered IDs it selected at completion time.
func (s *Service) PendingContinuationInputs(ctx context.Context, scope SessionScope, completionID string) ([]ContinuationInput, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := s.validateSessionScope(scope); err != nil {
		return nil, err
	}
	if invalidIdentifier(completionID, s.limits.MaxIDBytes, true) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	completion, ok := completionByNativeScope(state, scope, completionID)
	if !ok {
		return nil, ErrNotFound
	}
	if len(completion.ContinuationInputIDs) == 0 {
		return nil, ErrInvalid
	}
	if _, delivered := state.continuationDeliveries[continuationKey(completion)]; delivered {
		return nil, ErrTerminal
	}
	return continuationInputs(state, completion)
}

func (s *Service) validateOperatorInputRoute(route OperatorInputRoute) error {
	if invalidIdentifier(route.InputID, s.limits.MaxIDBytes, true) || invalidIdentifier(route.RunID, s.limits.MaxIDBytes, true) || route.ExpectedVersion == 0 {
		return ErrInvalid
	}
	if route.Disposition != OperatorInputDeliver && route.Disposition != OperatorInputDefer {
		return ErrInvalid
	}
	if route.ReviewID != "" && invalidIdentifier(route.ReviewID, s.limits.MaxIDBytes, true) {
		return ErrInvalid
	}
	if invalidOptionalText(route.Label, maxOperatorInputLabelBytes) || invalidOptionalText(route.Rationale, maxOperatorInputReasonBytes) {
		return ErrInvalid
	}
	return nil
}

func validateCompletionContinuation(state foldedState, auth Authority, input CompletionInput, limit int) error {
	if len(input.ContinuationInputIDs) > limit {
		return ErrLimit
	}
	deferred := make(map[string]struct{})
	for _, operatorInput := range state.operatorInputs {
		if !sameScope(operatorInput.SessionID, operatorInput.Generation, operatorInput.PluginID, auth) || operatorInput.RunID != input.RunID {
			continue
		}
		switch operatorInput.Status {
		case OperatorInputQueued, OperatorInputReviewing, OperatorInputReady:
			return ErrOperatorInputUnresolved
		case OperatorInputDeferred:
			deferred[operatorInput.ID] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(input.ContinuationInputIDs))
	for _, id := range input.ContinuationInputIDs {
		if invalidIdentifier(id, 256, true) {
			return ErrInvalid
		}
		if _, duplicate := seen[id]; duplicate {
			return ErrInvalid
		}
		if _, exists := deferred[id]; !exists {
			return ErrScope
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(deferred) {
		return ErrOperatorInputUnresolved
	}
	return nil
}

func deferredTasksFor(state foldedState, auth Authority) []DeferredTask {
	var tasks []DeferredTask
	for _, input := range state.operatorInputs {
		if !sameScope(input.SessionID, input.Generation, input.PluginID, auth) || input.Status != OperatorInputDeferred {
			continue
		}
		tasks = append(tasks, deferredTaskFromInput(input, deferredTaskStatus(state, input)))
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Ordinal == tasks[j].Ordinal {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].Ordinal < tasks[j].Ordinal
	})
	return tasks
}

func deferredTaskStatus(state foldedState, input OperatorInput) string {
	for _, completion := range state.completions {
		if completion.SessionID != input.SessionID || completion.Generation != input.Generation || completion.PluginID != input.PluginID || completion.RunID != input.RunID {
			continue
		}
		for _, inputID := range completion.ContinuationInputIDs {
			if inputID != input.ID {
				continue
			}
			if _, delivered := state.continuationDeliveries[continuationKey(completion)]; delivered {
				return DeferredTaskContinued
			}
			return DeferredTaskPendingContinuation
		}
	}
	return DeferredTaskOpen
}

func deferredTaskFromInput(input OperatorInput, status string) DeferredTask {
	title := strings.TrimSpace(input.Label)
	if title == "" {
		title = strings.Join(strings.Fields(input.Text), " ")
	}
	title = truncateUTF8(title, maxOperatorInputLabelBytes)
	return DeferredTask{
		ID: input.TaskID, InputID: input.ID, RunID: input.RunID,
		Ordinal: input.Ordinal, Title: title, Text: input.Text,
		Rationale: input.Rationale, Status: status, CreatedAt: input.UpdatedAt,
	}
}

func deferredTaskSummaryFromInput(input OperatorInput, status string) DeferredTaskSummary {
	task := deferredTaskFromInput(input, status)
	return DeferredTaskSummary{
		ID: task.ID, InputID: task.InputID, RunID: task.RunID,
		Ordinal: task.Ordinal, Title: task.Title, Status: task.Status,
	}
}

func activeWorkerRunForScope(state foldedState, scope SessionScope) (WorkerRun, bool) {
	var active WorkerRun
	found := false
	for _, run := range state.workerRuns {
		if run.SessionID != scope.SessionID || run.Generation != scope.Generation || effectiveWorkerRunStatus(state, run) != WorkerRunActive {
			continue
		}
		if found {
			return WorkerRun{}, false
		}
		active, found = run, true
	}
	return projectWorkerRun(state, active), found
}

func workerRunTerminal(state foldedState, run WorkerRun) bool {
	status := effectiveWorkerRunStatus(state, run)
	return status == WorkerRunCancelled || status == WorkerRunCompleted || status == WorkerRunInterrupted || status == WorkerRunStopped
}

func operatorInputRunMatches(input OperatorInput, run WorkerRun) bool {
	return input.SessionID == run.SessionID && input.Generation == run.Generation && input.PluginID == run.PluginID && input.RunID == run.RunID
}

func operatorInputByNativeScope(state foldedState, scope SessionScope, id string) (OperatorInput, bool) {
	var found OperatorInput
	ok := false
	for _, input := range state.operatorInputs {
		if input.SessionID == scope.SessionID && input.Generation == scope.Generation && input.ID == id {
			if ok {
				return OperatorInput{}, false
			}
			found, ok = input, true
		}
	}
	return found, ok
}

func completionByNativeScope(state foldedState, scope SessionScope, id string) (Completion, bool) {
	for _, completion := range state.completions {
		if completion.SessionID == scope.SessionID && completion.Generation == scope.Generation && completion.ID == id {
			return completion, true
		}
	}
	return Completion{}, false
}

func operatorInputKey(sessionID string, generation uint64, pluginID, id string) string {
	return scopeKey(sessionID, generation, pluginID) + "\x00" + id
}

func continuationKey(completion Completion) string {
	return operatorInputKey(completion.SessionID, completion.Generation, completion.PluginID, completion.ID)
}

func continuationFrom(completion Completion, delivery *continuationDelivery) Continuation {
	status := ContinuationPending
	updated := completion.CreatedAt
	receiverInputID := ""
	sequence := completion.WALSequence
	if delivery != nil {
		status = ContinuationDelivered
		updated = delivery.CreatedAt
		receiverInputID = delivery.ReceiverInputID
		sequence = delivery.WALSequence
	}
	return Continuation{
		CompletionID: completion.ID, SessionID: completion.SessionID,
		DeliveryID: completion.ContinuationDeliveryID,
		Generation: completion.Generation, PluginID: completion.PluginID,
		RunID: completion.RunID, InputIDs: cloneStrings(completion.ContinuationInputIDs),
		Status: status, ReceiverInputID: receiverInputID, WALSequence: sequence,
		CreatedAt: completion.CreatedAt, UpdatedAt: updated,
	}
}

func continuationInputs(state foldedState, completion Completion) ([]ContinuationInput, error) {
	inputs := make([]ContinuationInput, 0, len(completion.ContinuationInputIDs))
	for _, id := range completion.ContinuationInputIDs {
		input, ok := state.operatorInputs[operatorInputKey(completion.SessionID, completion.Generation, completion.PluginID, id)]
		if !ok || input.RunID != completion.RunID || input.Status != OperatorInputDeferred {
			return nil, ErrScope
		}
		inputs = append(inputs, ContinuationInput{ID: input.ID, Ordinal: input.Ordinal, Text: input.Text, Digest: input.Digest})
	}
	return inputs, nil
}

func invalidOperatorInputText(text string, max int) bool {
	return strings.TrimSpace(text) == "" || len(text) > max || !utf8.ValidString(text) || hasDisallowedControl(text)
}

func invalidOptionalText(text string, max int) bool {
	return text != "" && invalidText(text, max, false)
}

func operatorInputDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func operatorInputQueuedEvent(input OperatorInput) (Event, error) {
	data, err := json.Marshal(operatorInputQueuedV1{
		Schema: operatorInputEventSchemaV1, InputID: input.ID, RunID: input.RunID,
		Version: input.Version, Ordinal: input.Ordinal, Text: input.Text, Digest: input.Digest,
	})
	if err != nil {
		return Event{}, err
	}
	return Event{
		ID: "operator-input:" + input.ID, SessionID: input.SessionID,
		Generation: input.Generation, Kind: OperatorInputQueuedEvent,
		Data: data, TargetPlugin: input.PluginID, WALSequence: input.WALSequence,
		CreatedAt: input.CreatedAt, SubjectID: input.ID,
	}, nil
}

func foldOperatorInputEvent(state *foldedState, rawType string, meta eventMeta, input *OperatorInput, continuation *Continuation) (bool, error) {
	switch rawType {
	case "operator_input.queued", "operator_input.reviewing", "operator_input.ready", "operator_input.deferred", "operator_input.delivered":
		if input == nil || continuation != nil || input.ID == "" || input.SessionID != meta.SessionID || input.Generation != meta.Generation || input.PluginID == "" || input.RunID == "" || input.Ordinal == 0 || input.Version == 0 || input.Digest != operatorInputDigest(input.Text) || invalidOperatorInputText(input.Text, MaxOperatorInputBytes) || input.ReviewID != "" && invalidIdentifier(input.ReviewID, 256, true) || input.CreatedAt.IsZero() || input.UpdatedAt.IsZero() {
			return true, errors.New("lifecycle application fold: malformed operator input event")
		}
		if rawType == "operator_input.queued" {
			if meta.PluginID != "stado.dev/broker" {
				return true, errors.New("lifecycle application fold: operator input capture is not broker-authored")
			}
		} else if rawType == "operator_input.reviewing" || rawType == "operator_input.ready" || rawType == "operator_input.deferred" {
			if meta.PluginID != input.PluginID {
				return true, errors.New("lifecycle application fold: operator input route scope mismatch")
			}
		} else if meta.PluginID != "stado.dev/broker" {
			return true, errors.New("lifecycle application fold: operator input commit is not broker-authored")
		}
		key := operatorInputKey(input.SessionID, input.Generation, input.PluginID, input.ID)
		old, exists := state.operatorInputs[key]
		if err := validateOperatorInputTransition(rawType, old, exists, *input); err != nil {
			return true, err
		}
		state.operatorInputs[key] = *input
		if rawType == "operator_input.queued" {
			event, err := operatorInputQueuedEvent(*input)
			if err != nil {
				return true, err
			}
			state.events = append(state.events, event)
		}
		return true, nil
	case "continuation.delivered":
		if continuation == nil || input != nil || continuation.CompletionID == "" || continuation.DeliveryID == "" || continuation.SessionID != meta.SessionID || continuation.Generation != meta.Generation || continuation.PluginID == "" || continuation.RunID == "" || continuation.ReceiverInputID == "" || continuation.CreatedAt.IsZero() || continuation.UpdatedAt.IsZero() || continuation.Status != ContinuationDelivered || meta.PluginID != "stado.dev/broker" {
			return true, errors.New("lifecycle application fold: malformed continuation delivery")
		}
		completion, ok := completionByNativeScope(*state, SessionScope{SessionID: continuation.SessionID, Generation: continuation.Generation}, continuation.CompletionID)
		if !ok || completion.PluginID != continuation.PluginID || completion.RunID != continuation.RunID || completion.ContinuationDeliveryID != continuation.DeliveryID || continuation.ReceiverInputID != continuation.DeliveryID || len(completion.ContinuationInputIDs) == 0 {
			return true, errors.New("lifecycle application fold: continuation delivery has no matching completion")
		}
		key := continuationKey(completion)
		if _, exists := state.continuationDeliveries[key]; exists {
			return true, errors.New("lifecycle application fold: duplicate continuation delivery")
		}
		state.continuationDeliveries[key] = continuationDelivery{
			CompletionID: continuation.CompletionID, SessionID: continuation.SessionID,
			Generation: continuation.Generation, PluginID: continuation.PluginID,
			RunID: continuation.RunID, ReceiverInputID: continuation.ReceiverInputID,
			WALSequence: continuation.WALSequence, CreatedAt: continuation.UpdatedAt,
		}
		return true, nil
	default:
		return false, nil
	}
}

func validateOperatorInputTransition(eventType string, old OperatorInput, exists bool, next OperatorInput) error {
	if !exists {
		if eventType != "operator_input.queued" || next.Version != 1 || next.Status != OperatorInputQueued || !next.CreatedAt.Equal(next.UpdatedAt) || next.ReviewID != "" || next.TaskID != "" || next.ReceiverInputID != "" || next.Recovered {
			return errors.New("lifecycle application fold: invalid initial operator input transition")
		}
		return nil
	}
	if eventType == "operator_input.queued" || old.Version+1 != next.Version || old.ID != next.ID || old.RunID != next.RunID || old.Ordinal != next.Ordinal || old.Text != next.Text || old.Digest != next.Digest || !old.CreatedAt.Equal(next.CreatedAt) || !sameEntityScope(old.SessionID, old.Generation, old.PluginID, next.SessionID, next.Generation, next.PluginID) || next.UpdatedAt.Before(old.UpdatedAt) {
		return errors.New("lifecycle application fold: operator input identity or version conflict")
	}
	switch eventType {
	case "operator_input.reviewing":
		if old.Status != OperatorInputQueued || next.Status != OperatorInputReviewing || next.ReviewID == "" || next.Label != "" || next.Rationale != "" || next.TaskID != "" || next.ReceiverInputID != "" || next.Recovered {
			return errors.New("lifecycle application fold: invalid reviewing operator input transition")
		}
	case "operator_input.ready":
		if (old.Status != OperatorInputQueued && old.Status != OperatorInputReviewing) || next.Status != OperatorInputReady || next.ReviewID != old.ReviewID || next.TaskID != "" || next.ReceiverInputID != "" || next.Recovered {
			return errors.New("lifecycle application fold: invalid ready operator input transition")
		}
	case "operator_input.deferred":
		if (old.Status != OperatorInputQueued && old.Status != OperatorInputReviewing) || next.Status != OperatorInputDeferred || next.ReviewID != old.ReviewID || next.TaskID == "" || next.ReceiverInputID != "" || next.Recovered {
			return errors.New("lifecycle application fold: invalid deferred operator input transition")
		}
	case "operator_input.delivered":
		if (old.Status != OperatorInputReady && !(next.Recovered && (old.Status == OperatorInputQueued || old.Status == OperatorInputReviewing))) || next.Status != OperatorInputDelivered || next.ReviewID != old.ReviewID || next.Label != old.Label || next.Rationale != old.Rationale || next.ReceiverInputID == "" || next.TaskID != old.TaskID {
			return errors.New("lifecycle application fold: invalid delivered operator input transition")
		}
	default:
		return errors.New("lifecycle application fold: unknown operator input transition")
	}
	return nil
}
