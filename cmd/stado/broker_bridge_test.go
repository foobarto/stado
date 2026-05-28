package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
)

func TestBrokerDecisionLog_AppendsOnAdmit(t *testing.T) {
	// Point StateDir at a tempdir so the decision log writes there.
	stateRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateRoot)

	cfg := &config.Config{}
	svc, err := buildBrokerService(cfg)
	if err != nil {
		t.Fatalf("buildBrokerService: %v", err)
	}

	// Drive an admit decision through the service.
	dec := svc.Evaluate(broker.CapabilityRequest{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
		CWD:     "/work",
	})
	if !dec.Admit {
		t.Fatalf("expected admit, got %#v", dec)
	}

	logPath := filepath.Join(stateRoot, "stado", "broker", "decisions.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read decision log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("decision log is empty")
	}
	// Expect one JSONL line; parse + assert shape.
	var rec broker.DecisionRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("unmarshal decision record: %v (line=%q)", err, string(data))
	}
	if rec.Decision.Admit != true {
		t.Errorf("logged decision admit = false, want true")
	}
	if rec.Request.Purpose != broker.PurposeMainChat {
		t.Errorf("logged purpose = %q, want main-chat", rec.Request.Purpose)
	}
}

func TestBrokerDecisionLog_AppendsOnDeny(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateRoot)

	cfg := &config.Config{}
	svc, err := buildBrokerService(cfg)
	if err != nil {
		t.Fatalf("buildBrokerService: %v", err)
	}

	// Replace policy with deny-all so the next Evaluate is a deny.
	svc.SetPolicy(&broker.Policy{DefaultAdmit: false})

	dec := svc.Evaluate(broker.CapabilityRequest{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
	})
	if dec.Admit {
		t.Fatalf("expected deny, got %#v", dec)
	}

	logPath := filepath.Join(stateRoot, "stado", "broker", "decisions.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read decision log: %v", err)
	}
	if !strings.Contains(string(data), `"Admit":false`) {
		t.Errorf("decision log lacks deny record: %q", string(data))
	}
}

func TestBrokerDecisionLog_FilePermissions(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateRoot)

	cfg := &config.Config{}
	if _, err := buildBrokerService(cfg); err != nil {
		t.Fatalf("buildBrokerService: %v", err)
	}
	logPath := filepath.Join(stateRoot, "stado", "broker", "decisions.jsonl")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("decision log perm = %#o, want 0600 (owner-only)", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(logPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("broker dir perm = %#o, want 0700", dirInfo.Mode().Perm())
	}
}

func TestBrokerDecisionLog_RoundTripThroughDaemon(t *testing.T) {
	// End-to-end via the daemon: admit a session.create over the
	// real UDS, then assert the decision was recorded in the log
	// file. Confirms the full bridge (broker.Service →
	// MemoryWriter is what daemon_broker_test.go covers; this test
	// adds the file-backed writer to the integration coverage).
	stateRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateRoot)
	cfg := &config.Config{}

	socketPath := filepath.Join(t.TempDir(), "broker-decisionlog.sock")
	svc, err := buildBrokerService(cfg)
	if err != nil {
		t.Fatalf("buildBrokerService: %v", err)
	}
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

	// Wait for socket.
	deadline := time.Now().Add(2 * time.Second)
	var client *daemon.Client
	for time.Now().Before(deadline) {
		c, _, derr := daemon.DialAndHandshake(ctx, socketPath, "test")
		if derr == nil {
			client = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("daemon never accepted handshake")
	}
	defer client.Close()

	callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
	defer callCancel()
	var result broker.SessionHandleResult
	if err := client.Call(callCtx, broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat,
		Profile: broker.ProfileDefault,
		CWD:     "/work",
	}, &result); err != nil {
		t.Fatalf("session.create: %v", err)
	}

	logPath := filepath.Join(stateRoot, "stado", "broker", "decisions.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"Purpose":"main-chat"`) {
		t.Errorf("log %q lacks main-chat record", string(data))
	}
}
