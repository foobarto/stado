package main

// daemon_broker_test.go — integration tests for broker.v1.* JSON-RPC
// methods over a real UDS daemon process. Mirrors the pattern of
// internal/daemon/server_test.go's startTestServer but with the broker
// bridge wired in. Phase 1d acceptance criteria — see
// .agent/specs/open/v1-phase1-broker.md AC1.2 / AC1.3 / AC1.4.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/daemon"
)

// startTestDaemon spawns a daemon.Server in-process with the given
// broker policy. Returns a connected client + teardown closure.
// Mirrors internal/daemon/server_test.go's helper (which is
// unexported, so it's worth duplicating rather than exporting).
func startTestDaemon(t *testing.T, policy *broker.Policy) (*daemon.Client, func()) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	svc := broker.NewService(policy, nil)
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath:       socketPath,
		BrokerDispatcher: brokerDispatcherBridge(svc),
	})
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	var client *daemon.Client
	for time.Now().Before(deadline) {
		c, _, err := daemon.DialAndHandshake(ctx, socketPath, "test-broker")
		if err == nil {
			client = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		cancel()
		<-serveErr
		t.Fatal("daemon never accepted handshake")
	}
	teardown := func() {
		_ = client.Close()
		_ = srv.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Fatal("daemon did not shut down in time")
		}
	}
	return client, teardown
}

func TestBroker_SessionCreate_AdmitMintsHandle(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()

	params := broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
		CWD:     "/work",
	}
	var result broker.SessionHandleResult
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Call(ctx, broker.MethodSessionCreate, params, &result); err != nil {
		t.Fatalf("broker.v1.session.create: %v", err)
	}
	if result.SessionID == "" {
		t.Errorf("empty SessionID")
	}
	if len(result.SessionID) != 32 {
		t.Errorf("SessionID len = %d, want 32", len(result.SessionID))
	}
	if result.Purpose != broker.PurposeMainChat {
		t.Errorf("Purpose = %q, want %q", result.Purpose, broker.PurposeMainChat)
	}
	// Embedded default policy admits PurposeMainChat via the per-purpose
	// rule, not the global default fallback.
	if result.Rule != "purpose:main-chat" {
		t.Errorf("Rule = %q, want 'purpose:main-chat'", result.Rule)
	}
	if result.TraceRef == "" {
		t.Errorf("TraceRef empty for non-tool-run purpose")
	}
}

func TestBroker_SessionCreate_DeniedByPolicy(t *testing.T) {
	denyPolicy := &broker.Policy{
		DefaultAdmit:  false,
		PurposeAdmits: map[broker.Purpose]bool{broker.PurposeMainChat: false},
	}
	client, teardown := startTestDaemon(t, denyPolicy)
	defer teardown()

	params := broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, broker.MethodSessionCreate, params, nil)
	if err == nil {
		t.Fatal("expected error from denied policy")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v, want *daemon.Error", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerPolicyDeny {
		t.Errorf("err code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerPolicyDeny)
	}
}

func TestBroker_SessionCreate_RejectsInvalidPurpose(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()

	// Raw JSON so we can pass an arbitrary string the typed
	// SessionCreateParams wouldn't admit at marshal time.
	raw := map[string]any{
		"purpose": "bogus-purpose",
		"profile": "default",
		"cwd":     "/work",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, broker.MethodSessionCreate, raw, nil)
	if err == nil {
		t.Fatal("expected error from invalid purpose")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerInvalidPurpose {
		t.Errorf("err code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerInvalidPurpose)
	}
}

func TestBroker_SessionCreate_RejectsUnknownField(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()

	raw := map[string]any{
		"purpose":       "main-chat",
		"profile":       "default",
		"cwd":           "/work",
		"unknown_extra": "should be rejected (strict unmarshal)",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, broker.MethodSessionCreate, raw, nil)
	if err == nil {
		t.Fatal("expected error from unknown field")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerInvalidParams {
		t.Errorf("err code = %d, want %d (rejected unknown field)", rpcErr.Code, daemon.ErrCodeBrokerInvalidParams)
	}
}

func TestBroker_SessionTerminate_Cycle(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var created broker.SessionHandleResult
	if err := client.Call(ctx, broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
	}, &created); err != nil {
		t.Fatalf("session.create: %v", err)
	}

	var term broker.SessionTerminateResult
	if err := client.Call(ctx, broker.MethodSessionTerminate, broker.SessionTerminateParams{
		SessionID: created.SessionID, ControllerToken: created.ControllerToken,
	}, &term); err != nil {
		t.Fatalf("session.terminate (first): %v", err)
	}
	if !term.OK {
		t.Errorf("session.terminate OK = false")
	}

	err := client.Call(ctx, broker.MethodSessionTerminate, broker.SessionTerminateParams{
		SessionID: created.SessionID, ControllerToken: created.ControllerToken,
	}, nil)
	if err == nil {
		t.Fatal("expected error on second terminate")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerSessionTerminated {
		t.Errorf("second-terminate code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerSessionTerminated)
	}

	err = client.Call(ctx, broker.MethodSessionTerminate, broker.SessionTerminateParams{
		SessionID: "0000000000000000000000000000ffff", ControllerToken: "controller_unknown",
	}, nil)
	if err == nil {
		t.Fatal("expected error on unknown session")
	}
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerSessionNotFound {
		t.Errorf("unknown-terminate code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerSessionNotFound)
	}
}

func TestBroker_ToolRunSandbox_AdmitsHandle(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var result broker.ToolRunSandboxResult
	if err := client.Call(ctx, broker.MethodToolRunSandbox, broker.ToolRunSandboxParams{
		PluginName: "fs.read",
		CWD:        "/work",
	}, &result); err != nil {
		t.Fatalf("broker.v1.toolrun.sandbox: %v", err)
	}
	if result.SandboxHandle == "" {
		t.Errorf("empty SandboxHandle")
	}
	if len(result.SandboxHandle) != 32 {
		t.Errorf("SandboxHandle len = %d, want 32", len(result.SandboxHandle))
	}
}

func TestBroker_ToolRunSandbox_DeniedPlugin(t *testing.T) {
	denyPlugin := &broker.Policy{
		DefaultAdmit: true,
		PluginAdmits: map[string]bool{"fs.write": false},
	}
	client, teardown := startTestDaemon(t, denyPlugin)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, broker.MethodToolRunSandbox, broker.ToolRunSandboxParams{
		PluginName: "fs.write",
		CWD:        "/work",
	}, nil)
	if err == nil {
		t.Fatal("expected denial from per-plugin policy")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeBrokerPolicyDeny {
		t.Errorf("err code = %d, want %d", rpcErr.Code, daemon.ErrCodeBrokerPolicyDeny)
	}
	if !strings.Contains(rpcErr.Message, "plugin:fs.write") {
		t.Errorf("err message %q lacks 'plugin:fs.write'", rpcErr.Message)
	}
}

func TestBroker_PolicyQuery_RoundTrip(t *testing.T) {
	client, teardown := startTestDaemon(t, &broker.Policy{
		DefaultAdmit:  true,
		PurposeAdmits: map[broker.Purpose]bool{broker.PurposeSubagent: false},
	})
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var result broker.PolicyQueryResult
	if err := client.Call(ctx, broker.MethodPolicyQuery, broker.PolicyQueryParams{
		Request: broker.CapabilityRequest{
			Purpose: broker.PurposeSubagent,
			Profile: broker.ProfileDefault,
		},
	}, &result); err != nil {
		t.Fatalf("broker.v1.policy.query: %v", err)
	}
	if result.Decision.Admit {
		t.Errorf("decision = %#v, want deny (purpose subagent denied)", result.Decision)
	}
	if result.Decision.Rule != "purpose:subagent" {
		t.Errorf("rule = %q, want purpose:subagent", result.Decision.Rule)
	}
}

func TestBroker_NoDispatcher_MethodNotFound(t *testing.T) {
	// When BrokerDispatcher is nil (legacy daemon callers), broker.v1.*
	// methods return ErrCodeMethodNotFound. Asserts backward compat.
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	srv := daemon.NewServer(daemon.ServerOpts{
		SocketPath: socketPath,
		// BrokerDispatcher deliberately nil.
	})
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	defer func() {
		_ = srv.Stop()
		cancel()
		<-serveErr
	}()

	deadline := time.Now().Add(2 * time.Second)
	var client *daemon.Client
	for time.Now().Before(deadline) {
		c, _, err := daemon.DialAndHandshake(ctx, socketPath, "test")
		if err == nil {
			client = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("daemon never accepted handshake")
	}
	defer client.Close()

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	err := client.Call(callCtx, broker.MethodSessionCreate, json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error from nil BrokerDispatcher")
	}
	var rpcErr *daemon.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if rpcErr.Code != daemon.ErrCodeMethodNotFound {
		t.Errorf("err code = %d, want %d", rpcErr.Code, daemon.ErrCodeMethodNotFound)
	}
}
