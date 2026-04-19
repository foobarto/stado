package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

var configInitForce bool

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage stado's config.toml",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a commented template config.toml to the default location",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		path := cfg.ConfigPath
		if _, err := os.Stat(path); err == nil && !configInitForce {
			return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
		return nil
	},
}

func init() {
	configInitCmd.Flags().BoolVar(&configInitForce, "force", false, "Overwrite an existing config.toml")
	configCmd.AddCommand(configInitCmd)
	rootCmd.AddCommand(configCmd)
}

// defaultConfigTemplate is a commented TOML template that exercises every
// section users are likely to touch. Shipping it via `stado config init`
// lowers the first-run barrier — users edit instead of consulting docs.
const defaultConfigTemplate = `# stado — config.toml
# Lives at ~/.config/stado/config.toml (or $XDG_CONFIG_HOME/stado/config.toml).
# Environment variables prefixed with STADO_ override any value below.
#   STADO_DEFAULTS_PROVIDER=openai STADO_DEFAULTS_MODEL=gpt-4o stado

[defaults]
# The provider stado uses when you launch it. Bundled names:
#   anthropic  — direct anthropic-sdk-go (needs ANTHROPIC_API_KEY)
#   openai     — direct openai-go        (needs OPENAI_API_KEY)
#   google     — direct generative-ai-go (needs GEMINI_API_KEY)
#   ollama / llamacpp / vllm / lmstudio  — local OAI-compat runners
#   groq / openrouter / deepseek / xai / mistral / cerebras / litellm
#                                        — hosted OAI-compat services
provider = "anthropic"
model    = "claude-sonnet-4-5"

[approvals]
# What stado does with tool calls: "prompt" asks every time, "allowlist"
# auto-allows anything in the allowlist below, everything else prompts.
mode      = "prompt"
allowlist = ["read", "glob", "grep", "ripgrep", "ast_grep"]

# ---------------------------------------------------------------------------
# [inference.presets] — custom OAI-compat endpoints (use via defaults.provider).
# ---------------------------------------------------------------------------
# [inference.presets.my-proxy]
# endpoint = "https://my-proxy.example.com/v1"

# ---------------------------------------------------------------------------
# [mcp.servers] — MCP tool servers to auto-attach.
# Keys are server names; tools appear in the registry as "<name>_<tool>".
# ---------------------------------------------------------------------------
# [mcp.servers.github]
# command = "mcp-github"
# args    = ["--readonly"]
# env     = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }
#
# [mcp.servers.weather]
# url = "https://weather.example.com/mcp"

# ---------------------------------------------------------------------------
# [otel] — OpenTelemetry export. Off by default.
# ---------------------------------------------------------------------------
# [otel]
# enabled     = true
# endpoint    = "localhost:4317"
# protocol    = "grpc"          # or "http"
# insecure    = true            # plaintext gRPC to a local collector
# sample_rate = 1.0             # 0.0..1.0

# ---------------------------------------------------------------------------
# [context] — context-window thresholds. Both fractions of the active model's
#             max_context_tokens. See DESIGN §"Token accounting".
# ---------------------------------------------------------------------------
# [context]
# soft_threshold = 0.70   # TUI shows warning indicator at this fraction
# hard_threshold = 0.90   # future turns blocked pending fork / compact / abort

# ---------------------------------------------------------------------------
# [sandbox] / [git] / [acp] / [plugins] — reserved.
# Scaffolded here so users see them; fields land with the matching phases.
# ---------------------------------------------------------------------------
`
