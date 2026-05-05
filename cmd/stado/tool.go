package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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

// resolveToolName accepts canonical (fs.read), wire (fs__read), or bare (read)
// and returns the registered tool from the registry. Returns nil if not found.
func resolveToolName(reg interface {
	All() []toolLike
	Get(string) (toolLike, bool)
}, query string) (toolLike, bool) {
	// Direct match first.
	if t, ok := reg.Get(query); ok {
		return t, true
	}
	// Canonical fs.read → wire fs__read
	if strings.Contains(query, ".") {
		wire := strings.ReplaceAll(query, ".", "__")
		if t, ok := reg.Get(wire); ok {
			return t, true
		}
	}
	// Walk all and match by canonical name from metadata
	for _, t := range reg.All() {
		if runtime.LookupToolMetadata(t.Name()).Canonical == query {
			return t, true
		}
	}
	return nil, false
}

type toolLike interface {
	Name() string
	Description() string
	Schema() map[string]any
}

var toolListCmd = &cobra.Command{
	Use:     "list [glob]",
	Aliases: []string{"ls"},
	Short:   "List tools with state, plugin source, and categories",
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

		type row struct {
			canonical  string
			state      string
			plugin     string
			categories string
			desc       string
		}
		var rows []row
		for _, t := range reg.All() {
			md := runtime.LookupToolMetadata(t.Name())
			if md.Canonical == "" {
				continue // hidden internal tool
			}
			if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) && !runtime.ToolMatchesGlob(md.Canonical, glob) {
				continue
			}
			state := "enabled"
			if autoloadSet[t.Name()] {
				state = "autoloaded"
			}
			rows = append(rows, row{
				canonical:  md.Canonical,
				state:      state,
				plugin:     md.Plugin,
				categories: strings.Join(md.Categories, ", "),
				desc:       t.Description(),
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].canonical < rows[j].canonical })

		if toolJSONFlag {
			for _, r := range rows {
				entry := map[string]any{
					"name":       r.canonical,
					"state":      r.state,
					"plugin":     r.plugin,
					"categories": r.categories,
				}
				b, _ := json.Marshal(entry)
				fmt.Println(string(b))
			}
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tPLUGIN\tCATEGORIES")
		fmt.Fprintln(w, "────\t─────\t──────\t──────────")
		for _, r := range rows {
			cats := r.categories
			if cats == "" {
				cats = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.canonical, r.state, r.plugin, cats)
		}
		_ = w.Flush()
		fmt.Printf("\n%d tools (%d autoloaded)\n", len(rows), len(autoloaded))
		return nil
	},
}

var toolInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Full schema + description for a named tool (accepts canonical fs.read or wire fs__read)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reg := runtime.BuildDefaultRegistry()
		runtime.ApplyToolFilter(reg, cfg)

		query := args[0]
		// Try direct lookup
		t, ok := reg.Get(query)
		if !ok && strings.Contains(query, ".") {
			// canonical → wire
			wire := strings.ReplaceAll(query, ".", "__")
			t, ok = reg.Get(wire)
		}
		if !ok {
			// canonical lookup via metadata
			for _, candidate := range reg.All() {
				if runtime.LookupToolMetadata(candidate.Name()).Canonical == query {
					t = candidate
					ok = true
					break
				}
			}
		}
		if !ok {
			return fmt.Errorf("tool %q not found — try `stado tool list` to see available tools", query)
		}
		md := runtime.LookupToolMetadata(t.Name())
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		if toolJSONFlag {
			out := map[string]any{
				"name":        md.Canonical,
				"wire":        t.Name(),
				"plugin":      md.Plugin,
				"categories":  md.Categories,
				"description": t.Description(),
				"schema":      json.RawMessage(schema),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("Name:        %s\n", md.Canonical)
		fmt.Printf("Plugin:      %s\n", md.Plugin)
		if len(md.Categories) > 0 {
			fmt.Printf("Categories:  %s\n", strings.Join(md.Categories, ", "))
		}
		if md.Canonical != t.Name() {
			fmt.Printf("Wire name:   %s\n", t.Name())
		}
		fmt.Printf("Description: %s\n", t.Description())
		fmt.Printf("\nSchema:\n%s\n", schema)
		return nil
	},
}

var toolCatsCmd = &cobra.Command{
	Use:     "categories [query]",
	Aliases: []string{"cats"},
	Short:   "List canonical tool categories (optional substring filter)",
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
	Short: "Drop cached wasm instances; takes effect inside the running TUI session",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "note: tool reload takes effect inside a running stado session.")
		fmt.Fprintln(os.Stderr, "      Use /tool reload <glob> in the TUI to reload without restarting.")
		return nil
	},
}

func init() {
	toolCmd.PersistentFlags().BoolVar(&toolJSONFlag, "json", false, "Emit JSON output")
	toolCmd.AddCommand(toolListCmd, toolInfoCmd, toolCatsCmd, toolReloadCmd)
	rootCmd.AddCommand(toolCmd)
}
