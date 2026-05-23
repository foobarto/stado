package sandbox

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for a pipe, runs fn, restores os.Stderr,
// and returns what fn wrote. Restoration is immediate via defer (NOT
// t.Cleanup) — otherwise a second captureStderr call in the same test
// would race against a still-installed pipe-writer from the prior call
// after fn returns, and any post-fn writes would land in a closed fd.
// The pipe reader and writer are both closed explicitly to avoid fd
// leaks across the test binary's many subtests.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	defer r.Close()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy pipe: %v", err)
	}
	return buf.String()
}

// clearEnv removes the two env vars the announce path consults so each
// test starts from a known-clean baseline regardless of what the
// invoking shell exported.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(RewrappedEnvVar, "")
	t.Setenv(SuppressEnvVar, "")
	_ = os.Unsetenv(RewrappedEnvVar)
	_ = os.Unsetenv(SuppressEnvVar)
}

func TestWarnIfHostUnsandboxed_modeOff_emits(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})

	if !strings.Contains(out, "running without a process-containment sandbox") {
		t.Errorf("expected unsandboxed warning, got: %q", out)
	}
	if !strings.Contains(out, SuppressEnvVar) {
		t.Errorf("expected suppress-env-var hint mentioning %s, got: %q", SuppressEnvVar, out)
	}
}

// Empty-mode is the koanf default for [sandbox.mode]. Operators who
// never wrote a [sandbox] section get this. Must warn — that's the
// majority of installs today.
func TestWarnIfHostUnsandboxed_modeEmpty_emits(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: ""})
	})

	if !strings.Contains(out, "running without a process-containment sandbox") {
		t.Errorf("expected unsandboxed warning for empty mode, got: %q", out)
	}
}

// mode=wrap from a non-rewrapped parent: flags the gap that today only
// `stado run` re-execs. The message must NOT use the default off-mode
// wording — that would mislead operators who DID configure wrap.
func TestWarnIfHostUnsandboxed_modeWrapNotRewrapped_flagsGap(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "wrap"})
	})

	if !strings.Contains(out, "did not re-exec into the wrapper") {
		t.Errorf("expected wrap-gap warning, got: %q", out)
	}
	if strings.Contains(out, "running without a process-containment sandbox") {
		t.Errorf("wrap-gap message should not use off-mode wording, got: %q", out)
	}
}

// Wrapped child of mode=wrap: the sandbox IS active around us. No
// warning — that would be a lie.
func TestWarnIfHostUnsandboxed_modeWrapInsideChild_silent(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	t.Setenv(RewrappedEnvVar, "1")

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "wrap"})
	})

	if out != "" {
		t.Errorf("expected silence inside wrapped child, got: %q", out)
	}
}

// mode=external + wrapper-evidence present (STADO_REWRAPPED=1):
// operator's external setup is honored, silent.
func TestWarnIfHostUnsandboxed_modeExternal_wrapped_silent(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	t.Setenv(RewrappedEnvVar, "1")

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "external"})
	})

	if out != "" {
		t.Errorf("expected silence for mode=external inside wrapped child, got: %q", out)
	}
}

// mode=external + no wrapper evidence: only `stado run` validates this
// via MaybeRewrap; the TUI / headless / session-resume entry points
// reach this helper without re-execing. Warn so the operator notices
// their claimed external wrap isn't actually present.
func TestWarnIfHostUnsandboxed_modeExternal_unwrapped_warns(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	// Note: looksWrapped() may return true on a CI host that itself runs
	// inside a container — that would make this test skip the warning
	// branch and fail. Real CI is documented as expecting either branch
	// to be acceptable; the assertion here is for the dev-machine path
	// where neither marker is set and looksWrapped() returns false.
	if looksWrapped() {
		t.Skip("looksWrapped() returns true on this host; can't exercise the unwrapped-external warn branch")
	}

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "external"})
	})

	if !strings.Contains(out, "mode = \"external\" but no wrapper evidence") {
		t.Errorf("expected external-no-evidence warning, got: %q", out)
	}
}

// Wrapped child even under mode=off: rewrap marker wins. This case
// shouldn't happen in practice (mode=off skips the rewrap entirely),
// but defending the invariant means a future regression that sets the
// marker too eagerly doesn't bury the warning everywhere.
func TestWarnIfHostUnsandboxed_rewrappedChildOverridesModeOff(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	t.Setenv(RewrappedEnvVar, "1")

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})

	if out != "" {
		t.Errorf("rewrap marker should suppress warning, got: %q", out)
	}
}

func TestWarnIfHostUnsandboxed_suppressEnvVar_silent(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	t.Setenv(SuppressEnvVar, "1")

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})

	if out != "" {
		t.Errorf("expected suppression via env var, got: %q", out)
	}
}

// Suppression only kicks in when the value is exactly "1". A value of
// "0", "false", or unset must NOT suppress — otherwise a stale "false"
// from a wrapper script would silently disable the warning forever.
func TestWarnIfHostUnsandboxed_suppressEnvVar_nonOneValue_stillEmits(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)
	t.Setenv(SuppressEnvVar, "false")

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})

	if !strings.Contains(out, "running without a process-containment sandbox") {
		t.Errorf("expected warning when SuppressEnvVar=\"false\" (only \"1\" suppresses), got: %q", out)
	}
}

// sync.Once is process-wide. Three calls must produce exactly one
// warning block, not three. This is the load-bearing property — without
// it, every spawn site reaching for the helper would flood stderr.
func TestWarnIfHostUnsandboxed_onlyOnce(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)

	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})

	if got := strings.Count(out, "running without a process-containment sandbox"); got != 1 {
		t.Errorf("expected exactly 1 warning across 3 calls, got %d in: %q", got, out)
	}
}

// A subsequent call with a different mode after the first emission must
// still be a no-op: the sync.Once has fired. Re-warning under a new mode
// would be confusing ("which mode is actually active?").
func TestWarnIfHostUnsandboxed_modeChangeAfterFirstEmission_silent(t *testing.T) {
	resetAnnounceOnceForTest()
	clearEnv(t)

	_ = captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "off"})
	})
	// Second call with mode=wrap must NOT emit the wrap-gap message.
	out := captureStderr(t, func() {
		WarnIfHostUnsandboxed(WrapConfig{Mode: "wrap"})
	})

	if out != "" {
		t.Errorf("expected silence on second call regardless of mode, got: %q", out)
	}
}
