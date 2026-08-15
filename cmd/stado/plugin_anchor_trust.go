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
// Installs commit owner continuity and the signer key together in the atomic
// TrustStore file. Pre-v1 legacy split anchor metadata is deliberately not
// inferred: the operator must remove it and explicitly accept the owner again.

import (
	"encoding/hex"
	"errors"
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

type preparedAnchorTrust struct {
	OwnerKey    string
	Pubkey      string
	Fingerprint string
	FirstSight  bool
}

var fetchOwnerAnchorPubkey = plugins.FetchAnchorPubkey

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

// prepareAnchorTrust performs every owner/key decision without mutating trust
// state. The caller must verify digest, signature, package identity, CRL, and
// dependency policy before committing this candidate through
// TrustStore.TrustVerifiedAnchor.
func prepareAnchorTrust(cmd *cobra.Command, cfg *config.Config, id plugins.Identity, manifestFpr string, assumeYes bool) (preparedAnchorTrust, error) {
	trust := plugins.NewTrustStore(cfg.StateDir())
	unified, unifiedOK, err := trust.AnchorSigner(id.OwnerKey())
	if err != nil {
		return preparedAnchorTrust{}, fmt.Errorf("install: anchor trust read: %w", err)
	}
	cachedFP := ""
	cachedPubkey := ""
	if unifiedOK {
		cachedFP, cachedPubkey = unified.Fingerprint, unified.Pubkey
	}

	anchorPubkey, anchorFP, fetchOK, fetchErr := fetchAnchorKey(cmd, id)

	switch decideAnchor(cachedFP, anchorFP, manifestFpr, fetchOK) {
	case anchorProceed:
		if fetchOK {
			fmt.Fprintf(cmd.ErrOrStderr(), "anchor: %s trusted (fingerprint %s)\n", id.OwnerKey(), anchorFP)
			return preparedAnchorTrust{OwnerKey: id.OwnerKey(), Pubkey: anchorPubkey, Fingerprint: anchorFP}, nil
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "anchor: %s offline — using cached trust (fingerprint %s)\n", id.OwnerKey(), cachedFP)
			return preparedAnchorTrust{OwnerKey: id.OwnerKey(), Pubkey: cachedPubkey, Fingerprint: cachedFP}, nil
		}

	case anchorRefuseUnreachable:
		return preparedAnchorTrust{}, fmt.Errorf(
			"install refused: anchor repo at %s not reachable; first-time install requires anchor; cached owners are unaffected%s",
			id.AnchorURL(), fetchErrSuffix(fetchErr))

	case anchorRefuseSignerBinding:
		ref := anchorFP
		if !fetchOK {
			ref = cachedFP
		}
		return preparedAnchorTrust{}, fmt.Errorf(
			"install refused: %s's manifest is signed by %s but the owner's anchor key is %s — the plugin is not signed by the owner's anchor key (EP-0039 step 4)",
			id.OwnerKey(), manifestFpr, ref)

	case anchorRefuseMismatch:
		return preparedAnchorTrust{}, fmt.Errorf(
			"install refused: anchor fingerprint for %s changed (trusted %s, fetched %s) — possible key rotation or compromise. "+
				"If the rotation is expected, verify the new key out of band, then clear the pin with `stado plugin untrust-anchor %s` and reinstall",
			id.OwnerKey(), cachedFP, anchorFP, id.OwnerKey())

	case anchorFirstSight:
		if !assumeYes {
			if !promptYesNoTTY(cmd, fmt.Sprintf(
				"First install from %s. Trust anchor fingerprint %s for this owner's plugins?", id.OwnerKey(), anchorFP)) {
				return preparedAnchorTrust{}, fmt.Errorf(
					"install refused: anchor for %s not trusted on first sight. Re-run with --trust-anchor (after verifying the key out of band) or pre-trust via `stado plugin trust`",
					id.OwnerKey())
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "anchor: accepted %s on first sight (fingerprint %s); commit waits for package verification\n", id.OwnerKey(), anchorFP)
		return preparedAnchorTrust{OwnerKey: id.OwnerKey(), Pubkey: anchorPubkey, Fingerprint: anchorFP, FirstSight: true}, nil
	}
	return preparedAnchorTrust{}, errors.New("install: unknown anchor trust decision")
}

// fetchAnchorKey fetches and parses the owner's anchor pubkey, returning its
// canonical hex form, fingerprint, whether fetch+parse succeeded, and error (for the
// operator-facing message). A 404 (owner publishes no anchor) and a network
// error both yield fetchOK=false — first-time installs fail closed either way.
func fetchAnchorKey(cmd *cobra.Command, id plugins.Identity) (key, fingerprint string, ok bool, err error) {
	pubStr, ferr := fetchOwnerAnchorPubkey(cmd.Context(), id.AnchorURL())
	if ferr != nil {
		return "", "", false, ferr
	}
	pub, perr := plugins.ParsePubkey(strings.TrimSpace(pubStr))
	if perr != nil {
		return "", "", false, fmt.Errorf("anchor pubkey parse: %w", perr)
	}
	return hex.EncodeToString(pub), plugins.Fingerprint(pub), true, nil
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
