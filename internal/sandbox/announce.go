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
// process. Package-scoped (not per-entry-point) so a process that calls
// the helper from multiple entry points in sequence still warns at most
// once. Subprocesses are a different OS process and obviously do not
// share this state — each warns independently if it reaches the helper.
var announceOnce sync.Once

// WarnIfHostUnsandboxed emits a one-time stderr warning describing the
// host process's containment posture. Three branches:
//
//   - cfg.Mode = "wrap" but we are NOT the wrapped child — flags the
//     gap that today only `stado run` calls [MaybeRewrap], so launching
//     the TUI / headless / session-resume with mode=wrap leaves those
//     code paths unwrapped.
//   - cfg.Mode = "external" but the process is NOT wrapped (per
//     [RewrappedEnvVar] / [looksWrapped]) — operator claims to handle
//     wrapping externally but didn't. Only `stado run` validates this
//     today via [MaybeRewrap]; the other entry points need the warning.
//   - cfg.Mode = "off" or empty (the koanf default) — host is
//     unsandboxed; points at the config knob.
//
// Suppressed when:
//
//   - [RewrappedEnvVar] = "1" — we ARE the wrapped child; the sandbox
//     is active around us, no warning needed.
//   - [SuppressEnvVar] = "1" — operator/CI/test opt-out.
//   - cfg.Mode = "external" AND the process IS wrapped — the operator's
//     external setup is honored.
//
// Why one warning per process instead of one per spawn: stado spawns
// subprocesses from many call sites (TUI shell, plugin runners, LSP
// servers, daemon, hooks, schedule, MCP wrappers, ACP providers). A
// per-spawn warning would flood; a single startup warning conveys the
// same information once.
func WarnIfHostUnsandboxed(cfg WrapConfig) {
	announceOnce.Do(func() {
		for _, line := range HostUnsandboxedLines(cfg) {
			fmt.Fprintln(os.Stderr, line)
		}
	})
}

// HostUnsandboxedLines returns the warning lines [WarnIfHostUnsandboxed]
// would emit, or nil when no warning applies (wrapped child, suppressed
// via [SuppressEnvVar], or external mode with wrapper evidence). It is
// pure — no I/O, no [announceOnce] gating — so an entry point that owns
// the screen (the TUI, whose alt-screen swallows pre-launch stderr) can
// capture the banner and render it in-band instead of losing it. The
// stderr path ([WarnIfHostUnsandboxed]) and the in-band path must show
// the same text; keep this the single source of both.
func HostUnsandboxedLines(cfg WrapConfig) []string {
	if os.Getenv(RewrappedEnvVar) == "1" {
		return nil // wrapped child — sandbox active around us
	}
	if os.Getenv(SuppressEnvVar) == "1" {
		return nil
	}
	switch cfg.Mode {
	case "wrap":
		// Configured for wrap but we're not the wrapped child. Today
		// only `stado run` calls MaybeRewrap; TUI / session-resume /
		// headless do not. Surface the gap so operators don't assume
		// they're protected when they aren't.
		return []string{
			"stado: warn: [sandbox] mode = \"wrap\" but this entry point did not re-exec into the wrapper.",
			"stado: warn: containment applies only to `stado run` today; TUI / session resume / headless run unwrapped.",
			"stado: warn: suppress with " + SuppressEnvVar + "=1.",
		}
	case "external":
		// Operator claims to be running stado under an external wrapper.
		// Only `stado run` validates this via MaybeRewrap; other entry
		// points don't. If we can't see wrapper evidence (RewrappedEnvVar
		// already returned nil above; looksWrapped checks the
		// cgroup/proc/PID-1 heuristic), warn about the unwrapped entry.
		if looksWrapped() {
			return nil
		}
		return []string{
			"stado: warn: [sandbox] mode = \"external\" but no wrapper evidence detected for this entry point.",
			"stado: warn: only `stado run` validates external-mode wrapping today; TUI / session resume / headless do not.",
			"stado: warn: launch this entry point under your wrapper (bwrap/firejail/sandbox-exec/container), or set mode = \"wrap\" to have stado re-exec itself. Suppress with " + SuppressEnvVar + "=1.",
		}
	default: // "off" or empty
		return []string{
			"stado: warn: running without a process-containment sandbox.",
			"stado: warn: host subprocesses (shell, plugin runners, LSP, daemon, hooks) inherit the host's filesystem and network access.",
			"stado: warn: install a wrapper (bwrap/firejail on Linux, sandbox-exec on macOS) and set [sandbox] mode = \"wrap\" in config.toml; today only `stado run` re-execs. Suppress with " + SuppressEnvVar + "=1.",
		}
	}
}

// resetAnnounceOnceForTest re-arms [announceOnce] so a test can drive
// the warning logic multiple times in one process. Tests-only — the
// sync.Once is intentionally not user-resettable from outside the
// package because re-warning during normal operation would be noise.
func resetAnnounceOnceForTest() {
	announceOnce = sync.Once{}
}
