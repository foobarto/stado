package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

var pluginInfoJSON bool

var pluginInfoCmd = &cobra.Command{
	Use:   "info <plugin-id>",
	Short: "Show an installed plugin's details: tools, capabilities, author",
	Long: "Reads the installed plugin manifest and displays tools, capabilities,\n" +
		"author, and version in a readable format.\n\n" +
		"Use --json for machine-readable output (pairs with jq).",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
		dir, err := plugins.InstalledDir(pluginsDir, args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("plugin %q not installed — run `stado plugin list` to see installed plugins", args[0])
		}
		mf, _, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}

		if pluginInfoJSON {
			out, _ := json.MarshalIndent(mf, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}

		o := cmd.OutOrStdout()

		// Header
		fmt.Fprintf(o, "📦 %s  v%s\n", mf.Name, mf.Version)
		fmt.Fprintf(o, "   Author:       %s\n", mf.Author)
		fmt.Fprintf(o, "   Fingerprint:  %s\n", mf.AuthorPubkeyFpr)
		fmt.Fprintf(o, "   Wasm SHA256:  %s\n", mf.WASMSHA256)
		if mf.MinStadoVersion != "" {
			fmt.Fprintf(o, "   Requires:     stado >= %s\n", mf.MinStadoVersion)
		}
		fmt.Fprintln(o)

		// Capabilities
		fmt.Fprintf(o, "Capabilities (%d):\n", len(mf.Capabilities))
		for _, cap := range mf.Capabilities {
			fmt.Fprintf(o, "  • %s\n", cap)
		}
		fmt.Fprintln(o)

		// Tools
		fmt.Fprintf(o, "Tools (%d):\n", len(mf.Tools))
		w := tabwriter.NewWriter(o, 0, 0, 2, ' ', 0)
		for _, t := range mf.Tools {
			// Parse schema to list required params
			params := schemaParams(t.Schema)
			paramsStr := ""
			if params != "" {
				paramsStr = "  " + params
			}
			fmt.Fprintf(w, "  %-30s\t%s\n", t.Name+paramsStr, truncateStr(t.Description, 80))
		}
		_ = w.Flush()

		// Full tool details
		if len(mf.Tools) > 0 {
			fmt.Fprintln(o)
			fmt.Fprintln(o, "Tool schemas:")
			for _, t := range mf.Tools {
				fmt.Fprintf(o, "\n  %s\n", t.Name)
				fmt.Fprintf(o, "  %s\n", strings.Repeat("─", min(len(t.Name)+2, 60)))
				// Word-wrap description at 72 chars
				for _, line := range wordWrap(t.Description, 72) {
					fmt.Fprintf(o, "  %s\n", line)
				}
				// Pretty-print schema params
				if t.Schema != "" {
					if params := prettySchema(t.Schema); params != "" {
						fmt.Fprintf(o, "\n  Parameters:\n%s", params)
					}
				}
			}
		}

		fmt.Fprintln(o)
		fmt.Fprintf(o, "  stado plugin info %s --json | jq '.tools[].name'\n", args[0])
		return nil
	},
}

func init() {
	pluginInfoCmd.Flags().BoolVar(&pluginInfoJSON, "json", false, "Output raw manifest JSON (for scripting)")
}

// schemaParams extracts required parameter names from a JSON schema string.
func schemaParams(schema string) string {
	if schema == "" {
		return ""
	}
	var s struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		return ""
	}
	if len(s.Properties) == 0 {
		return ""
	}
	reqSet := map[string]bool{}
	for _, r := range s.Required {
		reqSet[r] = true
	}
	var parts []string
	for name := range s.Properties {
		if reqSet[name] {
			parts = append(parts, "<"+name+">")
		} else {
			parts = append(parts, "["+name+"]")
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// prettySchema formats a JSON schema's properties as indented lines.
func prettySchema(schema string) string {
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		return ""
	}
	if len(s.Properties) == 0 {
		return ""
	}
	reqSet := map[string]bool{}
	for _, r := range s.Required {
		reqSet[r] = true
	}
	var sb strings.Builder
	for name, propRaw := range s.Properties {
		var prop struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
		}
		_ = json.Unmarshal(propRaw, &prop)
		req := ""
		if reqSet[name] {
			req = " (required)"
		}
		typeStr := prop.Type
		if len(prop.Enum) > 0 {
			typeStr = strings.Join(prop.Enum, "|")
		}
		sb.WriteString(fmt.Sprintf("    %-22s %s%s\n", name, typeStr, req))
		if prop.Description != "" {
			for _, line := range wordWrap(prop.Description, 64) {
				sb.WriteString(fmt.Sprintf("    %-22s   %s\n", "", line))
			}
		}
	}
	return sb.String()
}

func wordWrap(s string, width int) []string {
	if len(s) <= width {
		return []string{s}
	}
	var lines []string
	for len(s) > width {
		cut := width
		for cut > 0 && s[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = width
		}
		lines = append(lines, s[:cut])
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		lines = append(lines, s)
	}
	return lines
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
