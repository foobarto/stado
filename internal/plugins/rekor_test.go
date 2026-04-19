package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestNewHashedRekord_ShapeAndHashMatchInputs asserts that the
// canonical fields inside the constructed entry line up with the
// inputs — the digest hex, the signature base64, and the PEM envelope.
func TestNewHashedRekord_ShapeAndHashMatchInputs(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("fake canonical manifest\n")
	digest := sha256.Sum256(manifest)
	sig := []byte("fake-signature-bytes-64-long-enough-for-the-schema-ok")

	entry, err := NewHashedRekord(digest[:], sig, pub)
	if err != nil {
		t.Fatalf("NewHashedRekord: %v", err)
	}
	if entry.APIVersion != "0.0.1" || entry.Kind != "hashedrekord" {
		t.Errorf("unexpected APIVersion/Kind: %+v", entry)
	}
	if entry.Spec.Data.Hash.Algorithm != "sha256" {
		t.Errorf("algo=%q, want sha256", entry.Spec.Data.Hash.Algorithm)
	}
	if entry.Spec.Data.Hash.Value != hex.EncodeToString(digest[:]) {
		t.Errorf("hash value mismatch")
	}
	if entry.Spec.Signature.PublicKey.Content == "" {
		t.Error("PEM pubkey empty")
	}
}

// TestNewHashedRekord_RejectsWrongDigestLen catches programmer error
// early — Rekor's sha256 field is fixed at 32 bytes.
func TestNewHashedRekord_RejectsWrongDigestLen(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := NewHashedRekord([]byte{1, 2, 3}, []byte("x"), pub)
	if err == nil {
		t.Fatal("expected error on short digest")
	}
}

// TestVerifyEntry_RoundTrip uses NewHashedRekord → serialise (simulate
// Rekor echo) → VerifyEntry and asserts match.
func TestVerifyEntry_RoundTrip(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	manifest := []byte("manifest-bytes")
	digest := sha256.Sum256(manifest)
	sig := []byte("sig-bytes")

	h, err := NewHashedRekord(digest[:], sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	bodyB64 := encodeHashedRekordBody(t, h)
	entry := RekorEntry{UUID: "abc", LogIndex: 42, Body: bodyB64}
	if err := VerifyEntry(entry, sig, pub, digest[:]); err != nil {
		t.Fatalf("VerifyEntry: %v", err)
	}
}

// TestVerifyEntry_RejectsMismatchedSig refuses when Rekor's echo has a
// different signature than the caller expects.
func TestVerifyEntry_RejectsMismatchedSig(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	digest := sha256.Sum256([]byte("x"))
	h, _ := NewHashedRekord(digest[:], []byte("sigA"), pub)
	body := encodeHashedRekordBody(t, h)
	if err := VerifyEntry(RekorEntry{Body: body}, []byte("sigB"), pub, digest[:]); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

// TestVerifyEntry_RejectsMismatchedPubkey covers the case where a
// collision on (hash, sig) but a different signer is presented.
func TestVerifyEntry_RejectsMismatchedPubkey(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	digest := sha256.Sum256([]byte("x"))
	h, _ := NewHashedRekord(digest[:], []byte("sig"), pub1)
	body := encodeHashedRekordBody(t, h)
	if err := VerifyEntry(RekorEntry{Body: body}, []byte("sig"), pub2, digest[:]); err == nil {
		t.Fatal("expected pubkey mismatch error")
	}
}

// TestVerifyEntry_RejectsMismatchedHash asserts the hash inside the
// entry must match the caller's expected sha256.
func TestVerifyEntry_RejectsMismatchedHash(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	digestA := sha256.Sum256([]byte("A"))
	digestB := sha256.Sum256([]byte("B"))
	h, _ := NewHashedRekord(digestA[:], []byte("sig"), pub)
	body := encodeHashedRekordBody(t, h)
	if err := VerifyEntry(RekorEntry{Body: body}, []byte("sig"), pub, digestB[:]); err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

// encodeHashedRekordBody mimics what Rekor echoes: the canonical JSON
// entry body, base64-encoded.
func encodeHashedRekordBody(t *testing.T, h HashedRekord) string {
	t.Helper()
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
