package application

// Generic, broker-owned asynchronous verification records. These records are
// intentionally facts rather than completion policy: an admitted lifecycle
// application may ask the native controller to run the operator's configured
// verification suite, but it cannot provide commands or decide what the
// resulting command outcomes mean for the application workflow.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	VerificationFinishedEvent  = "session.verification_finished"
	VerificationResultSchemaV1 = "stado.dev/session-verification-facts/v1"
	turnCommittedEvent         = "session.turn_committed"
	turnCommittedSchemaV1      = "stado.dev/session-turn-facts/v1"
)

type VerificationStatus string

const (
	VerificationRequested VerificationStatus = "requested"
	VerificationRunning   VerificationStatus = "running"
	VerificationTerminal  VerificationStatus = "terminal"
)

type VerificationOutcome string

const (
	VerificationCommandsSucceeded VerificationOutcome = "commands_succeeded"
	VerificationCommandFailed     VerificationOutcome = "command_failed"
	VerificationInfrastructure    VerificationOutcome = "infrastructure_error"
	VerificationCancelled         VerificationOutcome = "cancelled"
	VerificationNoSuite           VerificationOutcome = "no_suite"
)

type VerificationRequest struct {
	RunID                 string `json:"run_id"`
	ExpectedWorkerVersion uint64 `json:"expected_worker_version"`
	SourceEventSequence   uint64 `json:"source_event_sequence"`
}

type VerificationSourceAnchor struct {
	EventSequence   uint64 `json:"event_sequence"`
	SessionSequence uint64 `json:"session_sequence"`
	TurnRef         string `json:"turn_ref"`
	TreeDigest      string `json:"tree_digest"`
}

type VerificationClaim struct {
	ID              string   `json:"id"`
	ExpectedVersion uint64   `json:"expected_version"`
	SuiteDigest     string   `json:"suite_digest"`
	CommandDigests  []string `json:"command_digests"`
}

type VerificationCommandFact struct {
	Ordinal            int      `json:"ordinal"`
	CommandDigest      string   `json:"command_digest"`
	ResultDigest       string   `json:"result_digest"`
	Outcome            string   `json:"outcome"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureFingerprint string   `json:"failure_fingerprint,omitempty"`
	EvidenceRefs       []string `json:"evidence_refs,omitempty"`
}

type VerificationFinish struct {
	ID                 string                    `json:"id"`
	ExpectedVersion    uint64                    `json:"expected_version"`
	Outcome            VerificationOutcome       `json:"outcome"`
	FailureKind        string                    `json:"failure_kind,omitempty"`
	FailureFingerprint string                    `json:"failure_fingerprint,omitempty"`
	Commands           []VerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                  `json:"evidence_refs,omitempty"`
}

type Verification struct {
	ID                 string                    `json:"id"`
	SessionID          string                    `json:"session_id"`
	Generation         uint64                    `json:"generation"`
	PluginID           string                    `json:"plugin_id"`
	Owner              string                    `json:"owner"`
	RunID              string                    `json:"run_id"`
	WorkerVersion      uint64                    `json:"worker_version"`
	Version            uint64                    `json:"version"`
	WALSequence        uint64                    `json:"wal_sequence"`
	Status             VerificationStatus        `json:"status"`
	Source             VerificationSourceAnchor  `json:"source"`
	SourceEvidenceRefs []string                  `json:"source_evidence_refs,omitempty"`
	SuiteDigest        string                    `json:"suite_digest,omitempty"`
	CommandDigests     []string                  `json:"command_digests,omitempty"`
	Outcome            VerificationOutcome       `json:"outcome,omitempty"`
	FailureKind        string                    `json:"failure_kind,omitempty"`
	FailureFingerprint string                    `json:"failure_fingerprint,omitempty"`
	Commands           []VerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                  `json:"evidence_refs,omitempty"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
}

// VerificationResultEventV1 is the strict guest-visible terminal payload.
// Application scope, routing, WAL order, and time remain exclusively in the
// outer broker event envelope and cannot be confused with guest data.
type VerificationResultEventV1 struct {
	Schema             string                    `json:"schema"`
	VerificationID     string                    `json:"verification_id"`
	RunID              string                    `json:"run_id"`
	Version            uint64                    `json:"version"`
	Source             VerificationSourceAnchor  `json:"source"`
	SourceEvidenceRefs []string                  `json:"source_evidence_refs,omitempty"`
	SuiteDigest        string                    `json:"suite_digest"`
	CommandDigests     []string                  `json:"command_digests,omitempty"`
	Outcome            VerificationOutcome       `json:"outcome"`
	FailureKind        string                    `json:"failure_kind,omitempty"`
	FailureFingerprint string                    `json:"failure_fingerprint,omitempty"`
	Commands           []VerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                  `json:"evidence_refs,omitempty"`
}

// RequestVerification accepts only application/run coordinates and an exact
// event sequence. The broker derives the immutable turn anchor from its own
// still-pending event and refuses requests detached from the current worker.
func (s *Service) RequestVerification(ctx context.Context, auth Authority, input VerificationRequest, idem string) (Verification, error) {
	if err := checkContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Verification{}, err
	}
	if invalidIdentifier(input.RunID, s.limits.MaxIDBytes, true) || input.ExpectedWorkerVersion == 0 || input.SourceEventSequence == 0 {
		return Verification{}, ErrInvalid
	}
	return withMutation(s, auth, "verification.request", input, idem, func(state foldedState, now time.Time) (Verification, string, eventEnvelope, error) {
		if scopedVerificationCount(state, auth) >= s.limits.MaxVerificationRecords {
			return Verification{}, "", eventEnvelope{}, ErrLimit
		}
		worker, ok := state.workerRuns[entityKey(auth, input.RunID)]
		if !ok {
			return Verification{}, "", eventEnvelope{}, ErrNotFound
		}
		worker = projectWorkerRun(state, worker)
		if worker.Status != WorkerRunActive || worker.Version != input.ExpectedWorkerVersion {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: verification worker version is not active", ErrVersion)
		}
		cursor := state.cursors[scopeKey(auth.SessionID, auth.Generation, auth.PluginID)]
		if cursor.Sequence >= input.SourceEventSequence {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: source event is already acknowledged", ErrVersion)
		}
		event, ok := visibleEventAt(state, auth, input.SourceEventSequence)
		if !ok || event.Kind != turnCommittedEvent {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: source event is not a pending turn commit", ErrNotFound)
		}
		anchor, err := verificationAnchorFromEvent(event)
		if err != nil {
			return Verification{}, "", eventEnvelope{}, err
		}
		id, err := mint("verification_")
		if err != nil {
			return Verification{}, "", eventEnvelope{}, err
		}
		record := Verification{
			ID: id, SessionID: auth.SessionID, Generation: auth.Generation,
			PluginID: auth.PluginID, Owner: auth.PluginID, RunID: input.RunID,
			WorkerVersion: input.ExpectedWorkerVersion, Version: 1,
			Status: VerificationRequested, Source: anchor,
			SourceEvidenceRefs: cloneStrings(event.EvidenceRefs), CreatedAt: now, UpdatedAt: now,
		}
		return record, "verification.requested", eventEnvelope{Verification: &record}, nil
	})
}

// VerificationByID and NextVerification are native-controller reads. They are
// not registered in the guest operation table.
func (s *Service) VerificationByID(ctx context.Context, auth Authority, id string) (Verification, error) {
	if err := checkContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := s.validateAuthority(auth); err != nil || invalidIdentifier(id, s.limits.MaxIDBytes, true) {
		if err != nil {
			return Verification{}, err
		}
		return Verification{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return Verification{}, err
	}
	record, ok := state.verifications[entityKey(auth, id)]
	if !ok {
		return Verification{}, ErrNotFound
	}
	return cloneVerification(record), nil
}

func (s *Service) NextVerification(ctx context.Context, auth Authority) (Verification, error) {
	if err := checkContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Verification{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := fold(s.wal.Records())
	if err != nil {
		return Verification{}, err
	}
	var pending []Verification
	for _, record := range state.verifications {
		if sameScope(record.SessionID, record.Generation, record.PluginID, auth) && record.Status != VerificationTerminal {
			pending = append(pending, cloneVerification(record))
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	if len(pending) == 0 {
		return Verification{}, ErrNotFound
	}
	return pending[0], nil
}

// ClaimVerification is the first transition that permits native command
// execution. It requires the source callback's cursor ACK to be durable.
func (s *Service) ClaimVerification(ctx context.Context, auth Authority, input VerificationClaim, idem string) (Verification, error) {
	if err := checkContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Verification{}, err
	}
	if err := s.validateVerificationClaim(input); err != nil {
		return Verification{}, err
	}
	return withMutation(s, auth, "verification.claim", input, idem, func(state foldedState, now time.Time) (Verification, string, eventEnvelope, error) {
		current, ok := state.verifications[entityKey(auth, input.ID)]
		if !ok {
			return Verification{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.Status != VerificationRequested {
			return Verification{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != input.ExpectedVersion {
			return Verification{}, "", eventEnvelope{}, ErrVersion
		}
		cursor := state.cursors[scopeKey(auth.SessionID, auth.Generation, auth.PluginID)]
		if cursor.Sequence < current.Source.EventSequence {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: source event callback is not acknowledged", ErrNotDue)
		}
		worker, ok := state.workerRuns[entityKey(auth, current.RunID)]
		if !ok {
			return Verification{}, "", eventEnvelope{}, ErrNotFound
		}
		worker = projectWorkerRun(state, worker)
		if worker.Status != WorkerRunActive || worker.Version != current.WorkerVersion {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: verification worker version is no longer active", ErrVersion)
		}
		current.Version++
		current.Status = VerificationRunning
		current.SuiteDigest = input.SuiteDigest
		current.CommandDigests = cloneStrings(input.CommandDigests)
		current.UpdatedAt = now
		return current, "verification.running", eventEnvelope{Verification: &current}, nil
	})
}

func (s *Service) FinishVerification(ctx context.Context, auth Authority, input VerificationFinish, idem string) (Verification, error) {
	if err := checkContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return Verification{}, err
	}
	if err := s.validateVerificationFinish(input); err != nil {
		return Verification{}, err
	}
	return withMutation(s, auth, "verification.finish", input, idem, func(state foldedState, now time.Time) (Verification, string, eventEnvelope, error) {
		current, ok := state.verifications[entityKey(auth, input.ID)]
		if !ok {
			return Verification{}, "", eventEnvelope{}, ErrNotFound
		}
		if current.Status != VerificationRunning {
			return Verification{}, "", eventEnvelope{}, ErrTerminal
		}
		if current.Version != input.ExpectedVersion {
			return Verification{}, "", eventEnvelope{}, ErrVersion
		}
		worker, workerExists := state.workerRuns[entityKey(auth, current.RunID)]
		workerStatus := WorkerRunStatus("missing")
		workerVersion, workerTerminalSequence := uint64(0), uint64(0)
		if workerExists {
			worker = projectWorkerRun(state, worker)
			workerStatus, workerVersion, workerTerminalSequence = worker.Status, worker.Version, worker.TerminalSequence
		}
		if !workerExists || workerStatus != WorkerRunActive || workerVersion != current.WorkerVersion {
			// A pause, stop, completion, cancellation, or resumed worker version
			// wins over an in-flight verifier. Preserve the command facts but do
			// not let their result certify a different worker state.
			input.Outcome = VerificationCancelled
			input.FailureKind = "worker_terminal"
			input.FailureFingerprint = verificationFactDigest(fmt.Sprintf("worker:%s:%d:%d", workerStatus, workerVersion, workerTerminalSequence))
		}
		if len(current.CommandDigests) != len(input.Commands) {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: verification command fact count changed", ErrVersion)
		}
		for i := range input.Commands {
			if input.Commands[i].Ordinal != i+1 || input.Commands[i].CommandDigest != current.CommandDigests[i] {
				return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: verification command plan changed", ErrVersion)
			}
		}
		if !verificationOutcomeMatchesSuite(input.Outcome, input.FailureKind, len(current.CommandDigests)) {
			return Verification{}, "", eventEnvelope{}, fmt.Errorf("%w: verification suite/outcome mismatch", ErrVersion)
		}
		current.Version++
		current.Status = VerificationTerminal
		current.Outcome = input.Outcome
		current.FailureKind = input.FailureKind
		current.FailureFingerprint = input.FailureFingerprint
		current.Commands = cloneVerificationCommandFacts(input.Commands)
		current.EvidenceRefs = cloneStrings(input.EvidenceRefs)
		current.UpdatedAt = now
		return current, "verification.terminal", eventEnvelope{Verification: &current}, nil
	})
}

func (s *Service) validateVerificationClaim(input VerificationClaim) error {
	if invalidIdentifier(input.ID, s.limits.MaxIDBytes, true) || input.ExpectedVersion == 0 || !validFactDigest(input.SuiteDigest) || len(input.CommandDigests) > s.limits.MaxVerificationCommands {
		return ErrInvalid
	}
	for _, digest := range input.CommandDigests {
		if !validFactDigest(digest) {
			return ErrInvalid
		}
	}
	return nil
}

func (s *Service) validateVerificationFinish(input VerificationFinish) error {
	return validateVerificationFinishShape(input, s.limits)
}

func validateVerificationFinishShape(input VerificationFinish, limits Limits) error {
	if invalidIdentifier(input.ID, limits.MaxIDBytes, true) || input.ExpectedVersion == 0 || len(input.Commands) > limits.MaxVerificationCommands || validateRefs(input.EvidenceRefs, limits) != nil {
		return ErrInvalid
	}
	switch input.Outcome {
	case VerificationCommandsSucceeded, VerificationNoSuite:
		if input.FailureKind != "" || input.FailureFingerprint != "" {
			return ErrInvalid
		}
	case VerificationCommandFailed, VerificationInfrastructure, VerificationCancelled:
		if invalidIdentifier(input.FailureKind, limits.MaxIDBytes, true) || !validFactDigest(input.FailureFingerprint) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	switch input.Outcome {
	case VerificationCommandFailed:
		if input.FailureKind != "command_failed" {
			return ErrInvalid
		}
	case VerificationInfrastructure:
		if input.FailureKind != "infrastructure_error" && input.FailureKind != "suite_changed" {
			return ErrInvalid
		}
	case VerificationCancelled:
		if input.FailureKind != "cancelled" && input.FailureKind != "stale_anchor" && input.FailureKind != "worker_terminal" {
			return ErrInvalid
		}
	}
	seenSucceeded, seenFailed, seenInfrastructure, seenCancelled := 0, 0, 0, 0
	terminalSeen := false
	for _, fact := range input.Commands {
		if fact.Ordinal < 1 || !validFactDigest(fact.CommandDigest) || !validFactDigest(fact.ResultDigest) || validateRefs(fact.EvidenceRefs, limits) != nil {
			return ErrInvalid
		}
		switch fact.Outcome {
		case "succeeded":
			if terminalSeen || fact.FailureKind != "" || fact.FailureFingerprint != "" {
				return ErrInvalid
			}
			seenSucceeded++
		case "failed":
			if terminalSeen {
				return ErrInvalid
			}
			terminalSeen = true
			seenFailed++
		case "infrastructure_error":
			if terminalSeen {
				return ErrInvalid
			}
			terminalSeen = true
			seenInfrastructure++
		case "cancelled":
			if terminalSeen {
				return ErrInvalid
			}
			terminalSeen = true
			seenCancelled++
		case "not_run":
			if fact.FailureKind != "" || fact.FailureFingerprint != "" {
				return ErrInvalid
			}
			terminalSeen = true
		default:
			return ErrInvalid
		}
		failedFact := fact.Outcome == "failed" || fact.Outcome == "infrastructure_error" || fact.Outcome == "cancelled"
		if failedFact && (invalidIdentifier(fact.FailureKind, limits.MaxIDBytes, true) || !validFactDigest(fact.FailureFingerprint)) {
			return ErrInvalid
		}
		if fact.Outcome == "failed" && fact.FailureKind != "command_failed" || fact.Outcome == "infrastructure_error" && fact.FailureKind != "infrastructure_error" || fact.Outcome == "cancelled" && fact.FailureKind != "cancelled" {
			return ErrInvalid
		}
	}
	switch input.Outcome {
	case VerificationCommandsSucceeded:
		if len(input.Commands) == 0 || seenSucceeded != len(input.Commands) || seenFailed != 0 || seenInfrastructure != 0 || seenCancelled != 0 {
			return ErrInvalid
		}
	case VerificationCommandFailed:
		if seenFailed != 1 || seenInfrastructure != 0 || seenCancelled != 0 {
			return ErrInvalid
		}
	case VerificationInfrastructure:
		// A changed suite is an infrastructure fact discovered before any
		// command can be re-run, so all command outcomes may be not_run.
		if input.FailureKind == "suite_changed" {
			if seenSucceeded != 0 || seenFailed != 0 || seenInfrastructure != 0 || seenCancelled != 0 {
				return ErrInvalid
			}
		} else if seenInfrastructure != 1 || seenFailed != 0 || seenCancelled != 0 {
			return ErrInvalid
		}
	case VerificationCancelled:
		if input.FailureKind != "stale_anchor" && input.FailureKind != "worker_terminal" && seenCancelled != 1 {
			return ErrInvalid
		}
		if input.FailureKind != "stale_anchor" && input.FailureKind != "worker_terminal" && (seenFailed != 0 || seenInfrastructure != 0) {
			return ErrInvalid
		}
	}
	return nil
}

func verificationOutcomeMatchesSuite(outcome VerificationOutcome, failureKind string, commandCount int) bool {
	if commandCount > 0 {
		return outcome != VerificationNoSuite
	}
	switch outcome {
	case VerificationNoSuite:
		return true
	case VerificationCancelled:
		return failureKind == "stale_anchor" || failureKind == "worker_terminal"
	case VerificationInfrastructure:
		return failureKind == "suite_changed"
	default:
		return false
	}
}

func verificationAnchorFromEvent(event Event) (VerificationSourceAnchor, error) {
	var payload struct {
		Schema string `json:"schema"`
		Anchor struct {
			SessionSequence uint64 `json:"session_sequence"`
			TurnRef         string `json:"turn_ref"`
			TreeDigest      string `json:"tree_digest"`
		} `json:"anchor"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil || payload.Schema != turnCommittedSchemaV1 || payload.Anchor.SessionSequence == 0 || strings.TrimSpace(payload.Anchor.TurnRef) == "" || !validTreeDigest(payload.Anchor.TreeDigest) {
		return VerificationSourceAnchor{}, fmt.Errorf("%w: malformed turn commit anchor", ErrInvalid)
	}
	return VerificationSourceAnchor{
		EventSequence: event.WALSequence, SessionSequence: payload.Anchor.SessionSequence,
		TurnRef: payload.Anchor.TurnRef, TreeDigest: payload.Anchor.TreeDigest,
	}, nil
}

func visibleEventAt(state foldedState, auth Authority, sequence uint64) (Event, bool) {
	for _, event := range state.events {
		if event.WALSequence == sequence && event.SessionID == auth.SessionID && event.Generation == auth.Generation && (event.TargetPlugin == "" || event.TargetPlugin == auth.PluginID) {
			return event, true
		}
	}
	return Event{}, false
}

func validFactDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func verificationFactDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func validTreeDigest(value string) bool {
	if value == "empty" {
		return true
	}
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func verificationResultEvent(record Verification) VerificationResultEventV1 {
	return VerificationResultEventV1{
		Schema: VerificationResultSchemaV1, VerificationID: record.ID,
		RunID: record.RunID, Version: record.Version, Source: record.Source,
		SourceEvidenceRefs: cloneStrings(record.SourceEvidenceRefs),
		SuiteDigest:        record.SuiteDigest, CommandDigests: cloneStrings(record.CommandDigests),
		Outcome: record.Outcome, FailureKind: record.FailureKind,
		FailureFingerprint: record.FailureFingerprint,
		Commands:           cloneVerificationCommandFacts(record.Commands), EvidenceRefs: cloneStrings(record.EvidenceRefs),
	}
}

func verificationWALEvidenceRef(sequence uint64) string {
	return "broker:wal:lifecycle_application:" + fmt.Sprint(sequence)
}

func appendVerificationRef(refs []string, ref string) []string {
	for _, existing := range refs {
		if existing == ref {
			return refs
		}
	}
	return append(refs, ref)
}

func scopedVerificationCount(state foldedState, auth Authority) int {
	count := 0
	for _, record := range state.verifications {
		if sameScope(record.SessionID, record.Generation, record.PluginID, auth) {
			count++
		}
	}
	return count
}

func cloneVerification(value Verification) Verification {
	value.SourceEvidenceRefs = cloneStrings(value.SourceEvidenceRefs)
	value.CommandDigests = cloneStrings(value.CommandDigests)
	value.Commands = cloneVerificationCommandFacts(value.Commands)
	value.EvidenceRefs = cloneStrings(value.EvidenceRefs)
	return value
}

func cloneVerificationCommandFacts(values []VerificationCommandFact) []VerificationCommandFact {
	out := append([]VerificationCommandFact(nil), values...)
	for i := range out {
		out[i].EvidenceRefs = cloneStrings(out[i].EvidenceRefs)
	}
	return out
}

func validateVerificationTransition(eventType string, old Verification, exists bool, next Verification) error {
	limits := DefaultLimits()
	if !exists {
		if eventType != "verification.requested" || next.Version != 1 || next.Status != VerificationRequested || !next.CreatedAt.Equal(next.UpdatedAt) || next.Outcome != "" || next.SuiteDigest != "" || len(next.CommandDigests) != 0 || len(next.Commands) != 0 || validateRefs(next.SourceEvidenceRefs, limits) != nil || len(next.EvidenceRefs) != 0 {
			return errors.New("lifecycle application fold: invalid initial verification transition")
		}
		return nil
	}
	if next.ID != old.ID || next.SessionID != old.SessionID || next.Generation != old.Generation || next.PluginID != old.PluginID || next.Owner != old.Owner || next.RunID != old.RunID || next.WorkerVersion != old.WorkerVersion || next.Source != old.Source || !equalStrings(next.SourceEvidenceRefs, old.SourceEvidenceRefs) || !next.CreatedAt.Equal(old.CreatedAt) || next.Version != old.Version+1 || next.UpdatedAt.Before(old.UpdatedAt) {
		return errors.New("lifecycle application fold: verification identity or version conflict")
	}
	switch eventType {
	case "verification.running":
		if old.Status != VerificationRequested || next.Status != VerificationRunning || !validFactDigest(next.SuiteDigest) || next.Outcome != "" || len(next.Commands) != 0 || len(next.CommandDigests) > limits.MaxVerificationCommands || len(next.EvidenceRefs) != 0 {
			return errors.New("lifecycle application fold: invalid verification claim")
		}
		for _, digest := range next.CommandDigests {
			if !validFactDigest(digest) {
				return errors.New("lifecycle application fold: invalid verification command digest")
			}
		}
	case "verification.terminal":
		if old.Status != VerificationRunning || next.Status != VerificationTerminal || next.SuiteDigest != old.SuiteDigest || !equalStrings(next.CommandDigests, old.CommandDigests) || next.Outcome == "" {
			return errors.New("lifecycle application fold: invalid verification terminal transition")
		}
		finish := VerificationFinish{
			ID: next.ID, ExpectedVersion: old.Version, Outcome: next.Outcome,
			FailureKind: next.FailureKind, FailureFingerprint: next.FailureFingerprint,
			Commands: next.Commands, EvidenceRefs: next.EvidenceRefs,
		}
		if !verificationOutcomeMatchesSuite(next.Outcome, next.FailureKind, len(next.CommandDigests)) || len(next.Commands) != len(next.CommandDigests) || validateVerificationFinishShape(finish, limits) != nil {
			return errors.New("lifecycle application fold: malformed verification terminal facts")
		}
		for i := range next.Commands {
			if next.Commands[i].Ordinal != i+1 || next.Commands[i].CommandDigest != next.CommandDigests[i] {
				return errors.New("lifecycle application fold: verification terminal plan changed")
			}
		}
	default:
		return errors.New("lifecycle application fold: unknown verification transition")
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
