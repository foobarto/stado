package main

import (
	"crypto/sha256"
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
		fmt.Printf("OK  %s v%s  author=%s  sha256=%s  caps=%d\n",
			m.Name, m.Version, m.Author, m.WASMSHA256[:12], len(m.Capabilities))
		return nil
	},
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

func init() {
	pluginCmd.AddCommand(pluginTrustCmd, pluginUntrustCmd, pluginListCmd, pluginVerifyCmd, pluginDigestCmd)
	rootCmd.AddCommand(pluginCmd)
}
