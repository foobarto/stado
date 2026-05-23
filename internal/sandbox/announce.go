package sandbox

import (
	"fmt"
	"os"
	"sync"
)

// SuppressEnvVar disables the unsandboxed-host warning. Intended for
// CI/test invocations where the operator already knows the host isn't
// sandboxed and the warning is noise.
const SuppressEnvVar = "STADO_SUPPRESS_SANDBOX_WARN"

// announceOnce gates the unsandboxed warning to exactly one emission per
// process. The sync.Once is package-scoped (not per-entry-point) so a
// process that hits more than one entry — e.g. headless command that
// later spawns a TUI subprocess — still warns at most once.
var announceOnce sync.Once

// WarnIfHostUnsandboxed emits a one-time stderr warning describing the
// host process's containment posture. Suppressed when:
//
//   - [RewrappedEnvVar] = "1" — we ARE the wrapped child; the sandbox
//     is active around us, no warning needed.
//   - [SuppressEnvVar] = "1" — operator/CI/test opt-out.
//   - cfg.Mode = "external" — operator runs stado under their own
//     wrapper; [MaybeRewrap] already validates and errors if missing.
//
// When cfg.Mode = "wrap" but we are NOT the wrapped child, the warning
// flags the gap: today only `stado run` calls [MaybeRewrap], so launching
// the TUI / headless / session-resume with mode=wrap leaves those code
// paths unwrapped. The operator needs to know.
//
// When cfg.Mode is "off" (or empty — the koanf default), the warning
// states the host is unsandboxed and points at the config knob to enable
// containment.
//
// Why one warning per process instead of one per spawn: stado spawns
// subprocesses from many call sites (TUI shell, plugin runners, LSP
// servers, daemon, hooks, schedule, MCP wrappers, ACP providers). A
// per-spawn warning would flood; a single startup warning conveys the
// same information once.
func WarnIfHostUnsandboxed(cfg WrapConfig) {
	announceOnce.Do(func() {
		if os.Getenv(RewrappedEnvVar) == "1" {
			return // wrapped child — sandbox active around us
		}
		if os.Getenv(SuppressEnvVar) == "1" {
			return
		}
		switch cfg.Mode {
		case "wrap":
			// Configured for wrap but we're not the wrapped child. Today
			// only `stado run` calls MaybeRewrap; TUI / session-resume /
			// headless do not. Surface the gap so operators don't assume
			// they're protected when they aren't.
			fmt.Fprintln(os.Stderr, "stado: warn: [sandbox] mode = \"wrap\" but this entry point did not re-exec into the wrapper.")
			fmt.Fprintln(os.Stderr, "stado: warn: containment applies only to `stado run` today; TUI / session resume / headless run unwrapped.")
			fmt.Fprintln(os.Stderr, "stado: warn: suppress with "+SuppressEnvVar+"=1.")
		case "external":
			return // operator manages sandboxing externally
		default: // "off" or empty
			fmt.Fprintln(os.Stderr, "stado: warn: running without a process-containment sandbox.")
			fmt.Fprintln(os.Stderr, "stado: warn: host subprocesses (shell, plugin runners, LSP, daemon, hooks) inherit the host's filesystem and network access.")
			fmt.Fprintln(os.Stderr, "stado: warn: install bwrap and set [sandbox] mode = \"wrap\" in stado.toml; today only `stado run` re-execs. Suppress with "+SuppressEnvVar+"=1.")
		}
	})
}

// resetAnnounceOnceForTest re-arms [announceOnce] so a test can drive
// the warning logic multiple times in one process. Tests-only — the
// sync.Once is intentionally not user-resettable from outside the
// package because re-warning during normal operation would be noise.
func resetAnnounceOnceForTest() {
	announceOnce = sync.Once{}
}
