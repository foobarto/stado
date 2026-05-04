package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui"
)

var version = "0.0.0-dev"

// rootProvider / rootModel mirror --provider / --model on the root
// command. Subcommands inherit them as persistent flags; values are
// applied to cfg.Defaults after load via applyRootProviderOverrides.
var (
	rootProvider string
	rootModel    string
)

var rootCmd = &cobra.Command{
	Use:   "stado",
	Short: "Sandboxed, git-native coding-agent runtime",
	// With no subcommand, launch the TUI. stado boots without any API key
	// thanks to lazy provider init — the first prompt surfaces a helpful
	// message if credentials are missing.
	SilenceUsage:  true, // don't dump the full usage on RunE error
	SilenceErrors: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The TUI needs both a usable stdin and stdout TTY; without
		// one, bubbletea bails with a low-level "/dev/tty: no such
		// device" message. Catch that early with an actionable pointer
		// to the scripting surfaces (`run`, `headless`).
		if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("stado: interactive TUI requires a TTY — try `stado run --prompt \"...\"` for one-shot, or `stado headless` for JSON-RPC")
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		applyRootProviderOverrides(cfg)
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			return tui.Run(cfg)
		})
	},
}

// applyRootProviderOverrides honours --provider / --model passed on the
// root command (or any subcommand inheriting the persistent flag). It
// runs after config.Load so the override is the final word.
func applyRootProviderOverrides(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if p := strings.TrimSpace(rootProvider); p != "" {
		cfg.Defaults.Provider = p
	}
	if m := strings.TrimSpace(rootModel); m != "" {
		cfg.Defaults.Model = m
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print stado version",
	Run: func(cmd *cobra.Command, args []string) {
		// Share collectBuildInfo with `stado verify` so `version` and
		// `verify` can't disagree — both resolve `0.0.0-dev` through
		// debug.ReadBuildInfo() when the binary wasn't ldflags-stamped.
		fmt.Println(collectBuildInfo().Version)
	},
}

var configPathCmd = &cobra.Command{
	Use:   "config-path",
	Short: "Print the path to the config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Println(cfg.ConfigPath)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootProvider, "provider", "",
		"Provider override (anthropic, openai, google, ollama-cloud, litellm, or any configured preset). Beats defaults.provider in config.toml for this invocation.")
	rootCmd.PersistentFlags().StringVar(&rootModel, "model", "",
		"Model override for this invocation (e.g. claude-sonnet-4-6, gpt-5, kimi-k2.6). Beats defaults.model in config.toml.")
	rootCmd.AddCommand(versionCmd, configPathCmd)
	// Set Version so cobra wires up the standard `--version` global
	// flag (alongside the `stado version` subcommand). Same source
	// of truth: collectBuildInfo() reads debug.ReadBuildInfo() and
	// falls back to the package-level `version` variable when the
	// binary wasn't ldflags-stamped.
	rootCmd.Version = collectBuildInfo().Version
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
