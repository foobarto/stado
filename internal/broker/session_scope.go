package broker

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

const (
	sessionScopeWALStore       = "broker_session_scope"
	sessionReservationWALStore = "broker_session_reservation"
	sessionHandoffWALStore     = "broker_session_handoff"
	sessionScopeSchema         = 1
	sessionScopeLeaseDefault   = 30 * time.Second
	sessionReservationTTL      = 2 * time.Minute
	sessionHandoffTTL          = 2 * time.Minute
)

var (
	ErrSessionScopeUnavailable = errors.New("broker: durable session scope unavailable")
	ErrSessionScopeExists      = errors.New("broker: durable session scope already exists")
	ErrSessionScopeActive      = errors.New("broker: durable session scope is active")
	ErrSessionScopeCredential  = errors.New("broker: durable session adoption credential rejected")
)

type sessionScopeStatus string

const (
	sessionScopeAttached   sessionScopeStatus = "attached"
	sessionScopeDetached   sessionScopeStatus = "detached"
	sessionScopeTerminated sessionScopeStatus = "terminated"
)

// SessionAdoptionCredential is a native-controller bearer used to reopen one
// exact logical git session. It prevents guessing and accidental cross-client
// control. A 0600 copy on disk does not protect it from a malicious process
// already running as the same UID; the live orchestrator necessarily possesses
// equivalent controller authority.
type SessionAdoptionCredential struct {
	Subject      string `json:"subject"`
	Ticket       string `json:"ticket"`
	ResumeSecret string `json:"resume_secret"`
}

type sessionScopeState struct {
	durable             bool
	subject             string
	ticket              string
	ticketDigest        [sha256.Size]byte
	resumeSecret        string
	resumeDigest        [sha256.Size]byte
	status              sessionScopeStatus
	version             uint64
	leaseEpoch          uint64
	leaseUntil          time.Time
	reservationParentID string
}

type sessionScopeStore struct {
	wal               *wal.Store
	leaseTTL          time.Duration
	reservations      map[string]sessionScopeReservation
	usedTickets       map[[sha256.Size]byte]struct{}
	usedResumes       map[[sha256.Size]byte]struct{}
	handoffs          map[string]sessionSubjectHandoffReservation
	committedHandoffs map[string]sessionSubjectHandoffReservation
	handoffChild      map[string]string
}

type sessionScopeReservation struct {
	Schema          int               `json:"schema"`
	ID              string            `json:"id"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	Principal       string            `json:"principal"`
	Subject         string            `json:"subject"`
	RepoID          string            `json:"repo_id"`
	TicketDigest    [sha256.Size]byte `json:"-"`
	ResumeDigest    [sha256.Size]byte `json:"-"`
	TicketHash      string            `json:"ticket_digest"`
	ResumeHash      string            `json:"resume_digest"`
	ExpiresAt       time.Time         `json:"expires_at"`
	CreatedAt       time.Time         `json:"created_at"`
}

// sessionScopeSnapshot is a full, deterministic projection. Plaintext
// controller/adoption credentials are intentionally absent.
type sessionScopeSnapshot struct {
	Schema            int                          `json:"schema"`
	SessionID         string                       `json:"session_id"`
	Subject           string                       `json:"subject"`
	Handle            SessionHandle                `json:"handle"`
	ControllerDigest  string                       `json:"controller_digest,omitempty"`
	ControllerVersion uint64                       `json:"controller_version"`
	TicketDigest      string                       `json:"ticket_digest"`
	ResumeDigest      string                       `json:"resume_digest"`
	Status            sessionScopeStatus           `json:"status"`
	Principal         string                       `json:"principal"`
	RepoID            string                       `json:"repo_id"`
	ParentID          string                       `json:"parent_id,omitempty"`
	Generation        uint64                       `json:"generation"`
	Taint             Taint                        `json:"taint"`
	Version           uint64                       `json:"version"`
	LeaseEpoch        uint64                       `json:"lease_epoch,omitempty"`
	UpdatedAt         time.Time                    `json:"updated_at"`
	SubjectHandoff    *sessionSubjectHandoffCommit `json:"subject_handoff,omitempty"`
}

func (s *Service) configureSessionScopes(store *wal.Store) error {
	if store == nil {
		return ErrSessionScopeUnavailable
	}
	scopeStore := &sessionScopeStore{
		wal: store, leaseTTL: sessionScopeLeaseDefault,
		reservations: make(map[string]sessionScopeReservation),
		usedTickets:  make(map[[sha256.Size]byte]struct{}), usedResumes: make(map[[sha256.Size]byte]struct{}),
		handoffs:          make(map[string]sessionSubjectHandoffReservation),
		committedHandoffs: make(map[string]sessionSubjectHandoffReservation), handoffChild: make(map[string]string),
	}
	restored, pendingHandoffs, committedHandoffs, err := restoreSessionScopeState(store)
	if err != nil {
		return err
	}
	for id, handoff := range pendingHandoffs {
		scopeStore.handoffs[id] = handoff
		key := reservationKey(handoff.RepoID, handoff.ChildSubject)
		current := scopeStore.handoffs[scopeStore.handoffChild[key]]
		if current.ID == "" || handoff.CreatedAt.After(current.CreatedAt) ||
			(handoff.CreatedAt.Equal(current.CreatedAt) && handoff.ID > current.ID) {
			scopeStore.handoffChild[key] = id
		}
	}
	for id, handoff := range committedHandoffs {
		scopeStore.committedHandoffs[id] = handoff
	}
	if err := restoreSessionScopeReservations(store, restored, scopeStore); err != nil {
		return err
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	for id, state := range restored {
		if _, exists := s.sessions[id]; exists {
			return fmt.Errorf("durable session %q conflicts with process-local session", id)
		}
		s.sessions[id] = state
		scopeStore.usedTickets[state.scope.ticketDigest] = struct{}{}
		scopeStore.usedResumes[state.scope.resumeDigest] = struct{}{}
	}
	s.sessionScopes = scopeStore
	return nil
}

func restoreSessionScopeReservations(store *wal.Store, sessions map[string]*sessionState, target *sessionScopeStore) error {
	consumedTickets := make(map[[sha256.Size]byte][sha256.Size]byte, len(sessions))
	seenReservationTickets := make(map[[sha256.Size]byte]struct{})
	seenReservationResumes := make(map[[sha256.Size]byte]struct{})
	for _, state := range sessions {
		consumedTickets[state.scope.ticketDigest] = state.scope.resumeDigest
		target.usedTickets[state.scope.ticketDigest] = struct{}{}
		target.usedResumes[state.scope.resumeDigest] = struct{}{}
	}
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store != sessionReservationWALStore {
				continue
			}
			var reservation sessionScopeReservation
			if err := json.Unmarshal(event.Data, &reservation); err != nil {
				return fmt.Errorf("session reservation sequence %d: %w", record.Sequence, err)
			}
			if reservation.Schema != sessionScopeSchema || reservation.ID == "" || reservation.Principal == "" || reservation.ExpiresAt.IsZero() ||
				reservation.CreatedAt.IsZero() || !reservation.ExpiresAt.After(reservation.CreatedAt) {
				return fmt.Errorf("session reservation sequence %d: invalid envelope", record.Sequence)
			}
			if err := stadogit.ValidateSessionID(reservation.Subject); err != nil || reservation.RepoID == "" {
				return fmt.Errorf("session reservation sequence %d: invalid scope", record.Sequence)
			}
			ticket, err := decodeScopeDigest(reservation.TicketHash, false)
			if err != nil {
				return fmt.Errorf("session reservation sequence %d: ticket digest: %w", record.Sequence, err)
			}
			resume, err := decodeScopeDigest(reservation.ResumeHash, false)
			if err != nil {
				return fmt.Errorf("session reservation sequence %d: resume digest: %w", record.Sequence, err)
			}
			if _, duplicate := seenReservationTickets[ticket]; duplicate {
				return fmt.Errorf("session reservation sequence %d: duplicate ticket digest", record.Sequence)
			}
			if _, duplicate := seenReservationResumes[resume]; duplicate {
				return fmt.Errorf("session reservation sequence %d: duplicate resume digest", record.Sequence)
			}
			seenReservationTickets[ticket] = struct{}{}
			seenReservationResumes[resume] = struct{}{}
			if expectedResume, consumed := consumedTickets[ticket]; consumed {
				if subtle.ConstantTimeCompare(expectedResume[:], resume[:]) != 1 {
					return fmt.Errorf("session reservation sequence %d: consumed ticket has mismatched resume digest", record.Sequence)
				}
				continue
			}
			if _, exists := target.usedTickets[ticket]; exists {
				return fmt.Errorf("session reservation sequence %d: duplicate ticket digest", record.Sequence)
			}
			if _, exists := target.usedResumes[resume]; exists {
				return fmt.Errorf("session reservation sequence %d: duplicate resume digest", record.Sequence)
			}
			target.usedTickets[ticket] = struct{}{}
			target.usedResumes[resume] = struct{}{}
			reservation.TicketDigest, reservation.ResumeDigest = ticket, resume
			target.reservations[reservationKey(reservation.RepoID, reservation.Subject)] = reservation
		}
	}
	return nil
}

func restoreSessionScopeSnapshots(store *wal.Store) (map[string]*sessionState, error) {
	restored, _, _, err := restoreSessionScopeState(store)
	return restored, err
}

func restoreSessionScopeState(store *wal.Store) (map[string]*sessionState, map[string]sessionSubjectHandoffReservation, map[string]sessionSubjectHandoffReservation, error) {
	handoffs, err := decodeSessionSubjectHandoffs(store)
	if err != nil {
		return nil, nil, nil, err
	}
	latest := make(map[string]sessionScopeSnapshot)
	committed := make(map[string]struct{})
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store != sessionScopeWALStore {
				continue
			}
			var snapshot sessionScopeSnapshot
			if err := json.Unmarshal(event.Data, &snapshot); err != nil {
				return nil, nil, nil, fmt.Errorf("session scope sequence %d: %w", record.Sequence, err)
			}
			if err := validateSessionScopeSnapshot(event, snapshot); err != nil {
				return nil, nil, nil, fmt.Errorf("session scope sequence %d: %w", record.Sequence, err)
			}
			previous, ok := latest[snapshot.SessionID]
			if ok {
				if err := validateSessionScopeTransition(previous, snapshot, handoffs, record.Sequence); err != nil {
					return nil, nil, nil, fmt.Errorf("session %q transition: %w", snapshot.SessionID, err)
				}
			} else if snapshot.Version != 1 || snapshot.ControllerVersion != 1 || snapshot.Status != sessionScopeAttached || snapshot.ControllerDigest == "" {
				return nil, nil, nil, fmt.Errorf("session %q lacks a valid initial attachment", snapshot.SessionID)
			}
			if snapshot.SubjectHandoff != nil {
				if _, duplicate := committed[snapshot.SubjectHandoff.ID]; duplicate {
					return nil, nil, nil, fmt.Errorf("session handoff %q committed more than once", snapshot.SubjectHandoff.ID)
				}
				committed[snapshot.SubjectHandoff.ID] = struct{}{}
			}
			latest[snapshot.SessionID] = snapshot
		}
	}
	result := make(map[string]*sessionState, len(latest))
	subjects := make(map[string]string, len(latest))
	tickets := make(map[[sha256.Size]byte]string, len(latest))
	resumes := make(map[[sha256.Size]byte]string, len(latest))
	for id, snapshot := range latest {
		key := snapshot.RepoID + "\x00" + snapshot.Subject
		if previous := subjects[key]; previous != "" && previous != id && snapshot.Status != sessionScopeTerminated {
			return nil, nil, nil, fmt.Errorf("logical subject %q has multiple live durable sessions", snapshot.Subject)
		}
		if snapshot.Status != sessionScopeTerminated {
			subjects[key] = id
		}
		controller, err := decodeScopeDigest(snapshot.ControllerDigest, snapshot.Status == sessionScopeTerminated || snapshot.Status == sessionScopeDetached)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("session %q controller digest: %w", id, err)
		}
		// A controller authenticates one live broker ownership epoch. After a
		// daemon restart, the durable recovery bearer may adopt immediately but
		// the old in-memory controller must never resume raw native control.
		if snapshot.LeaseEpoch != store.Epoch() {
			controller = [sha256.Size]byte{}
		}
		ticket, err := decodeScopeDigest(snapshot.TicketDigest, false)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("session %q ticket digest: %w", id, err)
		}
		resume, err := decodeScopeDigest(snapshot.ResumeDigest, false)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("session %q resume digest: %w", id, err)
		}
		if previous := tickets[ticket]; previous != "" && previous != id {
			return nil, nil, nil, fmt.Errorf("sessions %q and %q share a recovery ticket digest", previous, id)
		}
		if previous := resumes[resume]; previous != "" && previous != id {
			return nil, nil, nil, fmt.Errorf("sessions %q and %q share a recovery resume digest", previous, id)
		}
		tickets[ticket] = id
		resumes[resume] = id
		terminated := snapshot.Status == sessionScopeTerminated
		result[id] = &sessionState{
			handle: snapshot.Handle, controller: controller,
			controllerVersion: snapshot.ControllerVersion,
			terminated:        terminated, principal: snapshot.Principal, repoID: snapshot.RepoID,
			parentID: snapshot.ParentID, generation: snapshot.Generation, taint: snapshot.Taint,
			scope: sessionScopeState{
				durable: true, subject: snapshot.Subject, ticketDigest: ticket,
				resumeDigest: resume, status: snapshot.Status, version: snapshot.Version,
				leaseEpoch: snapshot.LeaseEpoch,
			},
		}
	}
	committedHandoffs := make(map[string]sessionSubjectHandoffReservation, len(committed))
	for id := range committed {
		committedHandoffs[id] = handoffs[id]
		delete(handoffs, id)
	}
	for id, handoff := range handoffs {
		state := result[handoff.SessionID]
		if state == nil {
			return nil, nil, nil, fmt.Errorf("session handoff %q has no durable source scope", id)
		}
		if state.terminated || state.controllerVersion > handoff.ControllerVersion {
			delete(handoffs, id)
			continue
		}
		if state.controllerVersion != handoff.ControllerVersion || state.generation != handoff.Generation ||
			state.principal != handoff.Principal || state.repoID != handoff.RepoID || state.handle.CWD != handoff.SourceCWD ||
			state.scope.subject != handoff.SourceSubject ||
			subtle.ConstantTimeCompare(state.scope.ticketDigest[:], handoff.TicketDigest[:]) != 1 ||
			subtle.ConstantTimeCompare(state.scope.resumeDigest[:], handoff.ResumeDigest[:]) != 1 {
			return nil, nil, nil, fmt.Errorf("session handoff %q conflicts with restored source scope", id)
		}
	}
	return result, handoffs, committedHandoffs, nil
}

func validateSessionScopeSnapshot(event wal.Event, snapshot sessionScopeSnapshot) error {
	if snapshot.Schema != sessionScopeSchema {
		return fmt.Errorf("unsupported schema %d", snapshot.Schema)
	}
	if snapshot.SessionID == "" || event.Session != snapshot.SessionID || snapshot.Handle.SessionID != snapshot.SessionID {
		return errors.New("session identity mismatch")
	}
	if err := stadogit.ValidateSessionID(snapshot.Subject); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if snapshot.Generation == 0 || snapshot.ControllerVersion == 0 || snapshot.Version == 0 {
		return errors.New("generation and versions must be positive")
	}
	switch snapshot.Status {
	case sessionScopeAttached, sessionScopeDetached, sessionScopeTerminated:
	default:
		return fmt.Errorf("invalid status %q", snapshot.Status)
	}
	if snapshot.Handle.Purpose != PurposeMainChat || !snapshot.Handle.Profile.Valid() || snapshot.LeaseEpoch == 0 {
		return errors.New("durable scope has invalid handle or broker epoch")
	}
	if snapshot.Taint != TaintClean && snapshot.Taint != TaintTainted {
		return errors.New("durable scope has invalid taint")
	}
	return nil
}

func validateSessionScopeTransition(previous, next sessionScopeSnapshot, handoffs map[string]sessionSubjectHandoffReservation, transitionSequence uint64) error {
	if next.Version != previous.Version+1 {
		return fmt.Errorf("version %d does not follow %d", next.Version, previous.Version)
	}
	if next.Principal != previous.Principal || next.RepoID != previous.RepoID ||
		next.ParentID != previous.ParentID || next.Generation != previous.Generation || !sameSessionScopeHandle(previous.Handle, next.Handle) {
		return errors.New("immutable durable session identity changed")
	}
	if next.Subject == previous.Subject {
		if next.SubjectHandoff != nil {
			return errors.New("subject handoff marker did not change subject")
		}
	} else if err := validateSessionSubjectHandoffTransition(previous, next, handoffs, transitionSequence); err != nil {
		return err
	}
	if next.UpdatedAt.Before(previous.UpdatedAt) {
		return errors.New("durable session timestamp moved backwards")
	}
	if previous.Status == sessionScopeTerminated {
		return errors.New("terminated durable session changed")
	}
	if !IsSubsetOf(next.Handle.Effective, previous.Handle.Effective) {
		return errors.New("durable session effective authority widened")
	}
	switch {
	case next.ControllerVersion == previous.ControllerVersion:
		if next.TicketDigest != previous.TicketDigest || next.ResumeDigest != previous.ResumeDigest {
			return errors.New("recovery bearer changed without controller adoption")
		}
		if next.Status == sessionScopeAttached && next.ControllerDigest != previous.ControllerDigest {
			return errors.New("controller digest changed without adoption")
		}
		if previous.Status == sessionScopeDetached && next.Status != sessionScopeDetached {
			return errors.New("detached session changed without adoption")
		}
	case next.ControllerVersion == previous.ControllerVersion+1:
		if next.Status != sessionScopeAttached || next.ControllerDigest == "" {
			return errors.New("controller adoption did not produce an attached controller")
		}
		if next.TicketDigest != previous.TicketDigest || next.ResumeDigest != previous.ResumeDigest {
			return errors.New("durable recovery bearer changed during one-phase adoption")
		}
	default:
		return errors.New("controller version did not advance by at most one")
	}
	if next.Subject != previous.Subject && (previous.Status != sessionScopeAttached ||
		next.ControllerVersion != previous.ControllerVersion+1 || next.Status != sessionScopeAttached) {
		return errors.New("subject handoff did not rotate one attached controller version")
	}
	return nil
}

func sameSessionScopeHandle(left, right SessionHandle) bool {
	left.Effective = left.Ceiling
	right.Effective = right.Ceiling
	return reflect.DeepEqual(left, right)
}

func decodeScopeDigest(value string, allowEmpty bool) ([sha256.Size]byte, error) {
	if value == "" && allowEmpty {
		return [sha256.Size]byte{}, nil
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size {
		return [sha256.Size]byte{}, errors.New("invalid sha256 digest")
	}
	var digest [sha256.Size]byte
	copy(digest[:], raw)
	return digest, nil
}

func (s *Service) prepareDurableSessionScope(state *sessionState, credential SessionAdoptionCredential) error {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil {
		return ErrSessionScopeUnavailable
	}
	subject := credential.Subject
	if err := stadogit.ValidateSessionID(subject); err != nil {
		return fmt.Errorf("broker: logical session subject: %w", err)
	}
	if err := ValidateSessionAdoptionCredential(credential); err != nil {
		return err
	}
	now := s.now()
	state.scope = sessionScopeState{
		durable: true, subject: subject, ticket: credential.Ticket,
		ticketDigest: sha256.Sum256([]byte(credential.Ticket)),
		resumeSecret: credential.ResumeSecret,
		resumeDigest: sha256.Sum256([]byte(credential.ResumeSecret)),
		status:       sessionScopeAttached, version: 1, leaseEpoch: s.sessionScopes.wal.Epoch(),
		leaseUntil: now.Add(s.sessionScopes.leaseTTL),
	}
	return nil
}

func mintSessionAdoptionCredential(subject string) (SessionAdoptionCredential, error) {
	if err := stadogit.ValidateSessionID(subject); err != nil {
		return SessionAdoptionCredential{}, fmt.Errorf("broker: logical session subject: %w", err)
	}
	ticket, _, err := mintScopeBearer("scope_")
	if err != nil {
		return SessionAdoptionCredential{}, fmt.Errorf("broker: mint adoption ticket: %w", err)
	}
	resume, _, err := mintScopeBearer("resume_")
	if err != nil {
		return SessionAdoptionCredential{}, fmt.Errorf("broker: mint resume secret: %w", err)
	}
	return SessionAdoptionCredential{Subject: subject, Ticket: ticket, ResumeSecret: resume}, nil
}

func ValidateSessionAdoptionCredential(credential SessionAdoptionCredential) error {
	if err := stadogit.ValidateSessionID(credential.Subject); err != nil {
		return ErrSessionScopeCredential
	}
	if !validScopeBearer(credential.Ticket, "scope_") || !validScopeBearer(credential.ResumeSecret, "resume_") {
		return ErrSessionScopeCredential
	}
	return nil
}

func validScopeBearer(value, prefix string) bool {
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func mintScopeBearer(prefix string) (string, [sha256.Size]byte, error) {
	token, _, err := mintControllerToken()
	if err != nil {
		return "", [sha256.Size]byte{}, err
	}
	token = prefix + strings.TrimPrefix(token, "controller_")
	return token, sha256.Sum256([]byte(token)), nil
}

func reservationKey(repoID, subject string) string { return repoID + "\x00" + subject }

// ReserveSessionScope is the first half of crash-safe logical-session create.
// The already-live parent controller authenticates the native caller; the
// broker derives the repository and mints the recovery bearer. The reservation
// has no session/application authority and expires if the one-shot response is
// lost before the client can save and commit it.
func (s *Service) ReserveSessionScope(parentSessionID, parentControllerToken, subject, cwd string) (SessionAdoptionCredential, time.Time, error) {
	repoID, err := canonicalArtifactRepoID(cwd)
	if err != nil || repoID == "" {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeCredential
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	parent, ok := s.sessions[parentSessionID]
	if !ok {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionNotFound
	}
	if parent.terminated {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(parent, parentControllerToken); err != nil {
		return SessionAdoptionCredential{}, time.Time{}, err
	}
	if parent.repoID != repoID {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeCredential
	}
	return s.reserveSessionScopeLocked(parentSessionID, parent.principal, subject, repoID)
}

func (s *Service) reserveSessionScope(parentSessionID, principal, subject, repoID string) (SessionAdoptionCredential, time.Time, error) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	return s.reserveSessionScopeLocked(parentSessionID, principal, subject, repoID)
}

func (s *Service) reserveSessionScopeLocked(parentSessionID, principal, subject, repoID string) (SessionAdoptionCredential, time.Time, error) {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeUnavailable
	}
	if err := stadogit.ValidateSessionID(subject); err != nil || repoID == "" || principal == "" {
		return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeCredential
	}
	key := reservationKey(repoID, subject)
	now := s.now().UTC()
	for _, state := range s.sessions {
		if state.scope.durable && !state.terminated && state.repoID == repoID && state.scope.subject == subject {
			return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeExists
		}
	}
	if handoffID := s.sessionScopes.handoffChild[key]; handoffID != "" {
		if handoff, exists := s.sessionScopes.handoffs[handoffID]; exists && handoff.ExpiresAt.After(now) {
			return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeActive
		}
	}
	if current, ok := s.sessionScopes.reservations[key]; ok && current.ExpiresAt.After(now) {
		// A reservation is bound to the exact live parent that requested it.
		// After a daemon restart that process-local parent no longer exists, so
		// a newly authenticated parent with the same broker-derived principal
		// and repository may supersede the authority-free reservation. It must
		// save the newly minted bearer before commit; the old bearer remains
		// unusable and no application scope existed to strand.
		oldParent, oldParentExists := s.sessions[current.ParentSessionID]
		oldParentLive := oldParentExists && !oldParent.terminated
		if current.ParentSessionID == "" || current.ParentSessionID == parentSessionID || oldParentLive ||
			current.Principal != principal || current.RepoID != repoID {
			return SessionAdoptionCredential{}, time.Time{}, ErrSessionScopeActive
		}
	}
	var credential SessionAdoptionCredential
	for range 8 {
		candidate, err := mintSessionAdoptionCredential(subject)
		if err != nil {
			return SessionAdoptionCredential{}, time.Time{}, err
		}
		ticket := sha256.Sum256([]byte(candidate.Ticket))
		resume := sha256.Sum256([]byte(candidate.ResumeSecret))
		_, ticketUsed := s.sessionScopes.usedTickets[ticket]
		_, resumeUsed := s.sessionScopes.usedResumes[resume]
		if !ticketUsed && !resumeUsed {
			credential = candidate
			break
		}
	}
	if credential.Ticket == "" {
		return SessionAdoptionCredential{}, time.Time{}, errors.New("broker: recovery bearer collision limit reached")
	}
	id, err := mintSessionID()
	if err != nil {
		return SessionAdoptionCredential{}, time.Time{}, err
	}
	reservation := sessionScopeReservation{
		Schema: sessionScopeSchema, ID: id, ParentSessionID: parentSessionID,
		Principal: principal, Subject: subject, RepoID: repoID,
		TicketDigest: sha256.Sum256([]byte(credential.Ticket)), ResumeDigest: sha256.Sum256([]byte(credential.ResumeSecret)),
		ExpiresAt: now.Add(sessionReservationTTL), CreatedAt: now,
	}
	reservation.TicketHash = hex.EncodeToString(reservation.TicketDigest[:])
	reservation.ResumeHash = hex.EncodeToString(reservation.ResumeDigest[:])
	data, err := json.Marshal(reservation)
	if err != nil {
		return SessionAdoptionCredential{}, time.Time{}, err
	}
	idem := "session-reservation:" + reservation.ID
	if _, err := s.sessionScopes.wal.Append(wal.Transaction{
		ID: idem, IdempotencyKey: idem, Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionReservationWALStore, Type: "reserved", Data: data}},
	}); err != nil {
		return SessionAdoptionCredential{}, time.Time{}, fmt.Errorf("broker: persist session reservation: %w", err)
	}
	s.sessionScopes.reservations[key] = reservation
	s.sessionScopes.usedTickets[reservation.TicketDigest] = struct{}{}
	s.sessionScopes.usedResumes[reservation.ResumeDigest] = struct{}{}
	return credential, reservation.ExpiresAt, nil
}

func (s *Service) persistNewSessionScopeLocked(state *sessionState) error {
	for _, existing := range s.sessions {
		if existing.scope.durable && (subtle.ConstantTimeCompare(existing.scope.ticketDigest[:], state.scope.ticketDigest[:]) == 1 ||
			subtle.ConstantTimeCompare(existing.scope.resumeDigest[:], state.scope.resumeDigest[:]) == 1) {
			return ErrSessionScopeCredential
		}
		if existing.scope.durable && !existing.terminated && existing.repoID == state.repoID &&
			existing.scope.subject == state.scope.subject {
			return ErrSessionScopeExists
		}
	}
	key := reservationKey(state.repoID, state.scope.subject)
	reservation, ok := s.sessionScopes.reservations[key]
	if !ok || !reservation.ExpiresAt.After(s.now()) ||
		reservation.ParentSessionID != state.scope.reservationParentID ||
		reservation.Principal != state.principal || reservation.RepoID != state.repoID ||
		reservation.Subject != state.scope.subject ||
		subtle.ConstantTimeCompare(reservation.TicketDigest[:], state.scope.ticketDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(reservation.ResumeDigest[:], state.scope.resumeDigest[:]) != 1 {
		return ErrSessionScopeCredential
	}
	if reservation.ParentSessionID == "" {
		if state.scope.reservationParentID != "" {
			return ErrSessionScopeCredential
		}
	} else {
		parent, exists := s.sessions[state.scope.reservationParentID]
		if !exists || parent.terminated || parent.repoID != reservation.RepoID || parent.principal != reservation.Principal {
			return ErrSessionScopeCredential
		}
	}
	if err := s.appendSessionScopeSnapshotLocked(state, s.now()); err != nil {
		return err
	}
	delete(s.sessionScopes.reservations, key)
	return nil
}

func (s *Service) persistSessionScopeTransitionLocked(state *sessionState, status sessionScopeStatus, at time.Time) error {
	next := *state
	next.scope.status = status
	next.scope.version++
	next.scope.leaseEpoch = s.sessionScopes.wal.Epoch()
	if status != sessionScopeAttached {
		next.controller = [sha256.Size]byte{}
	}
	return s.appendSessionScopeSnapshotLocked(&next, at)
}

func (s *Service) appendSessionScopeSnapshotLocked(state *sessionState, at time.Time) error {
	return s.appendSessionScopeSnapshotWithHandoffLocked(state, at, nil)
}

func (s *Service) appendSessionScopeSnapshotWithHandoffLocked(state *sessionState, at time.Time, handoff *sessionSubjectHandoffCommit) error {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil {
		return ErrSessionScopeUnavailable
	}
	snapshot := sessionScopeSnapshot{
		Schema: sessionScopeSchema, SessionID: state.handle.SessionID, Subject: state.scope.subject,
		Handle: state.handle, ControllerDigest: hex.EncodeToString(state.controller[:]),
		ControllerVersion: state.controllerVersion,
		TicketDigest:      hex.EncodeToString(state.scope.ticketDigest[:]),
		ResumeDigest:      hex.EncodeToString(state.scope.resumeDigest[:]),
		Status:            state.scope.status, Principal: state.principal, RepoID: state.repoID,
		ParentID: state.parentID, Generation: state.generation, Taint: state.taint,
		Version: state.scope.version, LeaseEpoch: state.scope.leaseEpoch, UpdatedAt: at.UTC(),
		SubjectHandoff: handoff,
	}
	if snapshot.Status != sessionScopeAttached {
		snapshot.ControllerDigest = ""
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	idem := fmt.Sprintf("session-scope:%s:%d", snapshot.SessionID, snapshot.Version)
	_, err = s.sessionScopes.wal.Append(wal.Transaction{
		ID: idem, IdempotencyKey: idem, Principal: "broker", Actor: "broker:session-scope",
		Events: []wal.Event{{Store: sessionScopeWALStore, Type: string(snapshot.Status), Session: snapshot.SessionID, Data: data}},
	})
	if err != nil {
		return fmt.Errorf("broker: persist durable session scope: %w", err)
	}
	return nil
}

// AdoptSession atomically reopens a durable logical-session scope. It preserves
// SessionID and generation, rotates the live controller, and advances its
// version so pre-adoption plugin bindings fail closed. The recovery bearer is
// stable: rotating it in this one-phase response would create an unrecoverable
// crash window before the client can atomically replace its 0600 file.
func (s *Service) AdoptSession(credential SessionAdoptionCredential, cwd string) (SessionHandle, SessionAdoptionCredential, error) {
	if s.sessionScopes == nil || s.sessionScopes.wal == nil {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeUnavailable
	}
	if err := ValidateSessionAdoptionCredential(credential); err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeCredential
	}
	ticketDigest := sha256.Sum256([]byte(credential.Ticket))
	resumeDigest := sha256.Sum256([]byte(credential.ResumeSecret))
	repoID, err := canonicalArtifactRepoID(cwd)
	if err != nil || repoID == "" {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeCredential
	}

	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	var state *sessionState
	for _, candidate := range s.sessions {
		if !candidate.scope.durable || candidate.scope.subject != credential.Subject {
			continue
		}
		if subtle.ConstantTimeCompare(ticketDigest[:], candidate.scope.ticketDigest[:]) == 1 {
			state = candidate
			break
		}
	}
	if state == nil || state.repoID != repoID || subtle.ConstantTimeCompare(resumeDigest[:], state.scope.resumeDigest[:]) != 1 {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeCredential
	}
	if state.terminated || state.scope.status == sessionScopeTerminated {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionTerminated
	}
	now := s.now()
	currentEpoch := s.sessionScopes.wal.Epoch()
	if state.scope.status == sessionScopeAttached && state.scope.leaseEpoch == currentEpoch && now.Before(state.scope.leaseUntil) {
		return SessionHandle{}, SessionAdoptionCredential{}, ErrSessionScopeActive
	}
	controllerToken, controllerDigest, err := mintControllerToken()
	if err != nil {
		return SessionHandle{}, SessionAdoptionCredential{}, err
	}
	next := *state
	next.controller = controllerDigest
	next.controllerVersion++
	next.scope.status = sessionScopeAttached
	next.scope.version++
	next.scope.leaseEpoch = currentEpoch
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
	handle.subject = credential.Subject
	handle.adoptionTicket = credential.Ticket
	handle.resumeSecret = credential.ResumeSecret
	return handle, credential, nil
}

// DetachSession retires the live controller while leaving its durable
// application scope adoptable. TerminateSession is the irreversible path.
func (s *Service) DetachSession(sessionID, controllerToken string) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if state.terminated {
		return ErrSessionTerminated
	}
	if !state.scope.durable {
		return ErrSessionScopeUnavailable
	}
	if err := authenticateControllerLocked(state, controllerToken); err != nil {
		return err
	}
	now := s.now()
	if err := s.persistSessionScopeTransitionLocked(state, sessionScopeDetached, now); err != nil {
		return err
	}
	state.controller = [sha256.Size]byte{}
	state.scope.status = sessionScopeDetached
	state.scope.version++
	state.scope.leaseUntil = time.Time{}
	s.invalidateSessionBindingsLocked(sessionID)
	return nil
}

// HeartbeatSession renews only the in-memory live-owner lease. The durable WAL
// epoch makes a daemon restart immediately adoptable without waiting for an old
// wall-clock deadline.
func (s *Service) HeartbeatSession(sessionID, controllerToken string) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if state.terminated {
		return ErrSessionTerminated
	}
	if !state.scope.durable || state.scope.status != sessionScopeAttached {
		return ErrSessionScopeUnavailable
	}
	if err := authenticateControllerLocked(state, controllerToken); err != nil {
		return err
	}
	state.scope.leaseUntil = s.now().Add(s.sessionScopes.leaseTTL)
	return nil
}
