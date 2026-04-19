package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui"
)

var acpTools bool

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
		"refs and can be audited with `stado audit verify`.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		prov, provErr := tui.BuildProvider(cfg)
		if provErr != nil {
			fmt.Fprintf(os.Stderr, "stado acp: provider unavailable: %v\n", provErr)
		}
		fmt.Fprintln(os.Stderr, "stado acp: ready (ACP v1, stdio, tools=", acpTools, ")")
		s := acp.NewServer(cfg, prov)
		s.EnableTools = acpTools
		return s.Serve(cmd.Context(), os.Stdin, os.Stdout)
	},
}

func init() {
	acpCmd.Flags().BoolVar(&acpTools, "tools", false, "Enable tool-calling with git-native audit")
	rootCmd.AddCommand(acpCmd)
}
