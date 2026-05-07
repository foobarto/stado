//go:build linux || darwin

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Phase 1.2 of the 2026-Q2 refactor program: contract tests for
// the sandbox.Runner layer wired in production today.
//
// Tier 1 (every available runner): command construction +
// exec-allow-list error path. No subprocess executed.
//
// Tier 2 (only runners that enforce policy at runtime — bwrap on
// Linux, sandbox-exec on macOS): runs the built command and
// asserts the kernel-level denial actually fires. NoneRunner has
// no Tier 2 (it enforces nothing).
//
// Multi-layer composition (landlock + seccomp + bwrap stacked) is
// out of scope here — composition isn't wired in production today.
// Park as a separate spec when that integration lands.

// availableRunners returns every runner this host can use, in
// detect order. Wraps the Detect/detectList machinery so tests
// can iterate over each runner consistently.
func availableRunners(t *testing.T) []Runner {
	t.Helper()
	out := []Runner{NoneRunner{}}
	for _, r := range detectList() {
		// detectList already includes NoneRunner; skip it to avoid
		// double-running.
		if _, ok := r.(NoneRunner); ok {
			continue
		}
		if r.Available() {
			out = append(out, r)
		} else {
			t.Logf("skipping %s: Available()=false on this host", r.Name())
		}
	}
	return out
}

// enforcingRunners returns the subset of availableRunners that
// actually enforce Policy at runtime (FS, net) — bwrap on Linux,
// sandbox-exec on macOS. NoneRunner is excluded.
func enforcingRunners(t *testing.T) []Runner {
	t.Helper()
	var out []Runner
	for _, r := range availableRunners(t) {
		if _, ok := r.(NoneRunner); ok {
			continue
		}
		out = append(out, r)
	}
	return out
}

// tier2ReadyRunners returns the subset of enforcingRunners that
// can actually run a benign command on this host. `Available()`
// only checks the wrapper binary is on PATH — but bwrap requires
// user-namespace setup the kernel may refuse (nested containers,
// locked-down sysctls), and sandbox-exec may be present but
// disabled. The probe runs `true` under default-allow; runners
// that fail are skipped from Tier 2 with a clear log line.
//
// This avoids false-fails on hosts where the runner binary is
// installed but the kernel/system rejects its namespace requests
// — codex CI flagged this on round-4 review.
func tier2ReadyRunners(t *testing.T) []Runner {
	t.Helper()
	var out []Runner
	for _, r := range enforcingRunners(t) {
		cmd, err := r.Command(context.Background(), Policy{
			FSRead:  []string{"/"},
			FSWrite: []string{"/tmp"},
		}, "true", nil, nil)
		if err != nil {
			t.Logf("skipping %s tier 2: Command probe failed: %v", r.Name(), err)
			continue
		}
		if err := cmd.Run(); err != nil {
			t.Logf("skipping %s tier 2: probe Run failed: %v (runner present but host environment can't use it)", r.Name(), err)
			continue
		}
		out = append(out, r)
	}
	return out
}

// pickShell returns an absolute path to a POSIX shell suitable for
// `-c "..."` invocations under the configured Tier-2 runners. Falls
// back to t.Skip if no usable shell can be found.
func pickShell(t *testing.T) string {
	t.Helper()
	candidates := []string{"/usr/bin/sh", "/bin/sh"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("no /bin/sh or /usr/bin/sh found; cannot run subprocess tier-2 tests")
	return ""
}

// ---- Tier 1: command construction + exec allow-list ---------------------

// TestRunnerContract_T1_CommandShape: every available runner
// returns a non-nil *exec.Cmd for a valid name + empty allow-list.
// The exact wrapper flags are runner-specific (bwrap vs sandbox-
// exec vs no wrapper); the contract is just "you get something
// runnable back."
func TestRunnerContract_T1_CommandShape(t *testing.T) {
	for _, r := range availableRunners(t) {
		t.Run(r.Name(), func(t *testing.T) {
			cmd, err := r.Command(context.Background(), Policy{},
				"true", nil, nil)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if cmd == nil {
				t.Fatal("Command returned nil cmd, no error")
			}
			if cmd.Path == "" {
				t.Errorf("cmd.Path is empty")
			}
			if len(cmd.Args) == 0 {
				t.Errorf("cmd.Args is empty")
			}
		})
	}
}

// TestRunnerContract_T1_ExecAllowList_DeniesUnknown: when Policy
// declares Exec=[<allowed>] and the caller asks for a different
// name, the runner returns sandbox.Denied — no subprocess built.
// All runners share ResolveBinary, so this is a contract every
// runner satisfies identically.
func TestRunnerContract_T1_ExecAllowList_DeniesUnknown(t *testing.T) {
	for _, r := range availableRunners(t) {
		t.Run(r.Name(), func(t *testing.T) {
			cmd, err := r.Command(context.Background(),
				Policy{Exec: []string{"echo"}},
				"cat", nil, nil)
			if err == nil {
				t.Fatalf("expected Denied, got cmd=%v", cmd)
			}
			var denied Denied
			if !errors.As(err, &denied) {
				t.Fatalf("error type = %T (%v), want sandbox.Denied", err, err)
			}
			if denied.Reason == "" {
				t.Errorf("Denied.Reason is empty")
			}
			if cmd != nil {
				t.Errorf("cmd should be nil on denial, got %v", cmd)
			}
		})
	}
}

// TestRunnerContract_T1_ExecAllowList_PassesListed: when the
// requested name matches an entry in Policy.Exec, the runner
// builds the command without error.
func TestRunnerContract_T1_ExecAllowList_PassesListed(t *testing.T) {
	for _, r := range availableRunners(t) {
		t.Run(r.Name(), func(t *testing.T) {
			cmd, err := r.Command(context.Background(),
				Policy{Exec: []string{"true"}},
				"true", nil, nil)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if cmd == nil {
				t.Fatal("Command returned nil cmd, no error")
			}
		})
	}
}

// TestRunnerContract_T1_ExecAllowList_EmptyAllowsAny: empty
// Exec slice means "no restriction" — any name on PATH builds.
func TestRunnerContract_T1_ExecAllowList_EmptyAllowsAny(t *testing.T) {
	for _, r := range availableRunners(t) {
		t.Run(r.Name(), func(t *testing.T) {
			cmd, err := r.Command(context.Background(), Policy{},
				"true", nil, nil)
			if err != nil {
				t.Fatalf("Command with empty Exec: %v", err)
			}
			if cmd == nil {
				t.Fatal("expected cmd, got nil")
			}
		})
	}
}

// ---- Tier 2: runtime enforcement ----------------------------------------

// TestRunnerContract_T2_NegativeControl: a default-allow policy on
// `true` exits 0 under every runner including NoneRunner. For
// enforcing runners the probe in tier2ReadyRunners has already
// confirmed the runner can spawn a benign subprocess; this test
// re-asserts it cleanly under the standard test name. NoneRunner
// always runs (no probe needed).
func TestRunnerContract_T2_NegativeControl(t *testing.T) {
	cases := []Runner{NoneRunner{}}
	cases = append(cases, tier2ReadyRunners(t)...)
	for _, r := range cases {
		t.Run(r.Name(), func(t *testing.T) {
			cmd, err := r.Command(context.Background(), Policy{
				FSRead:  []string{"/"},
				FSWrite: []string{"/tmp"},
			}, "true", nil, nil)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if err := cmd.Run(); err != nil {
				t.Fatalf("Run: %v (stderr handled by Run)", err)
			}
		})
	}
}

// TestRunnerContract_T2_FSWriteDenied: enforcing runners (bwrap /
// sandbox-exec) refuse a write to a path not listed in
// Policy.FSWrite. The denial surfaces as a non-zero subprocess
// exit, not as an error from Command — Tier 2 enforcement happens
// inside the spawned process.
func TestRunnerContract_T2_FSWriteDenied(t *testing.T) {
	runners := tier2ReadyRunners(t)
	if len(runners) == 0 {
		t.Skipf("no enforcing runner ready on %s (binary missing or environment blocks it)", runtime.GOOS)
	}
	shell := pickShell(t)

	for _, r := range runners {
		t.Run(r.Name(), func(t *testing.T) {
			allow := t.TempDir() // listed in FSWrite
			deny := t.TempDir()  // NOT listed
			target := filepath.Join(deny, "should-fail")

			cmd, err := r.Command(context.Background(), Policy{
				// /tmp must be readable for the shell to walk into
				// the tempdir; the deny tempdir is reachable but
				// not writable.
				FSRead:  []string{"/tmp"},
				FSWrite: []string{allow},
				Exec:    []string{filepath.Base(shell)},
			}, filepath.Base(shell), []string{"-c",
				"printf hi > " + target}, nil)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			runErr := cmd.Run()
			if runErr == nil {
				t.Fatalf("expected non-zero exit (write to %s should be denied), got nil", target)
			}
			if _, ok := runErr.(*exec.ExitError); !ok {
				t.Fatalf("error type = %T (%v), want *exec.ExitError", runErr, runErr)
			}
			// Verify the file was NOT created (host-visible side).
			if _, statErr := os.Stat(target); statErr == nil {
				t.Errorf("target file %q was created despite denial", target)
			}
		})
	}
}

// TestRunnerContract_T2_FSWriteAllowed: same shape as the denied
// case but the target is inside Policy.FSWrite. Subprocess exits 0
// and the file is created.
func TestRunnerContract_T2_FSWriteAllowed(t *testing.T) {
	runners := tier2ReadyRunners(t)
	if len(runners) == 0 {
		t.Skipf("no enforcing runner ready on %s (binary missing or environment blocks it)", runtime.GOOS)
	}
	shell := pickShell(t)

	for _, r := range runners {
		t.Run(r.Name(), func(t *testing.T) {
			allow := t.TempDir()
			target := filepath.Join(allow, "should-succeed")

			cmd, err := r.Command(context.Background(), Policy{
				FSRead:  []string{"/tmp"},
				FSWrite: []string{allow},
				Exec:    []string{filepath.Base(shell)},
			}, filepath.Base(shell), []string{"-c",
				"printf hi > " + target}, nil)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if err := cmd.Run(); err != nil {
				t.Fatalf("Run: %v", err)
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("target file %q not created: %v", target, err)
			}
			if string(data) != "hi" {
				t.Errorf("target contents = %q, want %q", data, "hi")
			}
		})
	}
}
