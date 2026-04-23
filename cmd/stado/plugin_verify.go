package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

var pluginVerifyCmd = &cobra.Command{
	Use:   "verify <plugin-dir>",
	Short: "Check a plugin's signature, wasm digest, and rollback state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		dir := args[0]
		m, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			return err
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		if err := plugins.VerifyWASMDigest(m.WASMSHA256, wasmPath); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(m, sig); err != nil {
			return fmt.Errorf("verify: %w", err)
		}

		// Consult the CRL (if configured). Trust-store verify gets the
		// manifest past signature + rollback checks; the CRL is an
		// independent, revocable no-list per DESIGN §"Phase 7.6".
		// Airgap path: use the cached CRL from disk if fetching fails.
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("verify: %w", err)
			}
		}

		// Consult Rekor transparency log (if configured). DESIGN §"Phase
		// 7.7": defence-in-depth on top of the trust store — proves the
		// signature was publicly logged before install. Absence is
		// advisory; mismatch is fatal.
		if cfg.Plugins.RekorURL != "" {
			entries, tsErr := ts.Load()
			if tsErr == nil {
				if entry, ok := entries[m.AuthorPubkeyFpr]; ok {
					pubBytes, pErr := hex.DecodeString(entry.Pubkey)
					if pErr != nil || len(pubBytes) != ed25519.PublicKeySize {
						return fmt.Errorf("verify: trust-store pubkey malformed for %s", m.AuthorPubkeyFpr)
					}
					canonical, cErr := m.Canonical()
					if cErr != nil {
						return fmt.Errorf("verify: canonicalise: %w", cErr)
					}
					sigBytes, sErr := base64.StdEncoding.DecodeString(sig)
					if sErr != nil {
						return fmt.Errorf("verify: decode signature: %w", sErr)
					}
					if err := consultRekor(cmd.Context(), cfg.Plugins.RekorURL, canonical, sigBytes, ed25519.PublicKey(pubBytes)); err != nil {
						return fmt.Errorf("verify: %w", err)
					}
				}
			}
		}

		fmt.Printf("OK  %s v%s  author=%s  sha256=%s  caps=%d\n",
			m.Name, m.Version, m.Author, m.WASMSHA256[:12], len(m.Capabilities))
		return nil
	},
}

// consultCRL loads the cached CRL (airgap-friendly path), optionally
// refreshes from cfg.Plugins.CRLURL if cached-and-signed, and returns a
// non-nil error iff the manifest is revoked. No-op when cfg has no CRL
// configured (caller checks first).
func consultCRL(cfg *config.Config, m *plugins.Manifest) error {
	crlPath := filepath.Join(cfg.StateDir(), "plugins", "crl.json")
	var crl *plugins.CRL

	var pub ed25519.PublicKey
	if cfg.Plugins.CRLIssuerPubkey == "" {
		fmt.Fprintln(os.Stderr,
			"crl: warning — plugins.crl_issuer_pubkey not set; CRL refresh skipped. Using cached copy if present.")
	} else {
		p, err := decodeEd25519Pub(cfg.Plugins.CRLIssuerPubkey)
		if err != nil {
			return fmt.Errorf("crl: decode issuer pubkey: %w", err)
		}
		pub = p
	}

	// Try to refresh from URL when we have an issuer key. Failures are
	// non-fatal — we fall back to the cached copy.
	if pub != nil {
		fresh, err := plugins.Fetch(cfg.Plugins.CRLURL, pub)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crl: fetch failed (%v); falling back to cached copy\n", err)
		} else {
			crl = fresh
			if err := plugins.SaveLocal(fresh, crlPath); err != nil {
				fmt.Fprintf(os.Stderr, "crl: cache write failed (%v); continuing with in-memory copy\n", err)
			}
		}
	}

	if crl == nil {
		var err error
		crl, err = plugins.LoadLocal(crlPath)
		if err != nil {
			return fmt.Errorf("crl: load cached: %w", err)
		}
	}
	if crl == nil {
		if pub == nil {
			// No pubkey, no cache — we can't meaningfully consult a
			// CRL. Advisory only; a misconfigured environment shouldn't
			// silently bypass verification, so surface the state.
			fmt.Fprintln(os.Stderr, "crl: no issuer pubkey and no cache; revocation check skipped.")
		}
		return nil
	}

	revoked, reason := crl.IsRevoked(m.AuthorPubkeyFpr, m.Version, m.WASMSHA256)
	if revoked {
		return fmt.Errorf("plugin %s v%s is revoked — %s", m.Name, m.Version, reason)
	}
	return nil
}

// consultRekor checks that the plugin manifest has a Rekor transparency
// log entry matching its signature + signer + canonical digest. Treats
// `ErrRekorNotFound` and airgap-disabled errors as advisory (stderr
// hint) rather than fatal — the manifest sig is already verified by
// the trust store, Rekor is defence-in-depth.
//
// Hard-fails only when an entry exists but its contents don't match
// (mismatched sig / pubkey / hash) — that's evidence of tampering.
func consultRekor(ctx context.Context, rekorURL string, canonicalBytes, sig []byte, pub ed25519.PublicKey) error {
	if rekorURL == "" {
		return nil
	}
	entry, err := plugins.SearchByHash(ctx, rekorURL, canonicalBytes)
	if err != nil {
		if errors.Is(err, plugins.ErrRekorNotFound) {
			fmt.Fprintf(os.Stderr,
				"rekor: no log entry for this manifest at %s — advisory only\n", rekorURL)
			return nil
		}
		// Network errors / airgap stubs: advisory, fall back to the
		// trust-store verification that already happened.
		fmt.Fprintf(os.Stderr,
			"rekor: lookup failed (%v); falling back to trust-store verification\n", err)
		return nil
	}
	digest := sha256.Sum256(canonicalBytes)
	if err := plugins.VerifyEntry(*entry, sig, pub, digest[:]); err != nil {
		return fmt.Errorf("rekor entry mismatch (tampering?): %w", err)
	}
	fmt.Fprintf(os.Stderr, "rekor: matched entry %s (log index %d)\n", entry.UUID, entry.LogIndex)
	return nil
}

// decodeEd25519Pub accepts hex (64 chars) or base64 (44 chars) of a
// 32-byte Ed25519 public key.
func decodeEd25519Pub(s string) (ed25519.PublicKey, error) {
	if len(s) == ed25519.PublicKeySize*2 {
		b, err := hex.DecodeString(s)
		if err == nil && len(b) == ed25519.PublicKeySize {
			return ed25519.PublicKey(b), nil
		}
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bad base64/hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pubkey size %d, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
