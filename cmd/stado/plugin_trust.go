package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
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

// pluginUntrustAnchorCmd clears a per-owner anchor pin (distinct from the
// per-key signer store untrust above). EP-0039: after a legitimate anchor key
// rotation, the stored fingerprint no longer matches the published one and
// every reinstall refuses; clearing the pin lets the next install re-run
// trust-on-first-use against the new key.
var pluginUntrustAnchorCmd = &cobra.Command{
	Use:   "untrust-anchor <host/owner>",
	Short: "Clear a pinned owner anchor (e.g. after a key rotation)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := plugins.NewTrustStore(cfg.StateDir()).RemoveAnchor(args[0]); err != nil {
			return fmt.Errorf("untrust-anchor: %w", err)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "cleared anchor pin for", args[0])
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
		// Load trust store for author fingerprint → trusted status.
		ts := plugins.NewTrustStore(cfg.StateDir())
		trust, _ := ts.Load() // non-fatal if missing

		// Enumerate installed plugin directories.
		locations, err := listInstalledPluginLocations(cfg)
		if err != nil {
			return err
		}

		type row struct {
			name        string
			version     string
			tools       int
			toolNames   string // comma-joined, truncated
			author      string
			fingerprint string
			path        string // wasm path for installed plugins; "(embedded)" for bundled
			trusted     bool
			bundled     bool // indicates a binary-bundled plugin
			caps        int
			source      string
			storeKey    string
		}

		var rows []row
		for _, location := range locations {
			pkg := location.Package
			mf := &pkg.Manifest
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
				path:        filepath.Join(location.Dir, "plugin.wasm"),
				trusted:     trusted,
				caps:        len(mf.Capabilities),
				source:      pkg.Identity.Canonical,
				storeKey:    pkg.Record.StoreKey,
			})
		}

		// Also enumerate bundled plugins.
		for _, b := range bundled.List() {
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
				path:        "(embedded)",
				trusted:     true,
				bundled:     true,
				caps:        len(b.Capabilities),
				source:      "stado.dev/bundled/" + b.Name + "@" + b.Version,
				storeKey:    "-",
			})
		}

		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No plugins installed.")
			fmt.Fprintln(cmd.OutOrStdout(), "Install one with: stado plugin install <dir>")
			return nil
		}

		sort.Slice(rows, func(i, j int) bool { return rows[i].source < rows[j].source })

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
		fmt.Fprintln(w, "SOURCE\tALIAS\tVERSION\tTOOLS\tAUTHOR\tFINGERPRINT\tSTATUS\tSTORE KEY")
		fmt.Fprintln(w, "──────\t─────\t───────\t─────\t──────\t───────────\t──────\t─────────")
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
			// Strip any leading 'v' from the value: bundled plugins carry
			// version.Version (git-describe, already 'v0.64.0'), so the 'v%s'
			// format printed 'vv0.64.0' for them (P2.15). Disk plugins'
			// manifest versions usually have no 'v'. Normalise to exactly one.
			fmt.Fprintf(w, "%s\t%s\tv%s\t%d\t%s\t%s\t%s\t%s\n",
				r.source, r.name, strings.TrimPrefix(r.version, "v"), r.tools, r.author, fpr, status, r.storeKey)
		}
		_ = w.Flush()

		fmt.Fprintln(cmd.OutOrStdout())
		// Bare <name> resolves every plugin — bundled via LookupByName, disk
		// via ResolveInstalledPluginDir. The old `<name>-<version>` hint never
		// resolved a bundled plugin (the common case), and the vv version bug
		// made a copy-pasted id doubly wrong (P2.16).
		fmt.Fprintln(cmd.OutOrStdout(), "Tools per plugin: stado plugin info <canonical-source|store-key>")
		fmt.Fprintln(cmd.OutOrStdout(), "Trust a new key:  stado plugin trust <pubkey>")
		return nil
	},
}

// pluginInstalledCmd lists plugin IDs installed under the state dir.
// Separate from `plugin list` (which shows pinned authors) because
// dogfood #14 found users conflate the two. The output format matches
// the directory names that `plugin doctor` / `plugin info` <id> expect.
var pluginInstalledCmd = &cobra.Command{
	Use:   "installed",
	Short: "List installed plugins by canonical source and exact store key",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		_ = runtime.BuildDefaultRegistry(cfg) // unused — side-effect: triggers bundled-tool registrations
		locations, err := listInstalledPluginLocations(cfg)
		if err != nil {
			return err
		}
		if len(locations) == 0 {
			fmt.Fprintln(os.Stderr, "(no plugins installed)")
			return nil
		}
		for _, location := range locations {
			pkg := location.Package
			mf := &pkg.Manifest
			tools := len(mf.Tools)
			fmt.Printf("%s  store=%s  alias=%s  scope=%s  author=%s  tools=%d  caps=%d\n",
				pkg.Identity.Canonical, pkg.Record.StoreKey, mf.Name, location.Scope, mf.Author, tools, len(mf.Capabilities))
		}
		return nil
	},
}
