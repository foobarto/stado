package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/sandbox"
)

func startTestDurableDaemon(t *testing.T) (*daemon.Client, *broker.Service, func()) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	service := broker.NewService(broker.LoadEmbeddedDefaultPolicy(), nil)
	store, err := wal.Open(filepath.Join(t.TempDir(), "wal"))
	if err != nil {
		t.Fatal(err)
	}
	verifier := broker.ArtifactPluginVerifierFunc(func(_ context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		return identity, manifest, nil
	})
	if err := service.ConfigureArtifactStore(store, verifier); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureSessionLineageVerifier(broker.SessionLineageVerifierFunc(func(context.Context, broker.SessionLineageCheck) error {
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	server := daemon.NewServer(daemon.ServerOpts{
		SocketPath: socketPath, BrokerDispatcher: brokerDispatcherBridge(service),
	})
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	var client *daemon.Client
	for time.Now().Before(deadline) {
		candidate, _, err := daemon.DialAndHandshake(ctx, socketPath, "durable-session-test")
		if err == nil {
			client = candidate
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		cancel()
		<-serveErr
		t.Fatal("durable daemon never accepted handshake")
	}
	return client, service, func() {
		_ = client.Close()
		_ = server.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Fatal("durable daemon did not stop")
		}
		_ = service.Close()
	}
}

func TestAttachToBroker_DefaultOnTestBinaryRefused(t *testing.T) {
	// v2 default: STADO_BROKER_ATTACH unset → attach. We're a Go
	// test binary, so EnsureRunning refuses to auto-spawn ourselves
	// as a daemon. The helper translates that into Skipped so the
	// test infrastructure doesn't have to build a real stado binary.
	t.Setenv(envBrokerAttach, "")
	// Hermetic: pin the socket to a temp path nothing is listening on,
	// so EnsureRunning takes the spawn path (→ test-binary refusal)
	// rather than dialing a real daemon the developer happens to have
	// running at the default $XDG_RUNTIME_DIR socket. Without this the
	// test passes/fails depending on ambient daemon state.
	t.Setenv("STADO_DAEMON_SOCKET", filepath.Join(t.TempDir(), "daemon.sock"))
	ctx := context.Background()
	sess, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err != nil {
		t.Fatalf("attachToBroker (test binary path): %v", err)
	}
	if !sess.Skipped {
		t.Errorf("expected Skipped, got SessionID=%q", sess.SessionID)
	}
	if sess.SkipReason != "test-binary auto-spawn refused" {
		t.Errorf("SkipReason = %q, want 'test-binary auto-spawn refused'", sess.SkipReason)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close on skipped session: %v", err)
	}
}

func TestAttachToBroker_OptOutExplicitlyFalse(t *testing.T) {
	// STADO_BROKER_ATTACH=0 → explicit opt-out → Skipped with
	// reason "opt-out".
	t.Setenv(envBrokerAttach, "0")
	ctx := context.Background()
	sess, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	if !sess.Skipped {
		t.Errorf("expected Skipped on opt-out")
	}
	if sess.SkipReason == "" {
		t.Errorf("SkipReason empty")
	}
}

func TestAttachToBroker_OptInForms(t *testing.T) {
	// v2 default is on. Only the explicit opt-out values produce
	// false; everything else (including unrecognized values) means
	// attach.
	cases := []struct {
		envVal  string
		wantOpt bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"on", true},
		{"yes", true},
		{"", true}, // v2 default
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"off", false},
		{"no", false},
		{"random", true}, // unknown values default to on
	}
	for _, tc := range cases {
		t.Run("env="+tc.envVal, func(t *testing.T) {
			t.Setenv(envBrokerAttach, tc.envVal)
			if got := brokerAttachOptIn(); got != tc.wantOpt {
				t.Errorf("brokerAttachOptIn() = %v, want %v", got, tc.wantOpt)
			}
		})
	}
}

func TestBrokerSession_CloseOnNilSafe(t *testing.T) {
	var sess *BrokerSession
	if err := sess.Close(); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}

func TestBrokerSession_DoubleCloseSafe(t *testing.T) {
	sess := &BrokerSession{Skipped: true, SkipReason: "test"}
	if err := sess.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestBrokerSessionDurableLogicalPeerCreateDetachAdopt(t *testing.T) {
	client, _, teardown := startTestDurableDaemon(t)
	defer teardown()
	cwd := t.TempDir()
	var rootHandle broker.SessionHandleResult
	if err := client.Call(t.Context(), broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: broker.ProfileDefault, CWD: cwd,
	}, &rootHandle); err != nil {
		t.Fatal(err)
	}
	credentialStore, err := brokercredential.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := &BrokerSession{
		SessionID: rootHandle.SessionID, controllerToken: rootHandle.ControllerToken,
		Purpose: rootHandle.Purpose, Profile: broker.ProfileDefault, client: client,
		logicalCredentials: credentialStore,
	}
	firstController, err := root.OpenLogicalSession(t.Context(), cwd, "logical-session-a")
	if err != nil {
		t.Fatal(err)
	}
	first := firstController.(*BrokerSession)
	firstID, firstToken := first.SessionID, first.controllerToken
	credential, err := credentialStore.Load("logical-session-a")
	if err != nil || credential.Subject != "logical-session-a" {
		t.Fatalf("stored credential=%+v err=%v", credential, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.heartbeatStop != nil || first.heartbeatDone != nil {
		t.Fatal("detached logical peer retained heartbeat resources")
	}

	secondController, err := root.OpenLogicalSession(t.Context(), cwd, "logical-session-a")
	if err != nil {
		t.Fatal(err)
	}
	second := secondController.(*BrokerSession)
	if second.SessionID != firstID || second.controllerToken == firstToken {
		t.Fatalf("adopted session/controller=%q/%q initial=%q/%q", second.SessionID, second.controllerToken, firstID, firstToken)
	}
	stable, err := credentialStore.Load("logical-session-a")
	if err != nil || stable != credential {
		t.Fatalf("adoption rewrote stable recovery bearer: before=%+v after=%+v err=%v", credential, stable, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerSessionLogicalHandoffStagesBeforeCommitAndRotatesInPlace(t *testing.T) {
	client, service, teardown := startTestDurableDaemon(t)
	defer teardown()
	cwd := t.TempDir()
	var rootHandle broker.SessionHandleResult
	if err := client.Call(t.Context(), broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: broker.ProfileDefault, CWD: cwd,
	}, &rootHandle); err != nil {
		t.Fatal(err)
	}
	credentialStore, err := brokercredential.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := &BrokerSession{
		SessionID: rootHandle.SessionID, controllerToken: rootHandle.ControllerToken,
		Purpose: rootHandle.Purpose, Profile: broker.ProfileDefault, client: client,
		logicalCredentials: credentialStore,
	}
	controller, err := root.OpenLogicalSession(t.Context(), cwd, "logical-session-source")
	if err != nil {
		t.Fatal(err)
	}
	peer := controller.(*BrokerSession)
	oldToken := peer.controllerToken
	sourceCredential, err := credentialStore.Load("logical-session-source")
	if err != nil {
		t.Fatal(err)
	}
	turnRef := "refs/sessions/logical-session-source/turns/1"
	reservation, err := peer.ReserveLogicalSessionHandoff(t.Context(), "logical-session-child", turnRef)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := credentialStore.Load("logical-session-child")
	if err != nil || staged.Ticket != sourceCredential.Ticket || staged.ResumeSecret != sourceCredential.ResumeSecret {
		t.Fatalf("pre-staged child=%+v err=%v", staged, err)
	}
	if _, err := credentialStore.Load("logical-session-source"); err != nil {
		t.Fatalf("reserve removed authoritative source before commit: %v", err)
	}
	if err := peer.CommitLogicalSessionHandoff(t.Context(), reservation); err != nil {
		t.Fatal(err)
	}
	if peer.durableSubject != "logical-session-child" || peer.controllerToken == oldToken {
		t.Fatalf("committed peer subject/token=%q/%q", peer.durableSubject, peer.controllerToken)
	}
	if _, err := credentialStore.Load("logical-session-source"); !errors.Is(err, brokercredential.ErrNotFound) {
		t.Fatalf("committed source credential remains: %v", err)
	}
	oldCredential := sourceCredential
	if _, _, err := service.AdoptSession(oldCredential, cwd); !errors.Is(err, broker.ErrSessionScopeCredential) {
		t.Fatalf("old subject remained adoptable: %v", err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerSession_CloseDefersUntilApplicationEventLeaseReleased(t *testing.T) {
	client, teardown := startTestDaemon(t, broker.LoadEmbeddedDefaultPolicy())
	defer teardown()

	var handle broker.SessionHandleResult
	if err := client.Call(t.Context(), broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: broker.ProfileDefault, CWD: "/work",
	}, &handle); err != nil {
		t.Fatal(err)
	}
	sess := &BrokerSession{
		SessionID: handle.SessionID, controllerToken: handle.ControllerToken,
		Purpose: handle.Purpose, Profile: broker.ProfileDefault, client: client,
		applicationGeneration: 1,
	}
	scope, release, err := sess.LeaseApplicationEventContext()
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID != handle.SessionID || scope.Generation != 1 {
		t.Fatalf("leased scope=%+v", scope)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if sess.SessionID != handle.SessionID || !sess.applicationClosePending || sess.applicationClosed {
		t.Fatalf("close did not defer: session=%q pending=%v closed=%v", sess.SessionID, sess.applicationClosePending, sess.applicationClosed)
	}

	// The old controller remains authenticated while the observation is live.
	var tainted broker.SessionTaintResult
	if err := client.Call(t.Context(), broker.MethodSessionTaint, broker.SessionTaintParams{
		SessionID: handle.SessionID, ControllerToken: handle.ControllerToken, Taint: "tainted",
	}, &tainted); err != nil {
		t.Fatalf("leased session retired early: %v", err)
	}
	release()
	release() // idempotent lease release
	if sess.SessionID != "" || !sess.applicationClosed || sess.applicationLeases != 0 {
		t.Fatalf("last release did not retire session: session=%q leases=%d closed=%v", sess.SessionID, sess.applicationLeases, sess.applicationClosed)
	}
	if err := client.Call(t.Context(), broker.MethodSessionTaint, broker.SessionTaintParams{
		SessionID: handle.SessionID, ControllerToken: handle.ControllerToken, Taint: "clean",
	}, &tainted); err == nil {
		t.Fatal("released session remained authenticated")
	}
}

func TestBrokerPurposeFromFlags_PhaseOneAlwaysMainChat(t *testing.T) {
	if got := brokerPurposeFromFlags(); got != broker.PurposeMainChat {
		t.Errorf("brokerPurposeFromFlags() = %q, want %q (phase 1 always main-chat)", got, broker.PurposeMainChat)
	}
}

func TestBrokerProfileFromFlags_HonoursNoSandbox(t *testing.T) {
	defer func(prev bool) { noSandbox = prev }(noSandbox)

	noSandbox = false
	if got := brokerProfileFromFlags(); got != broker.ProfileDefault {
		t.Errorf("brokerProfileFromFlags() with flag unset = %q, want %q", got, broker.ProfileDefault)
	}
	noSandbox = true
	if got := brokerProfileFromFlags(); got != broker.ProfileNoSandbox {
		t.Errorf("brokerProfileFromFlags() with --no-sandbox = %q, want %q", got, broker.ProfileNoSandbox)
	}
}

// TestRootCommandAcceptsNoSandbox is the regression guard for the bug where
// `stado --no-sandbox` (TUI launch) failed with "unknown flag" — --no-sandbox
// was registered only on `run`, so the TUI / acp / headless / mcp-server (which
// all read brokerProfileFromFlags) had no way to opt out. It is now a
// persistent root flag honoured by every entry point.
func TestRootCommandAcceptsNoSandbox(t *testing.T) {
	if rootCmd.PersistentFlags().Lookup("no-sandbox") == nil {
		t.Fatal("rootCmd is missing the persistent --no-sandbox flag")
	}
}

func TestAnnounceSandboxMode_Skipped(t *testing.T) {
	var buf strings.Builder
	sess := &BrokerSession{Skipped: true, SkipReason: "test-binary auto-spawn refused"}
	sess.AnnounceSandboxMode(&buf, "stado")
	got := buf.String()
	if !strings.Contains(got, "skipped") {
		t.Errorf("announcement %q lacks 'skipped'", got)
	}
	if !strings.Contains(got, "test-binary auto-spawn refused") {
		t.Errorf("announcement %q lacks skip reason", got)
	}
	if !strings.Contains(got, "local sandbox policy still applies") {
		t.Errorf("announcement %q should distinguish broker skip from sandbox opt-out", got)
	}
}

func TestAnnounceSandboxMode_Active(t *testing.T) {
	var buf strings.Builder
	sess := &BrokerSession{
		SessionID: "abcdef0123456789abcdef0123456789",
		Purpose:   broker.PurposeMainChat,
		Profile:   broker.ProfileDefault,
		Ceiling: sandbox.Policy{
			FSWrite: []string{"/work", "/tmp"},
		},
	}
	sess.AnnounceSandboxMode(&buf, "stado run")
	got := buf.String()
	if !strings.Contains(got, "sandbox=default") {
		t.Errorf("announcement %q lacks 'sandbox=default'", got)
	}
	if !strings.Contains(got, "abcdef0123456789") {
		t.Errorf("announcement %q lacks SessionID", got)
	}
	if !strings.Contains(got, "/work") || !strings.Contains(got, "/tmp") {
		t.Errorf("announcement %q lacks writable paths", got)
	}
}

func TestAnnounceSandboxMode_NoSandboxProfile(t *testing.T) {
	var buf strings.Builder
	sess := &BrokerSession{
		SessionID: "00000000000000000000000000000000",
		Purpose:   broker.PurposeMainChat,
		Profile:   broker.ProfileNoSandbox,
		Ceiling:   sandbox.Policy{}, // empty
	}
	sess.AnnounceSandboxMode(&buf, "stado run")
	got := buf.String()
	if !strings.Contains(got, "sandbox=no-sandbox") {
		t.Errorf("announcement %q lacks 'sandbox=no-sandbox'", got)
	}
	// Phase 2/cloud-review bug_004: --no-sandbox is the operator's
	// explicit opt-out from the OS-level fence; the writable line
	// must reflect that, not contradict it with "read-only sandbox".
	if !strings.Contains(got, "(all paths — no OS-level fence applied)") {
		t.Errorf("announcement %q should reflect --no-sandbox's unrestricted-fs reality", got)
	}
}

func TestAnnounceSandboxMode_NilWriter(t *testing.T) {
	// Defensive: nil io.Writer should not panic.
	sess := &BrokerSession{Skipped: true}
	sess.AnnounceSandboxMode(nil, "stado")
}

func TestAnnounceSandboxMode_NilSession(t *testing.T) {
	var buf strings.Builder
	var sess *BrokerSession
	sess.AnnounceSandboxMode(&buf, "stado")
	got := buf.String()
	if !strings.Contains(got, "skipped") {
		t.Errorf("nil-session announcement %q should mention skipped state", got)
	}
}

// TestBrokerExecutorSandbox covers the one derivation shared by every
// executor-owning surface.
func TestBrokerExecutorSandbox(t *testing.T) {
	ceil := sandbox.Policy{FSWrite: []string{"/work", "/tmp"}}

	t.Run("nil session does not enforce", func(t *testing.T) {
		got := brokerExecutorSandbox(nil, false)
		if got.EnforceCeiling || got.Disabled {
			t.Error("nil session should not enforce")
		}
		if len(got.Ceiling.FSWrite) != 0 {
			t.Errorf("nil session should yield the zero policy, got %v", got.Ceiling.FSWrite)
		}
	})
	t.Run("skipped session does not enforce", func(t *testing.T) {
		got := brokerExecutorSandbox(&BrokerSession{Skipped: true, Ceiling: ceil}, false)
		if got.EnforceCeiling {
			t.Error("skipped attach should not enforce")
		}
		if len(got.Ceiling.FSWrite) != 2 {
			t.Errorf("skipped session should preserve the broker projection for diagnostics, got %v", got.Ceiling.FSWrite)
		}
	})
	t.Run("active session enforces the broker ceiling", func(t *testing.T) {
		got := brokerExecutorSandbox(&BrokerSession{Ceiling: ceil}, false)
		if !got.EnforceCeiling {
			t.Error("active session should enforce the ceiling")
		}
		if len(got.Ceiling.FSWrite) != 2 {
			t.Errorf("active session should return the broker ceiling, got %v", got.Ceiling.FSWrite)
		}
	})
	t.Run("no-sandbox opt-out disables runner", func(t *testing.T) {
		got := brokerExecutorSandbox(&BrokerSession{Ceiling: ceil}, true)
		if got.EnforceCeiling {
			t.Error("--no-sandbox should not enforce even with a projected ceiling")
		}
		if !got.Disabled {
			t.Error("--no-sandbox should select the disabled executor policy")
		}
	})
	t.Run("broker no-sandbox profile is authoritative", func(t *testing.T) {
		got := brokerExecutorSandbox(&BrokerSession{Profile: broker.ProfileNoSandbox}, false)
		if !got.Disabled || got.EnforceCeiling {
			t.Fatalf("policy = %+v, want disabled without ceiling enforcement", got)
		}
	})
}
