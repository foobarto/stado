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
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o600); err != nil {
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

# [approvals] is kept for compatibility with older config files, but
# bundled native tool approvals are no longer enforced by the TUI.
# Narrow native tools with [tools], or use approval-wrapper plugins that
# declare ui:approval when a human gate is required.

# ---------------------------------------------------------------------------
# [agent] — prompt and reasoning behavior.
#
# system_prompt_path is an editable Go text/template that stado executes for
# every provider system prompt. The default file is created automatically at:
#   ~/.config/stado/system-prompt.md
#
# Available template fields:
#   {{ .Provider }}              active provider name when known
#   {{ .Model }}                 active model id when known
#   {{ .ProjectInstructions }}   nearest AGENTS.md / CLAUDE.md body, if any
# ---------------------------------------------------------------------------
# [agent]
# thinking = "auto"                 # auto | on | off
# thinking_budget_tokens = 16384
# system_prompt_path = "~/.config/stado/system-prompt.md"

# ---------------------------------------------------------------------------
# [tui] — display-only terminal UI preferences.
# thinking_display controls provider-native thinking blocks in the viewport;
# it does not change provider requests or persisted transcripts.
# ---------------------------------------------------------------------------
# [tui]
# theme = "stado-dark"                # bundled id; omit to use theme.toml/default
# thinking_display = "show"          # show | tail | hide

# ---------------------------------------------------------------------------
# [memory] — opt-in approved-memory prompt context.
#
# Plugins with memory:* capabilities can propose or update append-only
# memories. Candidate memories never affect model prompts until you approve
# them with: stado memory approve <id>. Set enabled=true to inject
# approved, scoped, non-secret memories into TUI, run, headless, and ACP
# prompts as labeled untrusted context.
# ---------------------------------------------------------------------------
# [memory]
# enabled = false
# max_items = 8
# budget_tokens = 800

# ---------------------------------------------------------------------------
# [tools] — trim the bundled tool set.
#
# All tools are available by default. Use 'enabled' as an explicit allowlist
# OR 'disabled' to remove specific names. If both are set, 'enabled' wins.
# Unknown names are ignored with a stderr warning — typo-tolerant so a
# config doesn't break stado across renames.
#
# Bundled tool names (list via TUI /tools or headless tools.list):
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
# overrides = { read = "my-read-1.0.0", webfetch = "corp-webfetch@2.1.0" }
# overrides = { bash = "approval-bash-go-0.1.0", write = "approval-write-go-0.1.0", edit = "approval-edit-go-0.1.0" }

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
# # Linux net:<host> subprocess policies require pasta from the
# # passt package so only the proxy port is reachable inside the netns.
# # Stdio MCP servers must declare capabilities; empty lists are refused.
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
# [hooks] — run shell commands at completed turn boundaries across the
#           TUI, stado run, and headless session.prompt.
#           Notification-only in this release (cannot block or modify a
#           turn). Each hook runs /bin/sh -c <cmd> with a 5s wall-clock
#           cap; a JSON payload is piped to stdin so scripts can act on
#           token counts / cost.
# ---------------------------------------------------------------------------
# [hooks]
# post_turn = "notify-send stado 'turn complete'"
# # The payload: {"event":"post_turn","turn_index":N,"tokens_in":X,
# #               "tokens_out":Y,"cost_usd":Z,"text_excerpt":"...",
# #               "duration_ms":M}

# ---------------------------------------------------------------------------
# AGENTS.md / CLAUDE.md — project-level instructions. Drop either file in
# your repo root (or any parent directory) and stado auto-loads the body into
# {{ .ProjectInstructions }} in system-prompt.md. AGENTS.md wins if both
# exist; CLAUDE.md is supported for backwards-compat with existing repos.
# Nearest-wins when a monorepo has files at multiple levels.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# [sandbox] / [git] / [acp] — reserved. Fields land with the matching phases.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# [plugins] — plugin trust + revocation + transparency log (Phase 7.6 / 7.7)
# The bundled auto-compact background plugin is enabled by default.
# Add extra installed plugin IDs here if you want more long-lived
# background plugins ticking alongside it.
# ---------------------------------------------------------------------------
# [plugins]
# crl_url = "https://example.com/stado/plugin-crl.json"
# crl_issuer_pubkey = "hex-or-base64-of-ed25519-pubkey"
# rekor_url = "https://rekor.sigstore.dev"  # Rekor transparency log
# background = ["session-recorder-0.1.0"]
`
