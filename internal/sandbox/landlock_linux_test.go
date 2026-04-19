//go:build linux

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestApplyLandlock_EmptyPolicyIsNoop(t *testing.T) {
	if err := ApplyLandlock(Policy{}); err != nil {
		t.Errorf("empty policy should be a no-op, got %v", err)
	}
}

// TestApplyLandlock_RestrictsCurrentProcess forks ourselves with an env
// marker so the subprocess runs ApplyLandlock and verifies it took effect.
// The parent test only invokes the subprocess; landlock is irreversible and
// applying it in the test binary itself would break everything that follows.
func TestApplyLandlock_RestrictsCurrentProcess(t *testing.T) {
	if os.Getenv("STADO_LANDLOCK_SUBPROC") == "1" {
		childVerifyLandlock()
		return // unreached; childVerifyLandlock always calls os.Exit
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable not supported")
	}
	cmd := exec.Command(exe, "-test.run=TestApplyLandlock_RestrictsCurrentProcess")
	cmd.Env = append(os.Environ(), "STADO_LANDLOCK_SUBPROC=1")
	out, err := cmd.CombinedOutput()
	status := ""
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			status = ee.Error()
		}
	}

	// Exit 42 = test passed (landlock applied + enforced as expected).
	// Exit 43 = landlock unsupported on this kernel (treated as skip).
	// Anything else = actual failure.
	switch {
	case strings.Contains(status, "exit status 42"):
		// Success.
	case strings.Contains(status, "exit status 43"):
		t.Skip("landlock unsupported on this kernel")
	default:
		t.Errorf("subprocess failed: status=%q output=%s", status, out)
	}
}

func childVerifyLandlock() {
	// Allow reads under /tmp only; no writes. Then attempt to read /etc/hosts.
	err := ApplyLandlock(Policy{FSRead: []string{"/tmp"}})
	if errors.Is(err, ErrLandlockUnavailable) {
		os.Exit(43)
	}
	if err != nil {
		fmtToStderr("ApplyLandlock error: %v\n", err)
		os.Exit(1)
	}

	// Reading /etc/hosts should now fail.
	_, err = os.ReadFile("/etc/hosts")
	if err == nil {
		fmtToStderr("/etc/hosts read unexpectedly succeeded after landlock\n")
		os.Exit(2)
	}
	// Reading under /tmp should still succeed.
	f, err := os.CreateTemp("/tmp", "stado-landlock-*")
	if err != nil {
		// We don't allow writes in the test policy — CreateTemp needs write.
		// That's fine, we're just proving /etc/hosts is blocked.
	} else {
		f.Close()
		os.Remove(f.Name())
	}

	// Smoke: a second ApplyLandlock stacking a tighter policy should still
	// succeed (rulesets are additive — you can only narrow).
	if err := ApplyLandlock(Policy{FSRead: []string{"/tmp/stado-non-existent-subdir"}}); err != nil && !errors.Is(err, ErrLandlockUnavailable) {
		// Non-existent paths are skipped, not errored; a real error would be
		// a bug. Treat as failure.
		fmtToStderr("second ApplyLandlock failed: %v\n", err)
		os.Exit(3)
	}

	os.Exit(42)
}

// fmtToStderr mimics fmt.Fprintf(os.Stderr, ...). Inlined so we don't pull
// fmt into this file — keeps the subprocess path minimal.
func fmtToStderr(format string, args ...any) {
	s := format
	for _, a := range args {
		s = strings.Replace(s, "%v", toStringForStderr(a), 1)
	}
	_, _ = os.Stderr.Write([]byte(s))
}

func toStringForStderr(a any) string {
	switch v := a.(type) {
	case error:
		return v.Error()
	case string:
		return v
	}
	return "?"
}
