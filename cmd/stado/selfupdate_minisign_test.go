//go:build !airgap

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/audit"
)

// swapPubkey sets audit.EmbeddedMinisignPubkey for one test and restores
// the prior value via t.Cleanup.
func swapPubkey(t *testing.T, newVal string) {
	t.Helper()
	prev := audit.EmbeddedMinisignPubkey
	audit.EmbeddedMinisignPubkey = newVal
	t.Cleanup(func() { audit.EmbeddedMinisignPubkey = prev })
}

// TestVerifyChecksumsMinisig_NoPinAndNoSig: self-update must refuse to run
// without an embedded minisign trust root.
func TestVerifyChecksumsMinisig_NoPinAndNoSig(t *testing.T) {
	swapPubkey(t, "")
	err := verifyChecksumsMinisig([]byte("irrelevant"), "")
	if err == nil {
		t.Fatal("expected unpinned build to reject self-update")
	}
	if !strings.Contains(err.Error(), "embedded minisign pubkey") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestVerifyChecksumsMinisig_NoPinWithSig: a release-side minisig is not
// enough if the local build has no embedded verification key.
func TestVerifyChecksumsMinisig_NoPinWithSig(t *testing.T) {
	swapPubkey(t, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("bogus"))
	}))
	defer srv.Close()
	err := verifyChecksumsMinisig([]byte("checksums"), srv.URL)
	if err == nil {
		t.Fatal("expected unpinned build to reject self-update")
	}
	if !strings.Contains(err.Error(), "embedded minisign pubkey") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestVerifyChecksumsMinisig_PinWithoutSig: a pinned pubkey must make
// a missing minisig a hard failure; otherwise signature stripping
// silently downgrades trust to sha256-only.
func TestVerifyChecksumsMinisig_PinWithoutSig(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	swapPubkey(t, base64.StdEncoding.EncodeToString(pub))
	err := verifyChecksumsMinisig([]byte("checksums"), "")
	if err == nil {
		t.Fatal("expected pin + no-sig path to fail")
	}
	if !strings.Contains(err.Error(), "checksums.txt.minisig") {
		t.Fatalf("missing-minisig error = %v", err)
	}
}

// TestVerifyChecksumsMinisig_PinAndValidSig is the happy path the
// post-PR-O build will hit. Generate a throwaway keypair, swap the
// embedded pubkey, sign + serve a minisig, assert the check passes.
func TestVerifyChecksumsMinisig_PinAndValidSig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	swapPubkey(t, base64.StdEncoding.EncodeToString(pub))

	checksums := []byte("fake-checksum-blob\n")
	sig, err := audit.MinisignSign(priv, 0xbeefcafe, checksums,
		"untrusted comment", "trusted comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(sig)
	}))
	defer srv.Close()

	if err := verifyChecksumsMinisig(checksums, srv.URL); err != nil {
		t.Fatalf("valid minisig rejected: %v", err)
	}
}

// TestVerifyChecksumsMinisig_PinAndInvalidSig — the refusal path.
// Sign with one key, embed a different key, expect rejection.
func TestVerifyChecksumsMinisig_PinAndInvalidSig(t *testing.T) {
	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	swapPubkey(t, base64.StdEncoding.EncodeToString(pub2))

	checksums := []byte("blob\n")
	sig, _ := audit.MinisignSign(priv1, 0, checksums, "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(sig)
	}))
	defer srv.Close()

	err := verifyChecksumsMinisig(checksums, srv.URL)
	if err == nil {
		t.Fatal("expected verification failure for mismatched pubkey")
	}
	if !strings.Contains(err.Error(), "minisign") {
		t.Errorf("error should mention minisign: %v", err)
	}
}

// TestVerifyChecksumsMinisig_MalformedPubkey surfaces a clean error —
// a garbled embedded pubkey is a build-time bug, not a runtime advisory.
func TestVerifyChecksumsMinisig_MalformedPubkey(t *testing.T) {
	swapPubkey(t, "not-valid-base64!!!")

	// Present a minisig URL so we reach the decode path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("irrelevant"))
	}))
	defer srv.Close()

	err := verifyChecksumsMinisig([]byte("x"), srv.URL)
	if err == nil {
		t.Fatal("expected malformed-pubkey error")
	}
	if !strings.Contains(err.Error(), "pubkey") {
		t.Errorf("error should mention pubkey: %v", err)
	}
}
