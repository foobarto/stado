package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/workdirpath"
)

// ErrSessionNotFound is returned by Terminate when the supplied
// SessionID was never minted (or was already terminated and
// purged). Surfaced as the JSON-RPC ErrCodeBrokerSessionNotFound.
var ErrSessionNotFound = errors.New("broker: session not found")

// ErrSessionController is returned when a native-only operation does not
// present the controller capability minted with the target session. A matching
// local UID authenticates the outer OS principal, not control of a particular
// session.
var ErrSessionController = errors.New("broker: session controller authentication failed")

// ErrSessionTerminated is returned when an operation is attempted
// against a SessionID that was minted but has since been
// terminated. Distinguished from ErrSessionNotFound so the
// orchestrator can surface "you already ended this" separately
// from "wrong handle".
var ErrSessionTerminated = errors.New("broker: session terminated")

// sessionState is the broker-internal record of a minted session.
// Held under sessions.mu. Not exposed across the IPC.
type sessionState struct {
	handle     SessionHandle
	controller [sha256.Size]byte
	// controllerVersion changes whenever a durable logical-session scope is
	// adopted. Opaque plugin bindings capture this value so rotating a native
	// controller invalidates every old binding without changing generation (and
	// therefore without abandoning the durable application journal).
	controllerVersion uint64
	terminated        bool
	terminatedAt      time.Time
	scope             sessionScopeState
	// Artifact scope is broker-derived when the session is admitted. A WASM
	// guest never supplies these values on artifact calls.
	principal  string
	repoID     string
	parentID   string
	role       string
	mode       string
	generation uint64
	// taint is the session's current coarse provenance marker (phase 6).
	// Mutated via Service.SetTaint and persisted for audit/status; it does not
	// alter capability admission.
	taint Taint
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

	// artifacts is nil until the daemon attaches its canonical WAL-backed
	// artifact authority. Keeping the authority on Service makes broker.v1 the
	// only production write path while unit tests that exercise policy alone
	// remain lightweight.
	artifacts *artifactBrokerState

	// sessionScopes is installed with the canonical broker WAL. Only logical
	// git sessions opt into this durable scope; short-lived tool/subagent/root
	// connection handles remain process-local.
	sessionScopes *sessionScopeStore
	// sessionLineage independently verifies exact git parent/turn refs before
	// the broker moves a durable logical scope to an automatic-recovery child.
	sessionLineage SessionLineageVerifier

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
	d := s.evaluate(req)
	s.logDecision(req, d)
	return d
}

func (s *Service) evaluate(req CapabilityRequest) Decision {
	p := s.loadedPolicy()
	if p == nil {
		return Decision{Admit: false, Rule: "no-policy", Reason: ErrPolicyNotLoaded.Error()}
	}
	return p.Evaluate(req)
}

// CreateSession admits or denies a session-creation request and, on
// admission, mints a SessionHandle. Phase 1: the ceiling is a coarse
// approximation derived from purpose+profile (phase 4 will tighten
// the projection from the full request shape).
func (s *Service) CreateSession(req CapabilityRequest) (SessionHandle, Decision, error) {
	return s.createSession(req, SessionAdoptionCredential{})
}

// CreateSessionForSubject creates a durable broker scope for one exact logical
// git-session subject. The subject is association metadata, not authority: the
// broker-issued adoption credential is what permits a later process to reopen
// the scope.
func (s *Service) CreateSessionForSubject(req CapabilityRequest, subject string) (SessionHandle, Decision, error) {
	repoID, err := canonicalArtifactRepoID(req.CWD)
	if err != nil || repoID == "" {
		return SessionHandle{}, Decision{}, ErrSessionScopeCredential
	}
	credential, _, err := s.reserveSessionScope("", localArtifactPrincipal(), subject, repoID)
	if err != nil {
		return SessionHandle{}, Decision{}, err
	}
	return s.createSession(req, credential)
}

// CreateSessionForCredential records an already-prestaged native recovery
// bearer. Pre-staging closes the first-create response-loss window: the broker
// still derives all authority and stores only digests, while a client crash
// cannot orphan a scope whose only plaintext bearer was in the lost reply.
func (s *Service) CreateSessionForCredential(req CapabilityRequest, credential SessionAdoptionCredential) (SessionHandle, Decision, error) {
	return s.createSession(req, credential)
}

func (s *Service) createSession(req CapabilityRequest, credential SessionAdoptionCredential) (SessionHandle, Decision, error) {
	subject := credential.Subject
	if !req.Purpose.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid purpose %q", req.Purpose)
	}
	if !req.Profile.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid profile %q", req.Profile)
	}
	if req.Purpose == PurposeSubagent {
		if subject != "" {
			return SessionHandle{}, Decision{}, errors.New("broker: subagent cannot own a logical-session subject")
		}
		return s.createSubagentSession(req)
	}
	if subject != "" && req.Purpose != PurposeMainChat {
		return SessionHandle{}, Decision{}, errors.New("broker: durable logical-session scope requires main-chat purpose")
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
	ceiling, cwd, err := s.sessionCeiling(req, id)
	if err != nil {
		return SessionHandle{}, d, err
	}
	handle := SessionHandle{
		SessionID: id,
		Purpose:   req.Purpose,
		Profile:   req.Profile,
		CWD:       cwd,
		Ceiling:   ceiling,
		Effective: ceiling, // phase 4: initialize equal; narrows via NarrowEffective.
		TraceRef:  traceRefFor(req, id),
		ExpiresAt: time.Time{}, // phase 1: no broker-enforced expiry
		CreatedAt: now,
	}

	repoID, err := canonicalArtifactRepoID(handle.CWD)
	if err != nil {
		return SessionHandle{}, d, fmt.Errorf("broker: canonical repository scope: %w", err)
	}
	controllerToken, controllerDigest, err := mintControllerToken()
	if err != nil {
		return SessionHandle{}, d, fmt.Errorf("broker: mint session controller: %w", err)
	}
	handle.controllerToken = controllerToken
	storedHandle := handle
	storedHandle.controllerToken = ""
	state := &sessionState{
		handle: storedHandle, controller: controllerDigest, controllerVersion: 1,
		principal: localArtifactPrincipal(), repoID: repoID, generation: 1,
	}
	if subject != "" {
		if err := s.prepareDurableSessionScope(state, credential); err != nil {
			return SessionHandle{}, d, err
		}
		handle.subject = subject
		handle.adoptionTicket = state.scope.ticket
		handle.resumeSecret = state.scope.resumeSecret
		// The broker needs only digests after the one-shot response is built.
		state.scope.ticket = ""
		state.scope.resumeSecret = ""
		state.scope.reservationParentID = req.SessionID
	}

	s.sessionsMu.Lock()
	if state.scope.durable {
		if err := s.persistNewSessionScopeLocked(state); err != nil {
			s.sessionsMu.Unlock()
			return SessionHandle{}, d, err
		}
	}
	s.sessions[id] = state
	s.sessionsMu.Unlock()

	return handle, d, nil
}

func (s *Service) createSubagentSession(req CapabilityRequest) (SessionHandle, Decision, error) {
	if req.SessionID == "" {
		return SessionHandle{}, Decision{}, errors.New("broker: subagent parent session required")
	}
	id, err := mintSessionID()
	if err != nil {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: mint session id: %w", err)
	}
	base := s.evaluate(req)
	if !base.Admit {
		s.logDecision(req, base)
		return SessionHandle{}, base, nil
	}

	s.sessionsMu.Lock()
	parentState, ok := s.sessions[req.SessionID]
	if !ok {
		s.sessionsMu.Unlock()
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent parent: %w", ErrSessionNotFound)
	}
	if parentState.terminated {
		s.sessionsMu.Unlock()
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent parent: %w", ErrSessionTerminated)
	}
	parent := parentState.handle
	if req.Profile != parent.Profile {
		s.sessionsMu.Unlock()
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent profile %q differs from parent profile %q", req.Profile, parent.Profile)
	}
	d := base
	if !d.Admit {
		s.sessionsMu.Unlock()
		s.logDecision(req, d)
		return SessionHandle{}, d, nil
	}

	childCWD, err := reserveManagedWorktree(id)
	if err != nil {
		s.sessionsMu.Unlock()
		s.logDecision(req, d)
		return SessionHandle{}, d, err
	}
	parentCWD := parent.Effective.CWD
	writes := resolveRelativeScope(req.WriteScope, parentCWD)
	projected, _ := SubagentCeiling(parent.Effective, req.Role, req.Mode, writes)
	ceiling := rebasePolicyRoot(projected, parentCWD, childCWD)
	handle := SessionHandle{
		SessionID: id,
		Purpose:   req.Purpose,
		Profile:   req.Profile,
		CWD:       childCWD,
		Ceiling:   ceiling,
		Effective: ceiling,
		TraceRef:  traceRefFor(req, id),
		ExpiresAt: time.Time{},
		CreatedAt: s.now(),
	}
	controllerToken, controllerDigest, err := mintControllerToken()
	if err != nil {
		s.sessionsMu.Unlock()
		s.logDecision(req, d)
		return SessionHandle{}, d, fmt.Errorf("broker: mint session controller: %w", err)
	}
	handle.controllerToken = controllerToken
	storedHandle := handle
	storedHandle.controllerToken = ""
	s.sessions[id] = &sessionState{
		handle: storedHandle, controller: controllerDigest, controllerVersion: 1,
		principal: parentState.principal, repoID: parentState.repoID,
		parentID: parent.SessionID, role: req.Role, mode: req.Mode, generation: 1,
	}
	s.sessionsMu.Unlock()
	s.logDecision(req, d)
	return handle, d, nil
}

// sessionCeiling projects a broker-created child from the requesting
// parent's current effective set. The only path translation permitted is from
// the parent's checkout root to a validated stado-managed child worktree.
func (s *Service) sessionCeiling(req CapabilityRequest, sessionID string) (sandbox.Policy, string, error) {
	return projectCeiling(req), req.CWD, nil
}

func managedWorktreeRoot() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("broker: subagent worktree root: %w", err)
		}
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "stado", "worktrees"), nil
}

func reserveManagedWorktree(sessionID string) (string, error) {
	root, err := managedWorktreeRoot()
	if err != nil {
		return "", err
	}
	resolver := workdirpath.NewUserConfigResolver()
	if err := resolver.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("broker: reserve subagent worktree root: %w", err)
	}
	rootHandle, err := resolver.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("broker: open subagent worktree root: %w", err)
	}
	defer func() { _ = rootHandle.Close() }()
	if err := rootHandle.Mkdir(sessionID, 0o700); err != nil {
		return "", fmt.Errorf("broker: reserve subagent worktree: %w", err)
	}
	return filepath.Join(root, sessionID), nil
}

func rebasePolicyRoot(p sandbox.Policy, from, to string) sandbox.Policy {
	p.FSRead = rebasePaths(p.FSRead, from, to)
	p.FSWrite = rebasePaths(p.FSWrite, from, to)
	p.Mask = rebasePaths(p.Mask, from, to)
	p.CWD = to
	return p
}

func rebasePaths(paths []string, from, to string) []string {
	out := append([]string(nil), paths...)
	for i, path := range out {
		if path == from || isSubpath(from, path) {
			out[i] = rebasePath(path, from, to)
		}
	}
	return out
}

func rebasePath(path, from, to string) string {
	rel, err := filepath.Rel(from, path)
	if err != nil || rel == "." {
		return to
	}
	return filepath.Join(to, rel)
}

func isSubpath(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TerminateSession marks the named session terminated. Returns
// ErrSessionNotFound if the SessionID was never minted, or
// ErrSessionTerminated if it was already terminated. Idempotent
// callers should ignore ErrSessionTerminated.
func (s *Service) TerminateSession(sessionID, controllerToken string) error {
	s.sessionsMu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.sessionsMu.Unlock()
		return ErrSessionNotFound
	}
	if st.terminated {
		s.sessionsMu.Unlock()
		return ErrSessionTerminated
	}
	if err := authenticateControllerLocked(st, controllerToken); err != nil {
		s.sessionsMu.Unlock()
		return err
	}
	terminatedAt := s.now()
	if st.scope.durable {
		if err := s.persistSessionScopeTransitionLocked(st, sessionScopeTerminated, terminatedAt); err != nil {
			s.sessionsMu.Unlock()
			return err
		}
	}
	st.terminated = true
	st.terminatedAt = terminatedAt
	st.controller = [sha256.Size]byte{}
	if st.scope.durable {
		st.scope.status = sessionScopeTerminated
		st.scope.version++
		st.scope.leaseUntil = time.Time{}
	}
	s.invalidateSessionBindingsLocked(sessionID)
	s.sessionsMu.Unlock()
	return nil
}

// invalidateSessionBindingsLocked removes native-held opaque capabilities for
// one broker session. Callers hold sessionsMu; bind paths take locks in the
// same sessions -> artifacts order.
func (s *Service) invalidateSessionBindingsLocked(sessionID string) {
	if s.artifacts == nil {
		return
	}
	s.artifacts.mu.Lock()
	defer s.artifacts.mu.Unlock()
	for token, binding := range s.artifacts.bindings {
		if binding.sessionID == sessionID {
			delete(s.artifacts.bindings, token)
		}
	}
	for key := range s.artifacts.lifecycleBindings {
		if key.sessionID == sessionID {
			delete(s.artifacts.lifecycleBindings, key)
		}
	}
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

// mintControllerToken returns a 256-bit bearer and the only representation the
// broker retains. The plaintext token is delivered once in session.create and
// must remain in the native controller; it is never an operator-origin grant.
func mintControllerToken() (string, [sha256.Size]byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	token := "controller_" + hex.EncodeToString(random[:])
	return token, sha256.Sum256([]byte(token)), nil
}

// authenticateControllerLocked validates a session-scoped native capability.
// The caller must hold sessionsMu for reading or writing. Constant-time digest
// comparison avoids turning the broker into a token-prefix oracle.
func authenticateControllerLocked(session *sessionState, token string) error {
	if session == nil || token == "" {
		return ErrSessionController
	}
	digest := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(digest[:], session.controller[:]) != 1 {
		return ErrSessionController
	}
	return nil
}

func (s *Service) authenticateSessionController(sessionID, token string) error {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if session.terminated {
		return ErrSessionTerminated
	}
	return authenticateControllerLocked(session, token)
}

// projectCeiling produces a sandbox.Policy from the mount-and-
// namespace invariant table for the requested profile (see
// mount_table.go), narrowed by the role/mode/write_scope for
// sub-agent requests.
//
// ProfileNoSandbox returns an empty Policy — the runtime picks
// NoneRunner and no namespace isolation is applied. The broker
// has still admitted the request via Service.Evaluate, so the
// decision is captured in the broker-decision log.
//
// Direct callers use the profile baseline. Broker-created subagents go through
// Service.sessionCeiling, which projects from the actual parent effective set.
func projectCeiling(req CapabilityRequest) sandbox.Policy {
	if req.Profile == ProfileNoSandbox {
		return sandbox.Policy{CWD: req.CWD}
	}
	base := MountTableFor(req.Profile, req.CWD).ToPolicy()
	base.CWD = req.CWD
	if req.Purpose == PurposeSubagent {
		// Resolve write_scope entries against req.CWD before
		// projecting. The spawn_agent contract makes write_scope
		// repo-relative ("src/foo") but the parent ceiling's
		// FSWrite is absolute (/work). Without this resolution
		// step normal worker scopes would always be dropped —
		// Codex P2 review of PR #71.
		writes := resolveRelativeScope(req.WriteScope, req.CWD)
		child, _ := SubagentCeiling(base, req.Role, req.Mode, writes)
		return child
	}
	return base
}

// resolveRelativeScope turns each repo-relative entry in scope
// into an absolute path joined against cwd. Already-absolute
// entries pass through cleaned. Empty cwd means we can't resolve
// — relative entries pass through as-is (they'll be dropped by
// anyParentCovers, which is the right failure mode: refuse,
// don't widen).
func resolveRelativeScope(scope []string, cwd string) []string {
	if len(scope) == 0 {
		return nil
	}
	out := make([]string, 0, len(scope))
	for _, s := range scope {
		if s == "" {
			continue
		}
		if filepath.IsAbs(s) {
			out = append(out, filepath.Clean(s))
			continue
		}
		if cwd == "" {
			out = append(out, filepath.Clean(s))
			continue
		}
		out = append(out, filepath.Clean(filepath.Join(cwd, s)))
	}
	return out
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
