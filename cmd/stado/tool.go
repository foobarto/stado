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

// canonicalName converts a tool's internal name to its canonical display name.
// Uses single underscores as namespace separator (API-compatible — Anthropic's
// tool name regex is ^[a-zA-Z0-9_-]{1,64}$, dots are not allowed).
//
// Wire names (alias__tool) become alias_tool.
// Bare pre-EP-0038 names get their expected canonical prefix.
// Internal test tools (approval_demo) return "" to signal "hide from listing".
func canonicalName(name string) string {
	// Hide internal test tools.
	if name == "approval_demo" {
		return ""
	}
	// Wire form alias__tool → alias_tool (single underscore for display)
	if idx := strings.Index(name, "__"); idx >= 0 {
		alias := name[:idx]
		tool := name[idx+2:]
		return alias + "_" + tool
	}
	// Pre-EP-0038 bare names: map to their eventual canonical form.
	// These will be replaced by wire names once EP-0038b migration completes.
	switch name {
	case "read":
		return "fs_read"
	case "write":
		return "fs_write"
	case "edit":
		return "fs_edit"
	case "glob":
		return "fs_glob"
	case "grep":
		return "fs_grep"
	case "bash":
		return "shell_exec"
	case "webfetch":
		return "web_fetch"
	case "ripgrep":
		return "rg_search"
	case "ast_grep":
		return "astgrep_search"
	case "read_with_context":
		return "readctx_read"
	case "find_definition":
		return "lsp_definition"
	case "find_references":
		return "lsp_references"
	case "document_symbols":
		return "lsp_symbols"
	case "hover":
		return "lsp_hover"
	case "spawn_agent":
		return "agent_spawn"
	case "ls":
		return "ls_list"
	}
	return name
}

var toolLsCmd = &cobra.Command{
	Use:   "ls [glob]",
	Short: "List tools with state (autoloaded/enabled/disabled)",
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
				cname := canonicalName(t.Name())
				if cname == "" {
					continue
				}
				if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
					continue
				}
				state := "enabled"
				if autoloadSet[t.Name()] {
					state = "autoloaded"
				}
				entry := map[string]any{"name": cname, "wire": t.Name(), "state": state}
				b, _ := json.Marshal(entry)
				fmt.Println(string(b))
			}
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tDESCRIPTION")
		for _, t := range reg.All() {
			cname := canonicalName(t.Name())
			if cname == "" {
				continue // hide internal tools
			}
			if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
				continue
			}
			state := "enabled"
			if autoloadSet[t.Name()] {
				state = "autoloaded"
			}
			desc := t.Description()
			if len(desc) > 60 {
				desc = desc[:57] + "…"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", cname, state, desc)
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
		// Accept canonical dotted name (fs.read) or wire name (fs__read) or bare (read).
		query := args[0]
		t, ok := reg.Get(query)
		if !ok {
			// Try reversing canonical → wire (fs.read → fs__read)
			wire := strings.ReplaceAll(query, ".", "__")
			t, ok = reg.Get(wire)
		}
		if !ok {
			// Try reversing canonical → bare via known mappings
			for _, name := range reg.All() {
				if canonicalName(name.Name()) == query {
					t = name
					ok = true
					break
				}
			}
		}
		if !ok {
			return fmt.Errorf("tool %q not found — try `stado tool ls` to see available tools", query)
		}
		cname := canonicalName(t.Name())
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		if toolJSONFlag {
			out := map[string]any{
				"name":        cname,
				"wire":        t.Name(),
				"description": t.Description(),
				"schema":      json.RawMessage(schema),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("Name:        %s\n", cname)
		if cname != t.Name() {
			fmt.Printf("Wire name:   %s\n", t.Name())
		}
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
