package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui"
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Run stado as an Agent Client Protocol server over stdio",
	Long: `Run stado as an ACP (Agent Client Protocol) server, speaking JSON-RPC 2.0
over stdin/stdout. Used by editors (notably Zed) to drive stado as a
coding-agent backend.

Configure in Zed via:

  "agent_servers": {
    "stado": { "command": "stado", "args": ["acp"] }
  }

Permission prompts, file edits, and streamed updates flow over the same
channel. Tool calls are not yet plumbed through ACP — v1 is text-only.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		prov, provErr := tui.BuildProvider(cfg)
		if provErr != nil {
			// Non-fatal: start the server anyway so the client gets a
			// meaningful initialize response; prompts will then error.
			fmt.Fprintf(os.Stderr, "stado acp: provider unavailable: %v\n", provErr)
		}
		fmt.Fprintln(os.Stderr, "stado acp: ready (ACP v1, stdio)")
		s := acp.NewServer(cfg, prov)
		return s.Serve(cmd.Context(), os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(acpCmd)
}
