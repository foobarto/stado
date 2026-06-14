package main

// plugin_anchor_trust.go — owner anchor trust-on-first-use for remote installs
// (EP-0039 §A steps 2/4 + "Anchor unavailability"). On a remote install we
// fetch the owner's anchor pubkey from the well-known location and enforce:
//
//   - the manifest's signer fingerprint MUST equal the anchor fingerprint
//     (the manifest is signed by the owner's anchor key — EP step 4); else
//     refuse, even if some other key is globally trusted
//   - first sight of an owner → prompt (TTY) / --trust-anchor / refuse (non-TTY);
//     record the fingerprint on accept
//   - cached owner, fingerprint unchanged → proceed
//   - cached owner, fingerprint changed → refuse (rotation / compromise),
//     pointing at `stado plugin untrust-anchor`
//   - first sight + anchor unreachable or unpublished → refuse: first-time
//     install requires a reachable anchor (cached owners are unaffected)
//
// Distinct from the per-KEY signer TrustStore the install already runs — this
// is per-OWNER identity continuity, and it binds that identity to the signature.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/mattn/go-isatty"
)

type anchorDecision int

const (
	anchorProceed             anchorDecision = iota // trusted + signer matches → proceed
	anchorFirstSight                                // owner never seen → prompt / flag / refuse
	anchorRefuseMismatch                            // cached fingerprint != fetched (rotation)
	anchorRefuseSignerBinding                       // anchor fingerprint != manifest signer
	anchorRefuseUnreachable                         // first sight + anchor not obtained
)

// decideAnchor is the pure trust decision. Inputs:
//   - cachedFP:     the fingerprint stored for this owner ("" = first sight)
//   - anchorFP:     the fingerprint fetched from the owner's anchor ("" when the
//     fetch failed; valid only when fetchOK)
//   - manifestFpr:  the signer fingerprint the manifest claims (author_pubkey_fpr)
//   - fetchOK:      whether the anchor pubkey was successfully fetched
//
// No IO — fully unit-testable.
func decideAnchor(cachedFP, anchorFP, manifestFpr string, fetchOK bool) anchorDecision {
	if !fetchOK {
		// Anchor not obtained (404 / network error / proxy block).
		if cachedFP == "" {
			return anchorRefuseUnreachable // first-time install requires anchor
		}
		// Cached owner: EP says cached owners are unaffected by anchor outage,
		// but the manifest must still be signed by the trusted anchor key.
		if manifestFpr != cachedFP {
			return anchorRefuseSignerBinding
		}
		return anchorProceed
	}
	// Anchor fetched. The manifest MUST be signed by the anchor key (EP step 4),
	// otherwise a manifest signed by some other globally-trusted key would pass.
	if manifestFpr != anchorFP {
		return anchorRefuseSignerBinding
	}
	switch cachedFP {
	case "":
		return anchorFirstSight
	case anchorFP:
		return anchorProceed
	default:
		return anchorRefuseMismatch
	}
}

// enforceAnchorTrust gates a remote install on owner anchor TOFU + signer
// binding. manifestFpr is the manifest's author_pubkey_fpr; assumeYes accepts a
// first-sight anchor without prompting (--trust-anchor).
func enforceAnchorTrust(cmd *cobra.Command, cfg *config.Config, id plugins.Identity, manifestFpr string, assumeYes bool) error {
	store := plugins.NewAnchorTrustStore(cfg.StateDir())
	cachedFP, err := store.Fingerprint(id.OwnerKey())
	if err != nil {
		return fmt.Errorf("install: anchor trust read: %w", err)
	}

	anchorFP, fetchOK, fetchErr := fetchAnchorFingerprint(cmd, id)

	switch decideAnchor(cachedFP, anchorFP, manifestFpr, fetchOK) {
	case anchorProceed:
		if fetchOK {
			fmt.Fprintf(cmd.ErrOrStderr(), "anchor: %s trusted (fingerprint %s)\n", id.OwnerKey(), anchorFP)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "anchor: %s offline — using cached trust (fingerprint %s)\n", id.OwnerKey(), cachedFP)
		}
		return nil

	case anchorRefuseUnreachable:
		return fmt.Errorf(
			"install refused: anchor repo at %s not reachable; first-time install requires anchor; cached owners are unaffected%s",
			id.AnchorURL(), fetchErrSuffix(fetchErr))

	case anchorRefuseSignerBinding:
		ref := anchorFP
		if !fetchOK {
			ref = cachedFP
		}
		return fmt.Errorf(
			"install refused: %s's manifest is signed by %s but the owner's anchor key is %s — the plugin is not signed by the owner's anchor key (EP-0039 step 4)",
			id.OwnerKey(), manifestFpr, ref)

	case anchorRefuseMismatch:
		return fmt.Errorf(
			"install refused: anchor fingerprint for %s changed (trusted %s, fetched %s) — possible key rotation or compromise. "+
				"If the rotation is expected, verify the new key out of band, then clear the pin with `stado plugin untrust-anchor %s` and reinstall",
			id.OwnerKey(), cachedFP, anchorFP, id.OwnerKey())

	case anchorFirstSight:
		if !assumeYes {
			if !promptYesNoTTY(cmd, fmt.Sprintf(
				"First install from %s. Trust anchor fingerprint %s for this owner's plugins?", id.OwnerKey(), anchorFP)) {
				return fmt.Errorf(
					"install refused: anchor for %s not trusted on first sight. Re-run with --trust-anchor (after verifying the key out of band) or pre-trust via `stado plugin trust`",
					id.OwnerKey())
			}
		}
		if err := store.Trust(id.OwnerKey(), anchorFP, plugins.AnchorTrustOwner); err != nil {
			return fmt.Errorf("install: record anchor trust: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "anchor: trusted %s on first sight (fingerprint %s, scope owner)\n", id.OwnerKey(), anchorFP)
		return nil
	}
	return nil
}

// fetchAnchorFingerprint fetches and parses the owner's anchor pubkey, returning
// its fingerprint, whether the fetch+parse succeeded, and any error (for the
// operator-facing message). A 404 (owner publishes no anchor) and a network
// error both yield fetchOK=false — first-time installs fail closed either way.
func fetchAnchorFingerprint(cmd *cobra.Command, id plugins.Identity) (fingerprint string, ok bool, err error) {
	pubStr, ferr := plugins.FetchAnchorPubkey(cmd.Context(), id.AnchorURL())
	if ferr != nil {
		return "", false, ferr
	}
	pub, perr := plugins.ParsePubkey(strings.TrimSpace(pubStr))
	if perr != nil {
		return "", false, fmt.Errorf("anchor pubkey parse: %w", perr)
	}
	return plugins.Fingerprint(pub), true, nil
}

func fetchErrSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
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
	var resp string
	_, _ = fmt.Fscanln(cmd.InOrStdin(), &resp)
	switch strings.ToLower(strings.TrimSpace(resp)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
