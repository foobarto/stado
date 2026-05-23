package sandbox

import (
	"errors"
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

// Gemini deep-dive (post-v0.54.0): the CWD block previously
// emitted `(allow process-exec (subpath p.CWD))` alongside the
// file-read* allow. A tool with fs.write access to the workdir
// could drop a script and exec it, bypassing the per-binary
// allowlist that the loop further down emits. The CWD must stay
// readable (dyld + source loading) but NOT exec'able — only the
// binaries operator-declared in Policy.Exec are exec'able. This
// test pins the invariant against re-introduction.
func TestRenderSandboxProfile_CWDNotProcessExecable(t *testing.T) {
	p := Policy{CWD: "/tmp/work"}
	profile := RenderSandboxProfile(p)

	// CWD must be file-read*-allowed for dyld/source.
	if !strings.Contains(profile, `(allow file-read* (subpath "/tmp/work"))`) {
		t.Errorf("CWD should be file-read* allowed:\n%s", profile)
	}
	// CWD must NOT be process-exec subpath-allowed (the sandbox-
	// escape vector — drop binary in CWD, exec it).
	if strings.Contains(profile, `(allow process-exec (subpath "/tmp/work"))`) {
		t.Errorf("CWD subpath process-exec re-introduced — sandbox escape via fs.write+exec:\n%s", profile)
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

// Basename entries in Policy.Exec must be resolved to absolute paths
// before going into the `(literal ...)` predicate — sandbox-exec
// matches against the executed binary's absolute path, not the
// basename. Otherwise an `exec:git` capability yields a literal of
// "git" which never matches /usr/bin/git and the tool is denied at
// runtime.
//
// Codex P1 + Copilot ×2 caught this regression introduced when the
// wildcard was removed. Stubbing execLookPath keeps the test
// host-independent (no real /usr/bin lookup, no cross-platform skew).
func TestRenderSandboxProfile_BasenameResolvedToAbsolute(t *testing.T) {
	orig := execLookPath
	defer func() { execLookPath = orig }()
	stub := map[string]string{
		"git": "/usr/bin/git",
		"sh":  "/bin/sh",
	}
	execLookPath = func(name string) (string, error) {
		if abs, ok := stub[name]; ok {
			return abs, nil
		}
		return "", errors.New("not found")
	}

	p := Policy{Exec: []string{"git", "sh"}}
	profile := RenderSandboxProfile(p)

	for _, want := range []string{
		`(allow process-exec (literal "/usr/bin/git"))`,
		`(allow process-exec (literal "/bin/sh"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing resolved-basename allow %q:\n%s", want, profile)
		}
	}
	for _, unwanted := range []string{
		`(allow process-exec (literal "git"))`,
		`(allow process-exec (literal "sh"))`,
	} {
		if strings.Contains(profile, unwanted) {
			t.Errorf("profile contains unresolved basename %q — sandbox-exec literal won't match the executed absolute path:\n%s", unwanted, profile)
		}
	}
}

// Absolute paths in Policy.Exec pass through unchanged (don't go
// through LookPath). Operator-declared absolute paths should be honored
// literally.
func TestRenderSandboxProfile_AbsolutePathsPassThrough(t *testing.T) {
	orig := execLookPath
	defer func() { execLookPath = orig }()
	execLookPath = func(name string) (string, error) {
		t.Errorf("execLookPath should not be called for absolute paths; got %q", name)
		return "", errors.New("unexpected")
	}

	p := Policy{Exec: []string{"/opt/custom/bin/tool"}}
	profile := RenderSandboxProfile(p)

	want := `(allow process-exec (literal "/opt/custom/bin/tool"))`
	if !strings.Contains(profile, want) {
		t.Errorf("profile missing absolute-path allow %q:\n%s", want, profile)
	}
}

// LookPath failure on a basename must emit the entry as-given (will
// fail to match at runtime), NOT skip the rule entirely. Skipping
// would silently widen the sandbox by removing a deny — a typo
// should fail loudly at exec time, not become "allow everything else."
func TestRenderSandboxProfile_LookupFailureEmitsAsGiven(t *testing.T) {
	orig := execLookPath
	defer func() { execLookPath = orig }()
	execLookPath = func(name string) (string, error) {
		return "", errors.New("not found")
	}

	p := Policy{Exec: []string{"nonexistent-binary-xyzzy"}}
	profile := RenderSandboxProfile(p)

	want := `(allow process-exec (literal "nonexistent-binary-xyzzy"))`
	if !strings.Contains(profile, want) {
		t.Errorf("profile must emit unresolved entry as-given so the rule deny-fails predictably:\n%s", profile)
	}
}
