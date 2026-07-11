package broker

// ceiling_transitions_test.go — regression coverage for the
// narrow-only invariant of the capability ceiling:
//
//	capabilities NEVER widen along the spawn/narrow tree.
//
// Three transitions enforce this:
//   - SubagentCeiling: projects a child ceiling from parent + spawn
//     request; child MUST be <= parent on every field.
//   - IsSubsetOf: the predicate that defines "<=".
//   - NarrowEffective: gates in-session narrows through IsSubsetOf.
//
// This file complements ceiling_test.go; helpers are prefixed
// ceilTr_ and tests suffixed _CeilTr to avoid identifier clashes if
// the files ever coexist in the same package build.

import (
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
)

// ceilTrSetEnv pins the home/XDG vars CreateSession reads so the
// projected ceiling is deterministic regardless of the host that
// runs the test (mirrors the t.Setenv block in ceiling_test.go).
func ceilTrSetEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_DATA_HOME", "/home/test/.local/share")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
}

// ceilTrStrset turns a slice into a presence set for order-
// independent membership assertions.
func ceilTrStrset(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// -------------------------------------------------------------------
// 1. SubagentCeiling — projection never widens any field.
// -------------------------------------------------------------------

// TestSubagentCeiling_OutsideScopeDropped_CeilTr pins the headline
// security property: a worker+workspace_write whose write_scope names
// a path OUTSIDE the parent's FSWrite has that path DROPPED — it lands
// in the returned dropped slice and never in child.FSWrite. A genuine
// subpath of parent.FSWrite is allowed through.
func TestSubagentCeiling_OutsideScopeDropped_CeilTr(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work", "/tmp", "/home/test"},
		FSWrite: []string{"/work", "/tmp"},
	}
	child, dropped := SubagentCeiling(parent, "worker", "workspace_write", []string{
		"/work/pkg/sub", // subpath of /work — allowed
		"/tmp",          // exact match of /tmp — allowed
		"/etc",          // wholly outside parent FSWrite — dropped
		"/var/lib/x",    // wholly outside — dropped
	})

	gotWrite := ceilTrStrset(child.FSWrite)
	if !gotWrite["/work/pkg"] {
		t.Errorf("subpath /work/pkg/sub should project to mount root /work/pkg; child.FSWrite=%v", child.FSWrite)
	}
	if !gotWrite["/tmp"] {
		t.Errorf("exact match /tmp should be allowed; child.FSWrite=%v", child.FSWrite)
	}
	// The load-bearing assertion: an out-of-scope path must NOT leak
	// into the child's writable set.
	if gotWrite["/etc"] || gotWrite["/var/lib/x"] {
		t.Fatalf("out-of-scope path leaked into child.FSWrite=%v — ceiling WIDENED", child.FSWrite)
	}

	gotDropped := ceilTrStrset(dropped)
	if !gotDropped["/etc"] || !gotDropped["/var/lib/x"] {
		t.Errorf("out-of-scope paths should be surfaced in dropped; dropped=%v", dropped)
	}
	if gotDropped["/work/pkg/sub"] || gotDropped["/tmp"] {
		t.Errorf("in-scope paths must not be dropped; dropped=%v", dropped)
	}

	// Regardless of the request, the result must be a subset of the
	// parent — the universal attenuation invariant.
	if !IsSubsetOf(child, parent) {
		t.Errorf("child %+v is NOT a subset of parent %+v", child, parent)
	}
}

// TestSubagentCeiling_NonWorkerGetsNoWrites_CeilTr pins that any
// role/mode that is not exactly (worker, workspace_write) yields a
// child with FSWrite=nil — the most conservative projection — even
// when a write_scope is requested.
func TestSubagentCeiling_NonWorkerGetsNoWrites_CeilTr(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work"},
		FSWrite: []string{"/work"},
	}
	cases := []struct {
		name string
		role string
		mode string
	}{
		{"explorer/read_only", "explorer", "read_only"},
		{"worker/read_only", "worker", "read_only"},                 // right role, wrong mode
		{"explorer/workspace_write", "explorer", "workspace_write"}, // right mode, wrong role
		{"unknown/unknown", "weird", "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Even an in-bounds write_scope must be ignored for non-workers.
			child, dropped := SubagentCeiling(parent, tc.role, tc.mode, []string{"/work/pkg"})
			if child.FSWrite != nil {
				t.Errorf("non-worker FSWrite = %v, want nil", child.FSWrite)
			}
			if dropped != nil {
				t.Errorf("non-worker dropped = %v, want nil", dropped)
			}
			if !IsSubsetOf(child, parent) {
				t.Errorf("child %+v NOT a subset of parent %+v", child, parent)
			}
		})
	}
}

// TestSubagentCeiling_ReadNetExecEnvAttenuate_CeilTr pins that the
// non-write fields (FSRead, Net, Exec, Env) are copied from the
// parent and never exceed it. SubagentCeiling has no mechanism to
// ADD to these — the child can only ever inherit or (for FSWrite)
// shrink. We assert the projected child is a subset on EVERY field.
func TestSubagentCeiling_ReadNetExecEnvAttenuate_CeilTr(t *testing.T) {
	parent := sandbox.Policy{
		FSRead:  []string{"/work", "/usr/share"},
		FSWrite: []string{"/work"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"api.example.com", "cdn.example.com"}},
		Exec:    []string{"git", "go"},
		Env:     []string{"PATH", "HOME"},
	}
	child, _ := SubagentCeiling(parent, "worker", "workspace_write", []string{"/work/pkg"})

	// Reads inherit exactly.
	if !ceilTrStrset(child.FSRead)["/work"] || !ceilTrStrset(child.FSRead)["/usr/share"] {
		t.Errorf("child.FSRead = %v, want parent's read set", child.FSRead)
	}
	// Net inherits — never escalates beyond the parent's host set.
	if child.Net.Kind != sandbox.NetAllowHosts {
		t.Errorf("child.Net.Kind = %v, want inherited NetAllowHosts", child.Net.Kind)
	}
	if !netSubset(child.Net, parent.Net) {
		t.Errorf("child.Net %+v widens beyond parent.Net %+v", child.Net, parent.Net)
	}
	// Exec/Env inherit, never add a binary or env var the parent lacks.
	if !exactSubset(child.Exec, parent.Exec) {
		t.Errorf("child.Exec %v widens beyond parent.Exec %v", child.Exec, parent.Exec)
	}
	if !exactSubset(child.Env, parent.Env) {
		t.Errorf("child.Env %v widens beyond parent.Env %v", child.Env, parent.Env)
	}
	// And the whole-policy invariant.
	if !IsSubsetOf(child, parent) {
		t.Errorf("child %+v NOT a subset of parent %+v", child, parent)
	}
}

func TestSubagentCeiling_PreservesExecDenyAll(t *testing.T) {
	parent := sandbox.Policy{Exec: []string{}}
	child, _ := SubagentCeiling(parent, "explorer", "read_only", nil)
	if child.Exec == nil {
		t.Fatal("child.Exec is nil, want inherited non-nil empty deny-all")
	}
	if !IsSubsetOf(child, parent) {
		t.Fatal("deny-all child must remain a subset of deny-all parent")
	}
}

// -------------------------------------------------------------------
// 2. IsSubsetOf — the predicate that defines "<=".
// -------------------------------------------------------------------

// TestIsSubsetOf_WideningRejected_CeilTr is table-driven: every row
// is a candidate that WIDENS exactly one field relative to a fixed
// reference, and IsSubsetOf must return false. The final row is a
// genuine subset and must return true.
func TestIsSubsetOf_WideningRejected_CeilTr(t *testing.T) {
	ref := sandbox.Policy{
		FSRead:  []string{"/work", "/usr"},
		FSWrite: []string{"/work"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a", "b"}},
		Exec:    []string{"git", "go"},
		Env:     []string{"PATH"},
	}
	cases := []struct {
		name      string
		candidate sandbox.Policy
		want      bool
	}{
		{
			name:      "fsread-extra-path",
			candidate: sandbox.Policy{FSRead: []string{"/work", "/etc"}, Net: ref.Net},
			want:      false,
		},
		{
			name:      "fswrite-extra-path",
			candidate: sandbox.Policy{FSWrite: []string{"/work", "/etc"}, Net: ref.Net},
			want:      false,
		},
		{
			name:      "fswrite-sibling-prefix-not-subpath",
			candidate: sandbox.Policy{FSWrite: []string{"/workfoo"}, Net: ref.Net},
			want:      false, // /workfoo is NOT under /work
		},
		{
			name:      "net-allowall-over-allowhosts",
			candidate: sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowAll}},
			want:      false,
		},
		{
			name:      "net-allowhosts-extra-host",
			candidate: sandbox.Policy{Net: sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a", "b", "c"}}},
			want:      false,
		},
		{
			name:      "exec-extra-binary",
			candidate: sandbox.Policy{Exec: []string{"git", "rm"}, Net: ref.Net},
			want:      false,
		},
		{
			name:      "env-extra-var",
			candidate: sandbox.Policy{Env: []string{"PATH", "AWS_SECRET"}, Net: ref.Net},
			want:      false,
		},
		{
			name: "genuine-subset",
			candidate: sandbox.Policy{
				FSRead:  []string{"/work/pkg"}, // subpath of /work
				FSWrite: nil,                   // dropped writes
				Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowHosts, Hosts: []string{"a"}},
				Exec:    []string{"git"},
				Env:     nil,
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSubsetOf(tc.candidate, ref); got != tc.want {
				t.Errorf("IsSubsetOf(%+v, ref) = %v, want %v", tc.candidate, got, tc.want)
			}
		})
	}
}

func TestIsSubsetOf_ExecNilEmptySemantics(t *testing.T) {
	if IsSubsetOf(sandbox.Policy{Exec: nil}, sandbox.Policy{Exec: []string{"git"}}) {
		t.Fatal("unrestricted nil Exec must not be a subset of an allowlist")
	}
	if !IsSubsetOf(sandbox.Policy{Exec: []string{}}, sandbox.Policy{Exec: []string{"git"}}) {
		t.Fatal("non-nil empty deny-all Exec must be a subset of an allowlist")
	}
	if !IsSubsetOf(sandbox.Policy{Exec: []string{"git"}}, sandbox.Policy{Exec: nil}) {
		t.Fatal("an Exec allowlist must be a subset of unrestricted nil Exec")
	}
}

// -------------------------------------------------------------------
// 3. NarrowEffective — in-session transition gate.
// -------------------------------------------------------------------

// TestNarrowEffective_AcceptNarrowRejectWiden_CeilTr drives one
// session through an accepted narrow and a rejected widen on each
// field, proving NarrowEffective enforces IsSubsetOf and the ceiling
// is never mutated.
func TestNarrowEffective_AcceptNarrowRejectWiden_CeilTr(t *testing.T) {
	ceilTrSetEnv(t)
	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ceilingWriteN := len(handle.Ceiling.FSWrite)
	if ceilingWriteN == 0 {
		t.Fatalf("ceiling has no writable paths; cannot exercise narrow")
	}

	// Accepted narrow: keep reads, drop ALL writes (nil is the
	// strictest subset of anything).
	narrow := sandbox.Policy{
		FSRead:  append([]string(nil), handle.Effective.FSRead...),
		FSWrite: nil,
		Net:     handle.Effective.Net,
	}
	if err := svc.NarrowEffective(handle.SessionID, narrow); err != nil {
		t.Fatalf("accepted narrow rejected: %v", err)
	}
	got, _, err := svc.LookupSession(handle.SessionID)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if len(got.Effective.FSWrite) != 0 {
		t.Errorf("after narrow Effective.FSWrite = %v, want empty", got.Effective.FSWrite)
	}
	// Ceiling must be untouched by the narrow.
	if len(got.Ceiling.FSWrite) != ceilingWriteN {
		t.Errorf("ceiling FSWrite mutated by narrow: len=%d, want %d", len(got.Ceiling.FSWrite), ceilingWriteN)
	}

	// Rejected widen: now that effective writes are empty, trying to
	// re-add a path the (current) effective set lacks must be refused
	// — even if that path is still in the ceiling. The effective set
	// is monotone-narrowing.
	widen := sandbox.Policy{
		FSRead:  got.Effective.FSRead,
		FSWrite: []string{"/work"},
		Net:     got.Effective.Net,
	}
	err = svc.NarrowEffective(handle.SessionID, widen)
	if !errors.Is(err, ErrEffectiveWiderThanCeiling) {
		t.Fatalf("widen back to /work: err = %v, want ErrEffectiveWiderThanCeiling", err)
	}
	// The rejected widen must NOT have mutated the effective set.
	after, _, err := svc.LookupSession(handle.SessionID)
	if err != nil {
		t.Fatalf("LookupSession after rejected widen: %v", err)
	}
	if len(after.Effective.FSWrite) != 0 {
		t.Errorf("rejected widen mutated effective set: FSWrite=%v, want empty", after.Effective.FSWrite)
	}
}

// TestNarrowEffective_RejectsNetWiden_CeilTr proves the gate is not
// FS-only: widening the Net field (DenyAll -> AllowAll) is refused
// too. CreateSession's default profile produces a NetDenyAll ceiling,
// so any non-deny narrow request widens.
func TestNarrowEffective_RejectsNetWiden_CeilTr(t *testing.T) {
	ceilTrSetEnv(t)
	svc := NewService(DefaultPolicy(), nil)
	handle, _, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat,
		Profile: ProfileDefault,
		CWD:     "/work",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if handle.Effective.Net.Kind != sandbox.NetDenyAll {
		t.Skipf("default effective Net is %v, not NetDenyAll; net-widen scenario N/A", handle.Effective.Net.Kind)
	}

	widen := sandbox.Policy{
		FSRead:  handle.Effective.FSRead,
		FSWrite: handle.Effective.FSWrite,
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowAll},
	}
	if err := svc.NarrowEffective(handle.SessionID, widen); !errors.Is(err, ErrEffectiveWiderThanCeiling) {
		t.Fatalf("net widen DenyAll->AllowAll: err = %v, want ErrEffectiveWiderThanCeiling", err)
	}
}

// -------------------------------------------------------------------
// 4. anyParentCovers — LEXICAL containment, by design.
// -------------------------------------------------------------------

// TestAnyParentCovers_LexicalByDesign_CeilTr documents and pins that
// anyParentCovers uses filepath.Clean only (no EvalSymlinks). This is
// DELIBERATE: it computes the policy ceiling that bwrap consumes, and
// bwrap enforces containment via the mount namespace — the lexical
// check just shapes the policy, it is not itself the security
// boundary. Two properties matter here:
//
//   - "/workfoo" is NOT a subpath of "/work" (sibling-prefix guard:
//     a separator must follow the parent path).
//   - a request containing ".." is normalized by filepath.Clean
//     before the containment test, so "/work/../etc" escapes /work
//     and is correctly rejected, while "/work/a/../b" stays inside.
func TestAnyParentCovers_LexicalByDesign_CeilTr(t *testing.T) {
	parents := []string{"/work"}
	cases := []struct {
		name      string
		requested string
		want      bool
	}{
		{"exact-match", "/work", true},
		{"direct-subpath", "/work/pkg", true},
		{"deep-subpath", "/work/a/b/c", true},
		{"sibling-prefix-not-subpath", "/workfoo", false},
		{"sibling-prefix-deeper", "/workfoo/bar", false},
		{"dotdot-escapes-to-sibling", "/work/../etc", false}, // cleans to /etc
		{"dotdot-escapes-to-root", "/work/../../", false},    // cleans to /
		{"dotdot-stays-inside", "/work/a/../b", true},        // cleans to /work/b
		{"trailing-slash-cleaned", "/work/", true},           // cleans to /work
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyParentCovers(parents, tc.requested); got != tc.want {
				t.Errorf("anyParentCovers(%v, %q) = %v, want %v", parents, tc.requested, got, tc.want)
			}
		})
	}
}

// TestSubagentCeiling_DotDotEscapeDropped_CeilTr ties the lexical
// normalization to the end-to-end projection: a worker write_scope
// using ".." to climb out of the parent's writable root is cleaned
// and then dropped — it must never reach child.FSWrite.
func TestSubagentCeiling_DotDotEscapeDropped_CeilTr(t *testing.T) {
	parent := sandbox.Policy{FSWrite: []string{"/work"}}
	child, dropped := SubagentCeiling(parent, "worker", "workspace_write", []string{
		"/work/../etc", // cleans to /etc — ESCAPE, must be dropped
		"/work/a/../b", // cleans to /work/b — stays inside, allowed
	})
	gotWrite := ceilTrStrset(child.FSWrite)
	if gotWrite["/etc"] {
		t.Fatalf("dotdot escape /work/../etc leaked into child.FSWrite=%v — ceiling WIDENED", child.FSWrite)
	}
	if !gotWrite["/work"] {
		t.Errorf("/work/a/../b should project to mount root /work; child.FSWrite=%v", child.FSWrite)
	}
	// The escaped path is surfaced (under its cleaned form) in dropped.
	if !ceilTrStrset(dropped)["/etc"] {
		t.Errorf("dropped = %v, want it to contain the cleaned escape /etc", dropped)
	}
	if !IsSubsetOf(child, parent) {
		t.Errorf("child %+v NOT a subset of parent %+v", child, parent)
	}
}
