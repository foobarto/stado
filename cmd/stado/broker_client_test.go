package main

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/sandbox"
)

func TestAttachToBroker_DefaultOnTestBinaryRefused(t *testing.T) {
	// v2 default: STADO_BROKER_ATTACH unset → attach. We're a Go
	// test binary, so EnsureRunning refuses to auto-spawn ourselves
	// as a daemon. The helper translates that into Skipped so the
	// test infrastructure doesn't have to build a real stado binary.
	t.Setenv(envBrokerAttach, "")
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

func TestBrokerPurposeFromFlags_PhaseOneAlwaysMainChat(t *testing.T) {
	if got := brokerPurposeFromFlags(); got != broker.PurposeMainChat {
		t.Errorf("brokerPurposeFromFlags() = %q, want %q (phase 1 always main-chat)", got, broker.PurposeMainChat)
	}
}

func TestBrokerProfileFromFlags_PhaseOneAlwaysDefault(t *testing.T) {
	if got := brokerProfileFromFlags(); got != broker.ProfileDefault {
		t.Errorf("brokerProfileFromFlags() = %q, want %q (phase 1 always default; phase 1g wires --no-sandbox)", got, broker.ProfileDefault)
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
