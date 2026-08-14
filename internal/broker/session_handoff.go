package broker

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

var (
	ErrSessionHandoffUnavailable = errors.New("broker: logical-session handoff unavailable")
	ErrSessionHandoffConflict    = errors.New("broker: logical-session handoff conflicts with current scope")
)

// SessionLineageCheck is assembled exclusively from broker-recorded session
// state plus the exact requested child/turn anchor. Production implementations
// independently open the canonical sidecar refs; no guest assertion can mark a
// child as related.
type SessionLineageCheck struct {
	SourceCWD     string
	SourceSubject string
	ChildSubject  string
	SourceTurnRef string
}

// SessionLineageVerifier proves that ChildSubject is the direct git child of
// SourceSubject at SourceTurnRef. It is a broker composition seam, not guest
// authority. The production daemon configures an implementation that opens the
// canonical state paths itself.
type SessionLineageVerifier interface {
	VerifyDirectChild(context.Context, SessionLineageCheck) error
}

type SessionLineageVerifierFunc func(context.Context, SessionLineageCheck) error

func (f SessionLineageVerifierFunc) VerifyDirectChild(ctx context.Context, check SessionLineageCheck) error {
	return f(ctx, check)
}

func (s *Service) ConfigureSessionLineageVerifier(verifier SessionLineageVerifier) error {
	if verifier == nil {
		return errors.New("broker: session lineage verifier required")
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.sessionLineage != nil {
		return errors.New("broker: session lineage verifier already configured")
	}
	s.sessionLineage = verifier
	return nil
}

type SessionSubjectHandoffReservation struct {
	ID            string    `json:"handoff_id"`
	SourceSubject string    `json:"source_subject"`
	ChildSubject  string    `json:"child_subject"`
	SourceTurnRef string    `json:"source_turn_ref"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type sessionSubjectHandoffReservation struct {
	Schema            int               `json:"schema"`
	ID                string            `json:"id"`
	SessionID         string            `json:"session_id"`
	Generation        uint64            `json:"generation"`
	ControllerVersion uint64            `json:"controller_version"`
	ControllerDigest  [sha256.Size]byte `json:"-"`
	ControllerHash    string            `json:"controller_digest"`
	Principal         string            `json:"principal"`
	RepoID            string            `json:"repo_id"`
	SourceCWD         string            `json:"source_cwd"`
	SourceSubject     string            `json:"source_subject"`
	ChildSubject      string            `json:"child_subject"`
	SourceTurnRef     string            `json:"source_turn_ref"`
	TicketDigest      [sha256.Size]byte `json:"-"`
	ResumeDigest      [sha256.Size]byte `json:"-"`
	TicketHash        string            `json:"ticket_digest"`
	ResumeHash        string            `json:"resume_digest"`
	ExpiresAt         time.Time         `json:"expires_at"`
	CreatedAt         time.Time         `json:"created_at"`
	WALSequence       uint64            `json:"-"`
}

type sessionSubjectHandoffCommit struct {
	ID            string `json:"id"`
	SourceSubject string `json:"source_subject"`
	ChildSubject  string `json:"child_subject"`
	SourceTurnRef string `json:"source_turn_ref"`
}

func canonicalSessionTurnRef(subject, value string) (string, error) {
	if err := stadogit.ValidateSessionID(subject); err != nil {
		return "", err
	}
	prefix := "refs/sessions/" + subject + "/turns/"
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("handoff source turn ref does not name the current subject")
	}
	turn, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	if err != nil || turn < 1 || value != prefix+strconv.Itoa(turn) {
		return "", errors.New("handoff source turn ref is not canonical")
	}
	return value, nil
}

func (s *Service) ReserveSessionSubjectHandoff(ctx context.Context, sessionID, controllerToken, childSubject, sourceTurnRef string) (SessionSubjectHandoffReservation, error) {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil || s.sessionLineage == nil {
		return SessionSubjectHandoffReservation{}, ErrSessionHandoffUnavailable
	}
	if err := stadogit.ValidateSessionID(childSubject); err != nil {
		return SessionSubjectHandoffReservation{}, ErrSessionHandoffConflict
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return SessionSubjectHandoffReservation{}, ErrSessionNotFound
	}
	if state.terminated || state.scope.status == sessionScopeTerminated {
		return SessionSubjectHandoffReservation{}, ErrSessionTerminated
	}
	if !state.scope.durable || state.scope.status != sessionScopeAttached {
		return SessionSubjectHandoffReservation{}, ErrSessionHandoffConflict
	}
	if err := authenticateControllerLocked(state, controllerToken); err != nil {
		return SessionSubjectHandoffReservation{}, err
	}
	if childSubject == state.scope.subject {
		return SessionSubjectHandoffReservation{}, ErrSessionHandoffConflict
	}
	canonicalRef, err := canonicalSessionTurnRef(state.scope.subject, sourceTurnRef)
	if err != nil {
		return SessionSubjectHandoffReservation{}, ErrSessionHandoffConflict
	}
	now := s.now().UTC()
	for _, candidate := range s.sessions {
		if candidate.scope.durable && !candidate.terminated && candidate.repoID == state.repoID && candidate.scope.subject == childSubject {
			return SessionSubjectHandoffReservation{}, ErrSessionScopeExists
		}
	}
	childKey := reservationKey(state.repoID, childSubject)
	if pending, exists := s.sessionScopes.reservations[childKey]; exists && pending.ExpiresAt.After(now) {
		return SessionSubjectHandoffReservation{}, ErrSessionScopeActive
	}
	if existingID := s.sessionScopes.handoffChild[childKey]; existingID != "" {
		existing := s.sessionScopes.handoffs[existingID]
		if existing.ExpiresAt.After(now) {
			if existing.SessionID == sessionID && existing.SourceSubject == state.scope.subject &&
				existing.SourceTurnRef == canonicalRef && existing.ControllerVersion == state.controllerVersion {
				return exportedSessionHandoff(existing), nil
			}
			if existing.SessionID != sessionID || existing.ControllerVersion == state.controllerVersion {
				return SessionSubjectHandoffReservation{}, ErrSessionScopeActive
			}
		}
		delete(s.sessionScopes.handoffs, existingID)
		delete(s.sessionScopes.handoffChild, childKey)
	}
	for id, existing := range s.sessionScopes.handoffs {
		if existing.SessionID != sessionID || !existing.ExpiresAt.After(now) {
			continue
		}
		if existing.ControllerVersion != state.controllerVersion {
			delete(s.sessionScopes.handoffs, id)
			key := reservationKey(existing.RepoID, existing.ChildSubject)
			if s.sessionScopes.handoffChild[key] == id {
				delete(s.sessionScopes.handoffChild, key)
			}
			continue
		}
		return SessionSubjectHandoffReservation{}, ErrSessionScopeActive
	}
	check := SessionLineageCheck{
		SourceCWD: state.handle.CWD, SourceSubject: state.scope.subject,
		ChildSubject: childSubject, SourceTurnRef: canonicalRef,
	}
	if err := s.sessionLineage.VerifyDirectChild(ctx, check); err != nil {
		return SessionSubjectHandoffReservation{}, fmt.Errorf("broker: verify logical-session child lineage: %w", err)
	}
	id, err := mintSessionID()
	if err != nil {
		return SessionSubjectHandoffReservation{}, err
	}
	reservation := sessionSubjectHandoffReservation{
		Schema: sessionScopeSchema, ID: id, SessionID: sessionID, Generation: state.generation,
		ControllerVersion: state.controllerVersion, Principal: state.principal, RepoID: state.repoID,
		ControllerDigest: state.controller,
		SourceCWD:        state.handle.CWD, SourceSubject: state.scope.subject, ChildSubject: childSubject,
		SourceTurnRef: canonicalRef, TicketDigest: state.scope.ticketDigest, ResumeDigest: state.scope.resumeDigest,
		ExpiresAt: now.Add(sessionHandoffTTL), CreatedAt: now,
	}
	reservation.TicketHash = hex.EncodeToString(reservation.TicketDigest[:])
	reservation.ResumeHash = hex.EncodeToString(reservation.ResumeDigest[:])
	reservation.ControllerHash = hex.EncodeToString(reservation.ControllerDigest[:])
	data, err := json.Marshal(reservation)
	if err != nil {
		return SessionSubjectHandoffReservation{}, err
	}
	idem := "session-handoff-reserve:" + reservation.ID
	appendResult, err := s.sessionScopes.wal.Append(wal.Transaction{
		ID: idem, IdempotencyKey: idem, Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionHandoffWALStore, Type: "reserved", Session: sessionID, Data: data}},
	})
	if err != nil {
		return SessionSubjectHandoffReservation{}, fmt.Errorf("broker: persist logical-session handoff reservation: %w", err)
	}
	reservation.WALSequence = appendResult.Record.Sequence
	s.sessionScopes.handoffs[id] = reservation
	s.sessionScopes.handoffChild[childKey] = id
	return exportedSessionHandoff(reservation), nil
}

func exportedSessionHandoff(reservation sessionSubjectHandoffReservation) SessionSubjectHandoffReservation {
	return SessionSubjectHandoffReservation{
		ID: reservation.ID, SourceSubject: reservation.SourceSubject, ChildSubject: reservation.ChildSubject,
		SourceTurnRef: reservation.SourceTurnRef, ExpiresAt: reservation.ExpiresAt,
	}
}

func (s *Service) CommitSessionSubjectHandoff(ctx context.Context, sessionID, controllerToken, handoffID string, credential SessionAdoptionCredential) (SessionHandle, SessionAdoptionCredential, error) {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil || s.sessionLineage == nil {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionHandoffUnavailable
	}
	if err := ValidateSessionAdoptionCredential(credential); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeCredential
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionNotFound
	}
	if state.terminated || state.scope.status == sessionScopeTerminated {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionTerminated
	}
	reservation, ok := s.sessionScopes.handoffs[handoffID]
	if !ok {
		if committed, exists := s.sessionScopes.committedHandoffs[handoffID]; exists {
			return s.replayCommittedSessionSubjectHandoffLocked(state, controllerToken, committed, credential)
		}
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionHandoffConflict
	}
	if err := authenticateControllerLocked(state, controllerToken); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	if !reservation.ExpiresAt.After(s.now()) || reservation.SessionID != sessionID ||
		reservation.Generation != state.generation || reservation.ControllerVersion != state.controllerVersion ||
		reservation.Principal != state.principal || reservation.RepoID != state.repoID ||
		reservation.SourceCWD != state.handle.CWD || reservation.SourceSubject != state.scope.subject ||
		reservation.ChildSubject != credential.Subject {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionHandoffConflict
	}
	ticket := sha256.Sum256([]byte(credential.Ticket))
	resume := sha256.Sum256([]byte(credential.ResumeSecret))
	if subtle.ConstantTimeCompare(ticket[:], state.scope.ticketDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(resume[:], state.scope.resumeDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(ticket[:], reservation.TicketDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(resume[:], reservation.ResumeDigest[:]) != 1 {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeCredential
	}
	if err := s.sessionLineage.VerifyDirectChild(ctx, SessionLineageCheck{
		SourceCWD: reservation.SourceCWD, SourceSubject: reservation.SourceSubject,
		ChildSubject: reservation.ChildSubject, SourceTurnRef: reservation.SourceTurnRef,
	}); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, fmt.Errorf("broker: reverify logical-session child lineage: %w", err)
	}
	controllerTokenNext, controllerDigest, err := mintControllerToken()
	if err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	now := s.now().UTC()
	next := *state
	next.controller = controllerDigest
	next.controllerVersion++
	next.scope.subject = reservation.ChildSubject
	next.scope.version++
	next.scope.status = sessionScopeAttached
	next.scope.leaseEpoch = s.sessionScopes.wal.Epoch()
	next.scope.leaseUntil = now.Add(s.sessionScopes.leaseTTL)
	marker := &sessionSubjectHandoffCommit{
		ID: reservation.ID, SourceSubject: reservation.SourceSubject,
		ChildSubject: reservation.ChildSubject, SourceTurnRef: reservation.SourceTurnRef,
	}
	if err := s.appendSessionScopeSnapshotWithHandoffLocked(&next, now, marker); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	state.controller = next.controller
	state.controllerVersion = next.controllerVersion
	state.scope = next.scope
	delete(s.sessionScopes.handoffs, reservation.ID)
	s.sessionScopes.committedHandoffs[reservation.ID] = reservation
	childKey := reservationKey(reservation.RepoID, reservation.ChildSubject)
	if s.sessionScopes.handoffChild[childKey] == reservation.ID {
		delete(s.sessionScopes.handoffChild, childKey)
	}
	s.invalidateSessionBindingsLocked(sessionID)
	handle := state.handle
	handle.controllerToken = controllerTokenNext
	handle.subject = reservation.ChildSubject
	handle.adoptionTicket = credential.Ticket
	handle.resumeSecret = credential.ResumeSecret
	return handle, credential, nil
}

func (s *Service) replayCommittedSessionSubjectHandoffLocked(state *sessionState, priorControllerToken string, reservation sessionSubjectHandoffReservation, credential SessionAdoptionCredential) (SessionHandle, SessionAdoptionCredential, error) {
	if state == nil || state.terminated || state.scope.status == sessionScopeTerminated {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionTerminated
	}
	priorDigest := sha256.Sum256([]byte(priorControllerToken))
	ticket := sha256.Sum256([]byte(credential.Ticket))
	resume := sha256.Sum256([]byte(credential.ResumeSecret))
	if !reservation.ExpiresAt.After(s.now()) || reservation.SessionID != state.handle.SessionID || state.scope.subject != reservation.ChildSubject ||
		credential.Subject != reservation.ChildSubject || state.controllerVersion != reservation.ControllerVersion+1 ||
		state.generation != reservation.Generation || state.principal != reservation.Principal || state.repoID != reservation.RepoID ||
		subtle.ConstantTimeCompare(priorDigest[:], reservation.ControllerDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(ticket[:], reservation.TicketDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(resume[:], reservation.ResumeDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(ticket[:], state.scope.ticketDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(resume[:], state.scope.resumeDigest[:]) != 1 {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionHandoffConflict
	}
	controllerToken, controllerDigest, err := mintControllerToken()
	if err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	now := s.now().UTC()
	next := *state
	next.controller = controllerDigest
	next.controllerVersion++
	next.scope.version++
	next.scope.status = sessionScopeAttached
	next.scope.leaseEpoch = s.sessionScopes.wal.Epoch()
	next.scope.leaseUntil = now.Add(s.sessionScopes.leaseTTL)
	if err := s.appendSessionScopeSnapshotLocked(&next, now); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	state.controller = next.controller
	state.controllerVersion = next.controllerVersion
	state.scope = next.scope
	s.invalidateSessionBindingsLocked(state.handle.SessionID)
	handle := state.handle
	handle.controllerToken = controllerToken
	handle.subject = reservation.ChildSubject
	handle.adoptionTicket = credential.Ticket
	handle.resumeSecret = credential.ResumeSecret
	return handle, credential, nil
}

func decodeSessionSubjectHandoffs(store *wal.Store) (map[string]sessionSubjectHandoffReservation, error) {
	handoffs := make(map[string]sessionSubjectHandoffReservation)
	activeSession := make(map[string]string)
	activeChild := make(map[string]string)
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store == sessionScopeWALStore {
				var snapshot sessionScopeSnapshot
				if err := json.Unmarshal(event.Data, &snapshot); err == nil {
					if id := activeSession[snapshot.SessionID]; id != "" && handoffs[id].ControllerVersion != snapshot.ControllerVersion {
						delete(activeSession, snapshot.SessionID)
						delete(activeChild, reservationKey(handoffs[id].RepoID, handoffs[id].ChildSubject))
					}
					if snapshot.SubjectHandoff != nil {
						id := snapshot.SubjectHandoff.ID
						if reservation, exists := handoffs[id]; exists {
							delete(activeSession, reservation.SessionID)
							delete(activeChild, reservationKey(reservation.RepoID, reservation.ChildSubject))
						}
					}
				}
				continue
			}
			if event.Store != sessionHandoffWALStore {
				continue
			}
			var reservation sessionSubjectHandoffReservation
			if err := json.Unmarshal(event.Data, &reservation); err != nil {
				return nil, fmt.Errorf("session handoff sequence %d: %w", record.Sequence, err)
			}
			if reservation.Schema != sessionScopeSchema || reservation.ID == "" || reservation.SessionID == "" ||
				reservation.Generation == 0 || reservation.ControllerVersion == 0 || reservation.Principal == "" ||
				reservation.RepoID == "" || reservation.SourceCWD == "" || reservation.CreatedAt.IsZero() ||
				!reservation.ExpiresAt.After(reservation.CreatedAt) || event.Session != reservation.SessionID {
				return nil, fmt.Errorf("session handoff sequence %d: invalid envelope", record.Sequence)
			}
			if err := stadogit.ValidateSessionID(reservation.SourceSubject); err != nil {
				return nil, fmt.Errorf("session handoff sequence %d: invalid source subject", record.Sequence)
			}
			if err := stadogit.ValidateSessionID(reservation.ChildSubject); err != nil || reservation.ChildSubject == reservation.SourceSubject {
				return nil, fmt.Errorf("session handoff sequence %d: invalid child subject", record.Sequence)
			}
			canonical, err := canonicalSessionTurnRef(reservation.SourceSubject, reservation.SourceTurnRef)
			if err != nil || canonical != reservation.SourceTurnRef {
				return nil, fmt.Errorf("session handoff sequence %d: invalid source turn ref", record.Sequence)
			}
			ticket, err := decodeScopeDigest(reservation.TicketHash, false)
			if err != nil {
				return nil, fmt.Errorf("session handoff sequence %d: ticket digest: %w", record.Sequence, err)
			}
			resume, err := decodeScopeDigest(reservation.ResumeHash, false)
			if err != nil {
				return nil, fmt.Errorf("session handoff sequence %d: resume digest: %w", record.Sequence, err)
			}
			controller, err := decodeScopeDigest(reservation.ControllerHash, false)
			if err != nil {
				return nil, fmt.Errorf("session handoff sequence %d: controller digest: %w", record.Sequence, err)
			}
			if _, duplicate := handoffs[reservation.ID]; duplicate {
				return nil, fmt.Errorf("session handoff sequence %d: duplicate id", record.Sequence)
			}
			childKey := reservationKey(reservation.RepoID, reservation.ChildSubject)
			if previous := activeSession[reservation.SessionID]; previous != "" {
				if handoffs[previous].ExpiresAt.After(reservation.CreatedAt) {
					return nil, fmt.Errorf("session handoff sequence %d: session has overlapping reservation %q", record.Sequence, previous)
				}
				delete(activeChild, reservationKey(handoffs[previous].RepoID, handoffs[previous].ChildSubject))
			}
			if previous := activeChild[childKey]; previous != "" {
				if handoffs[previous].ExpiresAt.After(reservation.CreatedAt) {
					return nil, fmt.Errorf("session handoff sequence %d: child has overlapping reservation %q", record.Sequence, previous)
				}
				delete(activeSession, handoffs[previous].SessionID)
			}
			reservation.TicketDigest, reservation.ResumeDigest, reservation.ControllerDigest = ticket, resume, controller
			reservation.WALSequence = record.Sequence
			handoffs[reservation.ID] = reservation
			activeSession[reservation.SessionID] = reservation.ID
			activeChild[childKey] = reservation.ID
		}
	}
	return handoffs, nil
}

func validateSessionSubjectHandoffTransition(previous, next sessionScopeSnapshot, handoffs map[string]sessionSubjectHandoffReservation, transitionSequence uint64) error {
	marker := next.SubjectHandoff
	if marker == nil || marker.ID == "" {
		return errors.New("durable session subject changed without explicit handoff")
	}
	reservation, ok := handoffs[marker.ID]
	if !ok {
		return errors.New("durable session subject handoff lacks prior reservation")
	}
	if reservation.WALSequence == 0 || reservation.WALSequence >= transitionSequence {
		return errors.New("durable session subject handoff reservation was not durably prior")
	}
	if marker.SourceSubject != reservation.SourceSubject || marker.ChildSubject != reservation.ChildSubject ||
		marker.SourceTurnRef != reservation.SourceTurnRef || previous.SessionID != reservation.SessionID ||
		previous.Subject != reservation.SourceSubject || next.Subject != reservation.ChildSubject ||
		previous.Generation != reservation.Generation || previous.ControllerVersion != reservation.ControllerVersion ||
		previous.Principal != reservation.Principal || previous.RepoID != reservation.RepoID ||
		previous.Handle.CWD != reservation.SourceCWD || next.UpdatedAt.Before(reservation.CreatedAt) ||
		next.UpdatedAt.After(reservation.ExpiresAt) {
		return errors.New("durable session subject handoff does not match its reservation")
	}
	previousTicket, err := decodeScopeDigest(previous.TicketDigest, false)
	if err != nil || subtle.ConstantTimeCompare(previousTicket[:], reservation.TicketDigest[:]) != 1 {
		return errors.New("durable session subject handoff ticket mismatch")
	}
	previousResume, err := decodeScopeDigest(previous.ResumeDigest, false)
	if err != nil || subtle.ConstantTimeCompare(previousResume[:], reservation.ResumeDigest[:]) != 1 {
		return errors.New("durable session subject handoff resume mismatch")
	}
	previousController, err := decodeScopeDigest(previous.ControllerDigest, false)
	if err != nil || subtle.ConstantTimeCompare(previousController[:], reservation.ControllerDigest[:]) != 1 {
		return errors.New("durable session subject handoff prior controller mismatch")
	}
	return nil
}
