package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

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
			fmt.Fprintf(cmd.ErrOrStderr(), "install: pinned signer %s (author=%s)\n",
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

		if !filepath.IsLocal(m.Name) || !filepath.IsLocal(m.Version) ||
			strings.ContainsAny(m.Name, "/\\") || strings.ContainsAny(m.Version, "/\\") {
			return fmt.Errorf("install: plugin manifest Name or Version contains path separators or traversal (name=%q version=%q)", m.Name, m.Version)
		}

		dst := filepath.Join(cfg.StateDir(), "plugins", m.Name+"-"+m.Version)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s v%s already installed at %s\n",
				m.Name, m.Version, dst)
			return nil
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("install: copy: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "installed %s v%s at %s\n", m.Name, m.Version, dst)
		return nil
	},
}

// copyDir copies files + regular dirs from src to dst. Symlinks and
// specials are rejected — plugin packages should be plain files.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
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
	in, err := os.Open(src) // #nosec G304 -- source is an already-validated plugin package file.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	return writeReaderToPath(dst, mode, in)
}
