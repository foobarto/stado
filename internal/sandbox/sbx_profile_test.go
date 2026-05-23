package sandbox

import (
	"strings"
	"testing"
)

// TestRenderSandboxProfile_NoBlanketProcessExec is the load-bearing
// assertion for the macOS exec-allowlist invariant: the generated .sb
// profile must NOT contain the wildcard `(allow process-exec*)` rule.
// sandbox-exec uses union semantics — a single `(allow process-exec*)`
// cannot be narrowed by per-binary allow rules — so the wildcard
// silently nullifies the per-binary allowlist that the loop at the
// bottom of RenderSandboxProfile emits.
//
// Regression test for Codex finding #142 ("macOS sandbox profile
// allows unrestricted process exec"). If anyone re-adds the wildcard
// "to make /bin/sh work," they must instead declare /bin/sh in
// Policy.Exec; this test will catch the regression.
func TestRenderSandboxProfile_NoBlanketProcessExec(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
	}{
		{"empty", Policy{}},
		{"denyAll", DenyAll()},
		{"readOnlyFS", ReadOnlyFS("/etc")},
		{"withExec", Policy{Exec: []string{"/bin/sh"}}},
		{"withCWD", Policy{CWD: "/tmp/x"}},
		{"netAllowAll", Policy{Net: NetPolicy{Kind: NetAllowAll}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := RenderSandboxProfile(c.p)
			if strings.Contains(profile, "(allow process-exec*)") {
				t.Errorf("profile contains blanket `(allow process-exec*)` — this defeats the per-binary allowlist:\n%s", profile)
			}
		})
	}
}

// Per-binary allowlist entries from Policy.Exec must still appear in
// the profile. The fix for #142 dropped the wildcard but kept the
// per-binary loop; verify the per-binary path is intact and the literal
// form (`(allow process-exec (literal "..."))`) is what sandbox-exec
// expects.
func TestRenderSandboxProfile_PerBinaryAllowlist(t *testing.T) {
	p := Policy{Exec: []string{"/bin/sh", "/usr/bin/git"}}
	profile := RenderSandboxProfile(p)
	for _, want := range []string{
		`(allow process-exec (literal "/bin/sh"))`,
		`(allow process-exec (literal "/usr/bin/git"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing per-binary allow %q:\n%s", want, profile)
		}
	}
}

// process-fork must remain unconditionally allowed — without it the
// wrapped command can't fork at all, which breaks even single-process
// commands that internally fork for IPC or job control. process-fork
// is not equivalent to process-exec; a fork only duplicates the
// current process image.
func TestRenderSandboxProfile_ProcessForkAllowed(t *testing.T) {
	profile := RenderSandboxProfile(Policy{})
	if !strings.Contains(profile, "(allow process-fork)") {
		t.Errorf("profile missing `(allow process-fork)` — wrapped command can't fork:\n%s", profile)
	}
}

// Deny-default plus zero allow-rules means the profile is the most
// restrictive possible: no FS, no exec (other than CWD subpath if set),
// no net. Confirm the deny-default line is emitted.
func TestRenderSandboxProfile_DenyDefault(t *testing.T) {
	profile := RenderSandboxProfile(Policy{})
	if !strings.Contains(profile, "(deny default)") {
		t.Errorf("profile missing `(deny default)`:\n%s", profile)
	}
}
