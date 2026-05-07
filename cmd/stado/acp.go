package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/tui"
)

var (
	acpTools       bool
	acpMaxTurns    int
	acpNoTurnLimit bool
	acpResume      string
	acpPersona     string
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
		"  kind=subagent   subagent lifecycle event (fields: phase, status, role,\n" +
		"                  mode, child, childWorktree, parentSession, ...)\n" +
		"  kind=choice     wasm plugin requested operator pick (Q3)\n" +
		"                  fields: requestId, prompt, options[{id,label}], multi,\n" +
		"                  default[]. Client must reply via session/choice_response\n" +
		"                  with {sessionId, requestId, selected[], cancelled}.\n" +
		"  kind=approval   wasm plugin requested operator yes/no approval\n" +
		"                  fields: requestId, title, body. Client must reply via\n" +
		"                  session/approval_response with\n" +
		"                  {sessionId, requestId, allow:bool, cancelled:bool}.\n\n" +
		"Tool-only turns produce empty session/prompt text:\n" +
		"  Some models chain tool calls without producing any text deltas, so\n" +
		"  the session/update stream has zero kind=text events and the\n" +
		"  session/prompt success response carries text=\"\". The tool work\n" +
		"  itself is visible via kind=tool_call updates and committed to the\n" +
		"  session's git-native trace ref. Two ways to handle this:\n" +
		"  1. System prompt: instruct the model to always produce a final\n" +
		"     text summary, even when most of the turn is tool calls.\n" +
		"  2. Client side: treat empty text as \"see tool_call updates\";\n" +
		"     `stado stats --session <id> --json` lists the tool calls\n" +
		"     authoritatively if the client wasn't tracking them live.\n\n" +
		"Resuming a session:\n" +
		"  --resume <id-or-label>            attaches to an existing git-native\n" +
		"                                    session (full UUID, prefix ≥8, or\n" +
		"                                    description substring), loads its\n" +
		"                                    conversation history, and uses the\n" +
		"                                    same UUID as the wire sessionId.\n" +
		"  session/new {\"resumeSession\": ID} same effect, per-call. Must be a\n" +
		"                                    full UUID — prefix lookup is\n" +
		"                                    operator-only.\n\n" +
		"Persona:\n" +
		"  --persona <name>                  default persona for new sessions.\n" +
		"                                    Resolution: {cwd}/.stado/personas\n" +
		"                                    → <config-dir>/personas → bundled.\n" +
		"                                    Empty = [defaults].persona, then\n" +
		"                                    AgentLoop's legacy system-prompt\n" +
		"                                    path.\n" +
		"  session/new {\"persona\": NAME}     per-session override; wins over\n" +
		"                                    --persona for that session. Bad\n" +
		"                                    name fails session/new.",
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
		// Resolve --resume up front so a bad query fails before the
		// JSON-RPC loop opens. Friendly forms (prefix / description
		// substring) only work here — session/new on the wire takes
		// canonical UUIDs only.
		var resumeID string
		if acpResume != "" {
			resolved, err := resolveSessionID(cfg, acpResume)
			if err != nil {
				return fmt.Errorf("acp: --resume: %w", err)
			}
			resumeID = resolved
		}
		// Resolve --persona up front for the same reason: bad name
		// fails before any RPC opens. Per-call session/new still wins
		// over this CLI default. resolvePersona returns nil only when
		// no name was given anywhere; with --persona explicitly set it
		// either returns a persona or an error.
		var defaultPersona *personas.Persona
		if acpPersona != "" {
			p, err := resolvePersona(acpPersona, cfg)
			if err != nil {
				return fmt.Errorf("acp: --persona: %w", err)
			}
			defaultPersona = p
		}
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context) error {
			prov, provErr := tui.BuildProvider(cfg)
			if provErr != nil {
				fmt.Fprintf(os.Stderr, "stado acp: provider unavailable: %v\n", provErr)
			}
			personaTag := ""
			if defaultPersona != nil {
				personaTag = ", persona=" + defaultPersona.Name
			}
			if resumeID != "" {
				fmt.Fprintf(os.Stderr, "stado acp: ready (ACP v1, stdio, tools=%v, resume=%s%s)\n", acpTools, resumeID, personaTag)
			} else {
				fmt.Fprintf(os.Stderr, "stado acp: ready (ACP v1, stdio, tools=%v%s)\n", acpTools, personaTag)
			}
			s := acp.NewServer(cfg, prov)
			s.EnableTools = acpTools
			s.ResumeSessionID = resumeID
			s.DefaultPersona = defaultPersona
			return s.Serve(ctx, os.Stdin, os.Stdout)
		})
	},
}

func init() {
	acpCmd.Flags().BoolVar(&acpTools, "tools", false, "Enable tool-calling with git-native audit")
	acpCmd.Flags().IntVar(&acpMaxTurns, "max-turns", 0, "Per-prompt turn cap (operator default; 0 = use [acp] max_turns or built-in)")
	acpCmd.Flags().BoolVar(&acpNoTurnLimit, "no-turn-limit", false, "Effectively unlimited per-prompt turns (overrides --max-turns)")
	acpCmd.Flags().StringVar(&acpResume, "resume", "", "Resume an existing session (id, prefix ≥8, or description substring). Loads its conversation history and uses the canonical UUID as the wire sessionId.")
	acpCmd.Flags().StringVar(&acpPersona, "persona", "", "Persona name to apply by default. Resolution: {cwd}/.stado/personas → ~/.stado/personas → bundled. session/new {\"persona\":<name>} overrides per-call.")
	rootCmd.AddCommand(acpCmd)
}
