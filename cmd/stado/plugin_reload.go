package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
)

// pluginReloadCmd is the whole-plugin analogue to `tool reload`.
//
// Architecturally, stado re-reads plugin.wasm from disk on every
// tool invocation (no per-process wasm-bytes cache), so on the CLI
// a "reload" is a no-op — the next `stado tool run <name>` will
// already see updated bytes.
//
// The meaningful surface is in the long-running TUI session, where
// the registry is built once at startup. The CLI command validates
// that the plugin exists, points at the TUI slash command for
// in-session rebuild, and exits 0.
var pluginReloadCmd = &cobra.Command{
	Use:   "reload <plugin-name>",
	Short: "Re-read a plugin's tools and capabilities (effective inside a TUI session via /plugin reload)",
	Long: "Confirms the plugin is installed and prints the names of\n" +
		"tools it contributes. Tool invocations always re-read the\n" +
		"plugin.wasm bytes from disk, so on the CLI no explicit\n" +
		"reload is needed — the next `stado tool run <tool>` picks\n" +
		"up changes automatically.\n\n" +
		"In a running TUI session use `/plugin reload <name>` to\n" +
		"rebuild the in-memory registry; otherwise newly added\n" +
		"plugins won't be visible until the session restarts.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		name := args[0]

		// Resolve the active install dir via the same helper plugin info uses.
		dir, ok := runtime.ResolveInstalledPluginDir(cfg, name)
		if !ok {
			// Fall back to the literal `<name>-<version>` form so callers
			// who happen to pass a fully-qualified id still get a sensible
			// answer (mirrors plugin info).
			pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
			d, derr := plugins.InstalledDir(pluginsDir, name)
			if derr != nil {
				return derr
			}
			if _, err := os.Stat(d); err != nil {
				return fmt.Errorf("plugin %q not installed — run `stado plugin list`", name)
			}
			dir = d
		}

		mf, _, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s v%s\n", mf.Name, mf.Version)
		if len(mf.Tools) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "  (no tools declared)")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  tools (%d):\n", len(mf.Tools))
			for _, t := range mf.Tools {
				fmt.Fprintf(cmd.OutOrStdout(), "    • %s\n", t.Name)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Tool calls re-read plugin.wasm from disk on every invocation; no")
		fmt.Fprintln(cmd.OutOrStdout(), "explicit reload is needed for the CLI. In a running TUI session,")
		fmt.Fprintf(cmd.OutOrStdout(), "use /plugin reload %s to rebuild the in-memory registry.\n", name)
		return nil
	},
}
