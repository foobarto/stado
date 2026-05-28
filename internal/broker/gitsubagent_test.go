package broker

import (
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
)

func TestProjectGitSubagentCeiling_HappyPath(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work", "/tmp"},
		FSWrite: []string{"/work", "/tmp"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowAll},
	}
	spec := GitSubagentSpec{
		Hosts:                          []string{"github.com", "gitlab.example.com"},
		KeyPaths:                       map[string]string{"github.com": "~/.ssh/id_ed25519"},
		WriteScope:                     []string{"/work/vendor", "/tmp"},
		AllowEgressOnlyToDeclaredHosts: true,
	}
	child, dropped := ProjectGitSubagentCeiling(parent, spec)
	if len(dropped) != 0 {
		t.Errorf("unexpected dropped: %v", dropped)
	}
	if child.Net.Kind != sandbox.NetAllowHosts {
		t.Errorf("Net.Kind = %v, want NetAllowHosts", child.Net.Kind)
	}
	wantHosts := map[string]bool{"github.com": true, "gitlab.example.com": true}
	if len(child.Net.Hosts) != len(wantHosts) {
		t.Errorf("Net.Hosts = %v, want %v", child.Net.Hosts, wantHosts)
	}
	for _, h := range child.Net.Hosts {
		if !wantHosts[h] {
			t.Errorf("unexpected host: %q", h)
		}
	}
	if !containsString(child.Env, "SSH_AUTH_SOCK") {
		t.Errorf("Env should include SSH_AUTH_SOCK; got %v", child.Env)
	}
}

func TestProjectGitSubagentCeiling_DropsHostsNotInParent(t *testing.T) {
	parent := sandbox.Policy{
		FSWrite: []string{"/work"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"github.com"}},
	}
	spec := GitSubagentSpec{
		Hosts:                          []string{"github.com", "evil.example.com"},
		WriteScope:                     []string{"/work/vendor"},
		AllowEgressOnlyToDeclaredHosts: true,
	}
	child, dropped := ProjectGitSubagentCeiling(parent, spec)
	if len(child.Net.Hosts) != 1 || child.Net.Hosts[0] != "github.com" {
		t.Errorf("child Net.Hosts = %v, want [github.com] (evil dropped)", child.Net.Hosts)
	}
	found := false
	for _, d := range dropped {
		if d == "evil.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped should include evil.example.com; got %v", dropped)
	}
}

func TestProjectGitSubagentCeiling_ParentNetDenyDeniesAll(t *testing.T) {
	parent := sandbox.Policy{
		FSWrite: []string{"/work"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetDenyAll},
	}
	spec := GitSubagentSpec{
		Hosts:                          []string{"github.com"},
		AllowEgressOnlyToDeclaredHosts: true,
	}
	child, _ := ProjectGitSubagentCeiling(parent, spec)
	if child.Net.Kind != sandbox.NetDenyAll {
		t.Errorf("parent NetDenyAll should win; got %v", child.Net.Kind)
	}
}

func TestProjectGitSubagentCeiling_WriteScopeFiltered(t *testing.T) {
	parent := sandbox.Policy{FSWrite: []string{"/work"}}
	spec := GitSubagentSpec{
		Hosts:                          []string{"github.com"},
		WriteScope:                     []string{"/work/vendor", "/etc"},
		AllowEgressOnlyToDeclaredHosts: true,
	}
	child, dropped := ProjectGitSubagentCeiling(parent, spec)
	if len(child.FSWrite) != 1 || child.FSWrite[0] != "/work/vendor" {
		t.Errorf("FSWrite = %v, want [/work/vendor]", child.FSWrite)
	}
	found := false
	for _, d := range dropped {
		if d == "/etc" {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped should include /etc; got %v", dropped)
	}
}

func TestIsForbiddenForGitSubagent_PushForbidden(t *testing.T) {
	cases := [][]string{
		{"git", "push"},
		{"git", "push", "origin", "main"},
		{"/usr/bin/git", "push"},
		{"git", "-c", "user.name=x", "push"},
		{"git", "-C", "/work", "push"},
		{"git", "send-pack", "origin"},
	}
	for _, argv := range cases {
		if !IsForbiddenForGitSubagent(argv) {
			t.Errorf("%v should be forbidden", argv)
		}
	}
}

func TestIsForbiddenForGitSubagent_FetchAllowed(t *testing.T) {
	cases := [][]string{
		{"git", "clone", "https://github.com/foo/bar"},
		{"git", "fetch"},
		{"git", "pull", "origin", "main"},
		{"git", "ls-remote", "origin"},
		{"git", "log"},
		{"git", "show", "HEAD"},
		{"git", "diff"},
		{"git", "-c", "advice.detachedHead=false", "clone", "origin"},
	}
	for _, argv := range cases {
		if IsForbiddenForGitSubagent(argv) {
			t.Errorf("%v should be allowed", argv)
		}
	}
}

func TestIsForbiddenForGitSubagent_UnknownVerbDefaultsToForbidden(t *testing.T) {
	cases := [][]string{
		{"git", "branch", "-D", "old"},
		{"git", "config", "--global", "user.name", "x"},
		{"git", "tag"},
		{"git", "reset", "--hard"},
	}
	for _, argv := range cases {
		if !IsForbiddenForGitSubagent(argv) {
			t.Errorf("%v should default to forbidden (fail-safe)", argv)
		}
	}
}

func TestIsForbiddenForGitSubagent_NonGitCommands(t *testing.T) {
	// Non-git commands return false — the function defers to
	// other guards (the broker ceiling + the runner) for those.
	cases := [][]string{
		{"ls"},
		{"bash", "-c", "echo hello"},
		{"npm", "install"},
		{},
	}
	for _, argv := range cases {
		if IsForbiddenForGitSubagent(argv) {
			t.Errorf("%v is not git; should not be forbidden by this guard", argv)
		}
	}
}

func TestExtractGitVerb(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"git", "fetch"}, "fetch"},
		{[]string{"git", "push", "origin"}, "push"},
		{[]string{"/usr/bin/git", "clone", "url"}, "clone"},
		{[]string{"git", "-c", "user.name=x", "fetch"}, "fetch"},
		{[]string{"git", "-C", "/work", "log"}, "log"},
		{[]string{"git"}, ""}, // no verb
		{[]string{"ls"}, ""},  // not git
		{[]string{}, ""},      // empty
		{[]string{"git", "-c", "a=b", "-C", "/w", "diff"}, "diff"},
	}
	for _, tc := range cases {
		if got := extractGitVerb(tc.argv); got != tc.want {
			t.Errorf("extractGitVerb(%v) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

func TestCleanHosts_DeduplicatesAndSorts(t *testing.T) {
	got := cleanHosts([]string{"b.example.com", "a.example.com", "", "b.example.com", "  c.example.com  "})
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	if len(got) != len(want) {
		t.Fatalf("cleanHosts len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] %q, want %q", i, got[i], w)
		}
	}
}

func TestRoleConstants_MatchElevatedReserved(t *testing.T) {
	// Sanity: the role constants in this file match the strings
	// reserved by isElevatedSubagentRole in taint.go.
	if !isElevatedSubagentRole(RoleGitFetch) {
		t.Errorf("RoleGitFetch (%q) is not in elevated set", RoleGitFetch)
	}
	if !isElevatedSubagentRole(RoleGitSubagent) {
		t.Errorf("RoleGitSubagent (%q) is not in elevated set", RoleGitSubagent)
	}
}
