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

var pluginTrustPubkeyFile string

var pluginTrustCmd = &cobra.Command{
	Use:   "trust [pubkey] [author-name]",
	Short: "Pin a plugin author's Ed25519 public key (hex or base64)",
	Long: "trust pins an author pubkey by fingerprint. The pubkey can be provided\n" +
		"as the first positional argument (hex or base64), or via --pubkey-file.\n" +
		"Example: stado plugin trust --pubkey-file author.pubkey",
	Args: cobra.RangeArgs(0, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// EP-0039: --pubkey-file reads the key from a file (the build
		// convention writes author.pubkey alongside the seed).
		if pluginTrustPubkeyFile != "" {
			data, readErr := os.ReadFile(pluginTrustPubkeyFile)
			if readErr != nil {
				return fmt.Errorf("--pubkey-file: %w", readErr)
			}
			args = append([]string{strings.TrimSpace(string(data))}, args...)
		}
		if len(args) == 0 {
			return fmt.Errorf("usage: plugin trust <pubkey> or --pubkey-file <path>")
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
	Short: "List pinned plugin authors (trust-store entries). For installed plugins see `stado plugin installed`",
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

// pluginInstalledCmd lists plugin IDs installed under the state dir.
// Separate from `plugin list` (which shows pinned authors) because
// dogfood #14 found users conflate the two. The output format matches
// the directory names that `plugin run <id>` expects.
var pluginInstalledCmd = &cobra.Command{
	Use:   "installed",
	Short: "List installed plugins (matches directory names under state/plugins)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
		ids, err := plugins.ListInstalledDirs(pluginsDir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "(no plugins installed)")
				return nil
			}
			return fmt.Errorf("read plugins dir: %w", err)
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "(no plugins installed)")
			return nil
		}
		for _, id := range ids {
			mf, _, err := plugins.LoadFromDir(filepath.Join(pluginsDir, id))
			if err != nil {
				fmt.Printf("%s  (manifest load failed: %v)\n", id, err)
				continue
			}
			tools := len(mf.Tools)
			fmt.Printf("%s  author=%s  tools=%d  caps=%d\n",
				id, mf.Author, tools, len(mf.Capabilities))
		}
		return nil
	},
}
