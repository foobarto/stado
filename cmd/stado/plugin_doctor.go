package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/textutil"
)

// pluginDoctorCmd inspects an installed plugin and emits a
// surface-compatibility report. Solves the "I built a plugin, ran
// it, got a host-import capability error — now what?" first-time-author
// pain. Doctor parses the manifest's declared capabilities and tells
// the operator which `stado tool run` flag combination (or which
// surface entirely) the plugin needs. (`plugin run` was removed in
// favour of `tool run` — c2cd90d; the EP-0028 `--with-tool-host` flag
// became default behaviour under EP-0038.)
var pluginDoctorCmd = &cobra.Command{
	Use:   "doctor [project:|global:]<canonical-source|store-key>",
	Short: "Inspect an installed plugin and explain which surfaces / flags it needs",
	Long: "Resolves an exact canonical source/store key and reads its signed manifest,\n" +
		"classifies each declared capability, and prints a checklist\n" +
		"of compatible surfaces with the exact flags to pass. Useful\n" +
		"when `tool run` returns the documented \"plugin host has no\n" +
		"tool runtime context\" or \"stado_fs_read failed\" errors and\n" +
		"the operator wants to know which knob to flip.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pkg, _, err := resolveManagedInstalledPackage(cfg, args[0])
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			return fmt.Errorf("plugin %s not installed (run `stado plugin install <plugin-dir>` after building + signing it)", args[0])
		}
		dir, mf := pkg.Dir, &pkg.Manifest
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
		// R7: the cross-check note embeds http_proxy, which may carry
		// user:pass — redact embedded credentials before it reaches stdout.
		findings := crossCheckSandbox(mf, cfg.Redacted().Sandbox)
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
	requireToolHost                               // bundled-tool import — auto-provided on `stado tool run` + agent loop
	requireSession                                // needs `--session <id>` (or full agent loop)
	requireFullAgentLoop                          // ONLY works in TUI / `stado run`
	requireUIApproval                             // needs an approval bridge — TUI/headless agent loop
	requireUnsupported                            // removed/invalid cap; no execution surface can satisfy it
)

type capabilityNote struct {
	cap         string
	requirement pluginRequirement
	note        string
}

func classifyCapability(cap string) capabilityNote {
	cn := capabilityNote{cap: cap}
	switch {
	case cap == "cfg:state_dir":
		cn.requirement = requireNothing
		cn.note = "operator state-directory fact; stado resolves the rooted path before plugin execution"
		return cn
	case strings.HasPrefix(cap, "fs:read:cfg:state_dir/") || strings.HasPrefix(cap, "fs:write:cfg:state_dir/"):
		cn.requirement = requireNothing
		cn.note = "operator state-directory rooted; requires the exact cfg:state_dir capability and stays beneath that root"
		return cn
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
	case cap == "net:http_request" || strings.HasPrefix(cap, "net:http_request:"):
		cn.requirement = requireNothing
		cn.note = "stado_http_request primitive; optional :<host> suffix narrows the host allowlist"
		return cn
	case cap == "net:http_request_private":
		cn.requirement = requireNothing
		cn.note = "allows stado_http_request to reach private, loopback, and link-local addresses"
		return cn
	case cap == "terminal:open" || strings.HasPrefix(cap, "terminal:open:"):
		cn.requirement = requireUnsupported
		cn.note = "removed capability; use exec:pty[:<binary-glob>]"
		return cn
	case cap == "exec:bash" || cap == "exec:shallow_bash":
		cn.requirement = requireFullAgentLoop
		cn.note = "needs sandbox.Runner — `stado tool run` refuses this when no runner is available; use TUI / `stado run`"
		return cn
	case strings.HasPrefix(cap, "exec:"):
		cn.requirement = requireToolHost
		cn.note = "bundled-tool import (search / ast-grep); provided by `stado tool run`"
		return cn
	case cap == "lsp:query":
		cn.requirement = requireToolHost
		cn.note = "bundled-tool import (LSP); provided by `stado tool run`"
		return cn
	case cap == "registry:catalog":
		cn.requirement = requireToolHost
		cn.note = "bounded authenticated registry projection; needs the current tool registry supplied by a tool host or agent loop"
		return cn
	case strings.HasPrefix(cap, "context:resource:"):
		cn.requirement = requireFullAgentLoop
		cn.note = "bounded current-context resource catalog; available only inside a context-bearing agent loop"
		return cn
	case strings.HasPrefix(cap, "evidence:"):
		cn.requirement = requireSession
		cn.note = "broker-authenticated evidence capability; requires an owned session and exact evidence binding"
		return cn
	case strings.HasPrefix(cap, "provider:invoke:"):
		tokens, err := strconv.Atoi(strings.TrimPrefix(cap, "provider:invoke:"))
		if err != nil || tokens <= 0 || tokens > 2_000_000 {
			cn.requirement = requireUnsupported
			cn.note = "invalid provider capability; declare exact provider:invoke:<positive-token-ceiling-at-most-2000000>"
			return cn
		}
		cn.requirement = requireToolHost
		cn.note = "generic provider primitive with a signed cumulative token ceiling; needs a provider-enabled tool host or live session bridge"
		return cn
	case cap == "provider:invoke":
		cn.requirement = requireUnsupported
		cn.note = "invalid provider capability; a positive signed token ceiling is mandatory"
		return cn
	case strings.HasPrefix(cap, "session:"):
		cn.requirement = requireSession
		cn.note = "session-aware capability; needs `--session <id>` on `stado tool run` (or run inside agent loop)"
		return cn
	case strings.HasPrefix(cap, "artifact:"):
		cn.requirement = requireSession
		cn.note = "broker-authenticated artifact capability; lifecycle applications receive it from the interactive TUI session"
		return cn
	case strings.HasPrefix(cap, "agent:"):
		cn.requirement = requireSession
		cn.note = "broker-authenticated agent capability; requires an owned interactive session"
		return cn
	case strings.HasPrefix(cap, "timer:"):
		cn.requirement = requireSession
		cn.note = "durable application timer capability; requires a persistent lifecycle application session"
		return cn
	case strings.HasPrefix(cap, "lifecycle:"):
		cn.requirement = requireFullAgentLoop
		cn.note = "lifecycle subscription/decision capability; hosted only by the interactive TUI in this release"
		return cn
	case cap == "ui:approval" || cap == "ui:choice" || cap == "ui:print" || cap == "ui:render":
		cn.requirement = requireUIApproval
		cn.note = "needs an operator UI bridge — lifecycle applications receive it only from the interactive TUI"
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
	case strings.HasPrefix(cap, "net:dial:tcp:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_dial (TCP) — raw socket connection. Private addrs require net:http_request_private."
		return cn
	case strings.HasPrefix(cap, "net:dial:udp:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_dial (UDP) — connect-mode datagram socket. Private addrs require net:http_request_private."
		return cn
	case strings.HasPrefix(cap, "net:dial:unix:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_dial (Unix) — local IPC socket. Path-glob gated; refuses `..` traversal."
		return cn
	case strings.HasPrefix(cap, "net:listen:tcp:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_listen (TCP) — server-side bind. Loopback vs 0.0.0.0 must be spelled out in the cap; no implicit fallback. 8 listeners per plugin."
		return cn
	case strings.HasPrefix(cap, "net:listen:udp:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_listen (UDP) — bind for sendto/recvfrom. Outbound peers still gated by net:dial:udp:<peer-host>:<port> caps."
		return cn
	case strings.HasPrefix(cap, "net:listen:unix:"):
		cn.requirement = requireNothing
		cn.note = "stado_net_listen (Unix) — server-side IPC bind. Socket file is removed on listener close."
		return cn
	case cap == "net:http_client":
		cn.requirement = requireNothing
		cn.note = "stateful HTTP client with cookie jar; uses net:http_request:<host> caps as the host allowlist"
		return cn
	case cap == "net:multicast:udp":
		cn.requirement = requireNothing
		cn.note = "stado_net_setopt — enables broadcast / multicast group join+leave / multicast TTL+loopback on UDP listener handles. Use for discovery protocols (mDNS, SSDP, BACnet, NBNS)."
		return cn
	case cap == "net:icmp":
		cn.requirement = requireNothing
		cn.note = "stado_net_icmp_echo — ICMP echo (ping). Tries Linux unprivileged ICMP first, then raw if available. Set net.ipv4.ping_group_range or grant CAP_NET_RAW if echoes return 'operation not permitted'."
		return cn
	case strings.HasPrefix(cap, "net:"):
		cn.requirement = requireUnsupported
		cn.note = "unsupported plugin capability; use an explicit net:http_request, net:dial, net:listen, net:http_client, net:multicast, or net:icmp capability"
		return cn
	case cap == "dns:resolve":
		cn.requirement = requireNothing
		cn.note = "stado_dns_resolve — recursive DNS lookups (A, AAAA, TXT, MX, NS, PTR)"
		return cn
	case cap == "dns:resolve_private":
		cn.requirement = requireNothing
		cn.note = "stado_dns_resolve against a custom server on a private/loopback address (e.g. server=127.0.0.1:53). Without it a custom DNS server resolving to a private address is refused. Implies dns:resolve."
		return cn
	case cap == "dns:axfr":
		cn.requirement = requireNothing
		cn.note = "stado_dns_resolve_axfr — DNS zone transfer (RFC 5936). Most public servers refuse; useful against misconfigured infrastructure. Implies dns:resolve."
		return cn
	case cap == "dns:reverse":
		cn.requirement = requireNothing
		cn.note = "reverse DNS lookups (PTR queries by IP)"
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
			// Rune-safe: author-supplied descriptions may be non-ASCII.
			desc := textutil.TruncateRunes(t.Description, 80)
			fmt.Fprintf(&b, "  %-12s %s\n", t.Name, desc)
		}
		b.WriteString("\n")
	}

	// Classify capabilities and aggregate per-surface requirements.
	notes := make([]capabilityNote, 0, len(mf.Capabilities))
	hasWorkdir := false
	hasSession := false
	hasFullLoopOnly := false
	hasUIApproval := false
	hasUnsupported := false
	for _, c := range mf.Capabilities {
		cn := classifyCapability(c)
		notes = append(notes, cn)
		switch cn.requirement {
		case requireWorkdir:
			hasWorkdir = true
		case requireToolHost:
			// Tool host is auto-attached on `stado tool run` (and the
			// agent loop), so a bundled-tool import imposes no surface
			// constraint and needs no flag. EP-0028 (--with-tool-host)
			// folded into default behaviour under EP-0038.
		case requireSession:
			hasSession = true
		case requireFullAgentLoop:
			hasFullLoopOnly = true
		case requireUIApproval:
			hasUIApproval = true
		case requireUnsupported:
			hasUnsupported = true
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
	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}
	if mf.Lifecycle != nil {
		tuiOK := runtime.LifecycleApplicationSurfaceContract(runtime.ApplicationSurfaceTUI).Complete() && !hasUnsupported
		runOK := runtime.LifecycleApplicationSurfaceContract(runtime.ApplicationSurfaceRun).Complete() && !hasUnsupported
		headlessOK := runtime.LifecycleApplicationSurfaceContract(runtime.ApplicationSurfaceHeadless).Complete() && !hasUnsupported
		acpOK := runtime.LifecycleApplicationSurfaceContract(runtime.ApplicationSurfaceACP).Complete() && !hasUnsupported
		b.WriteString("Compatible surfaces:\n")
		fmt.Fprintf(&b, "  %s interactive TUI (`stado`)              %s\n", mark(tuiOK),
			surfaceReason(tuiOK, false, false, false, false, hasUnsupported, "full"))
		fmt.Fprintf(&b, "  %s stado run                              %s\n", mark(runOK), lifecycleSurfaceReason(runOK))
		fmt.Fprintf(&b, "  %s stado headless                         %s\n", mark(headlessOK), lifecycleSurfaceReason(headlessOK))
		fmt.Fprintf(&b, "  %s stado ACP                              %s\n", mark(acpOK), lifecycleSurfaceReason(acpOK))
		b.WriteString("  ✗ stado tool run                          not an application host\n")
		b.WriteString("\nSuggested invocation:\n  stado\n")
		return b.String(), nil
	}

	// Per-surface compatibility.
	b.WriteString("Compatible surfaces:\n")
	fullLoopOK := !hasUnsupported
	fmt.Fprintf(&b, "  %s stado run / TUI                       %s\n",
		mark(fullLoopOK), surfaceReason(fullLoopOK, false, false, false, false, hasUnsupported, "full"))

	plainOK := !hasWorkdir && !hasSession && !hasFullLoopOnly && !hasUIApproval && !hasUnsupported
	fmt.Fprintf(&b, "  %s stado tool run                        %s\n",
		mark(plainOK),
		surfaceReason(plainOK, hasWorkdir, hasSession, hasFullLoopOnly, hasUIApproval, hasUnsupported, "plain"))

	workdirOK := !hasSession && !hasFullLoopOnly && !hasUIApproval && !hasUnsupported
	fmt.Fprintf(&b, "  %s stado tool run --workdir=$PWD         %s\n",
		mark(workdirOK),
		surfaceReason(workdirOK, false, hasSession, hasFullLoopOnly, hasUIApproval, hasUnsupported, "workdir"))

	sessionOK := !hasFullLoopOnly && !hasUIApproval && !hasUnsupported
	fmt.Fprintf(&b, "  %s stado tool run --session <id>%s        %s\n",
		mark(sessionOK),
		spaceForWorkdir(hasWorkdir),
		surfaceReason(sessionOK, false, false, hasFullLoopOnly, hasUIApproval, hasUnsupported, "session"))

	b.WriteString("\nSuggested invocation:\n  ")
	b.WriteString(suggestInvocation(mf, hasWorkdir, hasSession, hasFullLoopOnly, hasUIApproval, hasUnsupported))
	b.WriteString("\n")
	return b.String(), nil
}

func lifecycleSurfaceReason(ok bool) string {
	if ok {
		return "complete persistent lifecycle application contract"
	}
	return "persistent lifecycle applications are not hosted"
}

func spaceForWorkdir(hasWorkdir bool) string {
	if hasWorkdir {
		return " --workdir=$PWD"
	}
	return ""
}

func surfaceReason(ok bool, hasWorkdir, hasSession, hasFullLoopOnly, hasUIApproval, hasUnsupported bool, surface string) string {
	if ok {
		switch surface {
		case "full":
			return "full agent loop — satisfies every supported capability above"
		case "plain":
			return "no flag-gated capabilities"
		case "workdir":
			if hasWorkdir {
				return "satisfies the workdir-rooted fs capability"
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
	if hasUnsupported {
		why = append(why, "manifest contains a removed or unsupported capability")
	}
	if hasWorkdir && surface == "plain" {
		why = append(why, "needs --workdir")
	}
	if hasSession && (surface == "plain" || surface == "workdir") {
		why = append(why, "needs --session")
	}
	if hasFullLoopOnly {
		why = append(why, "exec:bash (or similar) refused by all `tool run` paths — use TUI / `stado run`")
	}
	if hasUIApproval {
		why = append(why, "ui:approval needs the agent loop's approval bridge — TUI / `stado run`")
	}
	return strings.Join(why, "; ")
}

func suggestInvocation(mf *plugins.Manifest, hasWorkdir, hasSession, hasFullLoopOnly, hasUIApproval, hasUnsupported bool) string {
	if hasUnsupported {
		return "Fix the removed or unsupported manifest capability before running this plugin."
	}
	if hasFullLoopOnly || hasUIApproval {
		return "Use the TUI / `stado run`. Plugins with `exec:bash` or `ui:approval` cannot run from `tool run`."
	}
	tool := "<tool>"
	if len(mf.Tools) == 1 {
		tool = mf.Tools[0].Name
	}
	flags := ""
	if hasWorkdir {
		flags += " --workdir=$PWD"
	}
	if hasSession {
		flags += " --session <id>"
	}
	return fmt.Sprintf("stado tool run%s %s '<json-args>'", flags, tool)
}
