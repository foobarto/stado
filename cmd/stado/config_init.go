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
# The provider stado uses when you launch it. Leave both commented out
# and stado will probe bundled local runners (ollama / lmstudio /
# llamacpp / vllm) + any [inference.presets.*] you define below, using
# the first reachable one. Set explicit values here to pin a specific
# provider — no local probe is performed when the provider key is set.
#
# Bundled names:
#   anthropic  — direct anthropic-sdk-go (needs ANTHROPIC_API_KEY)
#   openai     — direct openai-go        (needs OPENAI_API_KEY)
#   google     — direct generative-ai-go (needs GEMINI_API_KEY)
#   ollama / llamacpp / vllm / lmstudio  — local OAI-compat runners
#   groq / openrouter / deepseek / xai / mistral / cerebras / litellm
#                                        — hosted OAI-compat services
# provider = "anthropic"
# model    = "claude-sonnet-4-6"

[approvals]
# What stado does with tool calls: "prompt" asks every time, "allowlist"
# auto-allows anything in the allowlist below, everything else prompts.
mode      = "prompt"
allowlist = ["read", "glob", "grep", "ripgrep", "ast_grep"]

# ---------------------------------------------------------------------------
# [tools] — trim the bundled tool set.
#
# All tools are available by default. Use 'enabled' as an explicit allowlist
# OR 'disabled' to remove specific names. If both are set, 'enabled' wins.
# Unknown names are ignored with a stderr warning — typo-tolerant so a
# config doesn't break stado across renames.
#
# Bundled tool names (list via 'stado headless' tools.list or session show):
#   bash                 shell exec
#   read, write, edit    file ops
#   glob, grep           filesystem patterns
#   ripgrep              fast content search
#   ast_grep             structural search via ast-grep
#   webfetch             fetch a URL → markdown
#   read_with_context    read + one hop of imports
#   find_definition, find_references, hover, document_symbols  (LSP-backed)
# ---------------------------------------------------------------------------
# [tools]
# enabled  = ["read", "grep", "bash"]   # allowlist — only these are active
# disabled = ["webfetch"]               # or: remove specific tools from the default set

# ---------------------------------------------------------------------------
# [inference.presets] — custom OAI-compat endpoints OR overrides for bundled
# preset names (lmstudio / ollama / …) when the server isn't on the default
# port. A user-defined preset with the same name as a bundled one wins.
# ---------------------------------------------------------------------------
# [inference.presets.my-proxy]
# endpoint = "https://my-proxy.example.com/v1"
#
# [inference.presets.lmstudio]    # override the bundled http://localhost:1234
# endpoint = "http://localhost:1235/v1"

# ---------------------------------------------------------------------------
# [mcp.servers] — MCP tool servers to auto-attach.
# Keys are server names; tools appear in the registry as "<name>_<tool>".
# ---------------------------------------------------------------------------
# [mcp.servers.github]
# command = "mcp-github"
# args    = ["--readonly"]
# env    = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }
# capabilities = [
#   "net:api.github.com",
#   "net:raw.githubusercontent.com",
#   "env:GITHUB_TOKEN",
# ]
# # Empty capabilities = unsandboxed (legacy default); stado warns on stderr.
# # Forms: fs:read:<path> | fs:write:<path> | net:<host>|allow|deny
# #        exec:<binary>  | env:<VAR>
#
# [mcp.servers.weather]
# url = "https://weather.example.com/mcp"   # HTTP servers don't participate in the sandbox

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
# [budget] — cumulative cost guardrail per session. Both values are in USD.
#            Zero (the default) disables the guard; set either to opt in.
# warn_usd paints a yellow status-bar pill + one-time system block.
# hard_usd blocks further turns pending '/budget ack' (or raising this cap).
# stado run exits 2 with ErrCostCapExceeded if hard_usd is crossed.
# hard_usd must be > warn_usd — a pair where hard ≤ warn is ignored (stderr warning).
# ---------------------------------------------------------------------------
# [budget]
# warn_usd = 1.00
# hard_usd = 5.00

# ---------------------------------------------------------------------------
# AGENTS.md / CLAUDE.md — project-level instructions. Drop either file in
# your repo root (or any parent directory) and stado auto-loads the body as
# the system prompt on every turn. AGENTS.md wins if both exist; CLAUDE.md
# is supported for backwards-compat with existing repos. Nearest-wins when
# a monorepo has files at multiple levels.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# [sandbox] / [git] / [acp] — reserved. Fields land with the matching phases.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# [plugins] — plugin trust + revocation + transparency log (Phase 7.6 / 7.7)
# ---------------------------------------------------------------------------
# [plugins]
# crl_url = "https://example.com/stado/plugin-crl.json"
# crl_issuer_pubkey = "hex-or-base64-of-ed25519-pubkey"
# rekor_url = "https://rekor.sigstore.dev"  # Rekor transparency log
`
