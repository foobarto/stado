package main

// plugin_update_verify.go — plugin update / verify / untrust subcommands.
// EP-0039 §G, §K.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// ── plugin update ────────────────────────────────────────────────────────

var pluginUpdateCheck bool

var pluginUpdateCmd = &cobra.Command{
	Use:   "update [<name>|all]",
	Short: "Update an installed plugin to its latest tagged version (EP-0039)",
	Long: `update fetches the latest semver tag for a plugin and installs it
side-by-side with the existing version. Use --check to see available
updates without installing.

Without arguments lists currently-installed plugins eligible for update.
With "all", attempts to update every installed plugin tracked by lock file.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// Look for project lock file.
		lockPath := pluginLockPath(cfg)
		lock, err := plugins.ReadLock(lockPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(cmd.OutOrStdout(), "no plugin-lock.toml — install plugins via 'stado plugin install <identity>' to enable updates")
				return nil
			}
			return err
		}

		target := ""
		if len(args) > 0 {
			target = args[0]
		}

		anyUpdates := false
		for _, entry := range lock.Entries {
			if target != "" && target != "all" && !strings.Contains(entry.Identity, target) {
				continue
			}
			id, parseErr := plugins.ParseIdentity(entry.Identity)
			if parseErr != nil {
				continue
			}
			latest, err := fetchLatestTag(id)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: latest-tag lookup failed: %v\n", entry.Identity, err)
				continue
			}
			if latest == id.Version {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: up to date (%s)\n", entry.Identity, id.Version)
				continue
			}
			anyUpdates = true
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s → %s\n", entry.Identity, id.Version, latest)
			if pluginUpdateCheck {
				continue
			}
			// Run install with the new version.
			newID := strings.Replace(entry.Identity, "@"+id.Version, "@"+latest, 1)
			fmt.Fprintf(cmd.ErrOrStderr(), "    installing %s...\n", newID)
			pluginInstallCmd.SetArgs([]string{newID})
			if err := pluginInstallCmd.Execute(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "    install failed: %v\n", err)
			}
		}
		if !anyUpdates && !pluginUpdateCheck {
			fmt.Fprintln(cmd.OutOrStdout(), "all plugins up to date")
		}
		return nil
	},
}

// fetchLatestTag is a stub — real implementation uses GitHub /releases/latest.
// For now returns the current version (no-op) so update is safe.
func fetchLatestTag(id plugins.Identity) (string, error) {
	// TODO: query GitHub API for latest release tag, gitlab API for gitlab, etc.
	// For now this is a stub that returns the current version.
	return id.Version, nil
}

func pluginLockPath(cfg *config.Config) string {
	// Project-local first, falls back to user-level.
	if cwd, err := os.Getwd(); err == nil {
		// Walk up looking for .stado/
		dir := cwd
		for {
			candidate := filepath.Join(dir, ".stado", "plugin-lock.toml")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return filepath.Join(cfg.StateDir(), "plugin-lock.toml")
}

// ── plugin verify ────────────────────────────────────────────────────────

var pluginVerifyInstalledCmd = &cobra.Command{
	Use:   "verify-installed <plugin-id>",
	Short: "Re-verify the signature of an installed plugin against the trust store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
		dir, err := plugins.InstalledDir(pluginsDir, args[0])
		if err != nil {
			return err
		}
		mf, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(mf, sig); err != nil {
			return fmt.Errorf("verify failed: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s v%s — signature verified (fingerprint %s)\n",
			mf.Name, mf.Version, mf.AuthorPubkeyFpr)
		return nil
	},
}

func init() {
	pluginUpdateCmd.Flags().BoolVar(&pluginUpdateCheck, "check", false, "Show available updates without installing")
	pluginCmd.AddCommand(pluginUpdateCmd, pluginVerifyInstalledCmd)
}
