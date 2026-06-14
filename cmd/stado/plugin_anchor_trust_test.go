package main

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
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

// TestAnchorStore_Remove: a cleared pin returns to first-sight (empty
// fingerprint), so a post-rotation reinstall can re-TOFU.
func TestAnchorStore_Remove(t *testing.T) {
	store := plugins.NewAnchorTrustStore(t.TempDir())
	const owner = "github.com/acme"
	if err := store.Trust(owner, "FP-1", plugins.AnchorTrustOwner); err != nil {
		t.Fatal(err)
	}
	if fp, _ := store.Fingerprint(owner); fp != "FP-1" {
		t.Fatalf("pre-remove fingerprint = %q; want FP-1", fp)
	}
	if err := store.Remove(owner); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fp, _ := store.Fingerprint(owner); fp != "" {
		t.Fatalf("post-remove fingerprint = %q; want empty (first sight)", fp)
	}
	// Idempotent: removing again is not an error.
	if err := store.Remove(owner); err != nil {
		t.Fatalf("second Remove should be no-op; got %v", err)
	}
}
