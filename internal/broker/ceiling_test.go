package broker

import (
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
)

func TestSubagentCeiling_ExplorerReadOnlyDropsWrites(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work", "/tmp", "/home/test"},
		FSWrite: []string{"/work", "/tmp"},
	}
	child, dropped := SubagentCeiling(parent, "explorer", "read_only", nil)
	if len(child.FSWrite) != 0 {
		t.Errorf("explorer child FSWrite = %v, want empty", child.FSWrite)
	}
	if len(dropped) != 0 {
		t.Errorf("explorer with no write_scope should not drop anything, got %v", dropped)
	}
	// Reads inherit.
	if len(child.FSRead) != len(parent.FSRead) {
		t.Errorf("explorer child FSRead len = %d, want %d", len(child.FSRead), len(parent.FSRead))
	}
}

func TestSubagentCeiling_WorkerWorkspaceWriteFiltersScope(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work", "/tmp", "/home/test"},
		FSWrite: []string{"/work", "/tmp"},
	}
	child, dropped := SubagentCeiling(parent, "worker", "workspace_write", []string{
		"/work/pkg/foo",   // inside parent /work, allowed
		"/work/pkg/bar",   // inside parent /work, allowed
		"/etc/passwd",     // outside parent writable, dropped
		"/home/test/.ssh", // outside parent writable, dropped
	})
	wantAllowed := map[string]bool{"/work/pkg/foo": true, "/work/pkg/bar": true}
	if len(child.FSWrite) != len(wantAllowed) {
		t.Errorf("worker child FSWrite = %v, want %v", child.FSWrite, wantAllowed)
	}
	for _, w := range child.FSWrite {
		if !wantAllowed[w] {
			t.Errorf("unexpected FSWrite: %q", w)
		}
		delete(wantAllowed, w)
	}
	wantDropped := map[string]bool{"/etc/passwd": true, "/home/test/.ssh": true}
	if len(dropped) != len(wantDropped) {
		t.Errorf("dropped = %v, want %v", dropped, wantDropped)
	}
	for _, d := range dropped {
		if !wantDropped[d] {
			t.Errorf("unexpected dropped: %q", d)
		}
	}
}

func TestSubagentCeiling_UnknownRoleTreatedAsReadOnly(t *testing.T) {
	parent := sandbox.Policy{FSWrite: []string{"/work"}}
	child, _ := SubagentCeiling(parent, "weird-role", "weird-mode", []string{"/work/pkg"})
	if len(child.FSWrite) != 0 {
		t.Errorf("unknown role should produce read-only child, got FSWrite=%v", child.FSWrite)
	}
}

func TestSubagentCeiling_SimilarPrefixDoesNotMatch(t *testing.T) {
	// /workfoo is NOT a subpath of /work — the prefix-match check
	// must require a path separator after the parent path.
	parent := sandbox.Policy{FSWrite: []string{"/work"}}
	child, dropped := SubagentCeiling(parent, "worker", "workspace_write", []string{"/workfoo"})
	if len(child.FSWrite) != 0 {
		t.Errorf("worker should not get /workfoo (parent is /work, not /workfoo); got %v", child.FSWrite)
	}
	if len(dropped) != 1 || dropped[0] != "/workfoo" {
		t.Errorf("dropped = %v, want [/workfoo]", dropped)
	}
}

func TestIsSubsetOf_ExactSameIsSubset(t *testing.T) {
	p := sandbox.Policy{FSRead: []string{"/a"}, FSWrite: []string{"/a"}}
	if !IsSubsetOf(p, p) {
		t.Errorf("policy should be a subset of itself")
	}
}

func TestIsSubsetOf_NarrowerIsSubset(t *testing.T) {
	parent := sandbox.Policy{FSRead: []string{"/a", "/b"}, FSWrite: []string{"/a"}}
	child := sandbox.Policy{FSRead: []string{"/a"}, FSWrite: nil}
	if !IsSubsetOf(child, parent) {
		t.Errorf("child should be a subset of parent")
	}
}

func TestIsSubsetOf_WiderIsNotSubset(t *testing.T) {
	parent := sandbox.Policy{FSRead: []string{"/a"}}
	wider := sandbox.Policy{FSRead: []string{"/a", "/b"}}
	if IsSubsetOf(wider, parent) {
		t.Errorf("wider should NOT be a subset of parent")
	}
}

func TestIsSubsetOf_SubpathIsSubset(t *testing.T) {
	parent := sandbox.Policy{FSWrite: []string{"/work"}}
	child := sandbox.Policy{FSWrite: []string{"/work/pkg"}}
	if !IsSubsetOf(child, parent) {
		t.Errorf("subpath child should be a subset of parent")
	}
}

func TestIsSubsetOf_NetDenyIsAlwaysSubset(t *testing.T) {
	parent := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a"}}}
	child := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetDenyAll}}
	if !IsSubsetOf(child, parent) {
		t.Errorf("NetDenyAll child should always be subset")
	}
}

func TestIsSubsetOf_NetAllowAllChildNotSubsetOfDenyParent(t *testing.T) {
	parent := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetDenyAll}}
	child := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowAll}}
	if IsSubsetOf(child, parent) {
		t.Errorf("NetAllowAll child should NOT be subset of NetDenyAll parent")
	}
}

func TestIsSubsetOf_NetHostsSubset(t *testing.T) {
	parent := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a", "b", "c"}}}
	child := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a", "b"}}}
	if !IsSubsetOf(child, parent) {
		t.Errorf("child with subset hosts should be subset")
	}
	wider := sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a", "b", "c", "d"}}}
	if IsSubsetOf(wider, parent) {
		t.Errorf("child with extra host should NOT be subset")
	}
}

func TestNarrowEffective_AllowsNarrowing(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(handle.Effective.FSWrite) == 0 {
		t.Fatalf("initial Effective.FSWrite empty")
	}

	// Narrow: drop /tmp from writes (keep /work).
	narrowed := sandbox.Policy{
		FSRead:  handle.Ceiling.FSRead,
		FSWrite: []string{"/work"},
	}
	if err := svc.NarrowEffective(handle.SessionID, narrowed); err != nil {
		t.Fatalf("NarrowEffective: %v", err)
	}
	got, _, err := svc.LookupSession(handle.SessionID)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if len(got.Effective.FSWrite) != 1 || got.Effective.FSWrite[0] != "/work" {
		t.Errorf("Effective.FSWrite after narrow = %v, want [/work]", got.Effective.FSWrite)
	}
	// Ceiling must be unchanged.
	if len(got.Ceiling.FSWrite) != len(handle.Ceiling.FSWrite) {
		t.Errorf("Ceiling.FSWrite changed: %v vs %v", got.Ceiling.FSWrite, handle.Ceiling.FSWrite)
	}
}

func TestNarrowEffective_RejectsWidening(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Try to widen — add a path not in the ceiling.
	wider := sandbox.Policy{
		FSRead:  handle.Ceiling.FSRead,
		FSWrite: append([]string(nil), handle.Ceiling.FSWrite...),
	}
	wider.FSWrite = append(wider.FSWrite, "/etc")
	err = svc.NarrowEffective(handle.SessionID, wider)
	if err == nil {
		t.Fatal("expected ErrEffectiveWiderThanCeiling, got nil")
	}
	if !errors.Is(err, ErrEffectiveWiderThanCeiling) {
		t.Errorf("err = %v, want ErrEffectiveWiderThanCeiling", err)
	}
}

func TestNarrowEffective_RepeatedNarrowingMonotonic(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Narrow once to /work + /tmp.
	first := sandbox.Policy{
		FSRead:  handle.Ceiling.FSRead,
		FSWrite: []string{"/work", "/tmp"},
	}
	if err := svc.NarrowEffective(handle.SessionID, first); err != nil {
		t.Fatalf("first narrow: %v", err)
	}
	// Narrow further to just /work.
	second := sandbox.Policy{
		FSRead:  handle.Ceiling.FSRead,
		FSWrite: []string{"/work"},
	}
	if err := svc.NarrowEffective(handle.SessionID, second); err != nil {
		t.Fatalf("second narrow: %v", err)
	}
	// Try to widen back from /work to /work + /tmp — must fail
	// because /tmp is no longer in the effective set.
	if err := svc.NarrowEffective(handle.SessionID, first); err == nil {
		t.Fatal("third call (re-widen) should fail")
	}
}

func TestNarrowEffective_TerminatedSession(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.TerminateSession(handle.SessionID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	err = svc.NarrowEffective(handle.SessionID, sandbox.Policy{})
	if !errors.Is(err, ErrSessionTerminated) {
		t.Errorf("err = %v, want ErrSessionTerminated", err)
	}
}

func TestNarrowEffective_UnknownSession(t *testing.T) {
	svc := NewService(DefaultPolicy(), nil)
	err := svc.NarrowEffective("unknown", sandbox.Policy{})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestSubagentCeiling_AttenuationInvariant(t *testing.T) {
	// The load-bearing invariant: the child ceiling SubagentCeiling
	// returns MUST be a subset of the parent. Test against several
	// parent + spawn-request combinations.
	cases := []struct {
		name       string
		parent     sandbox.Policy
		role, mode string
		ws         []string
	}{
		{"explorer/read_only", sandbox.Policy{FSRead: []string{"/a"}, FSWrite: []string{"/a"}}, "explorer", "read_only", nil},
		{"worker/scoped-in", sandbox.Policy{FSWrite: []string{"/work"}}, "worker", "workspace_write", []string{"/work/pkg"}},
		{"worker/scoped-mixed", sandbox.Policy{FSWrite: []string{"/work", "/tmp"}}, "worker", "workspace_write", []string{"/work/pkg", "/tmp/scratch", "/etc"}},
		{"worker/all-dropped", sandbox.Policy{FSWrite: []string{"/work"}}, "worker", "workspace_write", []string{"/etc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child, _ := SubagentCeiling(tc.parent, tc.role, tc.mode, tc.ws)
			if !IsSubsetOf(child, tc.parent) {
				t.Errorf("child %+v is NOT a subset of parent %+v", child, tc.parent)
			}
		})
	}
}

func TestErrEffectiveWiderThanCeiling_Message(t *testing.T) {
	// Sanity: the error message tells the operator what to do.
	got := ErrEffectiveWiderThanCeiling.Error()
	if !strings.Contains(got, "fork a new session") {
		t.Errorf("error %q lacks 'fork a new session' guidance", got)
	}
}

func TestCreateSession_EffectiveEqualsCeilingAtCreate(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(handle.Effective.FSRead) != len(handle.Ceiling.FSRead) {
		t.Errorf("Effective.FSRead len = %d, want %d", len(handle.Effective.FSRead), len(handle.Ceiling.FSRead))
	}
	if len(handle.Effective.FSWrite) != len(handle.Ceiling.FSWrite) {
		t.Errorf("Effective.FSWrite len = %d, want %d", len(handle.Effective.FSWrite), len(handle.Ceiling.FSWrite))
	}
}

func TestProjectCeiling_SubagentWriteScopeResolvedAgainstCWD(t *testing.T) {
	// Regression test for Codex P2 review of PR #71. spawn_agent's
	// write_scope is repo-relative ("src/foo"); the parent ceiling's
	// FSWrite is absolute (/work). projectCeiling must resolve the
	// relative entries against req.CWD before SubagentCeiling
	// projects, otherwise normal worker scopes are always dropped.
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	pol := projectCeiling(CapabilityRequest{
		Purpose:    PurposeSubagent,
		Profile:    ProfileDefault,
		CWD:        "/work",
		Role:       "worker",
		Mode:       "workspace_write",
		WriteScope: []string{"src/foo", "src/bar"},
	})
	wantWrites := map[string]bool{"/work/src/foo": true, "/work/src/bar": true}
	if len(pol.FSWrite) != 2 {
		t.Fatalf("FSWrite = %v, want %v (resolved against cwd)", pol.FSWrite, wantWrites)
	}
	for _, w := range pol.FSWrite {
		if !wantWrites[w] {
			t.Errorf("unexpected FSWrite %q (resolved against cwd /work)", w)
		}
		delete(wantWrites, w)
	}
	if len(wantWrites) != 0 {
		t.Errorf("missing FSWrite paths: %v", wantWrites)
	}
}

func TestResolveRelativeScope_Mix(t *testing.T) {
	cases := []struct {
		name  string
		scope []string
		cwd   string
		want  []string
	}{
		{"empty", nil, "/work", nil},
		{"all-relative", []string{"src", "pkg/foo"}, "/work", []string{"/work/src", "/work/pkg/foo"}},
		{"absolute-passes-through", []string{"/etc"}, "/work", []string{"/etc"}},
		{"mixed", []string{"src", "/tmp/cache"}, "/work", []string{"/work/src", "/tmp/cache"}},
		{"empty-cwd-relative-passes", []string{"src"}, "", []string{"src"}},
		{"empty-entry-dropped", []string{"", "src"}, "/work", []string{"/work/src"}},
		{"clean", []string{"src/../pkg"}, "/work", []string{"/work/pkg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRelativeScope(tc.scope, tc.cwd)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got %v)", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("[%d] %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestProjectCeiling_SubagentNarrowerThanMainChat(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	mainCeiling := projectCeiling(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	subCeiling := projectCeiling(CapabilityRequest{
		Purpose: PurposeSubagent,
		Profile: ProfileDefault,
		CWD:     "/work",
		Role:    "explorer",
		Mode:    "read_only",
	})
	// Explorer sub-agent has no writes.
	if len(subCeiling.FSWrite) != 0 {
		t.Errorf("explorer subagent ceiling FSWrite = %v, want empty", subCeiling.FSWrite)
	}
	// And the subagent ceiling must be a subset of the main-chat
	// ceiling (attenuation invariant).
	if !IsSubsetOf(subCeiling, mainCeiling) {
		t.Errorf("subagent ceiling %+v NOT a subset of main ceiling %+v", subCeiling, mainCeiling)
	}
}
