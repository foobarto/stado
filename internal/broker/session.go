package broker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/sandbox"
)

// ErrSessionNotFound is returned by Terminate when the supplied
// SessionID was never minted (or was already terminated and
// purged). Surfaced as the JSON-RPC ErrCodeBrokerSessionNotFound.
var ErrSessionNotFound = errors.New("broker: session not found")

// ErrSessionTerminated is returned when an operation is attempted
// against a SessionID that was minted but has since been
// terminated. Distinguished from ErrSessionNotFound so the
// orchestrator can surface "you already ended this" separately
// from "wrong handle".
var ErrSessionTerminated = errors.New("broker: session terminated")

// sessionState is the broker-internal record of a minted session.
// Held under sessions.mu. Not exposed across the IPC.
type sessionState struct {
	handle        SessionHandle
	terminated    bool
	terminated_at time.Time
}

// Service is the broker's runtime state holder. One instance per
// daemon process; constructed at daemon startup and registered
// via daemon.ServerOpts.BrokerDispatcher.
type Service struct {
	// policy is the loaded capability policy. Replaced atomically
	// on operator-triggered reload (phase 1b adds the reload path).
	policyMu sync.RWMutex
	policy   *Policy

	// sessions tracks every session.create handle minted by this
	// broker for the daemon's lifetime. Terminated sessions are
	// kept (not purged) so reuse-after-terminate returns the
	// terminated error rather than the not-found error.
	sessionsMu sync.RWMutex
	sessions   map[string]*sessionState

	// decisionsLog appends every admit/deny to the broker-decision
	// log. nil = decision logging disabled (test mode). Phase 5
	// hardens the writer; phase 1 wires the surface.
	decisionsLog DecisionWriter

	// now is overridable for tests.
	now func() time.Time
}

// NewService constructs a Service with the given policy and decision
// writer. Either may be nil — a nil policy makes every request a
// no-policy denial; a nil decisionsLog disables logging (test mode).
func NewService(policy *Policy, decisionsLog DecisionWriter) *Service {
	return &Service{
		policy:       policy,
		sessions:     make(map[string]*sessionState),
		decisionsLog: decisionsLog,
		now:          time.Now,
	}
}

// SetPolicy replaces the loaded policy atomically. Used by the
// policy reload path (phase 1b).
func (s *Service) SetPolicy(p *Policy) {
	s.policyMu.Lock()
	s.policy = p
	s.policyMu.Unlock()
}

// loadedPolicy returns the current policy under a read lock.
func (s *Service) loadedPolicy() *Policy {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.policy
}

// Evaluate runs the loaded policy against req. Convenience wrapper
// around Policy.Evaluate that adds decision logging.
func (s *Service) Evaluate(req CapabilityRequest) Decision {
	p := s.loadedPolicy()
	if p == nil {
		d := Decision{Admit: false, Rule: "no-policy", Reason: ErrPolicyNotLoaded.Error()}
		s.logDecision(req, d)
		return d
	}
	d := p.Evaluate(req)
	s.logDecision(req, d)
	return d
}

// CreateSession admits or denies a session-creation request and, on
// admission, mints a SessionHandle. Phase 1: the ceiling is a coarse
// approximation derived from purpose+profile (phase 4 will tighten
// the projection from the full request shape).
func (s *Service) CreateSession(req CapabilityRequest) (SessionHandle, Decision, error) {
	if !req.Purpose.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid purpose %q", req.Purpose)
	}
	if !req.Profile.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid profile %q", req.Profile)
	}
	d := s.Evaluate(req)
	if !d.Admit {
		return SessionHandle{}, d, nil
	}

	id, err := mintSessionID()
	if err != nil {
		return SessionHandle{}, d, fmt.Errorf("broker: mint session id: %w", err)
	}

	now := s.now()
	handle := SessionHandle{
		SessionID: id,
		Purpose:   req.Purpose,
		Ceiling:   projectCeiling(req),
		TraceRef:  traceRefFor(req, id),
		ExpiresAt: time.Time{}, // phase 1: no broker-enforced expiry
		CreatedAt: now,
	}

	s.sessionsMu.Lock()
	s.sessions[id] = &sessionState{handle: handle}
	s.sessionsMu.Unlock()

	return handle, d, nil
}

// TerminateSession marks the named session terminated. Returns
// ErrSessionNotFound if the SessionID was never minted, or
// ErrSessionTerminated if it was already terminated. Idempotent
// callers should ignore ErrSessionTerminated.
func (s *Service) TerminateSession(sessionID string) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if st.terminated {
		return ErrSessionTerminated
	}
	st.terminated = true
	st.terminated_at = s.now()
	return nil
}

// LookupSession returns the SessionHandle for sessionID along with
// its terminated flag. Returns ErrSessionNotFound if the ID was
// never minted. Used by other broker.v1.* methods that operate
// against an existing handle.
func (s *Service) LookupSession(sessionID string) (SessionHandle, bool, error) {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return SessionHandle{}, false, ErrSessionNotFound
	}
	return st.handle, st.terminated, nil
}

// logDecision appends a record to the decision writer if one is
// configured. Best-effort: write errors are logged via the writer
// itself (phase 5 hardens behaviour on writer failure).
func (s *Service) logDecision(req CapabilityRequest, d Decision) {
	if s.decisionsLog == nil {
		return
	}
	_ = s.decisionsLog.Write(DecisionRecord{
		Time:     s.now(),
		Request:  req,
		Decision: d,
	})
}

// mintSessionID returns a fresh 128-bit random session ID encoded
// as hex (32 chars). Collisions are vanishingly unlikely; on the
// off chance the random source fails, the error propagates and
// the broker refuses to mint.
func mintSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// projectCeiling produces a sandbox.Policy from the mount-and-
// namespace invariant table for the requested profile (see
// mount_table.go). Phase 3 narrows reads from today's "/" to the
// table-derived RO/RW set; phase 4 will tighten further from the
// full request shape (e.g. sub-agent WriteScope).
//
// ProfileNoSandbox returns an empty Policy — the runtime picks
// NoneRunner and no namespace isolation is applied. The broker
// has still admitted the request via Service.Evaluate, so the
// decision is captured in the broker-decision log.
func projectCeiling(req CapabilityRequest) sandbox.Policy {
	if req.Profile == ProfileNoSandbox {
		return sandbox.Policy{}
	}
	return MountTableFor(req.Profile, req.CWD).ToPolicy()
}

// traceRefFor returns the git ref name the broker would append
// trace events to for this session. Empty for PurposeToolRun (no
// trace ref). Phase 1: the ref name is computed but not yet used
// (phase 5 wires the broker as sole writer).
func traceRefFor(req CapabilityRequest, sessionID string) string {
	if req.Purpose == PurposeToolRun {
		return ""
	}
	return fmt.Sprintf("refs/sessions/%s/trace", sessionID)
}
