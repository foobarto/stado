package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// pluginDoctorCmd inspects an installed plugin and emits a
// surface-compatibility report. Solves the "I built a plugin, ran
// it, got `stado_http_get returned -1` — now what?" first-time-author
// pain that motivated the EP-0028 `--with-tool-host` work in the
// first place. Doctor parses the manifest's declared capabilities
// and tells the operator which `stado plugin run` flag combination
// (or which surface entirely) the plugin needs.
var pluginDoctorCmd = &cobra.Command{
	Use:   "doctor <plugin-id>",
	Short: "Inspect an installed plugin and explain which surfaces / flags it needs",
	Long: "Reads the plugin's manifest from `<state-dir>/plugins/<id>/`,\n" +
		"classifies each declared capability, and prints a checklist\n" +
		"of compatible surfaces with the exact flags to pass. Useful\n" +
		"when `plugin run` returns the documented \"plugin host has no\n" +
		"tool runtime context\" or \"stado_fs_read failed\" errors and\n" +
		"the operator wants to know which knob to flip.",
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
			return fmt.Errorf("plugin %s not installed (run `stado plugin install <plugin-dir>` after building + signing it)", args[0])
		}
		mf, _, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
		report, err := buildPluginDoctorReport(mf, dir)
		if err != nil {
			return err
		}
		fmt.Print(report)
		return nil
	},
}

// pluginRequirement classifies one declared capability — what it
// requires from the host surface. Used by the doctor table.
type pluginRequirement int

const (
	requireNothing       pluginRequirement = iota // satisfied on any surface
	requireWorkdir                                // needs `--workdir <path>` (or full agent loop)
	requireToolHost                               // needs `--with-tool-host` (or full agent loop)
	requireSession                                // needs `--session <id>` (or full agent loop)
	requireFullAgentLoop                          // ONLY works in TUI / `stado run`
	requireUIApproval                             // needs an approval bridge — TUI/headless agent loop
)

type capabilityNote struct {
	cap         string
	requirement pluginRequirement
	note        string
}

func classifyCapability(cap string) capabilityNote {
	cn := capabilityNote{cap: cap}
	switch {
	case strings.HasPrefix(cap, "fs:read:") || strings.HasPrefix(cap, "fs:write:"):
		path := cap[strings.IndexByte(cap, ':')+1:]
		path = path[strings.IndexByte(path, ':')+1:]
		if path == "." || path == "" || strings.HasPrefix(path, "./") || !strings.HasPrefix(path, "/") {
			cn.requirement = requireWorkdir
			cn.note = "workdir-rooted; needs `--workdir <path>` (or run inside agent loop with the right cwd)"
			return cn
		}
		cn.requirement = requireNothing
		cn.note = "absolute path; resolves identically on any surface"
		return cn
	case cap == "net:http_get" || strings.HasPrefix(cap, "net:"):
		cn.requirement = requireToolHost
		cn.note = "bundled-tool import (stado_http_get); needs `--with-tool-host` on plugin run"
		return cn
	case cap == "exec:bash" || cap == "exec:shallow_bash":
		cn.requirement = requireFullAgentLoop
		cn.note = "needs sandbox.Runner — only available in TUI / `stado run`. EP-0028 refuses this under --with-tool-host."
		return cn
	case strings.HasPrefix(cap, "exec:"):
		cn.requirement = requireToolHost
		cn.note = "bundled-tool import (search / ast-grep); needs `--with-tool-host`"
		return cn
	case cap == "lsp:query":
		cn.requirement = requireToolHost
		cn.note = "bundled-tool import (LSP); needs `--with-tool-host`"
		return cn
	case strings.HasPrefix(cap, "session:") || cap == "llm:invoke" || strings.HasPrefix(cap, "llm:invoke:") ||
		strings.HasPrefix(cap, "memory:"):
		cn.requirement = requireSession
		cn.note = "session-aware capability; needs `--session <id>` on plugin run (or run inside agent loop)"
		return cn
	case cap == "ui:approval":
		cn.requirement = requireUIApproval
		cn.note = "needs an approval bridge — only the TUI / headless agent loop provides one"
		return cn
	}
	cn.note = "unrecognised capability — passed through to the runtime as-is"
	return cn
}

// buildPluginDoctorReport renders the human-readable text body. Split
// out from RunE so it's directly testable.
func buildPluginDoctorReport(mf *plugins.Manifest, dir string) (string, error) {
	var b strings.Builder

	wasmPath := filepath.Join(dir, "plugin.wasm")
	wasmSize := int64(-1)
	if info, err := os.Stat(wasmPath); err == nil {
		wasmSize = info.Size()
	}

	fmt.Fprintf(&b, "Plugin:    %s v%s\n", mf.Name, mf.Version)
	fmt.Fprintf(&b, "Author:    %s\n", mf.Author)
	if mf.AuthorPubkeyFpr != "" {
		fmt.Fprintf(&b, "Signer:    %s\n", mf.AuthorPubkeyFpr)
	}
	if mf.WASMSHA256 != "" || wasmSize >= 0 {
		short := mf.WASMSHA256
		if len(short) > 12 {
			short = short[:12] + "…"
		}
		if wasmSize >= 0 {
			fmt.Fprintf(&b, "WASM:      sha256:%s (%d bytes)\n", short, wasmSize)
		} else {
			fmt.Fprintf(&b, "WASM:      sha256:%s\n", short)
		}
	}
	if mf.MinStadoVersion != "" {
		fmt.Fprintf(&b, "Min stado: %s\n", mf.MinStadoVersion)
	}
	b.WriteString("\n")

	if len(mf.Tools) == 0 {
		b.WriteString("Tools:     (none declared — plugin will be load-only)\n\n")
	} else {
		b.WriteString("Tools:\n")
		for _, t := range mf.Tools {
			desc := t.Description
			if len(desc) > 80 {
				desc = desc[:77] + "…"
			}
			fmt.Fprintf(&b, "  %-12s %s\n", t.Name, desc)
		}
		b.WriteString("\n")
	}

	// Classify capabilities and aggregate per-surface requirements.
	notes := make([]capabilityNote, 0, len(mf.Capabilities))
	hasWorkdir := false
	hasToolHost := false
	hasSession := false
	hasFullLoopOnly := false
	hasUIApproval := false
	for _, c := range mf.Capabilities {
		cn := classifyCapability(c)
		notes = append(notes, cn)
		switch cn.requirement {
		case requireWorkdir:
			hasWorkdir = true
		case requireToolHost:
			hasToolHost = true
		case requireSession:
			hasSession = true
		case requireFullAgentLoop:
			hasFullLoopOnly = true
		case requireUIApproval:
			hasUIApproval = true
		}
	}

	if len(notes) == 0 {
		b.WriteString("Capabilities: (none — plugin can do nothing requiring extra wiring)\n\n")
	} else {
		b.WriteString("Capabilities:\n")
		for _, cn := range notes {
			// Long absolute paths break the columnar layout. Wrap by
			// putting the note on the next line when the cap exceeds
			// the budget.
			if len(cn.cap) > 48 {
				fmt.Fprintf(&b, "  %s\n      → %s\n", cn.cap, cn.note)
			} else {
				fmt.Fprintf(&b, "  %-50s %s\n", cn.cap, cn.note)
			}
		}
		b.WriteString("\n")
	}

	// Per-surface compatibility.
	b.WriteString("Compatible surfaces:\n")
	fmt.Fprintf(&b, "  %s stado run / TUI                       full agent loop — always satisfies every capability above\n", "✓")

	plainOK := !hasWorkdir && !hasToolHost && !hasSession && !hasFullLoopOnly && !hasUIApproval
	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}
	fmt.Fprintf(&b, "  %s stado plugin run                      %s\n",
		mark(plainOK),
		surfaceReason(plainOK, hasWorkdir, hasToolHost, hasSession, hasFullLoopOnly, hasUIApproval, "plain"))

	workdirOK := !hasToolHost && !hasSession && !hasFullLoopOnly && !hasUIApproval
	fmt.Fprintf(&b, "  %s stado plugin run --workdir=$PWD       %s\n",
		mark(workdirOK),
		surfaceReason(workdirOK, false, hasToolHost, hasSession, hasFullLoopOnly, hasUIApproval, "workdir"))

	toolHostOK := !hasFullLoopOnly && !hasSession && !hasUIApproval
	fmt.Fprintf(&b, "  %s stado plugin run --with-tool-host%s    %s\n",
		mark(toolHostOK),
		spaceForWorkdir(hasWorkdir),
		surfaceReason(toolHostOK, false, false, hasSession, hasFullLoopOnly, hasUIApproval, "toolhost"))

	sessionOK := !hasFullLoopOnly && !hasUIApproval
	fmt.Fprintf(&b, "  %s stado plugin run --session <id>%s     %s\n",
		mark(sessionOK),
		spaceForFlags(hasWorkdir, hasToolHost),
		surfaceReason(sessionOK, false, false, false, hasFullLoopOnly, hasUIApproval, "session"))

	b.WriteString("\nSuggested invocation:\n  ")
	b.WriteString(suggestInvocation(mf, hasWorkdir, hasToolHost, hasSession, hasFullLoopOnly, hasUIApproval))
	b.WriteString("\n")
	return b.String(), nil
}

func spaceForWorkdir(hasWorkdir bool) string {
	if hasWorkdir {
		return " --workdir=$PWD"
	}
	return ""
}

func spaceForFlags(hasWorkdir, hasToolHost bool) string {
	var s string
	if hasWorkdir {
		s += " --workdir=$PWD"
	}
	if hasToolHost {
		s += " --with-tool-host"
	}
	return s
}

func surfaceReason(ok bool, hasWorkdir, hasToolHost, hasSession, hasFullLoopOnly, hasUIApproval bool, surface string) string {
	if ok {
		switch surface {
		case "plain":
			return "no flag-gated capabilities"
		case "workdir":
			if hasWorkdir {
				return "satisfies the workdir-rooted fs capability"
			}
			return "(more flags than this plugin needs — same outcome as the minimal row above)"
		case "toolhost":
			if hasToolHost {
				return "satisfies bundled-tool import(s)"
			}
			return "(more flags than this plugin needs — same outcome as the minimal row above)"
		case "session":
			if hasSession {
				return "satisfies session-aware capabilities"
			}
			return "(more flags than this plugin needs — same outcome as the minimal row above)"
		}
		return ""
	}
	var why []string
	if hasWorkdir && surface == "plain" {
		why = append(why, "needs --workdir")
	}
	if hasToolHost && (surface == "plain" || surface == "workdir") {
		why = append(why, "needs --with-tool-host")
	}
	if hasSession && (surface == "plain" || surface == "workdir" || surface == "toolhost") {
		why = append(why, "needs --session")
	}
	if hasFullLoopOnly {
		why = append(why, "exec:bash (or similar) refused by all `plugin run` paths — use TUI / `stado run`")
	}
	if hasUIApproval {
		why = append(why, "ui:approval needs the agent loop's approval bridge — TUI / `stado run`")
	}
	return strings.Join(why, "; ")
}

func suggestInvocation(mf *plugins.Manifest, hasWorkdir, hasToolHost, hasSession, hasFullLoopOnly, hasUIApproval bool) string {
	if hasFullLoopOnly || hasUIApproval {
		return "Use the TUI / `stado run`. Plugins with `exec:bash` or `ui:approval` cannot run from `plugin run`."
	}
	id := mf.Name + "-" + mf.Version
	tool := "<tool>"
	if len(mf.Tools) == 1 {
		tool = mf.Tools[0].Name
	}
	flags := ""
	if hasWorkdir {
		flags += " --workdir=$PWD"
	}
	if hasToolHost {
		flags += " --with-tool-host"
	}
	if hasSession {
		flags += " --session <id>"
	}
	return fmt.Sprintf("stado plugin run%s %s %s '<json-args>'", flags, id, tool)
}
