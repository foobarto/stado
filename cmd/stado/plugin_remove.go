package main

// plugin_remove.go — `stado plugin remove <selector>`. Removal resolves one
// authenticated source namespace in one explicit scope, revokes its live host
// receipts, and deletes every retained package row in that scope. Immutable
// lock rows remain provenance/tag-continuity history and never act as an
// installed-package index.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

var pluginRemoveCmd = &cobra.Command{
	Use:   "remove [project:|global:]<canonical-source|store-key>",
	Short: "Uninstall every package version from one exact source namespace",
	Long: `remove resolves an exact canonical source/store key, or a display alias
only when that alias names one source namespace. It removes every retained
version of that source in the selected row's scope. Prefix a selector with
project: or global: when the same row exists in both scopes. Immutable lock rows are retained as
source-continuity history so a later reinstall cannot accept a moved tag.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		selected, selectedRoot, err := resolveManagedInstalledPackage(cfg, args[0])
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("plugin %q is not installed", args[0])
			}
			return err
		}
		namespace := selected.Identity.Namespace
		resolver := workdirpath.NewUserConfigResolver()
		var removed []string
		receiptRemovals := make(map[string]map[string]struct{})
		type removal struct {
			dir string
		}
		var planned []removal
		for _, base := range []string{selectedRoot.Dir} {
			canonicalRoot, resolveErr := filepath.EvalSymlinks(base)
			if resolveErr != nil && !os.IsNotExist(resolveErr) {
				return fmt.Errorf("remove: resolve plugin root %s: %w", base, resolveErr)
			}
			packages, listErr := plugins.ListInstalledPackages(base)
			if listErr != nil {
				return fmt.Errorf("remove: enumerate %s: %w", base, listErr)
			}
			for _, pkg := range packages {
				if pkg.Identity.Namespace != namespace {
					continue
				}
				planned = append(planned, removal{dir: pkg.Dir})
				if receiptRemovals[canonicalRoot] == nil {
					receiptRemovals[canonicalRoot] = make(map[string]struct{})
				}
				receiptRemovals[canonicalRoot][pkg.Record.StoreKey] = struct{}{}
			}
		}
		if len(planned) == 0 {
			return fmt.Errorf("plugin %q is not installed", args[0])
		}
		// Revoke live host admission before deleting package bytes. A failed
		// deletion then leaves inert evidence rather than an admitted package.
		if err := plugins.RemoveInstallReceipts(cfg.StateDir(), receiptRemovals); err != nil {
			return fmt.Errorf("remove exact admission receipts: %w", err)
		}
		for _, item := range planned {
			if rmErr := resolver.RemoveAll(item.dir); rmErr != nil {
				return fmt.Errorf("remove %s: %w", item.dir, rmErr)
			}
			removed = append(removed, item.dir)
		}
		for _, base := range []string{selectedRoot.Dir} {
			if err := plugins.RemoveActivePackageMarker(base, namespace); err != nil {
				return fmt.Errorf("remove active marker: %w", err)
			}
		}

		for _, d := range removed {
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", d)
		}
		return nil
	},
}
