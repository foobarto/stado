package runtime

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/secrets"
	"github.com/foobarto/stado/pkg/tool"
)

// Host is the capability-gated bridge exposed to a plugin's wasm
// module. It owns the sandbox policy (derived from the manifest) and
// the slog.Logger used by stado_log.
//
// One Host per plugin instantiation — the capability lists in it are
// the manifest's, not the process's. Instantiate() builds + registers
// an instance of this type on the runtime before the wasm module runs.
type Host struct {
	Manifest plugins.Manifest
	Logger   *slog.Logger

	// Parsed from Manifest.Capabilities. These are the authoritative
	// allow-lists host-import calls check against. Empty slices mean
	// "deny all" — matches the strict default of plugin execution.
	FSRead  []string
	FSWrite []string
	NetHost []string // optional hostname allow-list for net:http_get
	Workdir string   // CWD the plugin sees for relative paths

	// Session/LLM capability gates — Phase 7.1b (PR K2). DESIGN
	// §"Plugin extension points for context management".
	//
	// SessionObserve gates stado_session_next_event (polling variant
	// of stado_session_observe — wasm-native, no callback refs).
	// SessionRead gates stado_session_read.
	// SessionFork gates stado_session_fork.
	// LLMInvokeBudget gates stado_llm_invoke; 0 = not permitted,
	// positive = per-session token budget ceiling. Default when
	// "llm:invoke" is declared without a suffix: 10000.
	SessionObserve  bool
	SessionRead     bool
	SessionFork     bool
	LLMInvokeBudget int
	MemoryPropose   bool
	MemoryRead      bool
	MemoryWrite     bool

	// SessionBridge wires the host-side session operations (read
	// history, fork, LLM invoke, subscribe-to-events). Nil when the
	// caller doesn't have a live session — in that case the gated
	// host imports return -1 with a diagnostic in the log. Exposed as
	// an interface so TUI / headless / tests can plug in different
	// backings.
	SessionBridge SessionBridge

	// MemoryBridge wires capability-gated memory operations for
	// plugin-backed long-lived facts. Nil means memory is unavailable
	// in this run context; host imports return -1 rather than falling
	// back to ambient storage.
	MemoryBridge MemoryBridge

	// FleetBridge wires the agent fleet operations backing the bundled
	// agent plugin's stado_agent_* host imports (EP-0038 §D Tier 1+).
	// Only the bundled agent plugin may declare agent:fleet cap; user
	// plugins are blocked at install time.
	// Nil on surfaces without a live runtime fleet (e.g. plugin run).
	FleetBridge FleetBridge

	// ApprovalBridge powers explicit plugin-requested human approval
	// prompts. Unlike the old global tool gate, this is opt-in per
	// plugin capability and may be nil on non-interactive surfaces.
	ApprovalBridge ApprovalBridge

	// ToolHost is the runtime host surface native tool wrappers call
	// through when a plugin uses the public built-in tool imports.
	// Nil is valid in non-session contexts like `stado plugin run`;
	// imports that require it return an error payload.
	ToolHost tool.Host

	// Public built-in tool capability bits. These map thin host
	// wrappers to the underlying native implementation while keeping
	// manifests narrow and auditable.
	NetHTTPGet            bool
	NetHTTPRequest        bool     // gates stado_http_request (POST/PUT/DELETE/PATCH/HEAD/GET)
	NetReqHost            []string // optional hostname allow-list for net:http_request:<host>
	NetHTTPRequestPrivate bool     // when true, stado_http_request's dial guard allows RFC1918 / loopback / link-local destinations. Off by default — opt-in via net:http_request_private cap.
	// NetHTTPClient gates stado_http_client_create / _close / _request (EP-0038e Tier 2).
	// A stateful client with cookie jar and redirect policy, distinct from the one-shot
	// stado_http_request import. Declared via net:http_client in the manifest.
	// The operator's NetReqHost allowlist applies as an outer bound: even when
	// opts.AllowedHosts is empty (allow-all), the client can only reach hosts
	// the operator approved via net:http_request:<host>.
	NetHTTPClient bool
	ExecBash    bool
	ExecSearch  bool
	ExecASTGrep bool
	ExecPTY     bool
	// ExecProc gates stado_proc_* and stado_exec (EP-0038 §B Tier 1).
	// ExecProcGlobs, when non-empty, restricts to any of the listed
	// exec:proc:<glob> patterns. An empty list means broad exec:proc.
	ExecProc      bool
	ExecProcGlobs []string
	// AgentFleet gates stado_agent_* (EP-0038 §D Tier 1+).
	// Only bundled agent plugin may declare this cap.
	AgentFleet bool
	// BundledBin gates stado_bundled_bin (EP-0038 §B Tier 1).
	BundledBin bool
	// DNSResolve / DNSReverse gate stado_dns_resolve / stado_dns_resolve_axfr (Tier 2).
	DNSResolve bool
	DNSReverse bool
	// CryptoHash gates stado_hash and stado_hmac (EP-0038 §B Tier 3).
	CryptoHash bool
	// Compress gates stado_compress / stado_decompress (Tier 3).
	Compress bool
	LSPQuery   bool
	UIApproval bool

	// PTYManager is the runtime-shared registry of PTY-backed
	// processes; survives plugin instantiation freshness so a session
	// created in one tool call can be driven from later calls.
	// Wired by the runtime when ExecPTY is granted; nil otherwise.
	PTYManager *pty.Manager
	// CfgStateDir is set by the `cfg:state_dir` capability and gates
	// the `stado_cfg_state_dir` host import. EP-0029. Operator-tooling
	// plugins (doctor, gc, info — currently in core, candidates for
	// migration) need to learn the install dir at
	// `<state-dir>/plugins/`; without this, those tools cannot exist
	// outside core. The capability is read-only; combined with
	// `fs:read:<returned-path>` it lets a plugin enumerate other
	// installed plugins. Operator opts in by trusting the signer.
	CfgStateDir bool

	// StateDir is the actual path returned to the plugin via
	// `stado_cfg_state_dir` when CfgStateDir is true. Populated by
	// the host caller (cmd/stado/plugin_run.go from cfg.StateDir(),
	// the bundled-tool wrappers from their runtime context) before
	// InstallHostImports. Empty string is valid; the host import
	// returns "" to the plugin and the plugin can fall back to
	// whatever degraded path it has.
	StateDir string

	// Secrets gates stado_secrets_* host imports. Populated when the
	// plugin manifest declares secrets:read[:<glob>] or
	// secrets:write[:<glob>]. Nil when neither is granted.
	Secrets *SecretsAccess

	// State gates stado_instance_* host imports — process-lifetime KV
	// store with per-plugin namespacing. Populated when the manifest
	// declares state:read[:<glob>] or state:write[:<glob>]. Nil when
	// neither is granted. The Store itself is per-Runtime; this struct
	// just records the manifest's allowed key patterns + plugin name.
	State *StateAccess

	// ToolInvoke gates stado_tool_invoke — wasm plugins calling other
	// registered tools. Populated when the manifest declares
	// tool:invoke[:<name-glob>]. The Invoke callback is wired by the
	// host caller (BuildExecutor / runPluginInvocation) to dispatch
	// against the active session's registry, with recursion bounded
	// by toolInvokeMaxDepth. Tester #3.
	ToolInvoke *ToolInvokeAccess

	// NetDial gates stado_net_dial / read / write / close (Tier 1
	// raw socket primitives). Populated when the manifest declares
	// net:dial:{tcp,udp,unix}:<host-or-path>:<port>. EP-0038g extends
	// the v0.36.0 TCP-only surface with UDP + Unix dial. ICMP still
	// deferred.
	NetDial *NetDialAccess

	// NetListen gates stado_net_listen / accept / close_listener.
	// Populated when the manifest declares net:listen:{tcp,unix}:*.
	// EP-0038g.
	NetListen *NetListenAccess

	// Progress is the operator-visible callback for stado_progress
	// emissions. Wired by the host caller (TUI, headless run, plugin
	// invoke shell). When nil the import drops silently — the plugin
	// shouldn't fail because the operator surface isn't connected.
	// EP-0038h. NOT for agent-loop integration; the model only sees
	// the final tool result.
	Progress func(plugin, text string)

	// llmTokensUsed tracks the per-session running total against
	// LLMInvokeBudget. Updated atomically inside the stado_llm_invoke
	// import so concurrent plugin calls don't race past the ceiling.
	llmTokensUsed int64
}

// SecretsAccess holds the capability gates and backing store for the
// stado_secrets_* host imports. Constructed lazily by NewHost when the
// manifest declares at least one secrets:* capability.
type SecretsAccess struct {
	Store        *secrets.Store
	ReadGlobs    []string // patterns from secrets:read:<glob>; empty = broad match-all
	WriteGlobs   []string // patterns from secrets:write:<glob>; empty = broad match-all
	AuditEmitter func(SecretsAuditEvent) // optional; nil = no-op
	PluginName   string                  // manifest.Name; included in audit events
}

// SecretsAuditEvent is the structured record emitted for every
// stado_secrets_* host-import call, whether allowed or denied.
// Values are never populated with the secret's value — only its name.
type SecretsAuditEvent struct {
	Plugin  string
	Op      string // "get" | "put" | "list" | "remove"
	Secret  string // empty for list
	Allowed bool
	Reason  string // populated when !Allowed
}

// CanRead reports whether the named secret is reachable under the
// declared secrets:read[:<glob>] capabilities. Empty ReadGlobs means
// broad (match-all). Uses filepath.Match for shell-glob semantics.
func (s *SecretsAccess) CanRead(name string) bool {
	if len(s.ReadGlobs) == 0 {
		return true
	}
	for _, g := range s.ReadGlobs {
		if matched, _ := filepath.Match(g, name); matched {
			return true
		}
	}
	return false
}

// CanWrite reports whether the named secret is writable under the
// declared secrets:write[:<glob>] capabilities. Empty WriteGlobs means
// broad (match-all).
func (s *SecretsAccess) CanWrite(name string) bool {
	if len(s.WriteGlobs) == 0 {
		return true
	}
	for _, g := range s.WriteGlobs {
		if matched, _ := filepath.Match(g, name); matched {
			return true
		}
	}
	return false
}

// CanList reports whether the plugin may call stado_secrets_list.
// Requires either broad read (empty ReadGlobs) or a pattern that
// matches "*" (covering all names).
func (s *SecretsAccess) CanList() bool {
	if len(s.ReadGlobs) == 0 {
		return true // broad read
	}
	for _, g := range s.ReadGlobs {
		if g == "*" {
			return true
		}
	}
	return false
}

// Audit calls the AuditEmitter if one is wired; otherwise it's a no-op.
func (s *SecretsAccess) Audit(ev SecretsAuditEvent) {
	if s.AuditEmitter != nil {
		s.AuditEmitter(ev)
	}
}

// SessionBridge is the capability-checked surface plugin code calls
// through. Every method corresponds to one host import gated by a
// matching `session:*` or `llm:*` capability in the plugin manifest.
// A nil SessionBridge is valid — it means the runtime doesn't have a
// session (e.g. `stado plugin run` outside a session context); the
// host imports return -1 with a diagnostic.
type SessionBridge interface {
	// NextEvent blocks until the next session event (or ctx deadline)
	// and returns an opaque JSON payload the plugin can parse. Empty
	// payload = no events yet (plugin should back off).
	NextEvent(ctx context.Context) ([]byte, error)
	// ReadField returns the current value of a named session field.
	// Supported names are spec-defined (see DESIGN): "message_count",
	// "token_count", "session_id", "last_turn_ref", "history".
	ReadField(name string) ([]byte, error)
	// Fork creates a new session rooted at atTurnRef with seedMessage
	// as its first user turn. Returns the new session ID.
	Fork(ctx context.Context, atTurnRef, seedMessage string) (sessionID string, err error)
	// InvokeLLM runs a one-shot completion against the active
	// provider with the given prompt, returning the aggregated reply
	// text and the number of tokens consumed (used to enforce the
	// per-session budget).
	InvokeLLM(ctx context.Context, prompt string) (reply string, tokensUsed int, err error)
}

// MemoryBridge is the capability-checked persistent-memory surface
// exposed to plugins that declare `memory:*` capabilities.
type MemoryBridge interface {
	Propose(ctx context.Context, payload []byte) error
	Query(ctx context.Context, payload []byte) ([]byte, error)
	Update(ctx context.Context, payload []byte) error
}

// ApprovalBridge is the interactive UI hook plugins can call when they
// explicitly want a human decision. Surfaces without a user-facing UI may
// leave it nil; the host import returns -1 in that case so the plugin can
// decide how to proceed.
type ApprovalBridge interface {
	RequestApproval(ctx context.Context, title, body string) (bool, error)
}

// FleetBridge is the capability-checked surface the bundled agent plugin
// calls through for stado_agent_* operations. EP-0038 §D Tier 1+.
// Nil on surfaces without a live runtime fleet.
type FleetBridge interface {
	// AgentSpawn starts a new child agent. Returns (agentID, sessionID).
	AgentSpawn(ctx context.Context, req AgentSpawnRequest) (AgentSpawnResult, error)
	// AgentList returns all agents in the caller's spawn tree.
	AgentList(ctx context.Context) ([]AgentListEntry, error)
	// AgentReadMessages drains the inbox for the given agent since offset.
	// Blocks up to timeoutMs milliseconds (0 = no wait).
	AgentReadMessages(ctx context.Context, id string, since int, timeoutMs int) (AgentMessages, error)
	// AgentSendMessage injects a user-role message into the agent's session.
	AgentSendMessage(ctx context.Context, id, msg string) error
	// AgentCancel requests cancellation of the given agent.
	AgentCancel(ctx context.Context, id string) error
}

// AgentSpawnRequest is the input to FleetBridge.AgentSpawn.
type AgentSpawnRequest struct {
	Prompt        string
	Model         string
	Async         bool
	Ephemeral     bool
	ParentSession string // empty = use caller's session
	AllowedTools  []string
	SandboxProfile string
}

// AgentSpawnResult is the output of FleetBridge.AgentSpawn.
type AgentSpawnResult struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	// FinalText is populated when Async=false and the agent completed.
	FinalText string `json:"final_text,omitempty"`
}

// AgentListEntry is one entry from FleetBridge.AgentList.
type AgentListEntry struct {
	ID          string  `json:"id"`
	SessionID   string  `json:"session_id"`
	Status      string  `json:"status"`
	Model       string  `json:"model"`
	StartedAt   string  `json:"started_at"`
	LastTurnAt  string  `json:"last_turn_at,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
}

// AgentMessages is the result of FleetBridge.AgentReadMessages.
type AgentMessages struct {
	Messages []AgentMessage `json:"messages"`
	Offset   int            `json:"offset"`
	Status   string         `json:"status"`
}

// AgentMessage is one item in AgentMessages.
type AgentMessage struct {
	Role    string          `json:"role"`             // "assistant" or "external_input"
	Content string          `json:"content,omitempty"`
	Source  string          `json:"source,omitempty"` // for external_input events
	Offset  int             `json:"offset,omitempty"`
	Summary string          `json:"summary,omitempty"`
}

// NewHost parses a manifest's capabilities into a Host.
func NewHost(m plugins.Manifest, workdir string, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Host{
		Manifest: m,
		Logger:   logger.With("plugin", m.Name),
		Workdir:  workdir,
	}
	for _, cap := range m.Capabilities {
		parts := strings.SplitN(cap, ":", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "fs":
			if len(parts) != 3 {
				continue
			}
			path := parts[2]
			var scope string
			if strings.HasPrefix(path, "cfg:") {
				// Path-template prefix; resolution is deferred to
				// allowRead/allowWrite because the host caller may
				// populate the cfg field (h.StateDir, etc.) AFTER
				// NewHost. Stored as-is; expansion happens at the
				// allow-list check via h.expandFSEntry. EP-0029
				// §"Future capabilities".
				scope = path
			} else {
				scope = normaliseCapabilityPath(workdir, path)
			}
			switch parts[1] {
			case "read":
				h.FSRead = append(h.FSRead, scope)
			case "write":
				h.FSWrite = append(h.FSWrite, scope)
			}
		case "net":
			if len(parts) == 2 && parts[1] == "http_get" {
				h.NetHTTPGet = true
				continue
			}
			// "net:http_request" — broad: any (public) host.
			// "net:http_request:<host>" — narrow: gates by exact
			// hostname. Any number of host entries can be appended.
			if parts[1] == "http_request" {
				h.NetHTTPRequest = true
				if len(parts) == 3 && parts[2] != "" {
					h.NetReqHost = append(h.NetReqHost, strings.ToLower(parts[2]))
				}
				continue
			}
			// "net:http_request_private" — when granted, the
			// dial guard for stado_http_request allows private
			// addresses (RFC1918, loopback, link-local). Implies
			// NetHTTPRequest.
			if parts[1] == "http_request_private" {
				h.NetHTTPRequest = true
				h.NetHTTPRequestPrivate = true
				continue
			}
			// "net:http_client" — stateful HTTP client with cookie jar.
			// Host allowlist still bounds reachable hosts (see NetReqHost).
			if parts[1] == "http_client" {
				h.NetHTTPClient = true
				continue
			}
			// "net:<host>" — the cap string after "net:" is the
			// exact host to allow. Special values "allow" / "deny" are
			// handled at the MCP layer but aren't exposed to plugins
			// here (too much power). "net:dial:..." / "net:listen:..."
			// are parsed by parseNetSocketCap below — don't junk-
			// populate NetHost with their multi-segment payloads.
			if parts[1] == "dial" || parts[1] == "listen" {
				break // out of the switch; parser block below handles it
			}
			rest := strings.Join(parts[1:], ":")
			if rest != "" && rest != "allow" && rest != "deny" {
				h.NetHost = append(h.NetHost, rest)
			}
		case "session":
			// DESIGN §"Plugin extension points for context management":
			// session:observe / session:read / session:fork.
			switch parts[1] {
			case "observe":
				h.SessionObserve = true
			case "read":
				h.SessionRead = true
			case "fork":
				h.SessionFork = true
			}
		case "llm":
			// llm:invoke or llm:invoke:<budget>. Default budget when
			// the suffix is omitted is 10000 — conservative ceiling
			// that forces explicit uplift for bigger workloads.
			if parts[1] != "invoke" {
				continue
			}
			budget := 10000
			if len(parts) == 3 && parts[2] != "" {
				if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
					budget = n
				}
			}
			h.LLMInvokeBudget = budget
		case "memory":
			switch parts[1] {
			case "propose":
				h.MemoryPropose = true
			case "read":
				h.MemoryRead = true
			case "write":
				h.MemoryWrite = true
			}
		case "terminal":
			// EP-0038: terminal:open is the new name for exec:pty.
			if parts[1] == "open" {
				h.ExecPTY = true
			}
		case "exec":
			switch parts[1] {
			case "shallow_bash", "bash":
				h.ExecBash = true
			case "search":
				h.ExecSearch = true
			case "ast_grep":
				h.ExecASTGrep = true
			case "pty":
				h.ExecPTY = true
			case "proc":
				h.ExecProc = true
				if len(parts) == 3 && parts[2] != "" {
					h.ExecProcGlobs = append(h.ExecProcGlobs, parts[2])
				}
			}
		case "bundled-bin":
			h.BundledBin = true
		case "dns":
			switch parts[1] {
			case "resolve":
				h.DNSResolve = true
			case "axfr":
				h.DNSResolve = true // axfr implies resolve
			case "reverse":
				h.DNSReverse = true
			}
		case "crypto":
			if parts[1] == "hash" {
				h.CryptoHash = true
			}
		case "compress":
			h.Compress = true
		case "agent":
			if parts[1] == "fleet" {
				h.AgentFleet = true
			}
		case "lsp":
			if parts[1] == "query" {
				h.LSPQuery = true
			}
		case "ui":
			if parts[1] == "approval" {
				h.UIApproval = true
			}
		case "cfg":
			// Read-only configuration introspection. EP-0029. Each
			// `cfg:<name>` entry maps to one Host bool that gates one
			// host import returning the named string.
			if parts[1] == "state_dir" {
				h.CfgStateDir = true
			}
		case "secrets":
			// secrets:read[:<glob>] and secrets:write[:<glob>].
			// Store/AuditEmitter are populated by the host caller after
			// NewHost returns; we only record the glob patterns here.
			switch parts[1] {
			case "read":
				if h.Secrets == nil {
					h.Secrets = &SecretsAccess{}
				}
				if len(parts) == 3 && parts[2] != "" {
					h.Secrets.ReadGlobs = append(h.Secrets.ReadGlobs, parts[2])
				}
				// len(parts) == 2 → broad read; ReadGlobs stays empty (match-all)
			case "write":
				if h.Secrets == nil {
					h.Secrets = &SecretsAccess{}
				}
				if len(parts) == 3 && parts[2] != "" {
					h.Secrets.WriteGlobs = append(h.Secrets.WriteGlobs, parts[2])
				}
				// len(parts) == 2 → broad write; WriteGlobs stays empty (match-all)
			}
		case "state":
			// state:read[:<glob>] and state:write[:<glob>] gate the
			// stado_instance_* host imports (process-lifetime KV store).
			// Same glob shape as secrets. The Store itself is per-Runtime;
			// this just records the manifest's allowed key patterns.
			switch parts[1] {
			case "read":
				if h.State == nil {
					h.State = &StateAccess{}
				}
				if len(parts) == 3 && parts[2] != "" {
					h.State.ReadGlobs = append(h.State.ReadGlobs, parts[2])
				}
			case "write":
				if h.State == nil {
					h.State = &StateAccess{}
				}
				if len(parts) == 3 && parts[2] != "" {
					h.State.WriteGlobs = append(h.State.WriteGlobs, parts[2])
				}
			}
		case "tool":
			// tool:invoke[:<name-glob>] — wasm plugins calling other
			// registered tools via stado_tool_invoke. Glob-shape
			// matches secrets + state. The Invoke callback is wired
			// by the host caller after NewHost returns.
			if parts[1] == "invoke" {
				if h.ToolInvoke == nil {
					h.ToolInvoke = &ToolInvokeAccess{}
				}
				if len(parts) == 3 && parts[2] != "" {
					h.ToolInvoke.AllowedGlobs = append(h.ToolInvoke.AllowedGlobs, parts[2])
				}
			}
		}
		// "net:dial:<transport>:..." and "net:listen:<transport>:..."
		// have more colon-separated segments than the SplitN(_, _, 3)
		// shape above can express. Re-split the raw cap string for
		// these two prefixes only.
		//
		//   net:dial:tcp:<host>:<port>   — 5 parts (EP-0038f)
		//   net:dial:udp:<host>:<port>   — 5 parts (EP-0038g)
		//   net:dial:unix:<path-glob>    — 4+ parts; path may contain
		//                                   colons → re-join from full[3:]
		//   net:listen:tcp:<host>:<port> — 5 parts (EP-0038g)
		//   net:listen:unix:<path-glob>  — 4+ parts (EP-0038g)
		if parts[0] == "net" && (parts[1] == "dial" || parts[1] == "listen") {
			full := strings.Split(cap, ":")
			h.parseNetSocketCap(full)
		}
	}
	return h
}

// parseNetSocketCap absorbs net:dial:* and net:listen:* capabilities,
// populating NetDial / NetListen as needed. `full` is the cap split
// on every colon (no SplitN limit) so transport-specific suffixes
// stay intact.
func (h *Host) parseNetSocketCap(full []string) {
	if len(full) < 4 {
		return
	}
	mode := full[1]      // "dial" | "listen"
	transport := full[2] // "tcp" | "udp" | "unix"
	switch transport {
	case "tcp", "udp":
		if len(full) < 5 {
			return
		}
		host, port := full[3], full[4]
		pat := NetDialPattern{Host: host, Port: port}
		switch mode {
		case "dial":
			if h.NetDial == nil {
				h.NetDial = &NetDialAccess{}
			}
			if transport == "tcp" {
				h.NetDial.TCPGlobs = append(h.NetDial.TCPGlobs, pat)
			} else {
				h.NetDial.UDPGlobs = append(h.NetDial.UDPGlobs, pat)
			}
		case "listen":
			if h.NetListen == nil {
				h.NetListen = &NetListenAccess{}
			}
			if transport == "tcp" {
				h.NetListen.TCPGlobs = append(h.NetListen.TCPGlobs, pat)
			} else {
				h.NetListen.UDPGlobs = append(h.NetListen.UDPGlobs, pat)
			}
		}
	case "unix":
		path := strings.Join(full[3:], ":")
		if path == "" {
			return
		}
		switch mode {
		case "dial":
			if h.NetDial == nil {
				h.NetDial = &NetDialAccess{}
			}
			h.NetDial.UnixGlobs = append(h.NetDial.UnixGlobs, path)
		case "listen":
			if h.NetListen == nil {
				h.NetListen = &NetListenAccess{}
			}
			h.NetListen.UnixGlobs = append(h.NetListen.UnixGlobs, path)
		}
	}
}

// AllowPrivateNetwork implements tool.HostNetworkPolicy. Returns
// true when the manifest declared `net:http_request_private`. The
// http_request tool probes for this via type assertion to decide
// whether to use the strict public-only dial guard or the loosened
// variant.
func (h *Host) AllowPrivateNetwork() bool { return h.NetHTTPRequestPrivate }

func (h *Host) NeedsMemoryBridge() bool {
	return h.MemoryPropose || h.MemoryRead || h.MemoryWrite
}

// allowRead / allowWrite perform the capability check. Current
// matching is prefix-based on absolute paths — a manifest entry of
// `/home/user/projects` allows any file under that tree. Glob support
// can be added later if real plugins ask for it.
func (h *Host) allowRead(abs string) bool  { return h.pathAllowedExpanded(abs, h.FSRead) }
func (h *Host) allowWrite(abs string) bool { return h.pathAllowedExpanded(abs, h.FSWrite) }

// pathAllowedExpanded is pathAllowed plus on-the-fly expansion of
// cfg:* path-template entries against h's populated cfg fields.
// Entries that fail to expand (cfg cap not declared, value not
// populated, unknown cfg name) are silently filtered — the plugin
// sees the same denied result as if the entry weren't in the
// allow-list. EP-0029 §"Future capabilities".
func (h *Host) pathAllowedExpanded(abs string, allow []string) bool {
	for _, a := range allow {
		expanded := h.expandFSEntry(a)
		if expanded == "" {
			continue
		}
		if expanded == abs || strings.HasPrefix(abs, strings.TrimRight(expanded, "/")+"/") {
			return true
		}
	}
	return false
}

// expandFSEntry resolves a `cfg:<name>[/<sub-path>]` path-template
// entry against h's populated cfg fields. Entries without the cfg:
// prefix are returned as-is. Returns "" when expansion isn't
// possible (cap not declared, value empty, unknown name) — the
// caller treats that as "no match".
//
// Supported names (extends as new cfg:* capabilities ship):
//   - state_dir → h.StateDir (requires cfg:state_dir cap)
func (h *Host) expandFSEntry(raw string) string {
	if !strings.HasPrefix(raw, "cfg:") {
		return raw
	}
	rest := raw[len("cfg:"):]
	name, sub, _ := strings.Cut(rest, "/")
	var value string
	switch name {
	case "state_dir":
		if !h.CfgStateDir {
			return ""
		}
		value = h.StateDir
	default:
		return ""
	}
	if value == "" {
		return ""
	}
	if sub == "" {
		return value
	}
	return filepath.Clean(value + "/" + sub)
}

func pathAllowed(abs string, allow []string) bool {
	for _, a := range allow {
		if a == abs || strings.HasPrefix(abs, strings.TrimRight(a, "/")+"/") {
			return true
		}
	}
	return false
}

func normaliseCapabilityPath(workdir, path string) string {
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return resolveAbs(workdir, path)
}
