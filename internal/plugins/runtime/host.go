package runtime

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
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
	NetHTTPGet     bool
	NetHTTPRequest bool       // gates stado_http_request (POST/PUT/DELETE/PATCH/HEAD/GET)
	NetReqHost     []string   // optional hostname allow-list for net:http_request:<host>
	ExecBash    bool
	ExecSearch  bool
	ExecASTGrep bool
	ExecPTY     bool
	LSPQuery    bool
	UIApproval  bool

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

	// llmTokensUsed tracks the per-session running total against
	// LLMInvokeBudget. Updated atomically inside the stado_llm_invoke
	// import so concurrent plugin calls don't race past the ceiling.
	llmTokensUsed int64
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
			// "net:<host>" — the cap string after "net:" is the
			// exact host to allow. Special values "allow" / "deny" are
			// handled at the MCP layer but aren't exposed to plugins
			// here (too much power).
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
		}
	}
	return h
}

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
