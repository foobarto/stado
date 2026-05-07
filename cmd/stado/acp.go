package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui"
)

var (
	acpTools       bool
	acpMaxTurns    int
	acpNoTurnLimit bool
)

// acpNoTurnLimitMagnitude is the value resolveMaxTurns reports when
// the operator passes --no-turn-limit. AgentLoop has its own
// "exceeded N turns" wrap; setting this very high effectively means
// "let the loop drive until done or context cancels", which matches
// the operator's intent. Mirror of `stado run --no-turn-limit`.
const acpNoTurnLimitMagnitude = 1 << 24

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Run stado as an Agent Client Protocol server over stdio",
	Long: "Run stado as an ACP (Agent Client Protocol) server, speaking JSON-RPC 2.0\n" +
		"over stdin/stdout. Used by editors (notably Zed) to drive stado as a\n" +
		"coding-agent backend.\n\n" +
		"Configure in Zed via:\n\n" +
		`  "agent_servers": {` + "\n" +
		`    "stado": { "command": "stado", "args": ["acp"] }` + "\n" +
		`  }` + "\n\n" +
		"With --tools the session runs the full audited executor loop: every\n" +
		"tool call the model makes is committed to the session's trace/tree\n" +
		"refs and can be audited with `stado audit verify`.\n\n" +
		"Environment:\n" +
		"  Before stado acp starts, .env is auto-loaded from CWD upward. This is\n" +
		"  the recommended place to inject provider credentials\n" +
		"  (ANTHROPIC_API_KEY, OPENAI_API_KEY, OLLAMA_CLOUD_API_KEY, etc.) without\n" +
		"  hardcoding them in editor configuration.\n\n" +
		"Turn budget — resolution order (highest priority first):\n" +
		"  1. session/new params: {\"maxTurns\": N}     (per-session pin)\n" +
		"  2. --max-turns / --no-turn-limit            (operator CLI flag)\n" +
		"  3. [acp] max_turns = N in config.toml       (operator default)\n" +
		"  4. built-in: 50 with --tools, 1 without\n\n" +
		"session/update notifications use these kinds:\n" +
		"  kind=text       text deltas streamed from the provider (field: text)\n" +
		"  kind=tool_call  one notification per completed tool call\n" +
		"                  (fields: name, input — input is a JSON-encoded string)\n" +
		"  kind=choice     wasm plugin requested operator pick (Q3)\n" +
		"                  fields: requestId, prompt, options[{id,label}], multi,\n" +
		"                  default[]. Client must reply via session/choice_response\n" +
		"                  with {sessionId, requestId, selected[], cancelled}.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// CLI flags override [acp] max_turns when set. Per-session
		// session/new param still wins above this.
		if acpNoTurnLimit {
			cfg.ACP.MaxTurns = acpNoTurnLimitMagnitude
		} else if acpMaxTurns > 0 {
			cfg.ACP.MaxTurns = acpMaxTurns
		}
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context) error {
			prov, provErr := tui.BuildProvider(cfg)
			if provErr != nil {
				fmt.Fprintf(os.Stderr, "stado acp: provider unavailable: %v\n", provErr)
			}
			fmt.Fprintln(os.Stderr, "stado acp: ready (ACP v1, stdio, tools=", acpTools, ")")
			s := acp.NewServer(cfg, prov)
			s.EnableTools = acpTools
			return s.Serve(ctx, os.Stdin, os.Stdout)
		})
	},
}

func init() {
	acpCmd.Flags().BoolVar(&acpTools, "tools", false, "Enable tool-calling with git-native audit")
	acpCmd.Flags().IntVar(&acpMaxTurns, "max-turns", 0, "Per-prompt turn cap (operator default; 0 = use [acp] max_turns or built-in)")
	acpCmd.Flags().BoolVar(&acpNoTurnLimit, "no-turn-limit", false, "Effectively unlimited per-prompt turns (overrides --max-turns)")
	rootCmd.AddCommand(acpCmd)
}
