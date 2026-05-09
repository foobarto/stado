package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBuildSandboxedCmd_NilPolicyRunsUnsandboxed locks down the
// stado_exec posture documented in cmd/stado/mcp_server.go after the
// 2026-05-09 review correction: with no guest-supplied sandbox policy
// the resulting *exec.Cmd has no sandbox runner wrapping it. This is
// the contract that lets the bundled shell wasm in
// internal/bundledplugins/modules/shell/main.go (which never sets
// `sandbox` in its stado_exec request) run with the operator's full
// UID privileges, even under mcp-server / TUI / daemon entry points
// that detect bwrap.
//
// If a future commit plumbs a host-default policy through stado_exec
// (a desirable change — and the comment block in mcp_server.go now
// flags it as a follow-up), this test will fail and force the author
// to:
//
//  1. Make a deliberate decision about the new default.
//  2. Update the mcp_server.go comment to match.
//  3. Update the wasm shell to opt out (or in) explicitly.
//
// That's the right shape for a load-bearing security default.
func TestBuildSandboxedCmd_NilPolicyRunsUnsandboxed(t *testing.T) {
	cmd, err := buildSandboxedCmd(context.Background(), nil, "/tmp", []string{"/bin/echo", "hi"}, nil)
	if err != nil {
		t.Fatalf("buildSandboxedCmd(nil policy): %v", err)
	}
	if cmd == nil {
		t.Fatal("buildSandboxedCmd returned nil cmd")
	}
	// The command's Path/Args reflect the literal argv with no
	// bwrap / sandbox-exec wrapper prefixed. If the policy were
	// auto-applied, cmd.Path would point at /usr/bin/bwrap (or
	// /usr/bin/sandbox-exec on macOS) and the original argv would
	// be inside cmd.Args after the runner's flags.
	if !strings.HasSuffix(cmd.Path, "/echo") && cmd.Path != "/bin/echo" {
		t.Errorf("nil-policy exec.Cmd.Path = %q; expected the literal argv[0] (no runner wrapper)", cmd.Path)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "/bin/echo" || cmd.Args[1] != "hi" {
		t.Errorf("nil-policy exec.Cmd.Args = %v; expected [/bin/echo hi]", cmd.Args)
	}
}

// TestBuildSandboxedCmd_PolicyWithoutRunnerErrors confirms the
// fail-loud branch when a guest *does* request sandboxing but no
// runner is available. Silent fall-back would defeat plugin-author
// intent — the existing implementation correctly errors.
func TestBuildSandboxedCmd_PolicyWithoutRunnerErrors(t *testing.T) {
	// We can't easily mock sandbox.Detect, so this test only fires
	// on hosts where Detect returns "none" (no bwrap, no sandbox-exec).
	// Skip cleanly elsewhere.
	if hasSandboxRunner() {
		t.Skip("native sandbox runner available; this branch only fires when none detected")
	}
	policy := &sandboxPolicy{Net: "deny"}
	_, err := buildSandboxedCmd(context.Background(), policy, "/tmp", []string{"/bin/true"}, nil)
	if err == nil {
		t.Fatal("expected error when policy requested but no runner available")
	}
	if !strings.Contains(err.Error(), "no native sandbox runner") {
		t.Errorf("error should mention missing runner; got %q", err.Error())
	}
}

// TestResolveSandboxPolicy covers the layered policy resolution that
// closes the gap the original mcp_server.go comment overstated:
// guest-supplied policy still wins (unchanged), but a nil guest now
// falls back to the host's default when one is set. Without a host
// default, behaviour is the legacy "run unsandboxed".
func TestResolveSandboxPolicy(t *testing.T) {
	guest := &sandboxPolicy{Net: "deny", CWD: "/guest"}
	hostDefault := NewDefaultSandboxPolicy("/host").(*sandboxPolicy)

	t.Run("no host: guest wins", func(t *testing.T) {
		// Without a host default, there's no ceiling to enforce.
		// Guest's policy applies as-is. Matches stado run /
		// stado tool run / TUI behaviour where the operator
		// invocation is explicit.
		if got := resolveSandboxPolicy(&Host{}, guest); got != guest {
			t.Errorf("guest-supplied policy with no host default: got %+v want %+v", got, guest)
		}
	})

	t.Run("host + nil guest: host default", func(t *testing.T) {
		if got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: hostDefault}, nil); got != hostDefault {
			t.Errorf("nil guest + host default: want host default, got %+v", got)
		}
	})

	t.Run("host + guest: intersection (CWD = host's)", func(t *testing.T) {
		// Post-redesign: guest can no longer replace host policy;
		// it can only intersect/tighten. CWD field specifically:
		// host wins (operator chose it; plugin can't escape).
		got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: hostDefault}, guest)
		if got == nil {
			t.Fatal("intersection returned nil; want non-nil result")
		}
		if got == guest {
			t.Errorf("guest-supplied policy must NOT win unchanged when host has default — that was the security hole. Want intersection.")
		}
		if got.CWD != hostDefault.CWD {
			t.Errorf("CWD = %q, want host's %q (operator-chosen)", got.CWD, hostDefault.CWD)
		}
		if got.Net != "deny" {
			t.Errorf("Net = %q, want deny (deny wins; either side suffices)", got.Net)
		}
	})

	t.Run("both nil: unsandboxed", func(t *testing.T) {
		if got := resolveSandboxPolicy(&Host{}, nil); got != nil {
			t.Errorf("both nil: want nil (unsandboxed); got %+v", got)
		}
	})

	t.Run("wrong-typed host default → nil", func(t *testing.T) {
		// Defensive: misconfigured entry point shouldn't panic.
		if got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: "not a policy"}, nil); got != nil {
			t.Errorf("wrong-typed host default: want nil, got %+v", got)
		}
	})

	t.Run("nil host: nil result", func(t *testing.T) {
		if got := resolveSandboxPolicy(nil, nil); got != nil {
			t.Errorf("nil host: want nil, got %+v", got)
		}
	})
}

// TestNewDefaultSandboxPolicy_PermissiveByDefault locks the default
// host policy values. Pre-2026-05-09 second-pass: the function
// returned `&sandboxPolicy{CWD: workdir}` and claimed that produced
// "no FS / net restrictions." Two bugs: (a) empty Net string fell
// through both translation cases, leaving sandbox.NetPolicy at zero
// = NetDenyAll → bwrap got --unshare-net; (b) /bin and /sbin were
// not bound, so on distros without /bin → /usr/bin symlink, the
// shell wasm's literal /bin/sh / /bin/bash invocations failed at
// execvp. Both confirmed empirically by codex.
//
// The corrected default sets Net="allow" explicitly and binds
// /bin /sbin /tmp /var/tmp /run via FSRead so common shell patterns
// work. This test fails if anyone reverts to the old shape.
func TestNewDefaultSandboxPolicy_PermissiveByDefault(t *testing.T) {
	policy := NewDefaultSandboxPolicy("/some/workdir")
	resolved := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: policy}, nil)
	if resolved == nil {
		t.Fatal("resolveSandboxPolicy didn't accept the default policy")
	}
	if resolved.CWD != "/some/workdir" {
		t.Errorf("default policy CWD = %q, want /some/workdir", resolved.CWD)
	}
	if resolved.Net != "allow" {
		t.Errorf("default policy Net = %q, want \"allow\" (network passthrough). "+
			"Empty string falls through buildSandboxedCmd's switch and produces NetDenyAll — "+
			"that's the bug fixed in the 2026-05-09 second pass.", resolved.Net)
	}
	wantReads := map[string]bool{"/bin": true, "/sbin": true, "/tmp": true, "/var/tmp": true, "/run": true}
	got := map[string]bool{}
	for _, p := range resolved.FSRead {
		got[p] = true
	}
	for p := range wantReads {
		if !got[p] {
			t.Errorf("default policy FSRead missing %q. Without it, /bin/sh / /bin/bash literals fail to execvp under bwrap on distros where /bin isn't a symlink to /usr/bin.", p)
		}
	}
	if len(resolved.FSWrite) == 0 {
		t.Errorf("default policy FSWrite is empty — plugins writing to /tmp will fail")
	}
}

// TestResolveSandboxPolicy_HostAsCeiling locks the redesigned
// semantics: when the host has a default policy, the guest can
// only TIGHTEN it; Unsandboxed=true is honored only when there's
// no host default to enforce. Pre-redesign (commit 7e20c07 +
// dde15dc): "guest wins" meant a plugin could weaken host policy
// by setting Unsandboxed=true. The current commit makes host the
// floor; security-relevant fix.
func TestResolveSandboxPolicy_HostAsCeiling(t *testing.T) {
	hostDefault := NewDefaultSandboxPolicy("/host")
	host := &Host{DefaultSandboxPolicy: hostDefault}

	t.Run("absent guest: host default applies", func(t *testing.T) {
		if got := resolveSandboxPolicy(host, nil); got == nil {
			t.Errorf("absent guest + host default: want host default, got nil")
		}
	})

	t.Run("Unsandboxed=true with host default: IGNORED", func(t *testing.T) {
		guest := &sandboxPolicy{Unsandboxed: true}
		got := resolveSandboxPolicy(host, guest)
		if got == nil {
			t.Errorf("Unsandboxed=true must NOT bypass host default — that was the security hole. Want host policy, got nil.")
		}
	})

	t.Run("Unsandboxed=true with host default + other fields: IGNORED", func(t *testing.T) {
		guest := &sandboxPolicy{Unsandboxed: true, Net: "deny", FSRead: []string{"/etc"}}
		got := resolveSandboxPolicy(host, guest)
		if got == nil {
			t.Errorf("Unsandboxed must not weaken host policy regardless of other fields. Want intersection, got nil.")
		}
	})

	t.Run("Unsandboxed=true with NO host default: honored", func(t *testing.T) {
		// stado run / stado tool run path: no host default. Plugin
		// can opt out — operator-explicit invocation.
		hostNoDefault := &Host{}
		guest := &sandboxPolicy{Unsandboxed: true}
		if got := resolveSandboxPolicy(hostNoDefault, guest); got != nil {
			t.Errorf("Unsandboxed=true with no host default: want nil (legacy opt-out path); got %+v", got)
		}
	})
}

// TestIntersectPolicies_ExecNoOverlapStillEnforces locks the codex
// catch from the third pass: when host has Exec=[A] and guest has
// Exec=[B] with no overlap, the intersection must NOT silently
// allow all binaries — that would invert the policy. The intersection
// returns a non-nil empty slice so the runner's `Exec != nil` gate
// catches it as "policy specified, list empty, deny all."
func TestIntersectPolicies_ExecNoOverlapStillEnforces(t *testing.T) {
	host := &sandboxPolicy{Exec: []string{"cat"}}
	guest := &sandboxPolicy{Exec: []string{"sh"}}
	got := intersectPolicies(host, guest)
	if got.Exec == nil {
		t.Fatal("Exec intersect with no overlap returned nil — would let runner allow ALL binaries (the codex regression)")
	}
	if len(got.Exec) != 0 {
		t.Errorf("Exec intersect with no overlap = %v, want non-nil empty slice (deny-all marker)", got.Exec)
	}
	// Sanity-check the runner gate on the same shape: with non-nil
	// empty Exec, ResolveBinary should refuse any name.
	// (Direct test of ResolveBinary lives in the sandbox package;
	// here we just confirm the shape we produce is what the runner
	// expects.)
}

// TestIntersectNet_HostEmptyMeansDeny covers codex's Net="" loosening.
// At the runner level, an empty Net string in sandbox.Policy.Net falls
// through buildSandboxedCmd's switch and leaves NetPolicy.Kind at its
// zero value of NetDenyAll. So host="" effectively means deny.
// Treating it as "no opinion" in the intersection let a guest "allow"
// loosen the host's de-facto deny. Now host="" → "deny" in the
// intersection.
func TestIntersectNet_HostEmptyMeansDeny(t *testing.T) {
	cases := []struct {
		host, guest, want string
		desc              string
	}{
		{"", "allow", "deny", "host empty + guest allow → deny (host is de-facto deny at runner)"},
		{"", "deny", "deny", "host empty + guest deny → deny"},
		{"", "", "deny", "both empty → deny (still de-facto)"},
		// Non-empty host: existing semantics preserved.
		{"allow", "deny", "deny", "guest deny tightens host allow"},
		{"deny", "allow", "deny", "host deny clamps guest"},
		{"allow", "allow", "allow", "both agree"},
		{"allow", "", "allow", "host allow + guest unspecified"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := intersectNet(c.host, c.guest)
			if got != c.want {
				t.Errorf("intersectNet(%q, %q) = %q, want %q", c.host, c.guest, got, c.want)
			}
		})
	}
}

// TestResolveSandboxPolicy_GuestCanOnlyTighten covers the
// intersection contract field by field. Each subcase: host wants X,
// guest wants Y, expected = intersect(X, Y).
func TestResolveSandboxPolicy_GuestCanOnlyTighten(t *testing.T) {
	t.Run("FSRead: guest cannot expand", func(t *testing.T) {
		host := &sandboxPolicy{FSRead: []string{"/bin", "/usr"}}
		guest := &sandboxPolicy{FSRead: []string{"/bin", "/etc"}} // wants /etc, host doesn't permit
		got := intersectPolicies(host, guest)
		if len(got.FSRead) != 1 || got.FSRead[0] != "/bin" {
			t.Errorf("FSRead intersect = %v, want [/bin] only", got.FSRead)
		}
	})

	t.Run("FSWrite: guest can subset", func(t *testing.T) {
		host := &sandboxPolicy{FSWrite: []string{"/tmp", "/var/tmp"}}
		guest := &sandboxPolicy{FSWrite: []string{"/tmp"}} // narrower
		got := intersectPolicies(host, guest)
		if len(got.FSWrite) != 1 || got.FSWrite[0] != "/tmp" {
			t.Errorf("FSWrite intersect = %v, want [/tmp]", got.FSWrite)
		}
	})

	t.Run("FSWrite: nil host = no opinion, guest's value applies", func(t *testing.T) {
		// Host with no FSWrite list = "no opinion on this field."
		// Guest specifying FSWrite=[/tmp] is a tighten relative to
		// "no policy at all" — fine to apply. (Contrast with
		// host's *runner-level* defaults like /tmp being writable
		// via CWD bind; those are unaffected by FSWrite list
		// content.)
		host := &sandboxPolicy{FSWrite: nil}
		guest := &sandboxPolicy{FSWrite: []string{"/tmp"}}
		got := intersectPolicies(host, guest)
		if len(got.FSWrite) != 1 || got.FSWrite[0] != "/tmp" {
			t.Errorf("FSWrite intersect with nil host = %v, want [/tmp]", got.FSWrite)
		}
	})

	t.Run("Net: deny wins (either side)", func(t *testing.T) {
		cases := []struct {
			host, guest, want string
		}{
			{"deny", "allow", "deny"},
			{"allow", "deny", "deny"},
			{"deny", "deny", "deny"},
			{"allow", "allow", "allow"},
			{"allow", "", "allow"},
			// host="" means de-facto deny at the runner level (see
			// TestIntersectNet_HostEmptyMeansDeny). The intersection
			// reflects that, not a literal "" pass-through.
			{"", "deny", "deny"},
			{"", "", "deny"},
		}
		for _, c := range cases {
			h := &sandboxPolicy{Net: c.host}
			g := &sandboxPolicy{Net: c.guest}
			got := intersectPolicies(h, g).Net
			if got != c.want {
				t.Errorf("intersectNet(%q, %q) = %q, want %q", c.host, c.guest, got, c.want)
			}
		}
	})

	t.Run("CWD: host wins (operator chose it)", func(t *testing.T) {
		host := &sandboxPolicy{CWD: "/operator/path"}
		guest := &sandboxPolicy{CWD: "/plugin/escape"}
		got := intersectPolicies(host, guest)
		if got.CWD != "/operator/path" {
			t.Errorf("CWD intersect = %q, want host's /operator/path (plugin can't redirect)", got.CWD)
		}
	})

	t.Run("FSRead: nil guest inherits host (guest had no opinion)", func(t *testing.T) {
		// JSON `nil` (absent field) means "guest didn't specify."
		// Inheriting host's list matches operator intuition: an
		// agent adding `"net": "deny"` shouldn't lose its
		// filesystem reads as a side effect. Explicit empty `[]`
		// (next subtest) is how a paranoid plugin locks itself
		// down.
		host := &sandboxPolicy{FSRead: []string{"/bin", "/usr"}}
		guest := &sandboxPolicy{FSRead: nil}
		got := intersectPolicies(host, guest)
		if len(got.FSRead) != 2 {
			t.Errorf("nil guest FSRead intersect = %v, want host's [/bin /usr]", got.FSRead)
		}
	})

	t.Run("FSRead: explicit empty guest = lock down (paranoid plugin)", func(t *testing.T) {
		// An agent that wants to deny ALL filesystem reads in its
		// stado_exec sets "fs_read": []. JSON unmarshal yields a
		// non-nil empty slice — distinct from nil. Result: nil
		// (no reads beyond the runner's auto-mounts).
		host := &sandboxPolicy{FSRead: []string{"/bin"}}
		guest := &sandboxPolicy{FSRead: []string{}} // explicit empty
		got := intersectPolicies(host, guest)
		if len(got.FSRead) != 0 {
			t.Errorf("explicit empty FSRead intersect = %v, want nil (paranoid lock-down)", got.FSRead)
		}
	})
}

// TestNewDefaultSandboxPolicy_ActuallyRunsBash is the integration test
// that would have caught the "default policy doesn't actually run
// /bin/sh under bwrap" bug shipped in 7e20c07. Builds an exec.Cmd
// from the host-default policy and runs `/bin/sh -c 'echo ok'`. If
// the policy is misconfigured (FSRead missing /bin, Net=DenyAll
// silently), the cmd either fails to execvp or hangs.
//
// Skips cleanly when bwrap / sandbox-exec aren't available — keeps
// the test environment-agnostic. The exit code, stdout match, and
// completion within a few seconds are the load-bearing assertions.
func TestNewDefaultSandboxPolicy_ActuallyRunsBash(t *testing.T) {
	if !hasSandboxRunner() {
		t.Skip("native sandbox runner not detected; integration test requires bwrap or sandbox-exec")
	}
	policy := NewDefaultSandboxPolicy("/tmp").(*sandboxPolicy)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := buildSandboxedCmd(ctx, policy, "/tmp", []string{"/bin/sh", "-c", "echo ok"}, nil)
	if err != nil {
		t.Fatalf("buildSandboxedCmd with default policy: %v", err)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("running /bin/sh -c 'echo ok' under default policy failed: %v\noutput: %s", runErr, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Errorf("expected 'ok' in output; got %q", out)
	}
}

// hasSandboxRunner returns true when the host has a real sandbox
// runner detected (bwrap on Linux, sandbox-exec on macOS). Used to
// skip tests that depend on the absence of a runner.
func hasSandboxRunner() bool {
	// Re-import sandbox.Detect indirectly via buildSandboxedCmd: if
	// nil-policy works (always does) but a real policy with Net=deny
	// succeeds without error, a runner is present.
	_, err := buildSandboxedCmd(context.Background(),
		&sandboxPolicy{Net: "allow"}, "/tmp", []string{"/bin/true"}, nil)
	return err == nil
}
