package main

// broker_client.go — shared helper used by orchestrator entry points
// (stado run, TUI, stado headless, stado acp, stado mcp-server) to
// attach to the broker and create a session.
//
// Phase 1: applying the returned ceiling is a no-op — entry points
// receive a SessionID + (for non-tool-run purposes) a TraceRef but
// don't yet enforce the ceiling at the OS level. That's phase 2.
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
	"os"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/daemon"
)

// brokerAttachTimeout is the maximum wall-clock time the helper
// waits for the broker to be reachable (including auto-spawn).
// Mirrors daemonAutoSpawnTimeout in tool_run_daemon.go.
const brokerAttachTimeout = 3 * time.Second

// envBrokerAttach gates whether orchestrator entry points attach to
// the broker. Default: unset → skip (phase 1 incremental rollout).
// Phase 2 flips the default to attach. Set to "1" / "true" / "on"
// to opt-in during phase 1; "0" / "false" / "off" / "no" to opt-out
// in phase 2.
const envBrokerAttach = "STADO_BROKER_ATTACH"

// BrokerSession is what an orchestrator entry point holds after a
// successful attach. SessionID and Ceiling are mirrored from the
// broker's reply. Skipped reports whether the attach was skipped
// (broker unreachable in a test binary, env opt-out, etc.) — entry
// points use this to decide whether to bother calling Close.
type BrokerSession struct {
	SessionID  string
	Purpose    broker.Purpose
	TraceRef   string
	Ceiling    any // sandbox.Policy in JSON; opaque to entry-point code
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

// brokerAttachOptIn reports whether the operator has opted into
// broker attach for orchestrator entry points. Phase 1: default is
// off so existing tests don't break. Phase 2 flips this default by
// inverting the conditional.
func brokerAttachOptIn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envBrokerAttach)))
	switch v {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// attachToBroker auto-spawns the broker if absent, dials it, and
// issues broker.v1.session.create with the supplied purpose +
// profile. Returns a BrokerSession the caller must Close on exit.
//
// Phase-1 behavior matrix:
//
//   - Opt-out (envBrokerAttach unset/false): returns a Skipped
//     BrokerSession with reason "opt-out". Entry point proceeds as
//     today.
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
		return &BrokerSession{Skipped: true, SkipReason: "opt-out (STADO_BROKER_ATTACH not set)"}, nil
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
		TraceRef:  result.TraceRef,
		Ceiling:   result.Ceiling,
		client:    cl,
	}, nil
}

// brokerPurposeFromFlags maps the current command's flags/context
// to a broker.Purpose. Phase 1: stado run, TUI, headless, ACP,
// MCP server all map to PurposeMainChat. Sub-agent spawns use
// PurposeSubagent (wired in phase 4 via spawn_agent → broker).
// tool-run is handled separately via attachToolRunBroker.
func brokerPurposeFromFlags() broker.Purpose {
	return broker.PurposeMainChat
}

// brokerProfileFromFlags maps the current command's flags to a
// broker.Profile. Phase 1: always ProfileDefault. Phase 1g wires
// the new --no-sandbox flag to ProfileNoSandbox; phase 2/3 wires
// --hardened (or equivalent operator opt-in) to ProfileHardened.
func brokerProfileFromFlags() broker.Profile {
	return broker.ProfileDefault
}

// isTestBinaryRefusal reports whether err is the test-binary
// auto-spawn refusal from daemon.EnsureRunning. The message is the
// stable test we can match against — EnsureRunning emits a
// `daemon auto-spawn refused: host binary ... looks like a Go test
// binary` string we can grep for here without exporting a sentinel.
func isTestBinaryRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "looks like a Go test binary")
}
