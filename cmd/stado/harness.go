package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// ── stado harness subcommand ───────────────────────────────────────────────

var harnessCmd = &cobra.Command{
	Use:   "harness",
	Short: "Manage harness mode and engagement folder layout",
}

var harnessInitCmd = &cobra.Command{
	Use:   "init [--mode <mode>]",
	Short: "Initialise harness folder layout for the current project",
	Long: `harness init creates the standard folder layout for the selected harness mode.

For --mode security (default):
  notes/engagements/          — per-target engagement directories
  .stado/harness/security.md  — customisable harness system prompt (optional)

Edit .stado/harness/security.md to override the built-in security harness prompt.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, _ := cmd.Flags().GetString("mode")
		if mode == "" {
			mode = "security"
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		switch mode {
		case "security":
			return initSecurityHarness(cmd, cwd)
		default:
			return fmt.Errorf("unknown harness mode %q; supported: security", mode)
		}
	},
}

func initSecurityHarness(cmd *cobra.Command, cwd string) error {
	dirs := []string{
		filepath.Join(cwd, "notes", "engagements"),
		filepath.Join(cwd, ".stado", "harness"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("harness init: create %s: %w", d, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created: %s\n", d)
	}
	// Write customisable harness prompt stub if it doesn't exist.
	promptPath := filepath.Join(cwd, ".stado", "harness", "security.md")
	if _, err := os.Stat(promptPath); os.IsNotExist(err) {
		stub := "# Security harness — project-level customisation\n\n" +
			"# This file overrides the built-in security harness system prompt.\n" +
			"# Delete this file to use the built-in template.\n\n" +
			"# Add project-specific rules, scope boundaries, tool overrides, etc. here.\n"
		if err := os.WriteFile(promptPath, []byte(stub), 0o644); err != nil {
			return fmt.Errorf("harness init: write %s: %w", promptPath, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created: %s (edit to customise)\n", promptPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nSecurity harness ready. Run with:\n  stado run --mode security --prompt \"start recon on <target>\"\n")
	return nil
}

func init() {
	harnessInitCmd.Flags().String("mode", "security", "Harness mode to initialise (security)")
	harnessCmd.AddCommand(harnessInitCmd)
	rootCmd.AddCommand(harnessCmd)
}
