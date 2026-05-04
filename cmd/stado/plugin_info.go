package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// pluginInfoCmd dumps an installed plugin's manifest as pretty
// JSON. Sibling to `plugin doctor` — doctor analyses (what surfaces
// + flags), info dumps (raw manifest fields). Useful for tooling
// that wants to script over plugin metadata without re-deriving the
// manifest format.
//
// The output goes to stdout so the operator can `stado plugin info
// <id> | jq '.tools[].name'` etc. Errors go to stderr.
var pluginInfoCmd = &cobra.Command{
	Use:   "info <plugin-id>",
	Short: "Dump an installed plugin's manifest as pretty JSON",
	Long: "Reads `<state-dir>/plugins/<plugin-id>/plugin.manifest.json`\n" +
		"and pretty-prints the parsed manifest to stdout. Pairs with\n" +
		"`stado plugin doctor <id>` (which analyses) — info dumps the\n" +
		"raw fields for tooling that wants to grep over them.",
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
		dir, err := plugins.InstalledDir(pluginsDir, args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("plugin %s not installed (run `stado plugin install <plugin-dir>` after building + signing it)", args[0])
		}
		mf, _, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
		out, err := json.MarshalIndent(mf, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal manifest: %w", err)
		}
		fmt.Println(string(out))
		return nil
	},
}
