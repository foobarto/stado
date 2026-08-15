package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

var (
	pluginGCKeep  int
	pluginGCApply bool
)

// pluginGCCmd is the orphaned-version sweeper. After enough plugin
// authoring iteration, `stado plugin installed` shows
// multiple source-keyed revisions side-by-side. Per (scope, canonical source
// namespace) group, keep the N newest versions and remove the rest. A manifest
// display name must never cause packages from distinct sources to be swept as
// one application.
//
// Default is dry-run, matching `session gc` precedent. --apply
// actually deletes. Sort by SemVer (plugins.VersionLess), with
// non-SemVer manifest versions sorted last (we don't drop them
// silently; we just can't compare them).
//
// Trust-store and rollback pins are NOT touched. The pin records
// the highest version a signer ever shipped, which is how rollback
// protection works; deleting an older install doesn't unwind the
// pin and shouldn't.
var pluginGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove older installed plugin versions, keeping the N newest per canonical source (dry-run by default)",
	Long: "Scans project-local and global plugin roots and groups by (scope, canonical\n" +
		"source namespace). Within each group, sorts by signed package SemVer\n" +
		"and keeps the --keep newest versions; the rest are listed (or\n" +
		"deleted if --apply is set). Default --keep is 1. Invalid store rows\n" +
		"fail discovery closed rather than being skipped.\n\n" +
		"Trust-store entries and rollback pins are not modified; the\n" +
		"highest-version-seen invariant survives version cleanup so a\n" +
		"freshly-deleted older version still cannot be reinstalled.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if pluginGCKeep < 1 {
			return fmt.Errorf("--keep must be >= 1, got %d", pluginGCKeep)
		}
		locations, err := listInstalledPluginLocations(cfg)
		if err != nil {
			return err
		}
		if len(locations) == 0 {
			fmt.Fprintln(os.Stderr, "(no plugins installed)")
			return nil
		}

		type entry struct {
			id       string
			version  string
			dir      string
			root     string
			storeKey string
			pinned   bool
		}
		groups := make(map[string][]entry) // key = "<scope>/<canonical source namespace>"
		var skipped int
		for _, location := range locations {
			pkg := location.Package
			mf := &pkg.Manifest
			key := location.Scope + "/" + pkg.Identity.Namespace
			marker, _, markerErr := plugins.ReadActivePackageStoreKey(filepath.Dir(location.Dir), pkg.Identity.Namespace)
			if markerErr != nil {
				return fmt.Errorf("gc: active marker for %s: %w", pkg.Identity.Namespace, markerErr)
			}
			groups[key] = append(groups[key], entry{
				id:       location.ID,
				version:  mf.Version,
				dir:      location.Dir,
				root:     filepath.Dir(location.Dir),
				storeKey: pkg.Record.StoreKey,
				pinned:   marker == pkg.Record.StoreKey,
			})
		}

		var keys []string
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var toDelete []entry
		for _, k := range keys {
			es := groups[k]
			// Newest first, with non-SemVer pushed to the end (preserved,
			// not deleted, when in doubt).
			sort.SliceStable(es, func(i, j int) bool {
				iLess, iErr := plugins.VersionLess(es[i].version, es[j].version)
				jLess, jErr := plugins.VersionLess(es[j].version, es[i].version)
				if iErr == nil && jErr == nil {
					switch {
					case iLess:
						return false
					case jLess:
						return true
					default:
						return es[i].storeKey < es[j].storeKey
					}
				}
				_, badI := plugins.VersionLess(es[i].version, "0.0.0")
				_, badJ := plugins.VersionLess(es[j].version, "0.0.0")
				switch {
				case badI != nil && badJ == nil:
					return false // i invalid → sorts last
				case badJ != nil && badI == nil:
					return true
				case es[i].version != es[j].version:
					return es[i].version > es[j].version
				default:
					return es[i].storeKey < es[j].storeKey
				}
			})
			var drop []entry
			for i, e := range es {
				if i >= pluginGCKeep && !e.pinned {
					drop = append(drop, e)
				}
			}
			if len(drop) == 0 {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s — keep %d, drop %d:\n", k, len(es)-len(drop), len(drop))
			for i, e := range es {
				if i < pluginGCKeep || e.pinned {
					suffix := ""
					if e.pinned {
						suffix = " (active)"
					}
					fmt.Fprintf(os.Stderr, "  KEEP   %s%s\n", e.id, suffix)
					continue
				}
				fmt.Fprintf(os.Stderr, "  DROP   %s\n", e.id)
				toDelete = append(toDelete, e)
			}
		}

		if len(toDelete) == 0 {
			fmt.Fprintf(os.Stderr, "no candidates (--keep=%d, %d skipped, %d group(s))\n",
				pluginGCKeep, skipped, len(groups))
			return nil
		}
		if !pluginGCApply {
			fmt.Fprintf(os.Stderr, "(dry run — rerun with --apply to delete %d plugin(s))\n", len(toDelete))
			return nil
		}

		receiptRemovals := make(map[string]map[string]struct{})
		for _, entry := range toDelete {
			canonicalRoot, resolveErr := filepath.EvalSymlinks(entry.root)
			if resolveErr != nil {
				return fmt.Errorf("gc: resolve plugin root %s: %w", entry.root, resolveErr)
			}
			if receiptRemovals[canonicalRoot] == nil {
				receiptRemovals[canonicalRoot] = make(map[string]struct{})
			}
			receiptRemovals[canonicalRoot][entry.storeKey] = struct{}{}
		}
		// Admission receipts are live authority, so revoke all selected rows
		// before deleting bytes. Immutable plugin-lock rows remain provenance
		// and tag-continuity history after collection.
		if err := plugins.RemoveInstallReceipts(cfg.StateDir(), receiptRemovals); err != nil {
			return fmt.Errorf("gc: remove exact admission receipts: %w", err)
		}

		var errs int
		for _, entry := range toDelete {
			if err := workdirpath.NewUserConfigResolver().RemoveAll(entry.dir); err != nil {
				fmt.Fprintf(os.Stderr, "remove %s: %v\n", entry.id, err)
				errs++
				continue
			}
			fmt.Fprintln(os.Stderr, "deleted", entry.id)
		}
		if errs > 0 {
			return fmt.Errorf("%d deletion error(s)", errs)
		}
		return nil
	},
}

func init() {
	pluginGCCmd.Flags().IntVar(&pluginGCKeep, "keep", 1,
		"Number of newest versions to keep per (scope, canonical source) group")
	pluginGCCmd.Flags().BoolVar(&pluginGCApply, "apply", false,
		"Delete the listed older versions (default: dry-run)")
}
