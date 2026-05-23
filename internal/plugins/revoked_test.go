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
	err := RevokedError("6c48b56f20c9c344")
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

// Defensive: RevokedError called with a non-revoked fingerprint shouldn't
// falsely claim revocation with an empty source. Should flag the caller bug.
func TestErrRevoked_unknownFingerprintReportsCallerBug(t *testing.T) {
	err := RevokedError("0000000000000000")
	msg := err.Error()
	if strings.Contains(msg, "is revoked:") {
		t.Errorf("should not falsely claim revocation for unknown fpr: %q", msg)
	}
	if !strings.Contains(msg, "internal error") {
		t.Errorf("should flag the caller bug: %q", msg)
	}
}

// Integration: VerifyManifest must reject a revoked fingerprint BEFORE
// consulting the trust store — i.e. even if the operator has explicitly
// trusted the key, the hard deny stands. (Trust path security guarantee.)
func TestVerifyManifest_revokedFingerprintRejectedEvenIfPinned(t *testing.T) {
	ts := NewTrustStore(t.TempDir())
	const revokedFpr = "6c48b56f20c9c344" // browser-demo.seed
	// Pre-populate the trust store with the revoked fpr (simulating an
	// operator who trusted it before the deny-list landed).
	if err := ts.Save(map[string]TrustEntry{
		revokedFpr: {
			Fingerprint: revokedFpr,
			Pubkey:      "db9710dd6dda135e5729f74cf2cdc8121a0628003ab8c89ea92986ec2922f67b",
		},
	}); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}
	err := ts.VerifyManifest(&Manifest{AuthorPubkeyFpr: revokedFpr}, "")
	if err == nil {
		t.Fatal("expected revoked error, got nil")
	}
	if !strings.Contains(err.Error(), "revoked") || !strings.Contains(err.Error(), "browser-demo.seed") {
		t.Errorf("expected revoked-error naming the leaked seed, got %v", err)
	}
}

// Integration: TrustVerified must reject before pinning — a TOFU install
// path cannot leave a revoked fingerprint in the trust store.
func TestTrustVerified_revokedFingerprintRejected_storeUnchanged(t *testing.T) {
	ts := NewTrustStore(t.TempDir())
	const revokedFpr = "6c48b56f20c9c344"
	_, err := ts.TrustVerified("", "", &Manifest{AuthorPubkeyFpr: revokedFpr}, "")
	if err == nil {
		t.Fatal("expected revoked error, got nil")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("expected revoked error, got %v", err)
	}
	// Store must remain empty — the deny runs before any Save.
	entries, err := ts.Load()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("trust store should be untouched, got %d entries", len(entries))
	}
}
