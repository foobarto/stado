package sandbox

import (
	"context"
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
