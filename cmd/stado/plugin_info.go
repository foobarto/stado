package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/textutil"
)

var pluginInfoJSON bool

var pluginInfoCmd = &cobra.Command{
	Use:   "info [project:|global:]<canonical-source|store-key>",
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
		_ = runtime.BuildDefaultRegistry(cfg) // unused — side-effect: triggers bundled-tool registrations
		// Bundled-first lookup: a name like "auto-compact" resolves via
		// bundled.LookupByName to the synthetic manifest baked
		// into the binary.
		if info, _, ok := bundled.LookupByName(args[0]); ok {
			mf := plugins.Manifest{
				Name:         bundled.ManifestNamePrefix + "-" + info.Name,
				Version:      info.Version,
				Author:       info.Author,
				Capabilities: info.Capabilities,
				Tools:        bundledToolDefsFromList(info),
			}
			if pluginInfoJSON {
				out, _ := json.MarshalIndent(mf, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			return printManifestInfo(cmd.OutOrStdout(), mf, info.Name, true)
		}

		// Disk-install lookup accepts an exact source-derived store key or a
		// friendly alias only when that alias identifies one installed source.
		// An optional project:/global: prefix disambiguates identical rows in
		// both roots without becoming part of authenticated package identity.
		pkg, _, err := resolveManagedInstalledPackage(cfg, args[0])
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			return fmt.Errorf("plugin %q not installed — run `stado plugin list` to see installed plugins", args[0])
		}
		mf := &pkg.Manifest

		if pluginInfoJSON {
			out, _ := json.MarshalIndent(struct {
				Identity plugins.RuntimeIdentity `json:"identity"`
				StoreKey string                  `json:"store_key"`
				Manifest *plugins.Manifest       `json:"manifest"`
			}{pkg.Identity, pkg.Record.StoreKey, mf}, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\nStore:  %s\n\n", pkg.Identity.Canonical, pkg.Record.StoreKey)
		return printManifestInfo(cmd.OutOrStdout(), *mf, args[0], false)
	},
}

func init() {
	pluginInfoCmd.Flags().BoolVar(&pluginInfoJSON, "json", false, "Output raw manifest JSON (for scripting)")
}

// bundledToolDefsFromList synthesises minimal ToolDef entries from a
// bundled.Info. Schema/description aren't tracked at the Info
// level; the resulting ToolDefs carry just the tool name. Operators
// reading `plugin info` for a bundled module should also use
// `stado tool info <toolname>` for full schema detail.
func bundledToolDefsFromList(info bundled.Info) []plugins.ToolDef {
	out := make([]plugins.ToolDef, 0, len(info.Tools))
	for _, name := range info.Tools {
		out = append(out, plugins.ToolDef{
			Name:        name,
			Description: "(bundled — use `stado tool info " + name + "` for full schema)",
		})
	}
	return out
}

// printManifestInfo renders the manifest details. Refactored from the
// inline body of pluginInfoCmd.RunE to allow reuse from the bundled
// path. When bundled is true, certain fields (fingerprint, wasm
// sha256) are omitted with sentinel values, and the per-tool schema
// section is replaced with a hint to use `stado tool info`.
func printManifestInfo(o io.Writer, mf plugins.Manifest, displayID string, bundled bool) error {
	// Strip a leading 'v' so a bundled plugin's git-describe version
	// ('v0.64.0') doesn't render as 'vv0.64.0' under the 'v%s' format (P2.15).
	header := fmt.Sprintf("📦 %s  v%s", textutil.StripControlChars(mf.Name), strings.TrimPrefix(textutil.StripControlChars(mf.Version), "v"))
	if bundled {
		header += "  (bundled)"
	}
	fmt.Fprintln(o, header)
	fmt.Fprintf(o, "   Author:       %s\n", textutil.StripControlChars(mf.Author))
	if bundled {
		fmt.Fprintln(o, "   Fingerprint:  -  (built into stado binary)")
	} else {
		fmt.Fprintf(o, "   Fingerprint:  %s\n", mf.AuthorPubkeyFpr)
		fmt.Fprintf(o, "   Wasm SHA256:  %s\n", mf.WASMSHA256)
	}
	if mf.MinStadoVersion != "" {
		fmt.Fprintf(o, "   Requires:     stado >= %s\n", mf.MinStadoVersion)
	}
	fmt.Fprintln(o)

	// Capabilities
	fmt.Fprintf(o, "Capabilities (%d):\n", len(mf.Capabilities))
	for _, c := range mf.Capabilities {
		fmt.Fprintf(o, "  • %s\n", textutil.StripControlChars(c))
	}
	fmt.Fprintln(o)

	// Tools
	if bundled {
		fmt.Fprintf(o, "Tools (%d):\n", len(mf.Tools))
		for _, t := range mf.Tools {
			fmt.Fprintf(o, "  %-30s  %s\n", textutil.StripControlChars(t.Name), truncateStr(textutil.SanitizeForTerminal(t.Description), 80))
		}
	} else {
		fmt.Fprintf(o, "Tools (%d):\n", len(mf.Tools))
		w := tabwriter.NewWriter(o, 0, 0, 2, ' ', 0)
		for _, t := range mf.Tools {
			params := schemaParams(t.Schema)
			paramsStr := ""
			if params != "" {
				paramsStr = "  " + params
			}
			fmt.Fprintf(w, "  %-30s\t%s\n", textutil.StripControlChars(t.Name)+paramsStr, truncateStr(textutil.SanitizeForTerminal(t.Description), 80))
		}
		_ = w.Flush()

		// Full tool details
		if len(mf.Tools) > 0 {
			fmt.Fprintln(o)
			fmt.Fprintln(o, "Tool schemas:")
			for _, t := range mf.Tools {
				fmt.Fprintf(o, "\n  %s\n", textutil.StripControlChars(t.Name))
				fmt.Fprintf(o, "  %s\n", strings.Repeat("─", min(len(t.Name)+2, 60)))
				for _, line := range wordWrap(textutil.SanitizeForTerminal(t.Description), 72) {
					fmt.Fprintf(o, "  %s\n", line)
				}
				if t.Schema != "" {
					if params := prettySchema(t.Schema); params != "" {
						fmt.Fprintf(o, "\n  Parameters:\n%s", params)
					}
				}
			}
		}
	}

	fmt.Fprintln(o)
	if bundled {
		fmt.Fprintf(o, "  stado tool info <toolname>   for individual schemas\n")
	} else {
		fmt.Fprintf(o, "  stado plugin info %s --json | jq '.tools[].name'\n", displayID)
	}
	return nil
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
		sb.WriteString(fmt.Sprintf("    %-22s %s%s\n", textutil.StripControlChars(name), textutil.StripControlChars(typeStr), req))
		if prop.Description != "" {
			for _, line := range wordWrap(textutil.SanitizeForTerminal(prop.Description), 64) {
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
	// Rune-safe: plugin tool descriptions are author-supplied and may be
	// non-ASCII; a raw byte slice would split a multibyte rune.
	return textutil.TruncateRunes(s, max)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
