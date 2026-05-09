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

	// 1. Guest non-nil: guest always wins, even if host has a default.
	if got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: hostDefault}, guest); got != guest {
		t.Errorf("guest-supplied policy should win; got %+v", got)
	}
	if got := resolveSandboxPolicy(&Host{}, guest); got != guest {
		t.Errorf("guest-supplied policy with no host default: got %+v", got)
	}

	// 2. Guest nil + host default set: host default applies.
	if got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: hostDefault}, nil); got != hostDefault {
		t.Errorf("nil guest + host default: want host default, got %+v", got)
	}

	// 3. Guest nil + host default nil: legacy unsandboxed (returns nil).
	if got := resolveSandboxPolicy(&Host{}, nil); got != nil {
		t.Errorf("both nil: want nil (unsandboxed); got %+v", got)
	}

	// 4. Defensive: host default of wrong type returns nil rather than
	//    panicking. A misconfigured entry point shouldn't crash the
	//    runtime.
	if got := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: "not a policy"}, nil); got != nil {
		t.Errorf("wrong-typed host default: want nil, got %+v", got)
	}

	// 5. Nil host (defensive — shouldn't happen in production but the
	//    helper handles it).
	if got := resolveSandboxPolicy(nil, nil); got != nil {
		t.Errorf("nil host: want nil, got %+v", got)
	}
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

// TestResolveSandboxPolicy_UnsandboxedOptOut: a guest plugin that
// sets `sandbox.unsandboxed=true` opts out of any host-default
// policy. The resolver returns nil, which buildSandboxedCmd then
// runs unsandboxed. Without an explicit Unsandboxed field, JSON
// unmarshal can't distinguish "absent" from "explicit null" — both
// yield (*sandboxPolicy)(nil), which the resolver treats as "use
// host default." Caught in 2026-05-09 second pass; the CHANGELOG
// claim that you could "set sandbox: null to opt out" was false.
func TestResolveSandboxPolicy_UnsandboxedOptOut(t *testing.T) {
	hostDefault := NewDefaultSandboxPolicy("/host")
	host := &Host{DefaultSandboxPolicy: hostDefault}

	// Absent guest: host default applies.
	if got := resolveSandboxPolicy(host, nil); got == nil {
		t.Errorf("absent guest with host default: want host default, got nil")
	}

	// Explicit Unsandboxed=true: bypass host default.
	guest := &sandboxPolicy{Unsandboxed: true}
	if got := resolveSandboxPolicy(host, guest); got != nil {
		t.Errorf("Unsandboxed=true: want nil (bypass host default); got %+v", got)
	}

	// Unsandboxed=true with other fields: still bypass. Operator
	// might fill the struct out of habit; the flag still wins.
	guest = &sandboxPolicy{Unsandboxed: true, Net: "deny", FSRead: []string{"/etc"}}
	if got := resolveSandboxPolicy(host, guest); got != nil {
		t.Errorf("Unsandboxed=true with fields: want nil; got %+v", got)
	}
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
