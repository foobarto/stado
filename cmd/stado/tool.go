package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Inspect and configure tools",
}

var toolJSONFlag bool

var toolLsCmd = &cobra.Command{
	Use:   "ls [glob]",
	Short: "List tools with state (autoloaded/enabled/disabled), plugin source, and categories",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reg := runtime.BuildDefaultRegistry()
		runtime.ApplyToolFilter(reg, cfg)
		autoloaded := runtime.AutoloadedTools(reg, cfg)
		autoloadSet := map[string]bool{}
		for _, t := range autoloaded {
			autoloadSet[t.Name()] = true
		}
		glob := ""
		if len(args) > 0 {
			glob = args[0]
		}
		if toolJSONFlag {
			for _, t := range reg.All() {
				if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
					continue
				}
				state := "enabled"
				if autoloadSet[t.Name()] {
					state = "autoloaded"
				}
				entry := map[string]any{"name": t.Name(), "state": state}
				b, _ := json.Marshal(entry)
				fmt.Println(string(b))
			}
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE")
		for _, t := range reg.All() {
			if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
				continue
			}
			state := "enabled"
			if autoloadSet[t.Name()] {
				state = "autoloaded"
			}
			fmt.Fprintf(w, "%s\t%s\n", t.Name(), state)
		}
		return w.Flush()
	},
}

var toolInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Full schema + description for a named tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reg := runtime.BuildDefaultRegistry()
		runtime.ApplyToolFilter(reg, cfg)
		t, ok := reg.Get(args[0])
		if !ok {
			return fmt.Errorf("tool %q not found", args[0])
		}
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		if toolJSONFlag {
			out := map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"schema":      json.RawMessage(schema),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("Name:        %s\n", t.Name())
		fmt.Printf("Description: %s\n", t.Description())
		fmt.Printf("Schema:\n%s\n", schema)
		return nil
	},
}

var toolCatsCmd = &cobra.Command{
	Use:   "cats [query]",
	Short: "List canonical tool categories (optional substring filter)",
	RunE: func(cmd *cobra.Command, args []string) error {
		q := ""
		if len(args) > 0 {
			q = strings.ToLower(args[0])
		}
		for _, c := range plugins.CanonicalCategories {
			if q != "" && !strings.Contains(strings.ToLower(c), q) {
				continue
			}
			fmt.Println(c)
		}
		return nil
	},
}

var toolReloadCmd = &cobra.Command{
	Use:   "reload [glob]",
	Short: "Signal the running TUI session to drop cached wasm instance(s) on next call",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Runtime-only operation: this CLI form just informs the operator.
		// Inside a running TUI session, use /tool reload <glob> instead.
		fmt.Fprintln(os.Stderr, "note: tool reload takes effect inside a running stado session.")
		fmt.Fprintln(os.Stderr, "      Use /tool reload <glob> in the TUI to reload without restarting.")
		return nil
	},
}

func init() {
	toolCmd.PersistentFlags().BoolVar(&toolJSONFlag, "json", false, "Emit JSON output")
	toolCmd.AddCommand(toolLsCmd, toolInfoCmd, toolCatsCmd, toolReloadCmd)
	rootCmd.AddCommand(toolCmd)
}
