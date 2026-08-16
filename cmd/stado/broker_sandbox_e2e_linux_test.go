//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/sandbox"
)

// TestE2E_BrokerAdmissionConstrainsRealSandbox closes the gap between tests
// that only inspect a broker decision and tests that only exercise a locally
// supplied sandbox policy. The ceiling returned through the real daemon RPC is
// composed with BwrapRunner, and an outside path requested by the caller is
// absent from the resulting namespace.
func TestE2E_BrokerAdmissionConstrainsRealSandbox(t *testing.T) {
	if !(sandbox.BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	root := t.TempDir()
	outside := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "broker-sandbox-e2e.sock")
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
	waitForBrokerSandboxSocket(t, socketPath)

	session, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, root)
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close broker session: %v", err)
		}
	}()
	if session.Skipped || session.SessionID == "" {
		t.Fatalf("broker admission did not return a live session: %+v", session)
	}

	shell := "/usr/bin/sh"
	if _, err := os.Stat(shell); err != nil {
		t.Skipf("%s unavailable: %v", shell, err)
	}
	allowedFile := filepath.Join(root, "allowed")
	outsideFile := filepath.Join(outside, "denied")
	runner := brokerExecutorSandbox(session, false).Runner(sandbox.BwrapRunner{})
	cmd, err := runner.Command(ctx, sandbox.Policy{
		CWD: root,
		// The untrusted caller asks for both roots. The broker ceiling must
		// remove outside before BwrapRunner constructs any bind mount.
		FSRead:  []string{root, outside},
		FSWrite: []string{root, outside},
		Exec:    []string{shell},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetDenyAll},
	}, shell, []string{
		"-c", `printf allowed > "$1"; if printf denied 2>/dev/null > "$2"; then exit 41; fi; printf broker-admission-ok`,
		"stado-broker-test", allowedFile, outsideFile,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range cmd.Args {
		if arg == outside {
			t.Fatalf("caller-requested path outside broker ceiling reached bwrap argv: %v", cmd.Args)
		}
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		text := string(out)
		if strings.Contains(text, "namespace") || strings.Contains(text, "uid map") ||
			strings.Contains(text, "Operation not permitted") || strings.Contains(text, "clone") {
			t.Skipf("bwrap cannot create namespaces on this host: %v\n%s", runErr, text)
		}
		t.Fatalf("broker-admitted sandbox failed: %v\n%s", runErr, text)
	}
	if string(out) != "broker-admission-ok" {
		t.Fatalf("sandbox output = %q", out)
	}
	if got, err := os.ReadFile(allowedFile); err != nil || string(got) != "allowed" {
		t.Fatalf("allowed broker-ceiling write = %q, %v", got, err)
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("outside broker-ceiling write reached host: %v", err)
	}
}

func waitForBrokerSandboxSocket(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("broker socket %s did not become ready", socketPath)
}
