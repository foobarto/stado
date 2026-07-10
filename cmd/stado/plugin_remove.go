package main

// plugin_remove.go — `stado plugin remove <name>`. EP-0039 §G: the install /
// update / use surface shipped without an uninstall, so a plugin could only be
// removed by hand-deleting its state dir (and the lock entry would then make
// `plugin update` resurrect it). This removes every installed version of a
// plugin and drops its lock entry.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

// safePluginName guards the removal below against traversal — install dirs are
// <name>-<version> and we match the <name>- prefix to find every version, so
// name must be a plain segment.
var safePluginName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// matchPluginVersionDirs returns the install-version directories for a plugin
// under base. It reads base literally (os.ReadDir) and filters by the
// "<name>-" prefix rather than filepath.Glob(base+"/<name>-*"): Glob would
// interpret glob metacharacters (`*`, `?`, `[`, `]`) anywhere in the full
// pattern — including the base path returned by AllPluginDirs() when the
// project lives under a directory whose name contains them — and could return
// directories outside base. ReadDir treats base as a literal path, so matches
// are always direct children of base (Codex #1).
func matchPluginVersionDirs(base, name string) []string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	prefix := name + "-"
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(base, e.Name()))
		}
	}
	return out
}

var pluginRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Uninstall a plugin (all installed versions) and drop its lock entry",
	Long: `remove deletes every installed version directory for <name> from the
project-local and global plugin dirs, and removes the plugin's plugin-lock.toml
entry so 'stado plugin update' won't reinstall it. <name> is the installed
plugin name (the install-dir prefix), e.g. 'gtfobins'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		name := args[0]
		if name == ".." || !safePluginName.MatchString(name) {
			return fmt.Errorf("invalid plugin name %q (expected a plain name like 'gtfobins')", name)
		}

		resolver := workdirpath.NewUserConfigResolver()
		var removed []string
		for _, base := range cfg.AllPluginDirs() {
			matches := matchPluginVersionDirs(base, name)
			for _, dir := range matches {
				info, lerr := os.Lstat(dir)
				if lerr != nil || !info.IsDir() {
					continue // only remove real directories, never a symlink/file
				}
				if rmErr := resolver.RemoveAll(dir); rmErr != nil {
					return fmt.Errorf("remove %s: %w", dir, rmErr)
				}
				removed = append(removed, dir)
			}
		}
		if len(removed) == 0 {
			return fmt.Errorf("plugin %q is not installed", name)
		}

		// Drop matching lock entries (best-effort) so `plugin update` doesn't
		// resurrect a removed plugin. Match on the identity's repo segment /
		// local alias — the common case where the install name equals the repo.
		for _, lockTarget := range pluginLockTargets(cfg) {
			lockPath := lockTarget.Path
			if lock, lerr := plugins.ReadLock(lockPath); lerr == nil {
				kept := plugins.NewLock()
				dropped := 0
				for _, e := range lock.Entries {
					if id, perr := plugins.ParseIdentity(e.Identity); perr == nil &&
						(id.Repo == name || id.LocalAlias() == name) {
						dropped++
						continue
					}
					kept.Add(e)
				}
				if dropped > 0 {
					if werr := kept.Write(lockPath); werr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not update %s: %v\n", lockPath, werr)
					}
				}
			}
		}

		for _, d := range removed {
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", d)
		}
		return nil
	},
}
