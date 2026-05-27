package main

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/broker"
)

func TestAttachToBroker_OptOutDefaultsToSkipped(t *testing.T) {
	t.Setenv(envBrokerAttach, "")
	ctx := context.Background()
	sess, err := attachToBroker(ctx, broker.PurposeMainChat, broker.ProfileDefault, "/work")
	if err != nil {
		t.Fatalf("attachToBroker: %v", err)
	}
	if !sess.Skipped {
		t.Errorf("expected Skipped, got SessionID=%q", sess.SessionID)
	}
	if sess.SkipReason == "" {
		t.Errorf("SkipReason empty")
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close on skipped session: %v", err)
	}
}

func TestAttachToBroker_OptInTestBinaryRefused(t *testing.T) {
	// Setting STADO_BROKER_ATTACH=1 makes the helper try to attach.
	// We're a Go test binary; EnsureRunning refuses to auto-spawn
	// ourselves as a daemon. The helper translates that into a
	// Skipped reason so existing tests don't have to build a real
	// stado binary just to exercise this path.
	t.Setenv(envBrokerAttach, "1")
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

func TestAttachToBroker_OptInForms(t *testing.T) {
	cases := []struct {
		envVal  string
		wantOpt bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"on", true},
		{"yes", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"off", false},
		{"no", false},
		{"random", false},
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
