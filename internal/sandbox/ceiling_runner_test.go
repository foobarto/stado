package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// recordingRunner captures the Policy + cmd it was asked to build,
// returning nil cmd (the test doesn't exec anything; it only cares
// about what Policy got handed down).
type recordingRunner struct {
	gotPolicy Policy
	gotCmd    string
	gotArgs   []string
	available bool
}

func (r *recordingRunner) Name() string    { return "recording" }
func (r *recordingRunner) Available() bool { return r.available }
func (r *recordingRunner) Command(_ context.Context, p Policy, name string, args, _ []string) (*exec.Cmd, error) {
	r.gotPolicy = p
	r.gotCmd = name
	r.gotArgs = args
	return nil, nil
}

func TestCeilingRunner_IntersectsWithCeiling(t *testing.T) {
	inner := &recordingRunner{available: true}
	ceiling := Policy{
		FSRead:  []string{"/work", "/tmp"},
		FSWrite: []string{"/work"},
	}
	wrapped := NewCeilingRunner(inner, ceiling)
	perCall := Policy{
		FSRead:  []string{"/work", "/etc", "/tmp"},
		FSWrite: []string{"/work", "/etc"},
	}
	_, _ = wrapped.Command(context.Background(), perCall, "true", nil, nil)

	// Inner saw the intersection: no /etc in either set.
	if hasPath(inner.gotPolicy.FSRead, "/etc") {
		t.Errorf("inner Policy FSRead leaked /etc: %v", inner.gotPolicy.FSRead)
	}
	if hasPath(inner.gotPolicy.FSWrite, "/etc") {
		t.Errorf("inner Policy FSWrite leaked /etc: %v", inner.gotPolicy.FSWrite)
	}
	if !hasPath(inner.gotPolicy.FSRead, "/work") || !hasPath(inner.gotPolicy.FSRead, "/tmp") {
		t.Errorf("inner Policy FSRead missing legitimate paths: %v", inner.gotPolicy.FSRead)
	}
	if !hasPath(inner.gotPolicy.FSWrite, "/work") {
		t.Errorf("inner Policy FSWrite missing /work: %v", inner.gotPolicy.FSWrite)
	}
}

func TestCeilingRunner_EmptyCeilingPassesThrough(t *testing.T) {
	// A zero-value Policy ceiling means "no constraints" — the
	// CeilingRunner should pass the per-call Policy through
	// unchanged (no intersection that would silently zero out).
	inner := &recordingRunner{available: true}
	wrapped := NewCeilingRunner(inner, Policy{})
	perCall := Policy{
		FSRead:  []string{"/work"},
		FSWrite: []string{"/work"},
	}
	_, _ = wrapped.Command(context.Background(), perCall, "true", nil, nil)
	if len(inner.gotPolicy.FSRead) != 1 || inner.gotPolicy.FSRead[0] != "/work" {
		t.Errorf("zero ceiling should pass through FSRead unchanged; got %v", inner.gotPolicy.FSRead)
	}
	if len(inner.gotPolicy.FSWrite) != 1 || inner.gotPolicy.FSWrite[0] != "/work" {
		t.Errorf("zero ceiling should pass through FSWrite unchanged; got %v", inner.gotPolicy.FSWrite)
	}
}

func TestCeilingRunner_NilInnerUsesNoneRunner(t *testing.T) {
	wrapped := NewCeilingRunner(nil, Policy{FSRead: []string{"/work"}})
	if wrapped.Name() != "none+ceiling" {
		t.Errorf("Name = %q, want none+ceiling", wrapped.Name())
	}
	// Calling Command should not panic. The inner NoneRunner just
	// builds a plain exec.Cmd; we ignore the returned Cmd here.
	cmd, err := wrapped.Command(context.Background(), Policy{}, "/bin/true", nil, nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd == nil {
		t.Errorf("NoneRunner.Command returned nil cmd")
	}
}

func TestCeilingRunner_Name(t *testing.T) {
	inner := &recordingRunner{available: true}
	wrapped := NewCeilingRunner(inner, Policy{FSRead: []string{"/w"}})
	if !strings.HasSuffix(wrapped.Name(), "+ceiling") {
		t.Errorf("Name = %q, want '+ceiling' suffix", wrapped.Name())
	}
	if !strings.HasPrefix(wrapped.Name(), "recording") {
		t.Errorf("Name = %q, want 'recording' prefix", wrapped.Name())
	}
}

func TestCeilingRunner_Available(t *testing.T) {
	avail := &recordingRunner{available: true}
	wrappedA := NewCeilingRunner(avail, Policy{})
	if !wrappedA.Available() {
		t.Errorf("Available should mirror inner (true)")
	}
	unavail := &recordingRunner{available: false}
	wrappedU := NewCeilingRunner(unavail, Policy{})
	if wrappedU.Available() {
		t.Errorf("Available should mirror inner (false)")
	}
}

func TestCeilingRunner_NetDenyAllAlwaysWins(t *testing.T) {
	inner := &recordingRunner{available: true}
	ceiling := Policy{
		FSRead: []string{"/work"},
		Net:    NetPolicy{Kind: NetDenyAll},
	}
	wrapped := NewCeilingRunner(inner, ceiling)
	perCall := Policy{
		FSRead: []string{"/work"},
		Net:    NetPolicy{Kind: NetAllowAll},
	}
	_, _ = wrapped.Command(context.Background(), perCall, "true", nil, nil)
	if inner.gotPolicy.Net.Kind != NetDenyAll {
		t.Errorf("ceiling NetDenyAll should win; got %v", inner.gotPolicy.Net.Kind)
	}
}

func TestCeilingRunner_NetAllowHostsIntersects(t *testing.T) {
	inner := &recordingRunner{available: true}
	ceiling := Policy{
		FSRead: []string{"/work"},
		Net:    NetPolicy{Kind: NetAllowHosts, Hosts: []string{"a", "b"}},
	}
	wrapped := NewCeilingRunner(inner, ceiling)
	perCall := Policy{
		FSRead: []string{"/work"},
		Net:    NetPolicy{Kind: NetAllowHosts, Hosts: []string{"a", "c"}},
	}
	_, _ = wrapped.Command(context.Background(), perCall, "true", nil, nil)
	if inner.gotPolicy.Net.Kind != NetAllowHosts {
		t.Errorf("Net.Kind = %v, want NetAllowHosts", inner.gotPolicy.Net.Kind)
	}
	if len(inner.gotPolicy.Net.Hosts) != 1 || inner.gotPolicy.Net.Hosts[0] != "a" {
		t.Errorf("Net.Hosts = %v, want [a]", inner.gotPolicy.Net.Hosts)
	}
}

// hasPath reports whether paths contains p (exact match).
func hasPath(paths []string, p string) bool {
	for _, q := range paths {
		if q == p {
			return true
		}
	}
	return false
}
