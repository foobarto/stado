package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicyMerge_FS(t *testing.T) {
	outer := Policy{FSRead: []string{"/a", "/b", "/c"}}
	inner := Policy{FSRead: []string{"/b", "/c", "/d"}}
	merged := outer.Merge(inner)
	if len(merged.FSRead) != 2 {
		t.Fatalf("FSRead intersection = %v, want 2 entries", merged.FSRead)
	}
}

func TestPolicyMerge_ExecNilEmptySemantics(t *testing.T) {
	tests := []struct {
		name       string
		outer      []string
		inner      []string
		want       []string
		wantNonNil bool
	}{
		{name: "unrestricted outer inherits allowlist", outer: nil, inner: []string{"git"}, want: []string{"git"}, wantNonNil: true},
		{name: "unrestricted inner preserves allowlist", outer: []string{"git"}, inner: nil, want: []string{"git"}, wantNonNil: true},
		{name: "explicit deny-all wins", outer: []string{}, inner: []string{"git"}, want: []string{}, wantNonNil: true},
		{name: "disjoint allowlists deny all", outer: []string{"git"}, inner: []string{"go"}, want: []string{}, wantNonNil: true},
		{name: "both unrestricted remain unrestricted", outer: nil, inner: nil, want: nil, wantNonNil: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := (Policy{Exec: tc.outer}).Merge(Policy{Exec: tc.inner}).Exec
			if tc.wantNonNil && got == nil {
				t.Fatalf("Exec = nil, want %#v", tc.want)
			}
			if !tc.wantNonNil && got != nil {
				t.Fatalf("Exec = %#v, want nil", got)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("Exec = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestPolicyMerge_Mask pins the union-combine semantics for Mask:
// masking is a RESTRICTION (render a path unreadable), so combining two
// policies must hide everything EITHER side wants hidden. Union is the
// safe (more-restrictive) combine — the inverse of FSRead's intersection
// but consistent with "stricter wins." Dedupes so a path masked by both
// sides appears once.
func TestPolicyMerge_Mask(t *testing.T) {
	outer := Policy{Mask: []string{"/home/u/.ssh", "/home/u/.aws"}}
	inner := Policy{Mask: []string{"/home/u/.ssh", "/home/u/.gcp"}}
	merged := outer.Merge(inner)
	want := map[string]bool{"/home/u/.ssh": true, "/home/u/.aws": true, "/home/u/.gcp": true}
	if len(merged.Mask) != len(want) {
		t.Fatalf("Mask union = %v, want %d distinct entries", merged.Mask, len(want))
	}
	for _, m := range merged.Mask {
		if !want[m] {
			t.Errorf("unexpected Mask entry %q", m)
		}
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("Mask union missing entries: %v", want)
	}
}

// TestPolicyMerge_Sockets pins the intersect-combine semantics for
// Sockets: a socket bind is an ALLOW (grant the guest a host socket), so
// only sockets BOTH sides grant survive the merge — consistent with
// FSRead/Exec/Env/Net-hosts intersection (inner narrows outer).
func TestPolicyMerge_Sockets(t *testing.T) {
	outer := Policy{Sockets: []string{"/run/a.sock", "/run/b.sock"}}
	inner := Policy{Sockets: []string{"/run/b.sock", "/run/c.sock"}}
	merged := outer.Merge(inner)
	if len(merged.Sockets) != 1 || merged.Sockets[0] != "/run/b.sock" {
		t.Fatalf("Sockets intersection = %v, want [/run/b.sock]", merged.Sockets)
	}
}

func TestPolicyMerge_NetStricterWins(t *testing.T) {
	cases := []struct {
		a, b NetKind
		want NetKind
	}{
		{NetAllowAll, NetAllowAll, NetAllowAll},
		{NetAllowAll, NetAllowHosts, NetAllowHosts},
		{NetAllowHosts, NetDenyAll, NetDenyAll},
		{NetDenyAll, NetAllowAll, NetDenyAll},
	}
	for _, c := range cases {
		got := (Policy{Net: NetPolicy{Kind: c.a}}).Merge(Policy{Net: NetPolicy{Kind: c.b}})
		if got.Net.Kind != c.want {
			t.Errorf("merge(%v,%v) = %v, want %v", c.a, c.b, got.Net.Kind, c.want)
		}
	}
}

func TestPolicyMerge_TimeoutShorter(t *testing.T) {
	a := Policy{Timeout: 30 * time.Second}
	b := Policy{Timeout: 5 * time.Second}
	if got := a.Merge(b).Timeout; got != 5*time.Second {
		t.Errorf("merged timeout = %v, want 5s", got)
	}
}

func TestDenyAll(t *testing.T) {
	p := DenyAll()
	if p.Net.Kind != NetDenyAll {
		t.Errorf("DenyAll net = %v, want NetDenyAll", p.Net.Kind)
	}
	if len(p.FSRead) != 0 || len(p.FSWrite) != 0 || len(p.Exec) != 0 {
		t.Errorf("DenyAll has non-empty allow-lists")
	}
	if len(p.Mask) != 0 || len(p.Sockets) != 0 {
		t.Errorf("DenyAll should leave Mask/Sockets empty, got Mask=%v Sockets=%v", p.Mask, p.Sockets)
	}
}

// TestDenyAll_DeniesExec is the load-bearing assertion: DenyAll is
// supposed to deny every exec, not just zero-length-ly omit the list.
// ResolveBinary distinguishes Exec=nil ("no policy, allow everything")
// from Exec=[] ("explicit deny-all") — the constructor must produce
// the latter or the function silently allows every binary. Regression
// test for the bug Codex flagged as #116 ("DenyAll allows everything").
func TestDenyAll_DeniesExec(t *testing.T) {
	p := DenyAll()
	if p.Exec == nil {
		t.Fatal("DenyAll().Exec is nil — ResolveBinary will allow every binary")
	}
	_, err := ResolveBinary(p, "sh")
	if err == nil {
		t.Fatal("ResolveBinary(DenyAll(),\"sh\") returned nil error — DenyAll is not actually denying exec")
	}
	// errors.As (not direct type assertion) so future wrapping with
	// fmt.Errorf("...: %w", err) won't silently regress the test.
	var denied Denied
	if !errors.As(err, &denied) {
		t.Errorf("ResolveBinary(DenyAll(),\"sh\") returned %T %v, want Denied", err, err)
	}
}

func TestReadOnlyFS(t *testing.T) {
	p := ReadOnlyFS("/etc", "/usr")
	if len(p.FSRead) != 2 {
		t.Errorf("FSRead = %v", p.FSRead)
	}
	if p.Net.Kind != NetDenyAll {
		t.Error("ReadOnlyFS should deny net")
	}
	if len(p.FSWrite) != 0 {
		t.Error("ReadOnlyFS should have no write paths")
	}
}

// Same load-bearing invariant as TestDenyAll_DeniesExec — ReadOnlyFS is
// documented as "no exec," but the original code left Exec=nil which
// ResolveBinary treats as "no restriction." The constructor must
// produce Exec=[] for the deny path to engage.
func TestReadOnlyFS_DeniesExec(t *testing.T) {
	p := ReadOnlyFS("/etc")
	if p.Exec == nil {
		t.Fatal("ReadOnlyFS().Exec is nil — ResolveBinary will allow every binary")
	}
	_, err := ResolveBinary(p, "sh")
	if err == nil {
		t.Fatal("ResolveBinary(ReadOnlyFS(\"/etc\"),\"sh\") returned nil error — ReadOnlyFS is not actually denying exec")
	}
	var denied Denied
	if !errors.As(err, &denied) {
		t.Errorf("ResolveBinary(ReadOnlyFS,\"sh\") returned %T %v, want Denied", err, err)
	}
}

func TestDetect_AlwaysReturnsSomething(t *testing.T) {
	r := Detect()
	if r == nil {
		t.Fatal("Detect returned nil")
	}
	if !r.Available() {
		t.Errorf("Detect returned unavailable runner: %s", r.Name())
	}
}

func TestNoneRunner_InheritsEnvOnEmptyAllow(t *testing.T) {
	// When Policy.Env is empty, NoneRunner passes an empty env (deny-by-default).
	cmd, err := NoneRunner{}.Command(context.Background(), Policy{}, "/bin/true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Env) != 0 {
		t.Errorf("expected empty env, got %d entries", len(cmd.Env))
	}
}

func TestResolveBinary_AllowList(t *testing.T) {
	_, err := ResolveBinary(Policy{Exec: []string{"cat"}}, "ls")
	if _, ok := err.(Denied); !ok {
		t.Errorf("expected Denied for non-allowlisted binary, got %v", err)
	}
}
