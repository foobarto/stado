package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/headless"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/tui"
)

var headlessPersona string

var headlessCmd = &cobra.Command{
	Use:   "headless",
	Short: "Run stado as a general-purpose JSON-RPC daemon over stdio",
	Long: "Line-delimited JSON-RPC 2.0 daemon. Useful for CI integration,\n" +
		"external tooling, and editors other than Zed (Zed should use `stado acp`).\n\n" +
		"Methods:\n" +
		"  session.new         → { sessionId }\n" +
		"  session.prompt      { sessionId, prompt, tools? } → { text }\n" +
		"  session.list        → [{ sessionId, turns, workdir }]\n" +
		"  session.cancel      { sessionId } → { cancelled }\n" +
		"  session.delete      { sessionId } → {}\n" +
		"  session.compact     { sessionId } → { summary, priorTurns, postTurns }\n" +
		"  tools.list          → [{ name, description, class }]\n" +
		"  providers.list      → { available, current }\n" +
		"  plugin.list         → { plugins: [{ id, author, capabilities, tools }] }\n" +
		"  plugin.run          { sessionId, id, tool, args? } → { content, error? }\n" +
		"  shutdown            → end the daemon (drains in-flight RPCs first).\n\n" +
		"Notifications:\n" +
		"  session.update      { sessionId, kind, text? | name? input? }\n" +
		"                      kind: text | tool_call | subagent | plugin_fork | context_warning | system\n" +
		"                      subagent: phase/status/child/childWorktree/role/mode/forkTree?/changedFiles?/scopeViolations?/adoptionCommand?\n\n" +
		"Persona:\n" +
		"  --persona <name>            default persona for new sessions.\n" +
		"                              Resolution: {cwd}/.stado/personas →\n" +
		"                              <config-dir>/personas → bundled.\n" +
		"                              Empty = [defaults].persona, then\n" +
		"                              AgentLoop's legacy system-prompt path.\n" +
		"  session.new {persona:NAME}  per-session override; wins over\n" +
		"                              --persona for that session.\n",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var defaultPersona *personas.Persona
		if headlessPersona != "" {
			p, err := resolvePersona(headlessPersona, cfg)
			if err != nil {
				return fmt.Errorf("headless: --persona: %w", err)
			}
			defaultPersona = p
		}
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context) error {
			prov, provErr := tui.BuildProvider(cfg)
			if provErr != nil {
				fmt.Fprintf(os.Stderr, "stado headless: provider unavailable: %v\n", provErr)
			}
			personaTag := ""
			if defaultPersona != nil {
				personaTag = " (persona=" + defaultPersona.Name + ")"
			}
			fmt.Fprintf(os.Stderr, "stado headless: ready (JSON-RPC 2.0, stdio)%s\n", personaTag)
			srv := headless.NewServer(cfg, prov)
			srv.DefaultPersona = defaultPersona
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		})
	},
}

func init() {
	headlessCmd.Flags().StringVar(&headlessPersona, "persona", "", "Persona name to apply by default. Resolution: {cwd}/.stado/personas → <config-dir>/personas → bundled. session.new {\"persona\": NAME} overrides per-call.")
	rootCmd.AddCommand(headlessCmd)
}
