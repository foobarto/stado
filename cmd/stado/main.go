package main

import (
	"context"
	"errors"
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
	"github.com/foobarto/stado/internal/telemetry"
)

var version = "0.0.0-dev"

type exitCodeError struct {
	Code int
	Err  error
}

func (e *exitCodeError) Error() string { return e.Err.Error() }
func (e *exitCodeError) Unwrap() error { return e.Err }

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
		// device" message. Catch that early with an actionable pointer to
		// the one-shot and persistent non-interactive `run` modes.
		if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("stado: interactive TUI requires a TTY — try `stado run --prompt \"...\"` for one-shot, or `stado run --headless` for JSON-RPC")
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
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context, rt *telemetry.Runtime) error {
			// Broker attach + ceiling enforcement (credential-dir mask +
			// ssh-agent forwarding) is shared with `session resume` via
			// launchInlineTUI so both inline-TUI launches behave identically.
			// The sandbox banner is folded into startupNotices because the
			// alt-screen clears pre-launch stderr.
			return launchInlineTUI(ctx, cfg, startupNotices, rt.M())
		})
	},
}

// splitBannerLines splits a captured multi-line banner into individual
// lines, dropping the trailing newline and an all-empty result. Used to
// fold AnnounceSandboxMode's buffered output into the TUI startup notices
// without introducing a blank trailing line.
func splitBannerLines(s string) []string {
	// Normalize CRLF → LF first so a Windows-style trailing "\r\n" (or an
	// internal one) doesn't leave stray "\r" control chars on the lines.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		// nil (not []string{""}) so appending to the notices slice doesn't
		// introduce a blank line for a suppressed/empty banner.
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

// noSandbox is the v1 sandbox opt-out, persistent across every entry point
// (TUI, run, run --headless, acp, mcp-server, session tree) — all of which resolve
// their posture through brokerProfileFromFlags(). Previously --no-sandbox was a
// run-only flag, so `stado --no-sandbox` (TUI) failed with "unknown flag" and
// the non-run entry points had no opt-out at all.
var noSandbox bool

func init() {
	rootCmd.PersistentFlags().StringVar(&rootProvider, "provider", "",
		"Provider override (anthropic, openai, google, ollama-cloud, litellm, or any configured preset). Beats defaults.provider in config.toml for this invocation.")
	rootCmd.PersistentFlags().StringVar(&rootModel, "model", "",
		"Model override for this invocation (e.g. claude-sonnet-4-6, gpt-5, kimi-k2.6). Beats defaults.model in config.toml.")
	rootCmd.PersistentFlags().BoolVar(&unsafeSkipBundleVerify, "unsafe-skip-bundle-verify", false,
		"Skip runtime verification of the appended user-bundled payload (loses tamper-evidence)")
	rootCmd.PersistentFlags().BoolVar(&noSandbox, "no-sandbox", false,
		"Opt out of the v1 default sandbox: disable bwrap + Landlock. The agent operates on your actual filesystem with no namespace isolation. Intended for development scenarios and explicit operator override; should not become the typical mode of operation. Inverted polarity from the retired --sandbox-fs flag — pre-1.0 breaking change, no alias.")
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
	// cobra adds the `completion` group lazily inside Execute(); register it
	// now so enforceSubcommandRequired can guard it too (otherwise a typo'd
	// shell in `stado completion <shell> > file` silently writes help text
	// into the sourced file and exits 0).
	rootCmd.InitDefaultCompletionCmd()
	enforceSubcommandRequired(rootCmd)
	if err := rootCmd.Execute(); err != nil {
		var coded *exitCodeError
		if errors.As(err, &coded) && coded.Code > 0 {
			os.Exit(coded.Code)
		}
		os.Exit(1)
	}
}

// enforceSubcommandRequired makes every pure "group" command — one that only
// holds subcommands and has no action of its own — reject a missing or unknown
// subcommand with a nonzero exit, instead of cobra's default of printing help
// and exiting 0.
//
// That default is a footgun. The documented completion install redirects into
// a sourced file (`stado completion fish > ~/.config/fish/completions/stado.fish`);
// a typo'd shell name (`stado completion fsh`) otherwise writes help text into
// that file and reports success, so neither the user nor a wrapping script sees
// the failure. The same silent-success affected `session`, `config`, `plugin`,
// `tool`, `schedule`, and `harness`. Top-level `stado <bad>` already
// errors; this makes the subcommand groups consistent.
//
// Commands with their own Run/RunE (the root TUI, leaf commands) are left
// untouched — only pure groups get the guard, and a bare `stado <group>` with
// no args still prints help (exit 0).
func enforceSubcommandRequired(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		enforceSubcommandRequired(child)
	}
	if cmd.HasSubCommands() && cmd.Run == nil && cmd.RunE == nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			// Match cobra's own unknown-command wording (no trailing
			// punctuation — keeps staticcheck ST1005 happy and reads the same
			// as the top-level `stado <bad>` error).
			c.SilenceUsage = true
			return fmt.Errorf("unknown command %q for %q", args[0], c.CommandPath())
		}
	}
}
