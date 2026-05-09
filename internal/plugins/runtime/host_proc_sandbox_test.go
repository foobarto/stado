package runtime

import (
	"context"
	"strings"
	"testing"
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

// TestNewDefaultSandboxPolicy_ShapeIsRunner confirms the host-default
// builder produces a policy that resolveSandboxPolicy accepts and
// hands to buildSandboxedCmd. We don't run the resulting cmd here
// (that requires bwrap detection); the shape check + roundtrip is
// the load-bearing contract.
func TestNewDefaultSandboxPolicy_ShapeIsRunner(t *testing.T) {
	policy := NewDefaultSandboxPolicy("/some/workdir")
	if policy == nil {
		t.Fatal("NewDefaultSandboxPolicy returned nil")
	}
	resolved := resolveSandboxPolicy(&Host{DefaultSandboxPolicy: policy}, nil)
	if resolved == nil {
		t.Fatal("resolveSandboxPolicy didn't accept the default policy")
	}
	if resolved.CWD != "/some/workdir" {
		t.Errorf("default policy CWD = %q, want /some/workdir", resolved.CWD)
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
