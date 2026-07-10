package broker

import (
	"crypto/rand"
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

// ErrSessionTerminated is returned when an operation is attempted
// against a SessionID that was minted but has since been
// terminated. Distinguished from ErrSessionNotFound so the
// orchestrator can surface "you already ended this" separately
// from "wrong handle".
var ErrSessionTerminated = errors.New("broker: session terminated")

// sessionState is the broker-internal record of a minted session.
// Held under sessions.mu. Not exposed across the IPC.
type sessionState struct {
	handle       SessionHandle
	terminated   bool
	terminatedAt time.Time
	// taint is the session's current provenance state (phase 6).
	// Mutated via Service.SetTaint; read by Service.EvaluateWithTaint
	// for capability-grant decisions that should refuse when the
	// requesting context is tainted.
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
	if !req.Purpose.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid purpose %q", req.Purpose)
	}
	if !req.Profile.Valid() {
		return SessionHandle{}, Decision{}, fmt.Errorf("broker: invalid profile %q", req.Profile)
	}
	var d Decision
	if req.Purpose == PurposeSubagent {
		if req.SessionID == "" {
			return SessionHandle{}, Decision{}, errors.New("broker: subagent parent session required")
		}
		parent, terminated, err := s.LookupSession(req.SessionID)
		if err != nil {
			return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent parent: %w", err)
		}
		if terminated {
			return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent parent: %w", ErrSessionTerminated)
		}
		if req.Profile != parent.Profile {
			return SessionHandle{}, Decision{}, fmt.Errorf("broker: subagent profile %q differs from parent profile %q", req.Profile, parent.Profile)
		}
		d = s.EvaluateWithTaint(req)
	} else {
		d = s.Evaluate(req)
	}
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

	s.sessionsMu.Lock()
	s.sessions[id] = &sessionState{handle: handle}
	s.sessionsMu.Unlock()

	return handle, d, nil
}

// sessionCeiling projects a broker-created child from the requesting
// parent's current effective set. The only path translation permitted is from
// the parent's checkout root to a validated stado-managed child worktree.
func (s *Service) sessionCeiling(req CapabilityRequest, sessionID string) (sandbox.Policy, string, error) {
	if req.Purpose != PurposeSubagent {
		return projectCeiling(req), req.CWD, nil
	}
	parent, terminated, err := s.LookupSession(req.SessionID)
	if err != nil {
		return sandbox.Policy{}, "", fmt.Errorf("broker: subagent parent: %w", err)
	}
	if terminated {
		return sandbox.Policy{}, "", fmt.Errorf("broker: subagent parent: %w", ErrSessionTerminated)
	}
	childCWD, err := reserveManagedWorktree(sessionID)
	if err != nil {
		return sandbox.Policy{}, "", err
	}
	parentCWD := parent.Effective.CWD
	writes := resolveRelativeScope(req.WriteScope, parentCWD)
	projected, _ := SubagentCeiling(parent.Effective, req.Role, req.Mode, writes)
	return rebasePolicyRoot(projected, parentCWD, childCWD), childCWD, nil
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
	st.terminatedAt = s.now()
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
