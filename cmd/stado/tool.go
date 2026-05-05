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
	"github.com/foobarto/stado/internal/tools"
	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Inspect and configure tools",
}

var toolJSONFlag bool

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
		if !ok {
			// canonical → wire (split on first dot, route through WireForm
			// so hyphenated plugin aliases normalise to underscores).
			if dot := strings.Index(query, "."); dot > 0 && dot < len(query)-1 {
				if wire, err := tools.WireForm(query[:dot], query[dot+1:]); err == nil {
					t, ok = reg.Get(wire)
				}
			}
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

// ── mutating verbs (EP-0037 §F) ────────────────────────────────────────────

var (
	toolMutateGlobal bool
	toolMutateConfig string
	toolMutateDryRun bool
)

func toolMutateConfigPath() (string, error) {
	if toolMutateConfig != "" {
		return toolMutateConfig, nil
	}
	if toolMutateGlobal {
		// Mirror config.defaultConfigPath: $XDG_CONFIG_HOME/stado/config.toml
		// or ~/.config/stado/config.toml.
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return xdg + "/stado/config.toml", nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		return home + "/.config/stado/config.toml", nil
	}
	// Project-local: .stado/config.toml under the cwd (or its
	// nearest ancestor — config.Load walks up). Default to creating
	// it in the cwd.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return cwd + "/.stado/config.toml", nil
}

func runToolMutate(verb, key, removeFromKey string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("tool %s: at least one tool name or glob is required", verb)
	}
	path, err := toolMutateConfigPath()
	if err != nil {
		return err
	}
	if toolMutateDryRun {
		fmt.Fprintf(os.Stderr, "tool %s: would update %s\n", verb, path)
		fmt.Fprintf(os.Stderr, "  add to [tools].%s: %v\n", key, args)
		if removeFromKey != "" {
			fmt.Fprintf(os.Stderr, "  remove from [tools].%s: %v\n", removeFromKey, args)
		}
		return nil
	}
	if removeFromKey != "" {
		// Best-effort cleanup of the inverse list. Silent no-op when the
		// inverse list is empty or the entry isn't there.
		_ = config.WriteToolsListRemove(path, removeFromKey, args)
	}
	if err := config.WriteToolsListAdd(path, key, args); err != nil {
		return fmt.Errorf("tool %s: %w", verb, err)
	}
	fmt.Printf("tool %s: updated %s ([tools].%s += %v)\n", verb, path, key, args)
	return nil
}

func runToolUnmutate(verb, key string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("tool %s: at least one tool name or glob is required", verb)
	}
	path, err := toolMutateConfigPath()
	if err != nil {
		return err
	}
	if toolMutateDryRun {
		fmt.Fprintf(os.Stderr, "tool %s: would update %s\n", verb, path)
		fmt.Fprintf(os.Stderr, "  remove from [tools].%s: %v\n", key, args)
		return nil
	}
	if err := config.WriteToolsListRemove(path, key, args); err != nil {
		return fmt.Errorf("tool %s: %w", verb, err)
	}
	fmt.Printf("tool %s: updated %s ([tools].%s -= %v)\n", verb, path, key, args)
	return nil
}

var toolEnableCmd = &cobra.Command{
	Use:   "enable <name|glob> [<name|glob>...]",
	Short: "Add tools to [tools].enabled (allowlist) and remove them from [tools].disabled if present",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runToolMutate("enable", "enabled", "disabled", args)
	},
}

var toolDisableCmd = &cobra.Command{
	Use:   "disable <name|glob> [<name|glob>...]",
	Short: "Add tools to [tools].disabled and remove them from [tools].enabled / [tools].autoload if present",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Disabling a tool should also pull it out of autoload — otherwise
		// the autoload entry silently masks the disable.
		path, err := toolMutateConfigPath()
		if err == nil && !toolMutateDryRun {
			_ = config.WriteToolsListRemove(path, "autoload", args)
		}
		return runToolMutate("disable", "disabled", "enabled", args)
	},
}

var toolAutoloadCmd = &cobra.Command{
	Use:   "autoload <name|glob> [<name|glob>...]",
	Short: "Add tools to [tools].autoload (schema sent every turn)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runToolMutate("autoload", "autoload", "", args)
	},
}

var toolUnautoloadCmd = &cobra.Command{
	Use:   "unautoload <name|glob> [<name|glob>...]",
	Short: "Remove tools from [tools].autoload (still reachable via tools.search/describe)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runToolUnmutate("unautoload", "autoload", args)
	},
}

func init() {
	toolCmd.PersistentFlags().BoolVar(&toolJSONFlag, "json", false, "Emit JSON output")
	for _, c := range []*cobra.Command{toolEnableCmd, toolDisableCmd, toolAutoloadCmd, toolUnautoloadCmd} {
		c.Flags().BoolVar(&toolMutateGlobal, "global", false, "Write the user-level config (~/.config/stado/config.toml) instead of the project's .stado/config.toml")
		c.Flags().StringVar(&toolMutateConfig, "config", "", "Explicit config file path (overrides --global and project default)")
		c.Flags().BoolVar(&toolMutateDryRun, "dry-run", false, "Print intended changes without writing")
	}
	toolCmd.AddCommand(toolListCmd, toolInfoCmd, toolCatsCmd, toolReloadCmd,
		toolEnableCmd, toolDisableCmd, toolAutoloadCmd, toolUnautoloadCmd)
	rootCmd.AddCommand(toolCmd)
}
