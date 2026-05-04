package main

// `stado integrations` — enumerate and detect external coding-agent
// CLIs stado can interoperate with via ACP / MCP.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/integrations"
)

var integrationsJSON bool

var integrationsCmd = &cobra.Command{
	Use:   "integrations",
	Short: "Detect external coding-agent CLIs (claude, gemini, codex, opencode, zed, aider) installed on this host",
	Long: "Scans PATH for known coding-agent binaries and HOME / XDG_*_HOME for\n" +
		"their config dirs, reporting what's installed and what protocols each\n" +
		"one speaks (ACP / MCP). Useful for setting up multi-agent workflows\n" +
		"where stado dispatches tasks to (or accepts work from) other agents.\n\n" +
		"Adding a new known integration: edit\n" +
		"internal/integrations/registry.go — that's the single source of truth.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ds := integrations.Detect(cmd.Context())
		if integrationsJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(ds)
		}
		return renderIntegrationsHuman(cmd.OutOrStdout(), ds)
	},
}

func renderIntegrationsHuman(w io.Writer, ds []integrations.Detection) error {
	if len(ds) == 0 {
		fmt.Fprintln(w, "No integrations registered.")
		return nil
	}

	var installed, missing []integrations.Detection
	for _, d := range ds {
		if d.Installed() {
			installed = append(installed, d)
		} else {
			missing = append(missing, d)
		}
	}

	if len(installed) > 0 {
		fmt.Fprintln(w, "Installed:")
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		for _, d := range installed {
			protoLabels := make([]string, 0, len(d.Protocols))
			for _, p := range d.Protocols {
				protoLabels = append(protoLabels, string(p))
			}
			ver := d.Version
			if ver == "" && d.BinaryPath != "" {
				ver = "(no version probe)"
			}
			pathLabel := d.BinaryPath
			if pathLabel == "" {
				pathLabel = "(config-only — binary not on PATH)"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				d.Name,
				strings.Join(protoLabels, "+"),
				ver,
				pathLabel)
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(missing) > 0 {
		fmt.Fprintln(w, "Available to install (not detected on this host):")
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		for _, d := range missing {
			protoLabels := make([]string, 0, len(d.Protocols))
			for _, p := range d.Protocols {
				protoLabels = append(protoLabels, string(p))
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n",
				d.Name,
				strings.Join(protoLabels, "+"),
				d.HelpURL)
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(installed) == 0 {
		fmt.Fprintln(w, "No external agents detected. See the URLs above to install one.")
	}
	return nil
}

func init() {
	integrationsCmd.Flags().BoolVar(&integrationsJSON, "json", false, "Emit JSON instead of the human-readable listing")
	rootCmd.AddCommand(integrationsCmd)
}

