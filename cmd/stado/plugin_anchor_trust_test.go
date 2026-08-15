package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/spf13/cobra"
)

// EP-0039: decideAnchor is the pure trust decision — anchor TOFU bound to the
// manifest signer, with fail-closed behaviour when the anchor can't be fetched.
func TestDecideAnchor(t *testing.T) {
	const (
		anchorFP   = "FP-ANCHOR"
		otherFP    = "FP-OTHER"
		manifestFP = "FP-ANCHOR" // a well-formed plugin: signer == anchor
	)
	cases := []struct {
		name        string
		cachedFP    string
		anchorFP    string
		manifestFpr string
		fetchOK     bool
		want        anchorDecision
	}{
		// Fetch OK, signer matches anchor.
		{"first sight, signer==anchor", "", anchorFP, manifestFP, true, anchorFirstSight},
		{"cached, unchanged", anchorFP, anchorFP, manifestFP, true, anchorProceed},
		// Rotation: anchor + manifest both moved to the new key, but the cached
		// pin still holds the old one → refuse until the pin is cleared.
		{"cached, rotated (signer follows new anchor)", anchorFP, "FP-NEW", "FP-NEW", true, anchorRefuseMismatch},
		// Fetch OK, signer does NOT match anchor (the P1: another trusted key
		// signed the manifest).
		{"first sight, signer!=anchor", "", anchorFP, otherFP, true, anchorRefuseSignerBinding},
		{"cached match but signer!=anchor", anchorFP, anchorFP, otherFP, true, anchorRefuseSignerBinding},
		// Fetch FAILED.
		{"first sight, anchor unreachable", "", "", manifestFP, false, anchorRefuseUnreachable},
		{"cached, anchor offline, signer matches cached", anchorFP, "", anchorFP, false, anchorProceed},
		{"cached, anchor offline, signer mismatches cached", anchorFP, "", otherFP, false, anchorRefuseSignerBinding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideAnchor(tc.cachedFP, tc.anchorFP, tc.manifestFpr, tc.fetchOK)
			if got != tc.want {
				t.Errorf("decideAnchor(%q,%q,%q,%v) = %d; want %d",
					tc.cachedFP, tc.anchorFP, tc.manifestFpr, tc.fetchOK, got, tc.want)
			}
		})
	}
}

func TestPrepareAnchorTrustFirstInstallOfflineReuseAndRotation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := &config.Config{}
	id, err := plugins.ParseIdentity("github.com/acme/stado-plugins/supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)
	fpr := plugins.Fingerprint(pub)
	originalFetch := fetchOwnerAnchorPubkey
	t.Cleanup(func() { fetchOwnerAnchorPubkey = originalFetch })
	fetchOwnerAnchorPubkey = func(context.Context, string) (string, error) { return pubHex, nil }
	cmd := &cobra.Command{}

	prepared, err := prepareAnchorTrust(cmd, cfg, id, fpr, true)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.FirstSight || prepared.Pubkey != pubHex {
		t.Fatalf("prepared first sight = %#v", prepared)
	}
	store := plugins.NewTrustStore(cfg.StateDir())
	if entries, err := store.Load(); err != nil || len(entries) != 0 {
		t.Fatalf("preparation mutated trust: entries=%#v err=%v", entries, err)
	}
	m := &plugins.Manifest{Name: "supervise", Version: "1.0.0", AuthorPubkeyFpr: fpr}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrustVerifiedAnchor(prepared.Pubkey, "official", id.Namespace(), prepared.OwnerKey, m, sig); err != nil {
		t.Fatal(err)
	}

	fetchOwnerAnchorPubkey = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	offline, err := prepareAnchorTrust(cmd, cfg, id, fpr, false)
	if err != nil {
		t.Fatalf("offline cached anchor: %v", err)
	}
	if offline.Pubkey != pubHex || offline.FirstSight {
		t.Fatalf("offline candidate = %#v", offline)
	}

	rotatedPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fetchOwnerAnchorPubkey = func(context.Context, string) (string, error) { return hex.EncodeToString(rotatedPub), nil }
	if _, err := prepareAnchorTrust(cmd, cfg, id, plugins.Fingerprint(rotatedPub), true); err == nil {
		t.Fatal("anchor rotation unexpectedly bypassed the existing owner pin")
	}
	anchored, ok, err := store.AnchorSigner(id.OwnerKey())
	if err != nil || !ok || anchored.Fingerprint != fpr {
		t.Fatalf("rotation changed trust: entry=%#v ok=%v err=%v", anchored, ok, err)
	}
}
