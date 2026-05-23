package plugins

import (
	"strings"
	"testing"
)

func TestIsRevoked_known(t *testing.T) {
	// One of the 12 leaked demo-seed fingerprints (browser-demo.seed).
	rev, src := IsRevoked("6c48b56f20c9c344")
	if !rev {
		t.Fatal("expected revoked=true for browser-demo.seed fingerprint")
	}
	if !strings.Contains(src, "browser-demo.seed") {
		t.Errorf("expected source to name browser-demo.seed, got %q", src)
	}
}

func TestIsRevoked_unknown(t *testing.T) {
	if rev, src := IsRevoked("0000000000000000"); rev || src != "" {
		t.Errorf("expected not-revoked, got rev=%v src=%q", rev, src)
	}
	if rev, _ := IsRevoked(""); rev {
		t.Error("empty fingerprint should not be revoked")
	}
}

func TestRevokedList_count(t *testing.T) {
	// Guard against accidentally shrinking the list. 12 demo seeds were
	// committed to history (browser/encode-zig/hello/hello-go/http-session/
	// image-info/ls/mcp-client/persistent-shell/state-dir-info/
	// webfetch-cached/web-search).
	if got, want := len(revokedFingerprints), 12; got != want {
		t.Errorf("revokedFingerprints: have %d entries, want %d", got, want)
	}
}

func TestErrRevoked_messageMentionsSeedAndSecurityMD(t *testing.T) {
	err := errRevoked("6c48b56f20c9c344")
	msg := err.Error()
	if !strings.Contains(msg, "browser-demo.seed") {
		t.Errorf("error should name the leaked seed file: %q", msg)
	}
	if !strings.Contains(msg, "SECURITY.md") {
		t.Errorf("error should point at SECURITY.md: %q", msg)
	}
	if !strings.Contains(msg, "revoked") {
		t.Errorf("error should say revoked: %q", msg)
	}
}
