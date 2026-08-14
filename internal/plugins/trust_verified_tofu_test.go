package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins the TrustVerified (anchor / TOFU signer-pin) decision ladder.
// TrustVerified is the only install-time path that may *create* a trust-store
// entry, so the load-bearing security property is: every rejected branch must
// refuse BEFORE writing, leaving no pin behind. A regression that pins on a
// failed verify would silently anchor an unverified — or revoked — signer.
//
// Helpers/identifiers are prefixed tvtofu* to avoid clashing with sibling
// test files that may later land in this package.

// tvtofuStore returns a fresh file-backed TrustStore under a temp dir.
func tvtofuStore(t *testing.T) *TrustStore {
	t.Helper()
	return &TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
}

// tvtofuKey generates an ed25519 keypair plus its fingerprint.
func tvtofuKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("tvtofu: generate key: %v", err)
	}
	return pub, priv, Fingerprint(pub)
}

// tvtofuSignedManifest builds a manifest whose author fpr matches pub and
// signs it with priv, returning (manifest, signatureB64).
func tvtofuSignedManifest(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, version string) (*Manifest, string) {
	t.Helper()
	m := &Manifest{Name: "tvtofu-plugin", Version: version, Author: "alice", AuthorPubkeyFpr: Fingerprint(pub)}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatalf("tvtofu: sign manifest: %v", err)
	}
	return m, sig
}

// tvtofuAssertEmpty fails if the store has any pinned entries.
func tvtofuAssertEmpty(t *testing.T, ts *TrustStore) {
	t.Helper()
	entries, err := ts.Load()
	if err != nil {
		t.Fatalf("tvtofu: load store: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected path left a pin behind: %+v", entries)
	}
}

// 1. Happy path: a valid pubkey + correctly-signed manifest whose fingerprint
// matches author_pubkey_fpr pins, returns the right fingerprint, and records
// the manifest version for rollback protection.
func TestTrustVerified_HappyPath_PinsWithFingerprintAndVersion(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, fpr := tvtofuKey(t)
	m, sig := tvtofuSignedManifest(t, pub, priv, "1.2.3")

	entry, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", m, sig)
	if err != nil {
		t.Fatalf("TrustVerified happy path: %v", err)
	}
	if entry.Fingerprint != fpr {
		t.Fatalf("entry fingerprint = %q, want %q", entry.Fingerprint, fpr)
	}
	if entry.Pubkey != hex.EncodeToString(pub) {
		t.Errorf("entry pubkey = %q, want %q", entry.Pubkey, hex.EncodeToString(pub))
	}
	if entry.Author != "alice" {
		t.Errorf("entry author = %q, want alice", entry.Author)
	}
	if entry.LastVersion != "1.2.3" {
		t.Errorf("entry last_version = %q, want 1.2.3 (rollback baseline)", entry.LastVersion)
	}

	// The pin must be persisted under its fingerprint key.
	store, err := ts.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := store[fpr]
	if !ok {
		t.Fatalf("happy path did not persist a pin under fpr %q: %+v", fpr, store)
	}
	if got.Fingerprint != fpr || got.LastVersion != "1.2.3" {
		t.Errorf("persisted entry = %+v, want fpr %q version 1.2.3", got, fpr)
	}

	// Sanity: the pinned key actually verifies the manifest signature.
	if err := m.Verify(pub, sig); err != nil {
		t.Errorf("pinned key should verify the manifest: %v", err)
	}
}

func TestCheckManifestVerifiesWithoutAdvancingRollbackState(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, fpr := tvtofuKey(t)
	baseline, baselineSig := tvtofuSignedManifest(t, pub, priv, "1.2.3")
	if _, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", baseline, baselineSig); err != nil {
		t.Fatal(err)
	}

	newer, newerSig := tvtofuSignedManifest(t, pub, priv, "1.3.0")
	if err := ts.CheckManifest(newer, newerSig); err != nil {
		t.Fatalf("read-only check: %v", err)
	}
	entries, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := entries[fpr].LastVersion; got != "1.2.3" {
		t.Fatalf("CheckManifest advanced last_version to %q", got)
	}

	older, olderSig := tvtofuSignedManifest(t, pub, priv, "1.2.2")
	if err := ts.CheckManifest(older, olderSig); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("CheckManifest rollback err=%v", err)
	}
}

// 2. Fingerprint mismatch: presenting a pubkey whose fingerprint != the
// manifest's author_pubkey_fpr is rejected, even when the supplied signature
// is itself valid for the *presented* key. No pin is left behind.
func TestTrustVerified_FingerprintMismatch_RejectedNoPin(t *testing.T) {
	ts := tvtofuStore(t)
	authorPub, authorPriv, authorFpr := tvtofuKey(t)
	otherPub, _, _ := tvtofuKey(t)

	// Manifest claims the author's fingerprint, signed by the author's key
	// (a valid signature) — but we present a DIFFERENT pubkey to pin.
	m, sig := tvtofuSignedManifest(t, authorPub, authorPriv, "1.0.0")
	if Fingerprint(otherPub) == authorFpr {
		t.Fatal("test setup: distinct keys collided on fingerprint")
	}

	_, err := ts.TrustVerified(hex.EncodeToString(otherPub), "mallory", m, sig)
	if err == nil {
		t.Fatal("TrustVerified must reject when presented pubkey fpr != manifest author_pubkey_fpr")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should name the fingerprint mismatch, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}

// 3. Bad signature: correct fingerprint (presented key matches the manifest's
// author_pubkey_fpr) but the signature does not verify -> rejected, no pin.
func TestTrustVerified_BadSignature_RejectedNoPin(t *testing.T) {
	ts := tvtofuStore(t)
	pub, _, fpr := tvtofuKey(t)
	// Manifest with the correct author fingerprint, but a bogus signature
	// (valid base64, 88 chars = 64-byte ed25519 sig, just doesn't verify).
	m := &Manifest{Name: "tvtofu-plugin", Version: "1.0.0", AuthorPubkeyFpr: fpr}
	bogusSig := strings.Repeat("A", 88)

	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", m, bogusSig)
	if err == nil {
		t.Fatal("TrustVerified must reject a non-verifying signature")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error should mention the signature, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}

// 3b. Bad signature where the manifest was tampered AFTER signing: the
// fingerprint still matches the real signing key, the signature is well-formed,
// but the canonical bytes changed so it no longer verifies. Must reject, no pin.
func TestTrustVerified_TamperedManifest_RejectedNoPin(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, _ := tvtofuKey(t)
	m, sig := tvtofuSignedManifest(t, pub, priv, "1.0.0")
	// Tamper a signed field — capability list — invalidating the signature
	// over the canonical bytes while keeping the author fingerprint intact.
	m.Capabilities = append(m.Capabilities, "host.FSWrite")

	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", m, sig)
	if err == nil {
		t.Fatal("TrustVerified must reject a tampered manifest")
	}
	tvtofuAssertEmpty(t, ts)
}

// 4. Revoked: a pubkey whose fingerprint is on the leaked-seed deny-list is
// hard-denied BEFORE any signing/pinning. Even a perfectly valid signature
// over a matching manifest cannot anchor a known-compromised key.
func TestTrustVerified_RevokedFingerprint_HardDeniedBeforePin(t *testing.T) {
	ts := tvtofuStore(t)
	const revokedFpr = "6c48b56f20c9c344" // plugins/examples/browser/browser-demo.seed

	// Sanity: confirm the deny-list still carries this fingerprint, so the
	// test fails loudly if the constant or the list drifts apart.
	if rev, _ := IsRevoked(revokedFpr); !rev {
		t.Fatalf("test premise broken: %q no longer revoked", revokedFpr)
	}

	pub, priv, _ := tvtofuKey(t)
	// Force the manifest to advertise the revoked fingerprint. We sign with
	// our freshly generated key so the signature is structurally valid — the
	// only reason to reject is the revocation, which must fire first.
	m := &Manifest{Name: "tvtofu-plugin", Version: "1.0.0", AuthorPubkeyFpr: revokedFpr}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = ts.TrustVerified(hex.EncodeToString(pub), "alice", m, sig)
	if err == nil {
		t.Fatal("TrustVerified must hard-deny a revoked fingerprint")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error should say revoked, got %v", err)
	}
	if !strings.Contains(err.Error(), "browser-demo.seed") {
		t.Errorf("error should name the leaked seed source, got %v", err)
	}
	// The deny precedes entryForKey/Save entirely — nothing may be written,
	// neither the revoked fpr nor the (legitimate) presented key's fpr.
	tvtofuAssertEmpty(t, ts)
}

// 4b. Revocation runs before fingerprint matching: even when the presented key
// genuinely matches the manifest's revoked fingerprint AND the signature
// verifies, the hard deny still wins. Pins nothing.
func TestTrustVerified_RevokedFingerprint_WinsOverValidMatchingSigner(t *testing.T) {
	ts := tvtofuStore(t)
	const revokedFpr = "6c48b56f20c9c344"
	if rev, _ := IsRevoked(revokedFpr); !rev {
		t.Fatalf("test premise broken: %q no longer revoked", revokedFpr)
	}
	pub, priv, _ := tvtofuKey(t)
	// A self-consistent, validly-signed manifest whose declared fpr is the
	// presented key's own fpr — but we overwrite the declared fpr with the
	// revoked one. With a matching presented fpr the only barrier is the
	// revocation check; assert it fires.
	m, _ := tvtofuSignedManifest(t, pub, priv, "1.0.0")
	m.AuthorPubkeyFpr = revokedFpr
	// Re-sign over the now-revoked-fpr manifest so the signature itself is
	// valid for `pub` — isolating revocation as the sole rejection cause.
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = ts.TrustVerified(hex.EncodeToString(pub), "alice", m, sig)
	if err == nil {
		t.Fatal("revoked deny must win even with a matching, validly-signed key")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error should say revoked, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}

// 5. Rollback/downgrade: once a version is pinned via TrustVerified, a later
// TrustVerified with a LOWER (but validly-signed) version is rejected and the
// pinned LastVersion is NOT regressed.
func TestTrustVerified_Rollback_LowerVersionRejected_PinUnchanged(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, fpr := tvtofuKey(t)

	// Pin a high version first.
	mHigh, sigHigh := tvtofuSignedManifest(t, pub, priv, "2.0.0")
	if _, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mHigh, sigHigh); err != nil {
		t.Fatalf("initial high-version pin: %v", err)
	}

	// Attempt a lower version through the same TOFU path.
	mLow, sigLow := tvtofuSignedManifest(t, pub, priv, "1.0.0")
	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mLow, sigLow)
	if err == nil {
		t.Fatal("TrustVerified must reject a rollback to a lower version")
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Errorf("error should mention rollback, got %v", err)
	}

	// The existing pin must survive with its high LastVersion intact.
	store, err := ts.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := store[fpr]
	if !ok {
		t.Fatalf("rollback attempt dropped the existing pin: %+v", store)
	}
	if got.LastVersion != "2.0.0" {
		t.Errorf("LastVersion regressed to %q after a rejected rollback; want 2.0.0", got.LastVersion)
	}
}

// 5b. Rollback uses semver ordering, not lexical: 1.2.0 < 1.10.0 numerically.
func TestTrustVerified_Rollback_SemverNotLexical(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, _ := tvtofuKey(t)

	mHigh, sigHigh := tvtofuSignedManifest(t, pub, priv, "1.10.0")
	if _, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mHigh, sigHigh); err != nil {
		t.Fatalf("pin 1.10.0: %v", err)
	}
	mLow, sigLow := tvtofuSignedManifest(t, pub, priv, "1.2.0")
	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mLow, sigLow)
	if err == nil {
		t.Fatal("1.2.0 < 1.10.0 under semver must be rejected as a rollback")
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Errorf("error should mention rollback, got %v", err)
	}
}

// 5c. Forward progression: a higher version through TrustVerified advances the
// pinned LastVersion (the legitimate upgrade path the rollback guard protects).
func TestTrustVerified_ForwardUpgradeAdvancesVersion(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, fpr := tvtofuKey(t)

	mLow, sigLow := tvtofuSignedManifest(t, pub, priv, "1.0.0")
	if _, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mLow, sigLow); err != nil {
		t.Fatalf("pin 1.0.0: %v", err)
	}
	mHigh, sigHigh := tvtofuSignedManifest(t, pub, priv, "1.1.0")
	entry, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", mHigh, sigHigh)
	if err != nil {
		t.Fatalf("upgrade 1.0.0 -> 1.1.0 should succeed: %v", err)
	}
	if entry.LastVersion != "1.1.0" {
		t.Errorf("returned LastVersion = %q, want 1.1.0", entry.LastVersion)
	}
	store, _ := ts.Load()
	if store[fpr].LastVersion != "1.1.0" {
		t.Errorf("persisted LastVersion = %q, want 1.1.0", store[fpr].LastVersion)
	}
}

// Edge: invalid semver in the manifest version is rejected (it cannot
// participate in rollback protection) and leaves no pin.
func TestTrustVerified_InvalidSemverVersion_RejectedNoPin(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, fpr := tvtofuKey(t)
	// "build-20260614" parses as a prerelease-only string (no major.minor.patch).
	m := &Manifest{Name: "tvtofu-plugin", Version: "build-20260614", AuthorPubkeyFpr: fpr}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = ts.TrustVerified(hex.EncodeToString(pub), "alice", m, sig)
	if err == nil {
		t.Fatal("TrustVerified must reject a non-semver manifest version")
	}
	if !strings.Contains(err.Error(), "semver") {
		t.Errorf("error should mention semver, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}

// Edge: nil manifest is rejected with a clear error and pins nothing.
func TestTrustVerified_NilManifest_Rejected(t *testing.T) {
	ts := tvtofuStore(t)
	pub, _, _ := tvtofuKey(t)
	_, err := ts.TrustVerified(hex.EncodeToString(pub), "alice", nil, "")
	if err == nil {
		t.Fatal("TrustVerified(nil manifest) must error")
	}
	if !strings.Contains(err.Error(), "nil manifest") {
		t.Errorf("error should name the nil manifest, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}

// Edge: a malformed pubkey string is rejected before any store mutation.
func TestTrustVerified_MalformedPubkey_RejectedNoPin(t *testing.T) {
	ts := tvtofuStore(t)
	pub, priv, _ := tvtofuKey(t)
	m, sig := tvtofuSignedManifest(t, pub, priv, "1.0.0")

	_, err := ts.TrustVerified("not-a-valid-key", "alice", m, sig)
	if err == nil {
		t.Fatal("TrustVerified must reject a malformed pubkey")
	}
	if !strings.Contains(err.Error(), "pubkey") {
		t.Errorf("error should mention the bad pubkey, got %v", err)
	}
	tvtofuAssertEmpty(t, ts)
}
