package broker

// taint.go — phase 6 implementation of provenance/taint tagging.
// DESIGN.md §"Context management" → "Provenance and taint": every
// span of data entering a session's context carries a provenance
// label assigned mechanically at ingestion. The trust-critical
// decision rule is policy over ORIGINS — a fact — never a content-
// safety judgment.
//
// Phase 6 wires the broker substrate:
//
//   - Per-session taint state (Clean / Tainted) tracked by the
//     broker, mutated via SetTaint by ingestion sites.
//   - SessionHandle carries the current taint state for the
//     orchestrator's banner / decision use.
//   - taint factors into broker.v1.policy.query and (in phase 7)
//     the socket-bearing git-sub-agent approval prompt.
//
// What this DOESN'T do: wire the actual ingestion points in the
// context-management layer. That requires per-span metadata in
// the message slice / Provider event stream — a substantial
// agent-loop change. Phase 6 readies the substrate; the runtime
// wiring follows.

import (
	"fmt"
)

// Taint is the per-session provenance state. Clean = no untrusted
// span has entered the context since the last operator turn;
// Tainted = at least one untrusted span has entered.
//
// The conservative over-approximation: a single untrusted span
// taints the entire turn. The taint baseline resets when the next
// operator turn (a TRUSTED span) arrives.
type Taint int

const (
	// TaintClean is the default state at session creation and the
	// state after each operator-turn reset.
	TaintClean Taint = iota

	// TaintTainted means at least one UNTRUSTED-origin span has
	// entered the session's context since the last operator turn.
	// All subsequent tool calls in this turn are tainted.
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
// sites (file reads, tool results, web fetches) when an UNTRUSTED
// span enters the context, and by the agent loop at operator-turn
// boundaries when the baseline resets to TaintClean.
//
// Returns ErrSessionNotFound for unknown handles and
// ErrSessionTerminated for terminated ones.
//
// Setting from Tainted → Clean is allowed (operator turn reset);
// setting from Clean → Tainted is the typical ingestion-site
// transition.
func (s *Service) SetTaint(sessionID string, t Taint) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if st.terminated {
		return ErrSessionTerminated
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

// EvaluateWithTaint runs the policy + taint-aware overlay against
// req. The base evaluation comes from Policy.Evaluate; if the
// requesting session is Tainted, a stricter overlay may deny what
// a Clean evaluation would admit. Phase 6 ships the substrate;
// the only overlay rule today is: capability-grant requests for
// PurposeSubagent with elevated-capability roles (currently none
// — placeholder for phase 7's git-sub-agent grant) require a
// Clean context to admit without prompting.
//
// The CapabilityRequest's SessionID field, when set, names the
// requesting session whose taint state is read. When empty, the
// evaluation is taint-agnostic (same as Service.Evaluate).
//
// Returns Decision with Rule="tainted-deny" when the overlay
// fires, otherwise the underlying policy decision.
func (s *Service) EvaluateWithTaint(req CapabilityRequest) Decision {
	base := s.evaluate(req)
	if !base.Admit {
		s.logDecision(req, base)
		return base
	}
	if req.SessionID == "" {
		s.logDecision(req, base)
		return base
	}
	t, err := s.Taint(req.SessionID)
	if err != nil || t == TaintClean {
		s.logDecision(req, base)
		return base
	}
	// Tainted context overlay: stricter for sub-agent grants that
	// would carry elevated capabilities. Phase 7's socket-bearing
	// git-sub-agent grant is the canonical case; phase 6 ships the
	// detection framework but no concrete elevated roles trigger
	// yet (the git sub-agent role lands in phase 7).
	if req.Purpose == PurposeSubagent && isElevatedSubagentRole(req.Role) {
		denied := Decision{
			Admit:  false,
			Rule:   "tainted-deny:" + req.Role,
			Reason: fmt.Sprintf("broker: %s sub-agent grant requires a clean (un-tainted) context", req.Role),
		}
		s.logDecision(req, denied)
		return denied
	}
	s.logDecision(req, base)
	return base
}

// isElevatedSubagentRole returns true for sub-agent roles that
// receive privileges the main chat session can't hold (e.g. the
// ssh-agent socket for the git sub-agent role). Phase 7 will
// extend this list with the real role names.
func isElevatedSubagentRole(role string) bool {
	switch role {
	case "git-fetch", "git-sub-agent":
		// Reserved role names for phase 7; not yet wired anywhere
		// else but the substrate recognises them so the taint
		// overlay fires deterministically as soon as phase 7
		// starts using them.
		return true
	}
	return false
}
