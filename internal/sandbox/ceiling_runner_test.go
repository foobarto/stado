package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestCeilingRunner_EnforcesExecCeiling(t *testing.T) {
	tests := []struct {
		name    string
		ceiling Policy
		perCall Policy
		want    []string
	}{
		{
			name:    "deny-all ceiling is not mistaken for zero policy",
			ceiling: Policy{Exec: []string{}},
			perCall: Policy{},
			want:    []string{},
		},
		{
			name:    "omitted per-call exec inherits ceiling allowlist",
			ceiling: Policy{Exec: []string{"git"}},
			perCall: Policy{},
			want:    []string{"git"},
		},
		{
			name:    "omitted ceiling exec preserves per-call allowlist",
			ceiling: Policy{FSRead: []string{"/work"}},
			perCall: Policy{FSRead: []string{"/work"}, Exec: []string{"git"}},
			want:    []string{"git"},
		},
		{
			name:    "disjoint allowlists deny all",
			ceiling: Policy{Exec: []string{"git"}},
			perCall: Policy{Exec: []string{"go"}},
			want:    []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recordingRunner{available: true}
			wrapped := NewCeilingRunner(inner, tc.ceiling)
			if _, err := wrapped.Command(context.Background(), tc.perCall, "true", nil, nil); err != nil {
				t.Fatalf("Command: %v", err)
			}
			if tc.want != nil && inner.gotPolicy.Exec == nil {
				t.Fatalf("inner Exec = nil, want %#v", tc.want)
			}
			if strings.Join(inner.gotPolicy.Exec, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("inner Exec = %#v, want %#v", inner.gotPolicy.Exec, tc.want)
			}
		})
	}
}

// TestCeilingRunner_AppliesCeilingMask proves a ceiling mask reaches the inner
// policy even when the per-call policy does not repeat it.
func TestCeilingRunner_AppliesCeilingMask(t *testing.T) {
	inner := &recordingRunner{available: true}
	ceiling := Policy{
		FSRead: []string{"/work"},
		Mask:   []string{"/home/u/.ssh"},
	}
	wrapped := NewCeilingRunner(inner, ceiling)
	perCall := Policy{FSRead: []string{"/work"}}
	_, _ = wrapped.Command(context.Background(), perCall, "true", nil, nil)

	if !hasPath(inner.gotPolicy.Mask, "/home/u/.ssh") {
		t.Errorf("ceiling mask not applied to inner Policy: %v", inner.gotPolicy.Mask)
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

func TestCeilingRunnerRejectsCWDOutsideCeiling(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	inner := &recordingRunner{available: true}
	wrapped := NewCeilingRunner(inner, Policy{FSRead: []string{allowed}})
	if _, err := wrapped.Command(context.Background(), Policy{CWD: outside}, "true", nil, nil); err == nil {
		t.Fatal("out-of-ceiling CWD was accepted")
	}
	if inner.gotPolicy.CWD != "" {
		t.Fatalf("inner runner was called with %+v", inner.gotPolicy)
	}
}

func TestCeilingRunnerRejectsCWDSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	inner := &recordingRunner{available: true}
	wrapped := NewCeilingRunner(inner, Policy{FSRead: []string{allowed}})
	if _, err := wrapped.Command(context.Background(), Policy{CWD: escape}, "true", nil, nil); err == nil {
		t.Fatal("symlink CWD escape was accepted")
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
