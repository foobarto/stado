package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sessioncontext"
)

func TestSessionStateCommandReadsDetachedScopeThroughBroker(t *testing.T) {
	socketPath, client, _, _, teardown := startTestDurableDaemonWithSocket(t)
	defer teardown()
	t.Setenv("STADO_DAEMON_SOCKET", socketPath)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := brokercredential.New(cfg.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	var rootHandle broker.SessionHandleResult
	if err := client.Call(t.Context(), broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: broker.ProfileDefault, CWD: cwd,
	}, &rootHandle); err != nil {
		t.Fatal(err)
	}
	root := &BrokerSession{
		SessionID: rootHandle.SessionID, controllerToken: rootHandle.ControllerToken,
		Purpose: rootHandle.Purpose, Profile: broker.ProfileDefault, client: client,
		logicalCredentials: credentials,
	}
	defer func() { _ = root.Close() }()
	controller, err := root.OpenLogicalSession(t.Context(), cwd, "logical-session-cli")
	if err != nil {
		t.Fatal(err)
	}
	peer := controller.(*BrokerSession)
	if err := peer.EnsureTrajectoryObjective(t.Context(), "inspect safely"); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := *sessionStateCmd
	var stdout, stderr bytes.Buffer
	cmd.SetContext(t.Context())
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.RunE(&cmd, []string{"logical-session-cli"}); err != nil {
		t.Fatalf("session state: %v stderr=%q", err, stderr.String())
	}
	var state sessioncontext.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatalf("decode output: %v output=%q", err, stdout.String())
	}
	if state.SessionID != "logical-session-cli" || state.Objective != "inspect safely" {
		t.Fatalf("state=%+v", state)
	}
}
