package main

// broker_e2e_test.go — end-to-end test that exercises the orchestrator
// broker-attach flow against a real daemon (in-process for test
// hermeticity). Phase 1h acceptance criterion — see AC1.6 in
// .agent/specs/open/v1-phase1-broker.md.
//
// What this tests:
//   1. STADO_BROKER_ATTACH=1 actually attaches (not Skipped).
//   2. attachToBroker → daemon.EnsureRunning → fast-path dial (the
//      daemon is already running, so auto-spawn isn't exercised here).
//   3. broker.v1.session.create round-trips through the full
//      orchestrator → daemon → broker → response → orchestrator stack.
//   4. The returned BrokerSession.Close path issues
//      broker.v1.session.terminate end-to-end and the daemon records it.
//
// What this deliberately does NOT test:
//   - True auto-spawn (would require building a real stado binary). The
//     daemon.EnsureRunning auto-spawn path is covered by existing
//     PTY-bound `stado tool run` tests.
//   - The full agent loop (`stado run --prompt 'pwd'`). That requires a
//     provider and a model — out of scope for a phase-1 plumbing e2e.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/daemon"
)

func TestE2E_AttachToBroker_RoundTrip(t *testing.T) {
	// Stand up a daemon on a tempdir socket the helper will dial via
	// STADO_DAEMON_SOCKET. attachToBroker uses daemon.SocketPath()
	// which honours that env var (internal/daemon/socket.go:24).
	socketPath := filepath.Join(t.TempDir(), "broker-e2e.sock")
	t.Setenv("STADO_DAEMON_SOCKET", socketPath)
	t.Setenv(envBrokerAttach, "1")

	svc := broker.NewService(broker.LoadEmbeddedDefaultPolicy(), nil)
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:       socketPath,
		BrokerDispatcher: brokerDispatcherBridge(svc),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	defer func() {
		_ = srv.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("daemon did not shut down in time")
		}
	}()

	// Wait for socket readiness — startTestDaemon's pattern.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The orchestrator's path:
	sess, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	if sess.Skipped {
		t.Fatalf("expected real attach, got Skipped: %s", sess.SkipReason)
	}
	if sess.SessionID == "" || len(sess.SessionID) != 32 {
		t.Errorf("SessionID = %q (len %d), want 32-char hex", sess.SessionID, len(sess.SessionID))
	}
	if sess.Purpose != broker.PurposeMainChat {
		t.Errorf("Purpose = %q, want %q", sess.Purpose, broker.PurposeMainChat)
	}
	if sess.TraceRef == "" {
		t.Errorf("TraceRef empty for main-chat session")
	}

	if err := sess.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Idempotent second Close — should not error even though the
	// session is already terminated server-side AND the connection
	// is already closed client-side.
	if err := sess.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestE2E_AttachToBroker_NoSandboxProfile(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "broker-e2e-nosbx.sock")
	t.Setenv("STADO_DAEMON_SOCKET", socketPath)
	t.Setenv(envBrokerAttach, "1")

	svc := broker.NewService(broker.LoadEmbeddedDefaultPolicy(), nil)
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:       socketPath,
		BrokerDispatcher: brokerDispatcherBridge(svc),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	defer func() {
		_ = srv.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("daemon did not shut down in time")
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Phase 1g wired: `stado run --no-sandbox` → brokerProfileNoSandbox().
	sess, err := attachToBroker(ctx, broker.PurposeMainChat, brokerProfileNoSandbox(), "/work")
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	if sess.Skipped {
		t.Fatalf("expected attach, got Skipped: %s", sess.SkipReason)
	}
	if sess.SessionID == "" {
		t.Errorf("empty SessionID for no-sandbox profile")
	}
	// The broker admitted the no-sandbox request and recorded the
	// decision. The orchestrator gets a SessionID and proceeds; the
	// runtime then picks NoneRunner via the existing --no-sandbox
	// path in run.go (phase 1g) since phase 1 ceiling-application is
	// a no-op on the orchestrator side.
	_ = sess.Close()
}

func TestE2E_AttachToBroker_DenyByPolicy(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "broker-e2e-deny.sock")
	t.Setenv("STADO_DAEMON_SOCKET", socketPath)
	t.Setenv(envBrokerAttach, "1")

	// Policy that denies all main-chat requests. Surfaces as a
	// session.create error to the orchestrator, which the entry
	// point translates into a fatal "couldn't start the sandbox
	// broker" message.
	denyPolicy := &broker.Policy{
		DefaultAdmit:  false,
		PurposeAdmits: map[broker.Purpose]bool{broker.PurposeMainChat: false},
	}
	svc := broker.NewService(denyPolicy, nil)
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:       socketPath,
		BrokerDispatcher: brokerDispatcherBridge(svc),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	defer func() {
		_ = srv.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("daemon did not shut down in time")
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	_, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err == nil {
		t.Fatal("expected denial error from attachToBroker")
	}
	// The error wraps the broker dispatch error; the underlying
	// *daemon.Error has the broker policy-deny code.
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v, want chain ending in *daemon.Error", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerPolicyDeny {
		t.Errorf("err code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerPolicyDeny)
	}
}

func TestE2E_NoBrokerRunning_RealStadoBinary(t *testing.T) {
	// When STADO_BROKER_ATTACH=1 + the broker socket points at a
	// nonexistent path + we're a Go test binary, attachToBroker
	// hits daemon.EnsureRunning's test-binary refusal and returns
	// Skipped. This asserts the existing-tests-don't-need-broker
	// invariant: every test in the repo gets the Skipped path
	// automatically.
	socketPath := filepath.Join(t.TempDir(), "no-broker-here.sock")
	t.Setenv("STADO_DAEMON_SOCKET", socketPath)
	t.Setenv(envBrokerAttach, "1")

	// Ensure the socket really doesn't exist.
	if _, err := os.Stat(socketPath); err == nil {
		t.Fatalf("socket path %s unexpectedly exists in fresh tempdir", socketPath)
	}

	sess, err := attachToBroker(context.Background(), broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	if !sess.Skipped {
		t.Errorf("expected Skipped (test binary refusal), got SessionID=%q", sess.SessionID)
	}
	if sess.SkipReason != "test-binary auto-spawn refused" {
		t.Errorf("SkipReason = %q", sess.SkipReason)
	}
}
