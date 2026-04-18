// Package sandbox implements platform-abstracted policy enforcement for
// stado's tool runtime.
//
// Tools declare the capabilities they need; the platform Runner enforces them.
// PLAN.md §3.1–3.7 covers the design; Linux landlock/seccomp/bwrap, macOS
// sandbox-exec, Windows job objects (minimal v1).
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
	out.Exec = intersect(p.Exec, other.Exec)
	out.Env = intersect(p.Env, other.Env)

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
func DenyAll() Policy {
	return Policy{Net: NetPolicy{Kind: NetDenyAll}}
}

// ReadOnlyFS builds a read-only FS policy from the given read paths with
// net denied and no exec. Handy for query-class tools.
func ReadOnlyFS(readGlobs ...string) Policy {
	return Policy{
		FSRead: readGlobs,
		Net:    NetPolicy{Kind: NetDenyAll},
	}
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
