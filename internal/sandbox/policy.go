// Package sandbox implements platform-abstracted policy enforcement for
// stado's tool runtime.
//
// Tools declare the capabilities they need; Linux runners enforce them.
package sandbox

import (
	"fmt"
	"strings"
	"time"
)

// Policy is the capability manifest a tool invocation runs under.
//
// FS glob/prefix syntax: leading "~" is expanded to the user home; trailing
// "/..." means "this dir and everything beneath"; a bare path is exact.
// Empty slices deny all access in that dimension.
type Policy struct {
	FSRead  []string
	FSWrite []string
	Net     NetPolicy
	Exec    []string // binary names allowed; unqualified names resolved via PATH
	Env     []string // environment var names to pass through (NONE otherwise)
	CWD     string   // required working directory; "" = inherit
	Timeout time.Duration

	// Mask names directories/files to render UNREADABLE inside the
	// sandbox even when an ancestor was bound RO (e.g. $HOME bound RO,
	// but the .ssh key dir must not be exfiltratable). The Linux runner
	// shadows each Mask path with an empty --tmpfs placed AFTER the
	// FSRead binds; safe files inside it (known_hosts, ssh config) are
	// re-bound on top via FSRead. Merge UNIONs Mask: masking is a restriction, so the
	// combined policy hides everything either side wants hidden.
	Mask []string

	// Sockets names host unix-socket paths to bind read-write into the
	// sandbox (e.g. $SSH_AUTH_SOCK for ssh-agent forwarding). Only the
	// socket crosses the boundary — never key bytes. The runner emits
	// --bind <sock> <sock>. Merge INTERSECTS Sockets: a
	// bind is an allow, so only sockets both sides grant survive.
	Sockets []string
}

// NetPolicy describes outgoing network access.
//
//	Kind=DenyAll       → no network
//	Kind=AllowHosts    → Hosts entries; hostnames or CIDRs
//	Kind=AllowAll      → unrestricted (discouraged; emits warning)
type NetPolicy struct {
	Kind  NetKind
	Hosts []string
}

type NetKind int

const (
	NetDenyAll NetKind = iota
	NetAllowHosts
	NetAllowAll
)

// Merge returns a policy that's the intersection of p and other in the
// restrictive fields (FSRead/FSWrite/Exec/Env) — call site is outer,
// argument is inner. Net downgrades to the stricter of the two; Timeout
// takes the shorter positive value.
func (p Policy) Merge(other Policy) Policy {
	out := p
	out.FSRead = intersect(p.FSRead, other.FSRead)
	out.FSWrite = intersect(p.FSWrite, other.FSWrite)
	out.Exec = intersectExec(p.Exec, other.Exec)
	out.Env = intersect(p.Env, other.Env)
	// Mask is a restriction: hide everything either side wants hidden
	// (union, more-restrictive combine). Sockets is an allow: only
	// sockets both sides grant survive (intersect, like FSRead/Env).
	out.Mask = union(p.Mask, other.Mask)
	out.Sockets = intersect(p.Sockets, other.Sockets)

	// Network: stricter Kind wins.
	switch {
	case p.Net.Kind == NetDenyAll || other.Net.Kind == NetDenyAll:
		out.Net = NetPolicy{Kind: NetDenyAll}
	case p.Net.Kind == NetAllowHosts || other.Net.Kind == NetAllowHosts:
		// Intersect host lists; unrestricted side inherits the other's list.
		hosts := p.Net.Hosts
		if p.Net.Kind == NetAllowAll {
			hosts = other.Net.Hosts
		} else if other.Net.Kind == NetAllowHosts {
			hosts = intersect(p.Net.Hosts, other.Net.Hosts)
		}
		out.Net = NetPolicy{Kind: NetAllowHosts, Hosts: hosts}
	default:
		out.Net = NetPolicy{Kind: NetAllowAll}
	}

	if other.Timeout > 0 && (p.Timeout == 0 || other.Timeout < p.Timeout) {
		out.Timeout = other.Timeout
	}
	return out
}

// Describe renders a short human-readable summary of the policy for logs /
// approval prompts.
func (p Policy) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fs_read=%d fs_write=%d exec=%d env=%d net=%s",
		len(p.FSRead), len(p.FSWrite), len(p.Exec), len(p.Env), p.Net.Describe())
	if len(p.Mask) > 0 {
		fmt.Fprintf(&b, " mask=%d", len(p.Mask))
	}
	if len(p.Sockets) > 0 {
		fmt.Fprintf(&b, " sockets=%d", len(p.Sockets))
	}
	if p.Timeout > 0 {
		fmt.Fprintf(&b, " timeout=%s", p.Timeout)
	}
	return b.String()
}

func (n NetPolicy) Describe() string {
	switch n.Kind {
	case NetDenyAll:
		return "deny"
	case NetAllowAll:
		return "allow-all"
	case NetAllowHosts:
		return fmt.Sprintf("allow(%d)", len(n.Hosts))
	}
	return "?"
}

// DenyAll returns the most restrictive policy — no FS, no net, no exec.
// Useful as the default when a tool doesn't declare one.
//
// The empty-slice Exec is load-bearing: [ResolveBinary] treats Exec=nil
// as "no policy" (allow any binary on PATH) and Exec=[] as "explicit
// deny-all". Leaving Exec at the zero value would make this constructor
// silently allow every binary — the opposite of its name. The test
// `TestDenyAll_DeniesExec` pins this invariant against future
// refactors.
func DenyAll() Policy {
	return Policy{
		Net:  NetPolicy{Kind: NetDenyAll},
		Exec: []string{},
	}
}

// ReadOnlyFS builds a read-only FS policy from the given read paths with
// net denied and no exec. Handy for query-class tools.
//
// Same Exec-must-be-empty-slice invariant as [DenyAll]; see that
// constructor's comment for why the zero value would be wrong here.
func ReadOnlyFS(readGlobs ...string) Policy {
	return Policy{
		FSRead: readGlobs,
		Net:    NetPolicy{Kind: NetDenyAll},
		Exec:   []string{},
	}
}

// WorktreeWrite returns a Policy that allows reading anywhere on the
// filesystem but only writing inside `worktree` (and /tmp, which many tools
// need for scratch files). Callers that maintain runtime-owned audit state
// must add its exact paths before applying this process-wide policy.
//
// Net left unset — callers layer NetPolicy themselves.
func WorktreeWrite(worktree string) Policy {
	return Policy{
		FSRead:  []string{"/"},
		FSWrite: []string{worktree, "/tmp"},
	}
}

// union returns the deduplicated combination of a and b, preserving
// the order in which entries first appear (a's entries first, then b's
// new ones). Used by Merge for restriction-class fields (Mask) where the
// safe combine is "everything either side wants."
func union(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func intersect(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	if len(b) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, s := range b {
		seen[s] = true
	}
	var out []string
	for _, s := range a {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

// intersectExec combines optional executable allowlists. Exec=nil is the
// unrestricted top value, while a non-nil empty slice is deny-all.
func intersectExec(a, b []string) []string {
	if a == nil {
		return cloneOptionalStrings(b)
	}
	if b == nil {
		return cloneOptionalStrings(a)
	}
	if len(a) == 0 || len(b) == 0 {
		return []string{}
	}
	out := intersect(a, b)
	if out == nil {
		return []string{}
	}
	return out
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
