package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
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
	Short: "List installed plugins with name, version, tools, author and trust status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		_ = runtime.BuildDefaultRegistry(cfg) // unused — side-effect: triggers bundled-tool registrations
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")

		// Load trust store for author fingerprint → trusted status.
		ts := plugins.NewTrustStore(cfg.StateDir())
		trust, _ := ts.Load() // non-fatal if missing

		// Enumerate installed plugin directories.
		ids, err := plugins.ListInstalledDirs(pluginsDir)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read plugins dir: %w", err)
		}

		type row struct {
			name        string
			version     string
			tools       int
			toolNames   string // comma-joined, truncated
			author      string
			fingerprint string
			trusted     bool
			bundled     bool // indicates a binary-bundled plugin
			caps        int
		}

		var rows []row
		for _, id := range ids {
			mf, _, loadErr := plugins.LoadFromDir(filepath.Join(pluginsDir, id))
			if loadErr != nil {
				// Show even if manifest is broken.
				rows = append(rows, row{name: id, version: "?", author: "manifest load failed"})
				continue
			}
			var toolNames []string
			for _, t := range mf.Tools {
				toolNames = append(toolNames, t.Name)
			}
			tns := strings.Join(toolNames, ", ")
			if len(tns) > 40 {
				tns = tns[:37] + "..."
			}
			_, trusted := trust[mf.AuthorPubkeyFpr]
			rows = append(rows, row{
				name:        mf.Name,
				version:     mf.Version,
				tools:       len(mf.Tools),
				toolNames:   tns,
				author:      mf.Author,
				fingerprint: mf.AuthorPubkeyFpr,
				trusted:     trusted,
				caps:        len(mf.Capabilities),
			})
		}

		// Also enumerate bundled plugins.
		for _, b := range bundledplugins.List() {
			toolsList := strings.Join(b.Tools, ", ")
			if len(toolsList) > 40 {
				toolsList = toolsList[:37] + "..."
			}
			rows = append(rows, row{
				name:        b.Name,
				version:     b.Version,
				tools:       len(b.Tools),
				toolNames:   toolsList,
				author:      b.Author,
				fingerprint: "",
				trusted:     true,
				bundled:     true,
				caps:        len(b.Capabilities),
			})
		}

		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No plugins installed.")
			fmt.Fprintln(cmd.OutOrStdout(), "Install one with: stado plugin install <dir>")
			return nil
		}

		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

		bundledCount, installedCount, trustedCount := 0, 0, 0
		for _, r := range rows {
			switch {
			case r.bundled:
				bundledCount++
				trustedCount++
			case r.trusted:
				installedCount++
				trustedCount++
			default:
				installedCount++
			}
		}

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		if bundledCount > 0 && installedCount > 0 {
			fmt.Fprintf(w, "%d plugins (%d bundled, %d installed", len(rows), bundledCount, installedCount)
		} else if bundledCount > 0 {
			fmt.Fprintf(w, "%d plugins (%d bundled", len(rows), bundledCount)
		} else {
			fmt.Fprintf(w, "%d plugins (%d installed", len(rows), installedCount)
		}
		if trustedCount < len(rows) {
			fmt.Fprintf(w, "; %d trusted, %d untrusted)", trustedCount, len(rows)-trustedCount)
		} else {
			fmt.Fprintf(w, "; all trusted)")
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "NAME\tVERSION\tTOOLS\tAUTHOR\tFINGERPRINT\tSTATUS")
		fmt.Fprintln(w, "────\t───────\t─────\t──────\t───────────\t──────")
		for _, r := range rows {
			status := "✓ trusted"
			switch {
			case r.bundled:
				status = "✓ bundled"
			case !r.trusted:
				status = "⚠ untrusted"
			}
			fpr := r.fingerprint
			if r.bundled {
				fpr = "-"
			} else if len(fpr) > 16 {
				fpr = fpr[:16]
			}
			fmt.Fprintf(w, "%s\tv%s\t%d\t%s\t%s\t%s\n",
				r.name, r.version, r.tools, r.author, fpr, status)
		}
		_ = w.Flush()

		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Tools per plugin: stado plugin info <name>-<version>")
		fmt.Fprintln(cmd.OutOrStdout(), "Trust a new key:  stado plugin trust <pubkey>")
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
		_ = runtime.BuildDefaultRegistry(cfg) // unused — side-effect: triggers bundled-tool registrations
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
