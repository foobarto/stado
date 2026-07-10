package main

// broker_client.go — shared helper used by orchestrator entry points
// (stado run, TUI, stado run --headless, stado acp, stado mcp-server) to
// attach to the broker and create a session.
//
// The returned ceiling is converted to runtime.ExecutorSandbox once at each
// entry point and passed into every executor factory owned by that surface.
//
// Auto-spawn behavior matches dispatchViaDaemon in tool_run_daemon.go:
// fast-path dial, on failure spawn `stado daemon start --quiet`
// detached, poll until socket appears. Test binaries are refused by
// daemon.EnsureRunning (Go test binary detection); for those, this
// helper returns a "skipped" BrokerSession so existing entry-point
// tests don't need to spawn a daemon.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tui"
)

// inlineTUIRunner is the function launchInlineTUI uses to boot the TUI. It is a
// var (defaulting to tui.Run) so tests can capture the notices and executor
// sandbox policy without booting the real alt-screen TUI (which blocks on
// stdin/render).
var inlineTUIRunner = tui.Run

// brokerExecutorSandbox turns the process-level broker decision into the
// shared runtime policy used by TUI, run, headless, ACP, and MCP executors.
func brokerExecutorSandbox(bs *BrokerSession, disabled bool) runtime.ExecutorSandbox {
	if bs == nil {
		return runtime.ExecutorSandbox{Disabled: disabled}
	}
	disabled = disabled || bs.Profile == broker.ProfileNoSandbox
	policy := runtime.ExecutorSandbox{Disabled: disabled}
	policy.Ceiling = bs.Ceiling
	policy.EnforceCeiling = !bs.Skipped && !disabled
	return policy
}

// launchInlineTUI attaches to the broker for an inline TUI launch — the bare
// `stado` command AND `stado session resume` — folds the sandbox-mode banner
// into the startup notices, enforces the broker's projected ceiling (the
// credential-dir mask + the forwarded ssh-agent socket), and boots the TUI.
// Both entry points share it so a resumed session gets exactly the same
// sandbox treatment as a fresh one (decision 2026-06-13: ssh-agent passthrough
// scope — previously `session resume` passed a zero ceiling + enforce=false and
// so ran un-fenced). Closes the broker session on return.
func launchInlineTUI(ctx context.Context, cfg *config.Config, notices []string) error {
	cwd, _ := os.Getwd()
	bs, err := attachToBroker(ctx, brokerPurposeFromFlags(), brokerProfileFromFlags(), cwd)
	if err != nil {
		return fmt.Errorf("stado: %w", err)
	}
	var banner strings.Builder
	bs.AnnounceSandboxMode(&banner, "stado")
	notices = append(notices, splitBannerLines(banner.String())...)
	defer func() {
		if closeErr := bs.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "stado: broker session.terminate: %v\n", closeErr)
		}
	}()
	return inlineTUIRunner(cfg, notices, brokerExecutorSandbox(bs, noSandbox))
}

// brokerAttachTimeout is the maximum wall-clock time the helper
// waits for the broker to be reachable (including auto-spawn).
// Mirrors daemonAutoSpawnTimeout in tool_run_daemon.go.
const brokerAttachTimeout = 3 * time.Second

// envBrokerAttach gates whether orchestrator entry points attach to
// the broker. v1 default in this release (phase 2 of the rollout):
// **attach** — every entry point goes through the broker by default.
// Set to "0" / "false" / "off" / "no" to opt out (development /
// unusual environments where the broker won't reach).
//
// Test infrastructure: existing tests stay green automatically
// because daemon.EnsureRunning refuses to auto-spawn the host
// binary as a daemon when it detects a Go test binary; the helper
// translates that into a Skipped session so no test setup needs to
// know about the broker.
const envBrokerAttach = "STADO_BROKER_ATTACH"

// BrokerSession is what an orchestrator entry point holds after a
// successful attach. SessionID and Ceiling are mirrored from the
// broker's reply. Skipped reports whether the attach was skipped
// (broker unreachable in a test binary, env opt-out, etc.) — entry
// points use this to decide whether to bother calling Close.
type BrokerSession struct {
	SessionID  string
	Purpose    broker.Purpose
	Profile    broker.Profile // mirrored from the request; useful for the startup banner
	TraceRef   string
	Ceiling    sandbox.Policy // typed; phase 2 uses this to inform runner choice
	Skipped    bool
	SkipReason string

	// client is the daemon connection that holds this session.
	// Closed by Close() after issuing session.terminate.
	client *daemon.Client
}

// Close issues broker.v1.session.terminate against the session and
// closes the underlying daemon connection. Idempotent: safe to call
// even when Skipped, when the session was never created, or after
// a previous Close. Returns the underlying error from terminate
// only (connection-close errors are logged via stderr and swallowed
// to keep entry-point shutdown paths uncomplicated).
func (s *BrokerSession) Close() error {
	if s == nil || s.Skipped {
		return nil
	}
	if s.client == nil {
		return nil
	}
	defer func() {
		_ = s.client.Close()
		s.client = nil
	}()
	if s.SessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	err := s.client.Call(ctx, broker.MethodSessionTerminate, broker.SessionTerminateParams{
		SessionID: s.SessionID,
	}, nil)
	s.SessionID = ""
	if err == nil {
		return nil
	}
	var rpcErr *daemon.Error
	if errors.As(err, &rpcErr) && (rpcErr.Code == daemon.ErrCodeBrokerSessionTerminated ||
		rpcErr.Code == daemon.ErrCodeBrokerSessionNotFound) {
		// Already terminated by some other path — idempotent.
		return nil
	}
	return err
}

// brokerAttachOptIn reports whether the broker attach is enabled
// for this orchestrator invocation. v2: defaults to on. Set
// STADO_BROKER_ATTACH=0 (or false/off/no) to opt out — useful for
// development scenarios where the broker can't be reached or for
// debugging the pre-broker code paths.
func brokerAttachOptIn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envBrokerAttach)))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// attachToBroker auto-spawns the broker if absent, dials it, and
// issues broker.v1.session.create with the supplied purpose +
// profile. Returns a BrokerSession the caller must Close on exit.
//
// Behavior matrix:
//
//   - Opt-out (envBrokerAttach explicitly set to a falsy value
//     0/false/off/no): returns a Skipped BrokerSession with reason
//     "opt-out (STADO_BROKER_ATTACH=<value>)". Entry point proceeds
//     as today (no broker session, no ceiling enforcement).
//   - Broker reachable: dials, creates session, returns the handle.
//   - Broker not reachable + spawn refused (test binary): returns a
//     Skipped BrokerSession with reason "test-binary". Entry-point
//     tests don't have to spawn a daemon.
//   - Broker not reachable + spawn failed (real binary): returns an
//     error. The entry point should surface it as "couldn't start
//     the sandbox broker: <reason>; either fix the reason or pass
//     --no-sandbox" (per AC1.6 in the phase-1 spec).
func attachToBroker(ctx context.Context, purpose broker.Purpose, profile broker.Profile, cwd string) (*BrokerSession, error) {
	if !brokerAttachOptIn() {
		raw := strings.ToLower(strings.TrimSpace(os.Getenv(envBrokerAttach)))
		return &BrokerSession{
			Skipped:    true,
			SkipReason: fmt.Sprintf("opt-out (STADO_BROKER_ATTACH=%q)", raw),
		}, nil
	}

	socketPath, err := daemon.SocketPath()
	if err != nil {
		return nil, fmt.Errorf("broker attach: resolve socket path: %w", err)
	}
	stadoBin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("broker attach: resolve stado binary: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	cl, _, err := daemon.EnsureRunning(dialCtx, socketPath, stadoBin, brokerAttachTimeout)
	if err != nil {
		if isTestBinaryRefusal(err) {
			return &BrokerSession{Skipped: true, SkipReason: "test-binary auto-spawn refused"}, nil
		}
		return nil, fmt.Errorf("broker attach: %w", err)
	}

	params := broker.SessionCreateParams{
		Purpose: purpose,
		Profile: profile,
		CWD:     cwd,
	}
	var result broker.SessionHandleResult
	callCtx, callCancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer callCancel()
	if err := cl.Call(callCtx, broker.MethodSessionCreate, params, &result); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("broker attach: session.create: %w", err)
	}

	return &BrokerSession{
		SessionID: result.SessionID,
		Purpose:   result.Purpose,
		Profile:   profile,
		TraceRef:  result.TraceRef,
		Ceiling:   result.Ceiling,
		client:    cl,
	}, nil
}

// AnnounceSandboxMode writes a one-time startup banner to w
// describing the sandbox state for surface (TUI / stado run /
// headless / ACP / mcp-server). Called from each entry point after
// attachToBroker so the operator sees the active profile + mount
// summary on stderr at every launch.
//
// When the broker attach is Skipped (test-binary refusal, env
// opt-out, etc.) the message indicates that — the operator can see
// from the banner whether the broker is or isn't in the path. This
// is the positive counterpart to today's WarnIfHostUnsandboxed,
// which only fires when sandboxing is NOT in place (DESIGN.md
// §"Sandbox" → "Sandbox-mode startup announcement").
func (s *BrokerSession) AnnounceSandboxMode(w io.Writer, surface string) {
	if w == nil {
		return
	}
	if s == nil || s.Skipped {
		reason := "(unknown reason)"
		if s != nil && s.SkipReason != "" {
			reason = "(" + s.SkipReason + ")"
		}
		fmt.Fprintf(w, "%s: broker attach skipped %s — broker ceiling inactive; local sandbox policy still applies unless --no-sandbox is set\n",
			surface, reason)
		return
	}
	profileTag := string(s.Profile)
	if profileTag == "" {
		profileTag = "(unknown)"
	}
	fmt.Fprintf(w, "%s: sandbox=%s session=%s (broker-mediated)\n", surface, profileTag, s.SessionID)
	writableSummary := summarizeFSWrite(s.Profile, s.Ceiling.FSWrite)
	fmt.Fprintf(w, "%s: writable: %s\n", surface, writableSummary)
	if maskedCount := countMaskedPaths(s.Profile); maskedCount > 0 {
		fmt.Fprintf(w, "%s: %d credential paths masked (~/.ssh/id_*, ~/.aws, ~/.git-credentials, …)\n",
			surface, maskedCount)
	}
	// ssh-agent forwarding, default-on (decision 2026-06-13): surface
	// that the host agent socket is bound (only the socket — key bytes
	// stay in the agent) so the operator knows git-over-ssh works AND
	// that a compromised session could abuse the socket for its
	// lifetime (the accepted residual).
	if len(s.Ceiling.Sockets) > 0 {
		fmt.Fprintf(w, "%s: ssh-agent forwarded (socket only; keys never enter the sandbox)\n", surface)
	}
}

// countMaskedPaths returns the count of credential-bearing paths
// the mount table explicitly denies in this profile. Used by the
// banner so operators see at-a-glance that secrets are masked
// rather than having to inspect the full mount table.
func countMaskedPaths(profile broker.Profile) int {
	if profile == broker.ProfileNoSandbox || profile == "" {
		return 0
	}
	return len(broker.MountTableFor(profile, "").MaskedPaths())
}

// summarizeFSWrite returns a short human-readable summary of the
// session's writable filesystem grant. Phase 2 default is launch
// cwd + /tmp; phase 3's mount table tightens this.
//
// ProfileNoSandbox produces an empty FSWrite (projectCeiling
// returns sandbox.Policy{}) because there is no broker-enforced
// ceiling — but the operator chose this mode explicitly and the
// runner is NoneRunner. Reporting "(none — read-only sandbox)"
// would directly contradict the sandbox=no-sandbox mode line.
// Cloud-review bug_004 / PR #71.
func summarizeFSWrite(profile broker.Profile, writes []string) string {
	if profile == broker.ProfileNoSandbox {
		return "(all paths — no OS-level fence applied)"
	}
	if len(writes) == 0 {
		return "(none — read-only sandbox)"
	}
	return strings.Join(writes, ", ")
}

// brokerPurposeFromFlags maps the current command's flags/context
// to a broker.Purpose. Phase 1: stado run, TUI, headless, ACP,
// MCP server all map to PurposeMainChat. Sub-agent spawns use
// PurposeSubagent (wired in phase 4 via spawn_agent → broker).
//
// `stado tool run` does NOT attach a broker session: it dispatches via the
// daemon (dispatchViaDaemon), and the daemon's daemonToolHost applies its own
// protective sandbox policy to the wasm tool call. Broker-mediated tool-run via
// the broker.v1.toolrun.sandbox handshake is deferred EP-0050 phase work and is
// not wired from the client yet (there is no attachToolRunBroker).
func brokerPurposeFromFlags() broker.Purpose {
	return broker.PurposeMainChat
}

// brokerProfileFromFlags maps the current command's flags to a
// broker.Profile. The persistent --no-sandbox flag selects the explicit
// opt-out profile; otherwise the sandboxed default. Honoured by every entry
// point that attaches to the broker (TUI, run, acp, run --headless, mcp-server,
// session tree), so the opt-out is no longer a run-only special case.
func brokerProfileFromFlags() broker.Profile {
	if noSandbox {
		return brokerProfileNoSandbox()
	}
	return broker.ProfileDefault
}

// brokerProfileNoSandbox returns the explicit operator opt-out
// profile. Used by `stado run --no-sandbox` (phase 1g) and any
// future surface that wires its own opt-out flag.
//
// Per DESIGN.md §"Broker" → "Non-session sandbox requests" the
// broker still mediates the request — the operator's decision is
// captured in the broker-decision log. ProfileNoSandbox configures
// the runtime to use NoneRunner with no namespace isolation.
func brokerProfileNoSandbox() broker.Profile {
	return broker.ProfileNoSandbox
}

// isTestBinaryRefusal reports whether err is the test-binary
// auto-spawn refusal from daemon.EnsureRunning. The message is the
// stable test we can match against — EnsureRunning emits a
// `daemon auto-spawn refused: host binary ... looks like a Go test
// binary` string we can grep for here without exporting a sentinel.
func isTestBinaryRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "looks like a Go test binary")
}
