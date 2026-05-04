package main

// `stado config providers` — print the bundled provider catalogue and
// help users wire one up. Reuses internal/config's KnownProviders()
// registry as the single source of truth.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

var (
	configProvidersSetupWrite  bool
	configProvidersSetupForce  bool
	configProvidersSetupAPIKey string
)

var configProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List bundled provider catalogue and current API-key status",
	Long: "Lists every provider stado supports out of the box, grouped by\n" +
		"how stado talks to them (native SDK / OAI-compat cloud / OAI-compat\n" +
		"local). Each row shows whether the conventional API-key env var is\n" +
		"set in the current shell.\n\n" +
		"To wire a new provider:\n" +
		"  stado config providers setup <name>           # print setup hints\n" +
		"  stado config providers setup <name> --write   # also write the\n" +
		"                                                  # [inference.presets.<name>]\n" +
		"                                                  # block to config.toml",
	RunE: func(cmd *cobra.Command, args []string) error {
		return renderProvidersList(cmd.OutOrStdout())
	},
}

var configProvidersListCmd = &cobra.Command{
	Use:   "list",
	Short: "Alias for `stado config providers`",
	RunE:  configProvidersCmd.RunE,
}

var configProvidersSetupCmd = &cobra.Command{
	Use:   "setup <name>",
	Short: "Print setup steps for a known provider; --write applies the config block",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(strings.TrimSpace(args[0]))
		p, ok := config.LookupKnownProvider(name)
		if !ok {
			return fmt.Errorf("unknown provider %q — run `stado config providers` for the catalogue", args[0])
		}
		out := cmd.OutOrStdout()
		return renderProviderSetup(out, p, configProvidersSetupWrite, configProvidersSetupForce, configProvidersSetupAPIKey)
	},
}

func renderProvidersList(w io.Writer) error {
	cats := []struct {
		title string
		kind  config.ProviderKind
		hint  string
	}{
		{"Native (first-party SDK)", config.ProviderKindNative, "set the env var below in your shell rc"},
		{"OAI-compatible — cloud", config.ProviderKindOAICompatCloud, "needs an [inference.presets.<name>] block + the env var"},
		{"OAI-compatible — local runner", config.ProviderKindOAICompatLocal, "no key needed; just confirm the endpoint is reachable"},
	}

	for _, cat := range cats {
		fmt.Fprintf(w, "%s — %s\n", cat.title, cat.hint)
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		for _, p := range config.KnownProviders() {
			if p.Kind != cat.kind {
				continue
			}
			status := providerKeyStatus(p)
			right := p.Endpoint
			if right == "" {
				right = "(native — no endpoint)"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", p.Name, status, right)
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Setup:")
	fmt.Fprintln(w, "  stado config providers setup <name>           # print steps only")
	fmt.Fprintln(w, "  stado config providers setup <name> --write   # also write the config block")
	return nil
}

func providerKeyStatus(p config.KnownProvider) string {
	if p.APIKeyEnv == "" {
		return "(no key needed)"
	}
	if v := os.Getenv(p.APIKeyEnv); v != "" {
		return fmt.Sprintf("✓ %s set", p.APIKeyEnv)
	}
	return fmt.Sprintf("✗ %s unset", p.APIKeyEnv)
}

func renderProviderSetup(w io.Writer, p config.KnownProvider, write, force bool, apiKeyInline string) error {
	fmt.Fprintf(w, "Provider: %s (%s)\n", p.Name, p.Kind)
	if p.HelpURL != "" {
		fmt.Fprintf(w, "Get an API key: %s\n", p.HelpURL)
	}
	fmt.Fprintln(w)

	switch p.Kind {
	case config.ProviderKindNative:
		fmt.Fprintf(w, "1. Set the API key in your shell rc:\n")
		fmt.Fprintf(w, "     export %s=...\n\n", p.APIKeyEnv)
		fmt.Fprintf(w, "2. Pin %s as your default in config.toml (optional):\n", p.Name)
		fmt.Fprintf(w, "     [defaults]\n     provider = %q\n\n", p.Name)
		fmt.Fprintf(w, "3. Use it for a one-off:\n")
		fmt.Fprintf(w, "     stado run --provider %s --model <id> \"...\"\n", p.Name)
		if write {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Note: --write is a no-op for native providers — there's no preset block to add.")
			fmt.Fprintln(w, "      The API key lives in your shell environment, not in config.toml.")
		}
	case config.ProviderKindOAICompatCloud:
		fmt.Fprintf(w, "1. Set the API key in your shell rc:\n")
		fmt.Fprintf(w, "     export %s=...\n\n", p.APIKeyEnv)
		fmt.Fprintf(w, "2. Add the preset block to config.toml:\n")
		fmt.Fprintf(w, "     [inference.presets.%s]\n", p.Name)
		fmt.Fprintf(w, "     endpoint    = %q\n", p.Endpoint)
		fmt.Fprintf(w, "     api_key_env = %q\n\n", p.APIKeyEnv)
		fmt.Fprintf(w, "3. Use it for a one-off:\n")
		fmt.Fprintf(w, "     stado run --provider %s --model <id> \"...\"\n", p.Name)
		if write {
			fmt.Fprintln(w)
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if !force {
				if existing, ok := cfg.Inference.Presets[p.Name]; ok && existing.Endpoint != "" {
					return fmt.Errorf("preset %s already exists in %s (endpoint=%s) — pass --force to overwrite",
						p.Name, cfg.ConfigPath, existing.Endpoint)
				}
			}
			if err := config.WriteInferencePreset(cfg.ConfigPath, p.Name, p.Endpoint, p.APIKeyEnv); err != nil {
				return fmt.Errorf("write preset: %w", err)
			}
			fmt.Fprintf(w, "✓ wrote [inference.presets.%s] to %s\n", p.Name, cfg.ConfigPath)
		}
	case config.ProviderKindOAICompatLocal:
		fmt.Fprintf(w, "1. Confirm the runner is reachable:\n")
		fmt.Fprintf(w, "     curl -s %s/models | head\n\n", p.Endpoint)
		fmt.Fprintf(w, "2. Add the preset block to config.toml (optional — the name\n")
		fmt.Fprintf(w, "   is bundled, but explicit > implicit):\n")
		fmt.Fprintf(w, "     [inference.presets.%s]\n", p.Name)
		fmt.Fprintf(w, "     endpoint = %q\n\n", p.Endpoint)
		fmt.Fprintf(w, "3. Use it for a one-off:\n")
		fmt.Fprintf(w, "     stado run --provider %s --model <id> \"...\"\n", p.Name)
		if write {
			fmt.Fprintln(w)
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if !force {
				if existing, ok := cfg.Inference.Presets[p.Name]; ok && existing.Endpoint != "" {
					return fmt.Errorf("preset %s already exists in %s (endpoint=%s) — pass --force to overwrite",
						p.Name, cfg.ConfigPath, existing.Endpoint)
				}
			}
			if err := config.WriteInferencePreset(cfg.ConfigPath, p.Name, p.Endpoint, ""); err != nil {
				return fmt.Errorf("write preset: %w", err)
			}
			fmt.Fprintf(w, "✓ wrote [inference.presets.%s] to %s\n", p.Name, cfg.ConfigPath)
		}
	}

	if apiKeyInline != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "API key (--api-key was passed) — paste this into your shell or rc file:")
		if p.APIKeyEnv != "" {
			fmt.Fprintf(w, "  export %s=%q\n", p.APIKeyEnv, apiKeyInline)
		} else {
			fmt.Fprintln(w, "  (this provider has no conventional env var; --api-key has no effect)")
		}
	}
	return nil
}

func init() {
	configProvidersSetupCmd.Flags().BoolVar(&configProvidersSetupWrite, "write", false,
		"Also write the [inference.presets.<name>] block to config.toml (no-op for native providers)")
	configProvidersSetupCmd.Flags().BoolVar(&configProvidersSetupForce, "force", false,
		"Overwrite an existing [inference.presets.<name>] block when --write is set")
	configProvidersSetupCmd.Flags().StringVar(&configProvidersSetupAPIKey, "api-key", "",
		"Print an export command with this API key value (for copying into your shell rc); the key is NOT written to config.toml")

	configProvidersCmd.AddCommand(configProvidersListCmd)
	configProvidersCmd.AddCommand(configProvidersSetupCmd)
	configCmd.AddCommand(configProvidersCmd)
}
