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
		// Cap-vs-sandbox cross-check. Surfaces conflicts between the
		// plugin's declared caps and the operator's [sandbox] config —
		// the kind of mismatch that produces "ENOENT / connection
		// refused" errors at runtime that the operator can't trace
		// back to their own config without help.
		findings := crossCheckSandbox(mf, cfg.Sandbox)
		if len(findings) > 0 {
			fmt.Println("\nSandbox cross-check:")
			for _, f := range findings {
				fmt.Println(f.Render())
			}
		}
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
	case cap == "secrets:read" || strings.HasPrefix(cap, "secrets:read:"):
		cn.requirement = requireNothing
		cn.note = "operator's secret store; stado provides — declare secrets:read:<your_secret_pattern> to narrow access"
		return cn
	case cap == "secrets:write" || strings.HasPrefix(cap, "secrets:write:"):
		cn.requirement = requireNothing
		cn.note = "writes to operator's secret store; stado provides — declare secrets:write:<your_secret_pattern> to narrow access"
		return cn
	case cap == "state:read" || strings.HasPrefix(cap, "state:read:"):
		cn.requirement = requireNothing
		cn.note = "process-lifetime in-memory KV (stado_instance_*); cleared on stado exit"
		return cn
	case cap == "state:write" || strings.HasPrefix(cap, "state:write:"):
		cn.requirement = requireNothing
		cn.note = "writes to in-memory KV (stado_instance_*); per-plugin namespaced"
		return cn
	case cap == "tool:invoke" || strings.HasPrefix(cap, "tool:invoke:"):
		cn.requirement = requireNothing
		cn.note = "stado_tool_invoke — plugin calls other registered tools; gated by name glob; depth-limited recursion"
		return cn
	case cap == "net:http_client":
		cn.requirement = requireNothing
		cn.note = "stateful HTTP client with cookie jar; uses net:http_request:<host> caps as the host allowlist"
		return cn
	}
	cn.note = "unrecognised capability — passed through to the runtime as-is"
	return cn
}

// sandboxFinding flags a mismatch between a plugin's declared caps
// and the operator's [sandbox] config. Three severities:
//
//   - error: the cap WILL fail at runtime under this sandbox config
//     (e.g. plugin declares net:http_request but [sandbox.wrap].network = "off")
//   - warn:  the cap MAY need extra setup (e.g. fs:read:/etc/passwd
//     not in [sandbox.wrap].bind_ro)
//   - info:  no concern; surfaced so the operator sees the
//     sandbox-cap relationship explicitly
type sandboxFinding struct {
	Cap      string
	Severity string // "error", "warn", "info"
	Note     string
}

func (f sandboxFinding) Render() string {
	icon := "i "
	switch f.Severity {
	case "error":
		icon = "✗ "
	case "warn":
		icon = "⚠ "
	}
	return fmt.Sprintf("  %s%-50s %s", icon, f.Cap, f.Note)
}

// crossCheckSandbox compares the plugin's declared capabilities
// against the operator's [sandbox] config and returns findings
// that point at concrete mismatches.
//
// Rules:
//   - sandbox.mode = "off" — no enforcement; emit nothing.
//   - sandbox.wrap.network = "off" — net:* caps will fail.
//   - sandbox.wrap.network = "namespaced" + no http_proxy — net:* caps
//     can't reach the host network without a proxy.
//   - sandbox.wrap.network = "namespaced" + http_proxy set — net:* caps
//     route through the proxy (informational).
//   - fs:read:/abs/path or fs:write:/abs/path — only flagged when the
//     path is NOT under sandbox.wrap.bind_ro / bind_rw. Workdir-rooted
//     paths (".", "./...") are auto-bound by stado and not flagged.
//   - exec:* caps — no sandbox constraint at the wrap layer; surfaced
//     as informational.
func crossCheckSandbox(mf *plugins.Manifest, sb config.Sandbox) []sandboxFinding {
	if sb.Mode == "" || sb.Mode == "off" {
		return nil
	}
	var out []sandboxFinding
	netBlocked := sb.Wrap.Network == "off"
	netNamespaced := sb.Wrap.Network == "namespaced"
	hasProxy := strings.TrimSpace(sb.HTTPProxy) != ""

	for _, c := range mf.Capabilities {
		switch {
		case strings.HasPrefix(c, "net:"):
			switch {
			case netBlocked:
				out = append(out, sandboxFinding{
					Cap: c, Severity: "error",
					Note: "[sandbox.wrap].network = \"off\" — this cap WILL fail at runtime",
				})
			case netNamespaced && !hasProxy:
				out = append(out, sandboxFinding{
					Cap: c, Severity: "error",
					Note: "[sandbox.wrap].network = \"namespaced\" with no [sandbox].http_proxy set — set http_proxy or change network mode",
				})
			case netNamespaced && hasProxy:
				out = append(out, sandboxFinding{
					Cap: c, Severity: "info",
					Note: "namespaced netns + http_proxy set — traffic routes through " + sb.HTTPProxy,
				})
			}
		case strings.HasPrefix(c, "fs:read:") || strings.HasPrefix(c, "fs:write:"):
			path := c[strings.IndexByte(c, ':')+1:]
			path = path[strings.IndexByte(path, ':')+1:]
			if path == "." || path == "" || strings.HasPrefix(path, "./") || !strings.HasPrefix(path, "/") {
				continue // workdir-rooted; stado auto-binds
			}
			isWrite := strings.HasPrefix(c, "fs:write:")
			if isWrite {
				if !pathInBindList(path, sb.Wrap.BindRW) {
					out = append(out, sandboxFinding{
						Cap: c, Severity: "warn",
						Note: "absolute path not in [sandbox.wrap].bind_rw — add it or this cap will fail at runtime",
					})
				}
			} else {
				if !pathInBindList(path, sb.Wrap.BindRO) && !pathInBindList(path, sb.Wrap.BindRW) {
					out = append(out, sandboxFinding{
						Cap: c, Severity: "warn",
						Note: "absolute path not in [sandbox.wrap].bind_ro — add it or this cap will fail at runtime",
					})
				}
			}
		case strings.HasPrefix(c, "exec:"):
			// exec:* runs through stado's bundled exec runner; the
			// wrap layer doesn't constrain it directly. Surface as
			// informational so the operator knows the cap-vs-config
			// relationship is checked.
			out = append(out, sandboxFinding{
				Cap: c, Severity: "info",
				Note: "exec runs through stado's runner; sandbox.wrap doesn't gate this directly",
			})
		}
	}
	return out
}

// pathInBindList returns true if `path` exactly matches or is under
// any entry in `binds`. Bind entries are absolute paths; sub-paths
// of a bound directory are reachable.
func pathInBindList(path string, binds []string) bool {
	for _, b := range binds {
		b = strings.TrimRight(b, "/")
		if path == b {
			return true
		}
		if strings.HasPrefix(path, b+"/") {
			return true
		}
	}
	return false
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
