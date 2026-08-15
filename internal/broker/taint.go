package broker

// Taint is the coarse per-session turn-provenance marker. It records whether a
// tool result has entered the current turn. It is audit metadata, not an
// authority input or content-safety judgment.
//
// Phase 6 wires the broker substrate:
//
//   - Per-session taint state (Clean / Tainted) tracked by the
//     broker, mutated via SetTaint by ingestion sites.
//   - SessionHandle carries the current taint state for the
//     orchestrator's banner / decision use.
//   - broker policy queries retain the requesting session identity so
//     provenance can be correlated in the audit trail.
//
// A single tool-result span marks the entire turn. The marker resets when the
// next operator turn arrives.
type Taint int

const (
	// TaintClean is the default state at session creation and the
	// state after each operator-turn reset.
	TaintClean Taint = iota

	// TaintTainted means at least one UNTRUSTED-origin span has
	// entered the session's context since the last operator turn.
	// The marker remains set until the next operator-turn reset.
	TaintTainted
)

func (t Taint) String() string {
	switch t {
	case TaintClean:
		return "clean"
	case TaintTainted:
		return "tainted"
	}
	return "unknown"
}

// SetTaint sets the session's taint state. Called by ingestion
// sites when a tool result enters the context, and by the agent loop at
// operator-turn boundaries when the baseline resets to TaintClean.
//
// Returns ErrSessionNotFound for unknown handles and
// ErrSessionTerminated for terminated ones.
//
// Setting from Tainted → Clean is allowed (operator turn reset);
// setting from Clean → Tainted is the typical ingestion-site
// transition.
func (s *Service) SetTaint(sessionID, controllerToken string, t Taint) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if st.terminated {
		return ErrSessionTerminated
	}
	if err := authenticateControllerLocked(st, controllerToken); err != nil {
		return err
	}
	if st.scope.durable {
		next := *st
		next.taint = t
		next.scope.version++
		if err := s.appendSessionScopeSnapshotLocked(&next, s.now()); err != nil {
			return err
		}
		st.scope.version = next.scope.version
	}
	st.taint = t
	return nil
}

// Taint returns the session's current taint state. Returns
// ErrSessionNotFound for unknown handles.
func (s *Service) Taint(sessionID string) (Taint, error) {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return TaintClean, ErrSessionNotFound
	}
	return st.taint, nil
}

// EvaluateWithTaint evaluates req and records the resulting policy decision.
// The name is retained for the broker RPC contract. Taint is provenance and
// audit state; it does not select a second policy matrix or alter capability
// grants. Authority is determined by explicit capabilities and attenuation.
//
// CapabilityRequest.SessionID still identifies the requesting session in the
// decision record; it does not change the decision.
func (s *Service) EvaluateWithTaint(req CapabilityRequest) Decision {
	decision := s.evaluate(req)
	s.logDecision(req, decision)
	return decision
}
