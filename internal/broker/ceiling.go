package broker

// ceiling.go — phase 4 implementation of the ceiling/effective-set
// vocabulary. The ceiling is the immutable maximum a session may
// ever hold; the effective set is what it currently holds. The
// effective set may only narrow during a session — widening
// requires forking a new session. See DESIGN.md §"Sessions and
// sub-agents" → "Capability ceiling and effective set".

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/sandbox"
)

// ErrEffectiveWiderThanCeiling is returned by NarrowEffective when
// the proposed new effective set is not a subset of the existing
// effective set (i.e. it would widen, not narrow).
var ErrEffectiveWiderThanCeiling = errors.New("broker: proposed effective set would widen, not narrow — fork a new session instead")

// SubagentCeiling projects a sub-agent's ceiling from the parent
// session's ceiling + the spawn request's role / mode / write_scope.
// The result is GUARANTEED to be a (non-strict) subset of the
// parent ceiling — "capability never escalates along the spawn
// tree", DESIGN.md §"Sessions and sub-agents".
//
// Phase 4 rules:
//
//   - role=explorer (default) + mode=read_only (default) → child
//     ceiling has FSWrite=[] (no writes). Reads inherit parent.
//   - role=worker + mode=workspace_write + write_scope=[paths] →
//     child ceiling FSWrite is the intersection of (paths under
//     parent's FSWrite) and parent's FSWrite. Paths outside the
//     parent's writable set are dropped silently with a note in
//     the dropped slice the caller can surface.
//   - Other combinations: treat as read-only (most conservative).
//
// The function never widens. If parent has FSWrite=[A] and the
// spawn requests write_scope=[A, B] where B is outside A, the
// child gets FSWrite=[A] only.
//
// Returns the projected child ceiling + a list of dropped paths
// (those the request asked for but the parent didn't permit).
func SubagentCeiling(parent sandbox.Policy, role, mode string, writeScope []string) (sandbox.Policy, []string) {
	child := sandbox.Policy{
		// Reads always attenuate to parent's read set; the child
		// can only read what the parent could read.
		FSRead:  append([]string(nil), parent.FSRead...),
		Net:     parent.Net,
		Exec:    append([]string(nil), parent.Exec...),
		Env:     withoutString(parent.Env, "SSH_AUTH_SOCK"),
		CWD:     parent.CWD,
		Timeout: parent.Timeout,
		Mask:    append([]string(nil), parent.Mask...),
	}

	// Default (and read-only roles) get no writes.
	if role != "worker" || mode != "workspace_write" {
		return child, nil
	}

	// worker + workspace_write: intersect write_scope with parent
	// writable set.
	var allowed, dropped []string
	for _, requested := range writeScope {
		req := filepath.Clean(requested)
		if anyParentCovers(parent.FSWrite, req) {
			allowed = append(allowed, req)
		} else {
			dropped = append(dropped, req)
		}
	}
	child.FSWrite = allowed
	return child, dropped
}

func withoutString(values []string, drop string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}

// anyParentCovers reports whether requested is the same as, or a
// subpath of, any path in parentPaths. Used to validate sub-agent
// write_scope requests against the parent's writable set.
func anyParentCovers(parentPaths []string, requested string) bool {
	requested = filepath.Clean(requested)
	for _, parent := range parentPaths {
		parent = filepath.Clean(parent)
		if requested == parent {
			return true
		}
		// requested must start with parent + "/" to count as a
		// subpath (so /workfoo is NOT a subpath of /work).
		if strings.HasPrefix(requested, parent+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// IsSubsetOf reports whether candidate is a (non-strict) subset of
// reference, treating each Policy field independently:
//
//   - FSRead/FSWrite/Exec/Env: every element of candidate must be
//     covered by some element of reference (same path or a parent
//     path in the FS cases; exact match for Exec/Env).
//   - Net: candidate's NetPolicy must be at-or-below reference's
//     (DenyAll < AllowHosts < AllowAll; AllowHosts must be a
//     subset of reference's host set when both are AllowHosts).
//   - CWD: ignored here; CeilingRunner separately resolves it and requires it
//     to be covered by the ceiling's readable/writable paths before mounting.
//   - Timeout: ignored.
//
// Used by NarrowEffective to validate the narrow-only invariant.
func IsSubsetOf(candidate, reference sandbox.Policy) bool {
	if !fsSubset(candidate.FSRead, reference.FSRead) {
		return false
	}
	if !fsSubset(candidate.FSWrite, reference.FSWrite) {
		return false
	}
	if !exactSubset(candidate.Exec, reference.Exec) {
		return false
	}
	if !exactSubset(candidate.Env, reference.Env) {
		return false
	}
	if !netSubset(candidate.Net, reference.Net) {
		return false
	}
	if !exactSubset(candidate.Sockets, reference.Sockets) {
		return false
	}
	// Masks restrict access, so a child must retain every parent mask.
	if !exactSubset(reference.Mask, candidate.Mask) {
		return false
	}
	if reference.Timeout > 0 && (candidate.Timeout == 0 || candidate.Timeout > reference.Timeout) {
		return false
	}
	return true
}

// fsSubset reports whether every path in candidate is the same as,
// or a subpath of, some path in reference.
func fsSubset(candidate, reference []string) bool {
	for _, c := range candidate {
		if !anyParentCovers(reference, c) {
			return false
		}
	}
	return true
}

// exactSubset reports whether every element of candidate appears
// in reference. Used for fields with no path-prefix semantics
// (Exec binary names; Env variable names).
func exactSubset(candidate, reference []string) bool {
	if len(candidate) == 0 {
		return true
	}
	ref := make(map[string]struct{}, len(reference))
	for _, r := range reference {
		ref[r] = struct{}{}
	}
	for _, c := range candidate {
		if _, ok := ref[c]; !ok {
			return false
		}
	}
	return true
}

// netSubset reports whether candidate Net is at-or-below reference
// Net. NetDenyAll is the floor; NetAllowAll is the ceiling;
// NetAllowHosts is between, and a candidate AllowHosts must be a
// subset of reference AllowHosts when both are AllowHosts.
func netSubset(candidate, reference sandbox.NetPolicy) bool {
	if candidate.Kind == sandbox.NetDenyAll {
		return true // always at-or-below
	}
	if reference.Kind == sandbox.NetAllowAll {
		return true // reference permits everything
	}
	if candidate.Kind == sandbox.NetAllowAll && reference.Kind != sandbox.NetAllowAll {
		return false // candidate would widen
	}
	if candidate.Kind == sandbox.NetAllowHosts && reference.Kind == sandbox.NetAllowHosts {
		refHosts := make(map[string]struct{}, len(reference.Hosts))
		for _, h := range reference.Hosts {
			refHosts[h] = struct{}{}
		}
		for _, h := range candidate.Hosts {
			if _, ok := refHosts[h]; !ok {
				return false
			}
		}
		return true
	}
	// candidate is NetAllowHosts but reference is NetDenyAll →
	// candidate would widen.
	return false
}

// NarrowEffective narrows the session's effective capability set
// to narrowed. Returns ErrEffectiveWiderThanCeiling if narrowed
// is not a subset of the current effective set. Returns
// ErrSessionNotFound / ErrSessionTerminated for invalid handles.
// The ceiling itself is never modified.
func (s *Service) NarrowEffective(sessionID string, narrowed sandbox.Policy) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if st.terminated {
		return ErrSessionTerminated
	}
	// Restriction-only fields are sticky. Callers narrowing a capability set
	// commonly specify only the allow dimensions; omission must not remove a
	// parent mask or timeout and accidentally turn a partial update into a
	// widening request.
	narrowed.Mask = appendMissing(narrowed.Mask, st.handle.Effective.Mask)
	if narrowed.Timeout == 0 {
		narrowed.Timeout = st.handle.Effective.Timeout
	}
	if !IsSubsetOf(narrowed, st.handle.Effective) {
		return fmt.Errorf("%w (session %s)", ErrEffectiveWiderThanCeiling, sessionID)
	}
	st.handle.Effective = narrowed
	return nil
}

func appendMissing(dst, required []string) []string {
	out := append([]string(nil), dst...)
	seen := make(map[string]struct{}, len(out))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := seen[value]; ok {
			continue
		}
		out = append(out, value)
		seen[value] = struct{}{}
	}
	return out
}
