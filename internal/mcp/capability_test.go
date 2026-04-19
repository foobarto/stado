package mcp

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
)

func TestParseCapabilities_Empty(t *testing.T) {
	got, err := ParseCapabilities(nil)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(got.FSRead) != 0 || len(got.FSWrite) != 0 ||
		len(got.Exec) != 0 || len(got.Env) != 0 {
		t.Errorf("empty input should yield empty policy, got %+v", got)
	}
}

func TestParseCapabilities_FullMix(t *testing.T) {
	caps := []string{
		"fs:read:/usr/lib/go",
		"fs:read:/home/user/.cache/go-build",
		"fs:write:/tmp/work",
		"net:api.github.com",
		"net:raw.githubusercontent.com",
		"exec:git",
		"exec:go",
		"env:GITHUB_TOKEN",
	}
	p, err := ParseCapabilities(caps)
	if err != nil {
		t.Fatalf("mix: %v", err)
	}

	// Two FS read paths accumulated in order.
	if len(p.FSRead) != 2 ||
		p.FSRead[0] != "/usr/lib/go" ||
		p.FSRead[1] != "/home/user/.cache/go-build" {
		t.Errorf("FSRead = %v", p.FSRead)
	}
	if len(p.FSWrite) != 1 || p.FSWrite[0] != "/tmp/work" {
		t.Errorf("FSWrite = %v", p.FSWrite)
	}
	// Two net hosts and NetAllowHosts kind.
	if p.Net.Kind != sandbox.NetAllowHosts {
		t.Errorf("Net.Kind = %v, want NetAllowHosts", p.Net.Kind)
	}
	if len(p.Net.Hosts) != 2 {
		t.Errorf("Net.Hosts = %v", p.Net.Hosts)
	}
	if len(p.Exec) != 2 || p.Exec[0] != "git" || p.Exec[1] != "go" {
		t.Errorf("Exec = %v", p.Exec)
	}
	if len(p.Env) != 1 || p.Env[0] != "GITHUB_TOKEN" {
		t.Errorf("Env = %v", p.Env)
	}
}

func TestParseCapabilities_NetDeny(t *testing.T) {
	p, err := ParseCapabilities([]string{"net:deny"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Net.Kind != sandbox.NetDenyAll {
		t.Errorf("Net.Kind = %v, want NetDenyAll", p.Net.Kind)
	}
}

func TestParseCapabilities_NetAllow(t *testing.T) {
	p, err := ParseCapabilities([]string{"net:allow"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Net.Kind != sandbox.NetAllowAll {
		t.Errorf("Net.Kind = %v, want NetAllowAll", p.Net.Kind)
	}
}

// TestParseCapabilities_RejectsBadInputs guards the parser: typos widen
// nothing.
func TestParseCapabilities_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		in      string
		wantMsg string
	}{
		{"foo", "missing"},
		{"fs:unknown:/p", "fs mode"},
		{"fs:read", "fs requires"},
		{"nonsense:value", "unknown kind"},
	}
	for _, tc := range cases {
		_, err := ParseCapabilities([]string{tc.in})
		if err == nil {
			t.Errorf("%q: expected error, got nil", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%q: error = %q, want containing %q", tc.in, err.Error(), tc.wantMsg)
		}
	}
}

// TestParseCapabilities_PathWithColonsSurvives — fs:read:/odd:path/file
// (a colon in the path) should not be mangled.
func TestParseCapabilities_PathWithColonsSurvives(t *testing.T) {
	p, err := ParseCapabilities([]string{"fs:read:/tmp/odd:weird:path"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.FSRead) != 1 || p.FSRead[0] != "/tmp/odd:weird:path" {
		t.Errorf("path mangled: %v", p.FSRead)
	}
}
