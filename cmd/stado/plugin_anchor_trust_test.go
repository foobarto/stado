package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// EP-0039: owner anchor trust-on-first-use. evaluateAnchorTrust is the pure
// decision core; fingerprintFromAnchorFile reads the fetched author.pubkey.

func TestEvaluateAnchorTrust_Verdicts(t *testing.T) {
	store := plugins.NewAnchorTrustStore(t.TempDir())
	const owner = "github.com/acme"

	// No fetched fingerprint → no anchor published.
	if v, _, err := evaluateAnchorTrust(store, owner, ""); err != nil || v != anchorNoPubkey {
		t.Fatalf("empty fp: verdict %v err %v; want anchorNoPubkey", v, err)
	}

	// Owner never seen → first sight.
	if v, _, err := evaluateAnchorTrust(store, owner, "FP-1"); err != nil || v != anchorFirstSight {
		t.Fatalf("unseen owner: verdict %v err %v; want anchorFirstSight", v, err)
	}

	// After trusting, the same fingerprint is trusted; a different one mismatches.
	if err := store.Trust(owner, "FP-1", plugins.AnchorTrustOwner); err != nil {
		t.Fatal(err)
	}
	if v, stored, err := evaluateAnchorTrust(store, owner, "FP-1"); err != nil || v != anchorTrusted || stored != "FP-1" {
		t.Fatalf("matching fp: verdict %v stored %q err %v; want anchorTrusted/FP-1", v, stored, err)
	}
	if v, stored, err := evaluateAnchorTrust(store, owner, "FP-2"); err != nil || v != anchorMismatch || stored != "FP-1" {
		t.Fatalf("changed fp: verdict %v stored %q err %v; want anchorMismatch/FP-1", v, stored, err)
	}
}

func TestFingerprintFromAnchorFile(t *testing.T) {
	dir := t.TempDir()

	// Absent file → ("", nil): owner publishes no anchor.
	if fp, err := fingerprintFromAnchorFile(dir); err != nil || fp != "" {
		t.Fatalf("absent anchor: fp %q err %v; want empty/nil", fp, err)
	}

	// A real pubkey → fingerprint equals plugins.Fingerprint(pub).
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "author.pubkey"), []byte(hex.EncodeToString(pub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := fingerprintFromAnchorFile(dir)
	if err != nil {
		t.Fatalf("read anchor: %v", err)
	}
	if want := plugins.Fingerprint(pub); got != want {
		t.Fatalf("fingerprint = %q; want %q", got, want)
	}

	// Garbage content → parse error (not silently treated as "no anchor").
	if err := os.WriteFile(filepath.Join(dir, "author.pubkey"), []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprintFromAnchorFile(dir); err == nil {
		t.Fatal("garbage anchor pubkey should error, got nil")
	}
}
