package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Security-research harness — default system prompt addition. EP-0030.
// Operators override this by creating .stado/harness/security.md in their
// project; stado loads that file in preference to this built-in template.
const securityHarnessBuiltin = `# Security-research mode

You are operating in security-research mode. The following discipline applies
to ALL work in this session, regardless of what the user asks.

## Recon first

Never move to exploitation before mapping the attack surface. Phase order:
1. Enumerate what exists (services, endpoints, parameters, versions, assets).
2. Understand what each component does and where inputs flow.
3. Hypothesise weaknesses. THEN test.

Skipping recon is the primary source of missed findings and wasted effort.

## Abusability filter — PoC or it didn't happen

"Vulnerable package present" / "config smell" / "theoretical bypass" are NOT
findings without an end-to-end attacker PoC demonstrating observable harm.
Maintain separate lists: **candidate** (unverified) vs **verified** (PoC done).
Never promote a candidate to a finding without running the PoC.

## Prerequisite vs impact check

Before reporting a finding, identify the access level required to trigger it.
If an attacker at that access already has the claimed capability via other means,
you are restating the prerequisite, not adding uplift. Compute the DELTA:
stealth, detachment, persistence-after-remediation, time-window extension,
cross-tenant impact, infrastructure reduction.

## Anti-confirmation bias

- First-look signals are HYPOTHESES, not findings. Walk back claims that do not
  survive scrutiny.
- When stuck on one approach for more than 30 minutes with no progress, switch
  layer (app → transport → infrastructure) or switch actor (developer → SRE → auditor).
- Never speculate aloud. "Probably works because..." without a test is noise.

## Data organisation

Maintain findings in notes/engagements/<target>/:
  recon/        — scan output, enumeration notes
  loot/         — captured secrets, credentials, session tokens
  writeup.md    — structured finding narrative
  scratch.md    — working notes, not for sharing

## Programming discipline

Helper scripts written during an engagement are throwaway tools, not production
code. No over-engineering. Minimum viable for the task at hand.`

// loadSecurityHarness returns the security harness system prompt addition.
// Checks for .stado/harness/security.md in the project first; falls back
// to the built-in template.
func loadSecurityHarness(workdir string) string {
	if workdir != "" {
		custom := filepath.Join(workdir, ".stado", "harness", "security.md")
		if data, err := os.ReadFile(custom); err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				return content
			}
		}
	}
	return securityHarnessBuiltin
}

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
