package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

// ── plugin use ────────────────────────────────────────────────────────────

// pluginUseCmd switches the active version for a plugin per-project.
// Active-version state is stored in .stado/active-plugins/<name> per EP-0039 §F.
var pluginUseCmd = &cobra.Command{
	Use:   "use <name>@<version>",
	Short: "Switch the active version for an installed plugin (per-project)",
	Long: "Writes the active-version marker for the named plugin.\n" +
		"The version must already be installed (`stado plugin installed` to list).\n" +
		"Example: stado plugin use my-plugin@v2.0.0",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nameVer := args[0]
		parts := strings.SplitN(nameVer, "@", 2)
		if len(parts) != 2 {
			return fmt.Errorf("usage: plugin use <name>@<version>")
		}
		name, version := parts[0], parts[1]

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		installDir := filepath.Join(cfg.StateDir(), "plugins", name+"-"+version)
		if _, err := os.Stat(installDir); os.IsNotExist(err) {
			return fmt.Errorf("plugin %s@%s is not installed (use `stado plugin install` first)", name, version)
		}

		// Write active-version marker to project .stado/ if available,
		// otherwise user-level config.
		activeDir := filepath.Join(cfg.StateDir(), "plugins", "active")
		if err := os.MkdirAll(activeDir, 0o755); err != nil {
			return fmt.Errorf("plugin use: create active dir: %w", err)
		}
		markerPath := filepath.Join(activeDir, name)
		if err := os.WriteFile(markerPath, []byte(version), 0o644); err != nil {
			return fmt.Errorf("plugin use: write marker: %w", err)
		}
		fmt.Printf("active: %s → %s\n", name, version)
		return nil
	},
}

// ── plugin dev ───────────────────────────────────────────────────────────

// pluginDevCmd collapses gen-key → sign → trust → install for plugin authoring.
// EP-0039 §I quality pass.
var pluginDevCmd = &cobra.Command{
	Use:   "dev <dir>",
	Short: "Build, trust, and install a plugin from a local directory (dev workflow)",
	Long: `plugin dev <dir> collapses the plugin authoring workflow:

  1. Generate a dev seed in <dir>/.stado/dev.seed (if not present)
  2. Sign the manifest with the dev seed
  3. Trust the dev pubkey (TOFU, local scope)
  4. Install from the local directory (with --force to pick up wasm changes)

The dev seed is ephemeral: use 'plugin gen-key' + 'plugin sign' + 'plugin trust'
for production keys intended for distribution.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return fmt.Errorf("plugin dev: directory %q does not exist", dir)
		}

		devSeedDir := filepath.Join(dir, ".stado")
		if err := os.MkdirAll(devSeedDir, 0o700); err != nil {
			return fmt.Errorf("plugin dev: create .stado dir: %w", err)
		}
		seedPath := filepath.Join(devSeedDir, "dev.seed")

		// Step 1: generate dev seed if not present.
		if _, err := os.Stat(seedPath); os.IsNotExist(err) {
			fmt.Fprintf(cmd.OutOrStdout(), "Generating dev seed at %s...\n", seedPath)
			if err := pluginGenKeyCmd.RunE(pluginGenKeyCmd, []string{seedPath}); err != nil {
				return fmt.Errorf("plugin dev: gen-key: %w", err)
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Using existing dev seed at %s\n", seedPath)
		}

		// Step 2: sign manifest with dev seed.
		fmt.Fprintln(cmd.OutOrStdout(), "Signing manifest...")
		origKey := pluginSignKeyPath
		origWasm := pluginSignWasm
		pluginSignKeyPath = seedPath
		pluginSignWasm = ""
		signErr := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.template.json")})
		pluginSignKeyPath = origKey
		pluginSignWasm = origWasm
		if signErr != nil {
			return fmt.Errorf("plugin dev: sign: %w", signErr)
		}

		// Step 3: read pubkey and trust.
		pubkeyPath := filepath.Join(dir, "author.pubkey")
		pubkeyData, err := os.ReadFile(pubkeyPath)
		if err != nil {
			return fmt.Errorf("plugin dev: read pubkey %s: %w", pubkeyPath, err)
		}
		pubkey := strings.TrimSpace(string(pubkeyData))
		keyPreview := pubkey
		if len(keyPreview) > 8 {
			keyPreview = keyPreview[:8]
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Trusting dev key %s...\n", keyPreview+"...")

		origTrustFile := pluginTrustPubkeyFile
		pluginTrustPubkeyFile = ""
		trustErr := pluginTrustCmd.RunE(pluginTrustCmd, []string{pubkey})
		pluginTrustPubkeyFile = origTrustFile
		if trustErr != nil {
			return fmt.Errorf("plugin dev: trust: %w", trustErr)
		}

		// Step 4: install with --force (picks up rebuilt wasm).
		fmt.Fprintln(cmd.OutOrStdout(), "Installing...")
		origForce := pluginInstallForce
		pluginInstallForce = true
		installErr := pluginInstallCmd.RunE(pluginInstallCmd, []string{dir})
		pluginInstallForce = origForce
		if installErr != nil {
			return fmt.Errorf("plugin dev: install: %w", installErr)
		}
		return nil
	},
}

