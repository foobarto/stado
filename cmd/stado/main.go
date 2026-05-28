package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/dotenv"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/plugins/userbundled"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tui"
)

var version = "0.0.0-dev"

// formatVersion builds the version string shown by `stado version` and
// `stado --version`. When a user-bundled payload is loaded, it appends a
// "(custom: N plugins, bundler=XXXXXXXX)" marker; when signature
// verification was skipped it appends "[unsafe-skip-verify]".
func formatVersion() string {
	base := collectBuildInfo().Version
	if userbundled.Bundler != nil {
		fpr := plugins.Fingerprint(userbundled.Bundler)
		var n int
		for _, info := range bundled.List() {
			if info.WasmSource != nil {
				n++
			}
		}
		if n > 0 {
			base += fmt.Sprintf(" (custom: %d plugins, bundler=%s)", n, fpr[:8])
		}
	}
	if userbundled.SkipVerifyApplied {
		base += " [unsafe-skip-verify]"
	}
	return base
}

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
		// EP-0042 follow-up: the bare-TUI path skips MaybeRewrap, so
		// surface the unsandboxed-host gap (mode=off OR mode=wrap
		// configured-but-not-rewrapped). Capture the banner instead of
		// printing it — the alt-screen TUI clears pre-launch stderr — and
		// hand it to tui.Run to render in-band as a system block.
		startupNotices := sandbox.HostUnsandboxedLines(sandbox.WrapConfig{Mode: cfg.Sandbox.Mode})
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context) error {
			// v1 broker attach (phase 1: opt-in via STADO_BROKER_ATTACH=1).
			cwd, _ := os.Getwd()
			brokerSession, brokerErr := attachToBroker(ctx, brokerPurposeFromFlags(), brokerProfileFromFlags(), cwd)
			if brokerErr != nil {
				return fmt.Errorf("stado: %w", brokerErr)
			}
			// Capture the sandbox-mode banner (sandbox=… session=…,
			// writable: …, masked-paths) into the same notices instead of
			// the about-to-be-cleared stderr.
			var banner strings.Builder
			brokerSession.AnnounceSandboxMode(&banner, "stado")
			startupNotices = append(startupNotices, splitBannerLines(banner.String())...)
			defer func() {
				if closeErr := brokerSession.Close(); closeErr != nil {
					fmt.Fprintf(os.Stderr, "stado: broker session.terminate: %v\n", closeErr)
				}
			}()

			return tui.Run(cfg, startupNotices)
		})
	},
}

// splitBannerLines splits a captured multi-line banner into individual
// lines, dropping the trailing newline and an all-empty result. Used to
// fold AnnounceSandboxMode's buffered output into the TUI startup notices
// without introducing a blank trailing line.
func splitBannerLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
		fmt.Println(formatVersion())
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

var unsafeSkipBundleVerify bool

func init() {
	rootCmd.PersistentFlags().StringVar(&rootProvider, "provider", "",
		"Provider override (anthropic, openai, google, ollama-cloud, litellm, or any configured preset). Beats defaults.provider in config.toml for this invocation.")
	rootCmd.PersistentFlags().StringVar(&rootModel, "model", "",
		"Model override for this invocation (e.g. claude-sonnet-4-6, gpt-5, kimi-k2.6). Beats defaults.model in config.toml.")
	rootCmd.PersistentFlags().BoolVar(&unsafeSkipBundleVerify, "unsafe-skip-bundle-verify", false,
		"Skip runtime verification of the appended user-bundled payload (loses tamper-evidence)")
	rootCmd.AddCommand(versionCmd, configPathCmd, secretsCmd)
	// Set Version so cobra wires up the standard `--version` global
	// flag (alongside the `stado version` subcommand). Same source
	// of truth: collectBuildInfo() reads debug.ReadBuildInfo() and
	// falls back to the package-level `version` variable when the
	// binary wasn't ldflags-stamped.
	rootCmd.Version = formatVersion()
}

func main() {
	// Auto-load .env files from cwd → filesystem root, closer files
	// winning. Shell-set env always beats .env. Done before
	// rootCmd.Execute so config.Load() and provider construction
	// see the populated env. Disable via STADO_DOTENV_DISABLE=1 if
	// the auto-load surprises a setup.
	if os.Getenv("STADO_DOTENV_DISABLE") == "" {
		_ = dotenv.LoadHierarchy("")
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
