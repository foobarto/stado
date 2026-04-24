package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/headless"
	"github.com/foobarto/stado/internal/tui"
)

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
		"                      kind: text | tool_call | plugin_fork | context_warning | system\n",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return withTelemetry(cmd.Context(), cfg, func(ctx context.Context) error {
			prov, provErr := tui.BuildProvider(cfg)
			if provErr != nil {
				fmt.Fprintf(os.Stderr, "stado headless: provider unavailable: %v\n", provErr)
			}
			fmt.Fprintln(os.Stderr, "stado headless: ready (JSON-RPC 2.0, stdio)")
			return headless.NewServer(cfg, prov).Serve(ctx, os.Stdin, os.Stdout)
		})
	},
}

func init() {
	rootCmd.AddCommand(headlessCmd)
}
