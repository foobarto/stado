package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSignedCRL(t *testing.T, entries []CRLEntry) (*CRL, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	c := &CRL{
		Version:  1,
		IssuedAt: time.Now().UTC().Truncate(time.Second),
		Entries:  entries,
	}
	if err := c.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return c, pub
}

func TestCRLIsRevokedExactMatch(t *testing.T) {
	c, _ := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "fpr1", Version: "1.0.0", WASMSha256: "aabb", Reason: "bug in auth"},
	})
	revoked, reason := c.IsRevoked("fpr1", "1.0.0", "aabb")
	if !revoked || reason != "bug in auth" {
		t.Errorf("exact match: revoked=%v reason=%q", revoked, reason)
	}

	revoked, _ = c.IsRevoked("fpr1", "1.0.1", "aabb")
	if revoked {
		t.Errorf("version mismatch should not revoke")
	}
}

func TestCRLIsRevokedVersionWildcard(t *testing.T) {
	c, _ := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "fpr1", Version: "", WASMSha256: "", Reason: "author compromised"},
	})
	for _, v := range []string{"1.0.0", "2.0.0", "99.0.0"} {
		revoked, _ := c.IsRevoked("fpr1", v, "any-sha")
		if !revoked {
			t.Errorf("wildcard entry should revoke version %s", v)
		}
	}
	if r, _ := c.IsRevoked("other-fpr", "1.0.0", "any"); r {
		t.Errorf("wildcard entry only applies to matching fingerprint")
	}
}

func TestCRLSignRoundTrip(t *testing.T) {
	c, pub := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "abc", Version: "1.0.0", WASMSha256: "deadbeef"},
	})
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify re-parse + verification succeeds.
	parsed, err := parseAndVerify(raw, pub)
	if err != nil {
		t.Fatalf("parseAndVerify: %v", err)
	}
	if len(parsed.Entries) != 1 {
		t.Errorf("entries = %d", len(parsed.Entries))
	}
}

func TestCRLSignatureMismatchRejected(t *testing.T) {
	c, _ := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "abc", Version: "1.0.0"},
	})
	raw, _ := json.Marshal(c)
	// Generate a different pubkey and verify against it — should fail.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := parseAndVerify(raw, other); err == nil {
		t.Error("expected verification to fail against wrong pubkey")
	}
}

func TestCRLTamperDetected(t *testing.T) {
	c, pub := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "abc", Version: "1.0.0", Reason: "original"},
	})
	raw, _ := json.Marshal(c)
	tampered := strings.Replace(string(raw), "original", "BADNESS", 1)
	if _, err := parseAndVerify([]byte(tampered), pub); err == nil {
		t.Error("tamper should invalidate signature")
	}
}

func TestLoadLocalMissingIsNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	c, err := LoadLocal(path)
	if err != nil {
		t.Fatalf("missing file = error: %v", err)
	}
	if c != nil {
		t.Error("missing file should return nil CRL")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	c, _ := newSignedCRL(t, []CRLEntry{
		{AuthorFingerprint: "abc", Version: "1.0.0"},
	})
	path := filepath.Join(t.TempDir(), "crl.json")
	if err := SaveLocal(c, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadLocal(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].AuthorFingerprint != "abc" {
		t.Errorf("round-trip mismatch: %+v", loaded)
	}
}

// Fetch tests live in crl_fetch_test.go (tagged !airgap) so they only
// build in the online flavour.
