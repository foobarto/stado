package main

// plugin_anchor_trust.go — owner anchor trust-on-first-use for remote installs
// (EP-0039). The remote fetch already downloads the owner's `author.pubkey`
// from the well-known anchor; this gate compares its fingerprint against the
// per-owner AnchorTrustStore and enforces TOFU:
//
//   - first sight of an owner  → prompt (TTY) or refuse (non-TTY) unless
//     --trust-anchor is passed; on accept, record the fingerprint
//   - fingerprint matches       → proceed silently
//   - fingerprint changed       → refuse (rotation / compromise)
//   - no anchor published        → warn + proceed (signature still verifies)
//
// Distinct from the per-KEY TrustStore (signer pinning) the install already
// runs — this is per-OWNER identity continuity.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/mattn/go-isatty"
)

type anchorTrustVerdict int

const (
	anchorNoPubkey   anchorTrustVerdict = iota // no author.pubkey published — can't TOFU
	anchorTrusted                              // fetched fingerprint matches the stored one
	anchorFirstSight                           // owner never seen before — TOFU candidate
	anchorMismatch                             // stored fingerprint differs — refuse
)

// evaluateAnchorTrust is the pure decision: compare a fetched anchor
// fingerprint (empty = none published) against the per-owner store. No IO
// beyond the store read, so it's unit-testable.
func evaluateAnchorTrust(store *plugins.AnchorTrustStore, ownerKey, fetchedFingerprint string) (verdict anchorTrustVerdict, stored string, err error) {
	if fetchedFingerprint == "" {
		return anchorNoPubkey, "", nil
	}
	stored, err = store.Fingerprint(ownerKey)
	if err != nil {
		return 0, "", err
	}
	// fetchedFingerprint is non-empty here (the no-pubkey case returned above),
	// so a tagged switch on stored is unambiguous.
	switch stored {
	case "":
		return anchorFirstSight, "", nil
	case fetchedFingerprint:
		return anchorTrusted, stored, nil
	default:
		return anchorMismatch, stored, nil
	}
}

// fingerprintFromAnchorFile reads <dir>/author.pubkey and returns its
// fingerprint. Returns ("", nil) when the file is absent (owner publishes no
// anchor). Returns an error only when the file exists but can't be parsed.
func fingerprintFromAnchorFile(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "author.pubkey"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", nil
	}
	pub, err := plugins.ParsePubkey(raw)
	if err != nil {
		return "", fmt.Errorf("anchor pubkey parse: %w", err)
	}
	return plugins.Fingerprint(pub), nil
}

// enforceAnchorTrust gates a remote install on owner anchor TOFU. stagingDir is
// the fetched plugin dir (containing author.pubkey, if any). assumeYes accepts
// a first-sight anchor without prompting (the --trust-anchor flag).
func enforceAnchorTrust(cmd *cobra.Command, cfg *config.Config, id plugins.Identity, stagingDir string, assumeYes bool) error {
	fp, err := fingerprintFromAnchorFile(stagingDir)
	if err != nil {
		return fmt.Errorf("install: anchor: %w", err)
	}
	store := plugins.NewAnchorTrustStore(cfg.StateDir())
	verdict, stored, err := evaluateAnchorTrust(store, id.OwnerKey(), fp)
	if err != nil {
		return fmt.Errorf("install: anchor trust: %w", err)
	}
	switch verdict {
	case anchorNoPubkey:
		fmt.Fprintf(cmd.ErrOrStderr(),
			"stado: warn: %s publishes no anchor pubkey — skipping owner trust-on-first-use (the manifest signature is still verified)\n",
			id.OwnerKey())
		return nil
	case anchorTrusted:
		fmt.Fprintf(cmd.ErrOrStderr(), "anchor: %s trusted (fingerprint %s)\n", id.OwnerKey(), fp)
		return nil
	case anchorMismatch:
		return fmt.Errorf(
			"install refused: anchor fingerprint for %s changed (trusted %s, fetched %s) — possible key rotation or compromise. "+
				"If the rotation is expected, verify the new key out of band, then `stado plugin untrust`/re-trust before reinstalling",
			id.OwnerKey(), stored, fp)
	case anchorFirstSight:
		if !assumeYes {
			if !promptYesNoTTY(cmd, fmt.Sprintf(
				"First install from %s. Trust anchor fingerprint %s for this owner's plugins?", id.OwnerKey(), fp)) {
				return fmt.Errorf(
					"install refused: anchor for %s not trusted on first sight. Re-run with --trust-anchor (after verifying the key out of band) or pre-trust via `stado plugin trust`",
					id.OwnerKey())
			}
		}
		if err := store.Trust(id.OwnerKey(), fp, plugins.AnchorTrustOwner); err != nil {
			return fmt.Errorf("install: record anchor trust: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "anchor: trusted %s on first sight (fingerprint %s, scope owner)\n", id.OwnerKey(), fp)
		return nil
	}
	return nil
}

// promptYesNoTTY asks a yes/no question on an interactive terminal. On a
// non-TTY stdin it returns false (refuse) rather than blocking — first-sight
// anchor trust must be an explicit decision, never a silent default.
func promptYesNoTTY(cmd *cobra.Command, question string) bool {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s [non-interactive: refusing — pass --trust-anchor to accept]\n", question)
		return false
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N]: ", question)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
