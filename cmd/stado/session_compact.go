package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

var sessionCompactCmd = &cobra.Command{
	Use:   "compact <id>",
	Short: "Advisory: compaction is plugin-driven; use a session-aware WASM plugin",
	Long: "Compaction is intentionally plugin-driven rather than a built-in\n" +
		"core rewrite path. Resolve the target session ID, then run a\n" +
		"session-aware plugin against it with:\n\n" +
		"  stado plugin run --session <id> <plugin-id> <tool> [json-args]\n\n" +
		"The example auto-compact plugin in plugins/default/auto-compact/\n" +
		"uses session:read + llm:invoke + session:fork to create a compacted\n" +
		"child session without mutating the parent.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id, err := resolveSessionID(cfg, args[0])
		if err != nil {
			return fmt.Errorf("session compact: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"session %s: compaction is plugin-driven.\n  Use: stado plugin run --session %s <plugin-id> <tool> [json-args]\n",
			id, id)
		return nil
	},
}
