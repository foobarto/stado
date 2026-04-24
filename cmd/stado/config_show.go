package main

// `stado config show` — print the resolved effective config after
// koanf has merged config.toml + STADO_* env vars + defaults. Useful
// for answering "why is stado using X?" without having to read the
// loader code.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

var configShowJSON bool

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the resolved effective config (file + env + defaults merged)",
	Long: "Loads the config the same way the TUI, headless server, and every\n" +
		"CLI subcommand do, then prints it. Answers:\n" +
		"  · which provider will stado use\n" +
		"  · which model\n" +
		"  · what thresholds are active\n" +
		"  · which config file was loaded from (useful when STADO_* env vars\n" +
		"    are overriding the disk version)\n\n" +
		"Default output is a readable text form; --json pipes into jq.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if configShowJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		}
		return renderConfigHuman(os.Stdout, cfg)
	},
}

// renderConfigHuman prints a key=value listing with section
// headers. Indentation is fixed (not the zero-values lost-art of
// TOML round-tripping) because this surface is for reading, not
// editing. The actual config.toml is the editable source.
func renderConfigHuman(w interface {
	Write(p []byte) (int, error)
}, cfg *config.Config) error {
	write := func(format string, args ...interface{}) {
		fmt.Fprintf(w, format, args...)
	}

	write("config file    %s\n", cfg.ConfigPath)
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		write("                (file does not exist yet — all values come from defaults + env)\n")
	}
	write("state dir      %s\n", cfg.StateDir())
	write("worktree dir   %s\n\n", cfg.WorktreeDir())

	write("[defaults]\n")
	write("  provider     %s\n", fallback(cfg.Defaults.Provider, "(unset — probes local runners at boot)"))
	write("  model        %s\n\n", fallback(cfg.Defaults.Model, "(unset)"))

	write("[approvals]\n")
	write("  mode         %s\n", cfg.Approvals.Mode)
	if len(cfg.Approvals.Allowlist) > 0 {
		write("  allowlist    %s\n", strings.Join(cfg.Approvals.Allowlist, ", "))
	}
	write("\n")

	write("[agent]\n")
	write("  thinking                 %s\n", cfg.Agent.Thinking)
	write("  thinking_budget_tokens   %d\n", cfg.Agent.ThinkingBudgetTokens)
	write("  system_prompt_path       %s\n\n", cfg.Agent.SystemPromptPath)

	write("[memory]\n")
	write("  enabled        %v\n", cfg.Memory.Enabled)
	write("  max_items      %d\n", cfg.Memory.EffectiveMaxItems())
	write("  budget_tokens  %d\n\n", cfg.Memory.EffectiveBudgetTokens())

	write("[context]\n")
	write("  soft_threshold   %.2f\n", cfg.Context.SoftThreshold)
	write("  hard_threshold   %.2f\n\n", cfg.Context.HardThreshold)

	// Always render [budget] so users can see "(unset)" and remember
	// the knob exists. Zero values mean no cap; label them that way
	// so the listing doubles as documentation.
	write("[budget]\n")
	if cfg.Budget.WarnUSD > 0 {
		write("  warn_usd   $%.2f\n", cfg.Budget.WarnUSD)
	} else {
		write("  warn_usd   (unset — no warn pill)\n")
	}
	if cfg.Budget.HardUSD > 0 {
		write("  hard_usd   $%.2f\n\n", cfg.Budget.HardUSD)
	} else {
		write("  hard_usd   (unset — no hard gate)\n\n")
	}

	// [tools] — allowlist/denylist of the bundled tool set. Empty
	// lists = "all defaults available"; listing here lets users
	// confirm their config.toml actually took effect.
	if len(cfg.Tools.Enabled) > 0 || len(cfg.Tools.Disabled) > 0 {
		write("[tools]\n")
		if len(cfg.Tools.Enabled) > 0 {
			write("  enabled    %s\n", strings.Join(cfg.Tools.Enabled, ", "))
		}
		if len(cfg.Tools.Disabled) > 0 {
			write("  disabled   %s\n", strings.Join(cfg.Tools.Disabled, ", "))
		}
		if len(cfg.Tools.Overrides) > 0 {
			keys := make([]string, 0, len(cfg.Tools.Overrides))
			for k := range cfg.Tools.Overrides {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				write("  override   %s -> %s\n", k, cfg.Tools.Overrides[k])
			}
		}
		write("\n")
	}

	if cfg.OTel.Enabled || cfg.OTel.Endpoint != "" {
		write("[otel]\n")
		write("  enabled    %v\n", cfg.OTel.Enabled)
		if cfg.OTel.Endpoint != "" {
			write("  endpoint   %s\n", cfg.OTel.Endpoint)
		}
		if cfg.OTel.Protocol != "" {
			write("  protocol   %s\n", cfg.OTel.Protocol)
		}
		write("\n")
	}

	if len(cfg.Inference.Presets) > 0 {
		write("[inference.presets]\n")
		names := make([]string, 0, len(cfg.Inference.Presets))
		for n := range cfg.Inference.Presets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			write("  %s  →  %s\n", n, cfg.Inference.Presets[n].Endpoint)
		}
		write("\n")
	}

	if len(cfg.Plugins.Background) > 0 {
		write("[plugins]\n")
		write("  background   %s\n", strings.Join(cfg.Plugins.Background, ", "))
		if cfg.Plugins.CRLURL != "" {
			write("  crl_url      %s\n", cfg.Plugins.CRLURL)
		}
		if cfg.Plugins.RekorURL != "" {
			write("  rekor_url    %s\n", cfg.Plugins.RekorURL)
		}
		write("\n")
	}

	if len(cfg.MCP.Servers) > 0 {
		write("[mcp.servers]\n")
		names := make([]string, 0, len(cfg.MCP.Servers))
		for n := range cfg.MCP.Servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			srv := cfg.MCP.Servers[n]
			target := srv.Command
			if target == "" {
				target = srv.URL
			}
			write("  %s  →  %s\n", n, target)
			if len(srv.Capabilities) > 0 {
				write("      caps: %s\n", strings.Join(srv.Capabilities, ", "))
			}
		}
	}

	return nil
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func init() {
	configShowCmd.Flags().BoolVar(&configShowJSON, "json", false, "Emit JSON instead of the human-readable listing")
	configCmd.AddCommand(configShowCmd)
}
