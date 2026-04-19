package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage trusted plugin signers + verify plugin packages",
	Long: "stado's plugin model: every plugin ships a signed manifest. Before it\n" +
		"can run, the author's Ed25519 public key must be pinned via\n" +
		"`stado plugin trust`, and the signature + wasm sha256 + minimum\n" +
		"stado-version + rollback protection are all checked by\n" +
		"`stado plugin verify`.\n\n" +
		"The wazero runtime that actually executes plugin wasm is a follow-up;\n" +
		"the trust layer lands first so an unsigned or downgraded plugin can\n" +
		"never reach the runtime.",
}

var pluginTrustCmd = &cobra.Command{
	Use:   "trust <pubkey> [author-name]",
	Short: "Pin a plugin author's Ed25519 public key (hex or base64)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		author := ""
		if len(args) == 2 {
			author = args[1]
		}
		e, err := ts.Trust(args[0], author)
		if err != nil {
			return err
		}
		fmt.Printf("trusted %s  author=%s\n", e.Fingerprint, e.Author)
		return nil
	},
}

var pluginUntrustCmd = &cobra.Command{
	Use:   "untrust <fingerprint>",
	Short: "Remove a pinned plugin author",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.Untrust(args[0]); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "untrusted", args[0])
		return nil
	},
}

var pluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pinned plugin authors",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		store, err := ts.Load()
		if err != nil {
			return err
		}
		if len(store) == 0 {
			fmt.Fprintln(os.Stderr, "(no plugin signers pinned)")
			return nil
		}
		for _, e := range store {
			lv := e.LastVersion
			if lv == "" {
				lv = "-"
			}
			fmt.Printf("%s  author=%s  last_version=%s\n", e.Fingerprint, e.Author, lv)
		}
		return nil
	},
}

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
		} else if err := plugins.SaveLocal(fresh, crlPath); err != nil {
			fmt.Fprintf(os.Stderr, "crl: cache write failed (%v); continuing with in-memory copy\n", err)
		}
	}

	crl, err := plugins.LoadLocal(crlPath)
	if err != nil {
		return fmt.Errorf("crl: load cached: %w", err)
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

var pluginDigestCmd = &cobra.Command{
	Use:   "digest <file>",
	Short: "Print the sha256 of a wasm blob (useful for manifest authoring)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		fmt.Println(hex.EncodeToString(h.Sum(nil)))
		return nil
	},
}

var pluginInstallSigner string

var pluginInstallCmd = &cobra.Command{
	Use:   "install <plugin-dir>",
	Short: "Verify and install a plugin into stado's plugin directory",
	Long: "Runs the same verification as `stado plugin verify` and, on success,\n" +
		"copies the plugin directory into $XDG_DATA_HOME/stado/plugins/\n" +
		"<name>-<version>/. Idempotent — re-installing the same version is a\n" +
		"no-op advisory; a newer version installs alongside so rollback is a\n" +
		"directory swap.\n\n" +
		"When the plugin's author key isn't pinned, install fails with a hint\n" +
		"pointing at `stado plugin trust <pubkey>`. Pass --signer <pubkey> to\n" +
		"TOFU-pin inline (manifest carries only the fingerprint; stado needs\n" +
		"the full Ed25519 public key to pin). Only use --signer when you've\n" +
		"verified the signer out of band — the install's trust gate can't\n" +
		"detect a supply-chain swap on its own.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		src := args[0]
		m, sig, err := plugins.LoadFromDir(src)
		if err != nil {
			return err
		}
		wasmPath := filepath.Join(src, "plugin.wasm")
		if err := plugins.VerifyWASMDigest(m.WASMSHA256, wasmPath); err != nil {
			return fmt.Errorf("install: %w", err)
		}

		// Optional TOFU path: pin the caller-provided pubkey before the
		// trust-store check. If the pubkey's fingerprint doesn't match
		// the manifest's author_pubkey_fpr, VerifyManifest will fail
		// in the next step — the pin alone doesn't authorise a mismatch.
		ts := plugins.NewTrustStore(cfg.StateDir())
		if pluginInstallSigner != "" {
			entry, err := ts.Trust(pluginInstallSigner, m.Author)
			if err != nil {
				return fmt.Errorf("install: --signer: %w", err)
			}
			if entry.Fingerprint != m.AuthorPubkeyFpr {
				return fmt.Errorf("install: --signer fingerprint %s does not match manifest author_pubkey_fpr %s",
					entry.Fingerprint, m.AuthorPubkeyFpr)
			}
			fmt.Fprintf(os.Stderr, "install: pinned signer %s (author=%s)\n",
				entry.Fingerprint, m.Author)
		}
		if err := ts.VerifyManifest(m, sig); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("install: %w", err)
			}
		}

		dst := filepath.Join(cfg.StateDir(), "plugins", m.Name+"-"+m.Version)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(os.Stderr, "install: %s v%s already installed at %s\n",
				m.Name, m.Version, dst)
			return nil
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("install: copy: %w", err)
		}
		fmt.Printf("installed %s v%s at %s\n", m.Name, m.Version, dst)
		return nil
	},
}

// copyDir copies files + regular dirs from src to dst. Symlinks and
// specials are rejected — plugin packages should be plain files.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("symlink not allowed: %s", from)
		case info.IsDir():
			if err := copyDir(from, to); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyPluginFile(from, to, info.Mode()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file mode for %s: %v", from, info.Mode())
		}
	}
	return nil
}

func copyPluginFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func init() {
	pluginInstallCmd.Flags().StringVar(&pluginInstallSigner, "signer", "",
		"Pin the plugin's author Ed25519 pubkey (hex or base64) inline before verification. Only use when you've verified the signer out of band.")
	pluginCmd.AddCommand(pluginTrustCmd, pluginUntrustCmd, pluginListCmd, pluginVerifyCmd, pluginDigestCmd, pluginInstallCmd)
	rootCmd.AddCommand(pluginCmd)
}
