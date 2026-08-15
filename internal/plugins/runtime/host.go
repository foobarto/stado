package runtime

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/secrets"
	"github.com/foobarto/stado/pkg/tool"
)

// Host is the capability-gated bridge exposed to a plugin's wasm
// module. It owns the sandbox policy (derived from the manifest) and
// the slog.Logger used by stado_log.
//
// One Host per tool invocation — the capability lists in it are the selected
// signed tool's effective subset of its package manifest, not the process's.
// Module identity and instantiation remain bound to the complete manifest.
type Host struct {
	Manifest plugins.Manifest
	// Identity is derived by the native loader from the installed, bundled, or
	// explicit local plugin source. Guest JSON and Manifest.Name never supply
	// this value. Artifact kinds and authenticated broker calls depend on it.
	Identity plugins.RuntimeIdentity
	Logger   *slog.Logger

	// Parsed from Manifest.Capabilities. These are the authoritative
	// allow-lists host-import calls check against. Empty slices mean
	// "deny all" — matches the strict default of plugin execution.
	FSRead  []string
	FSWrite []string
	Workdir string // CWD the plugin sees for relative paths

	// Session/provider capability gates. Session imports expose generic session
	// observations and transitions. ProviderInvokeBudget gates the generic
	// provider primitive; a mandatory positive suffix is the signed per-instance
	// cumulative token ceiling.
	//
	// SessionObserve gates stado_session_next_event (polling variant
	// of stado_session_observe — wasm-native, no callback refs).
	// SessionRead gates stado_session_read.
	// SessionFork gates stado_session_fork.
	SessionObserve       bool
	SessionRead          bool
	SessionFork          bool
	ProviderInvokeBudget int
	ArtifactPropose      []string
	ArtifactRead         []string
	ArtifactEdit         []string
	ArtifactObserve      []string
	EvidenceCatalog      map[string]bool
	EvidenceSearch       map[string]bool
	EvidenceOpen         map[string]bool
	EvidenceValidate     bool

	// SessionBridge wires host-side session observations/transitions and may
	// borrow a live provider for the separately gated provider primitive. Nil when the
	// caller doesn't have a live session — in that case the gated
	// host imports return -1 with a diagnostic in the log. Exposed as
	// an interface so TUI / headless / tests can plug in different
	// backings.
	SessionBridge SessionBridge

	// ArtifactBridge wires capability-gated, authenticated artifact calls to
	// the broker. Nil means artifacts are unavailable in this run context;
	// imports fail closed and never fall back to cfg:state_dir, a direct WAL,
	// or the legacy memory JSONL store (EP-0063).
	ArtifactBridge ArtifactBridge
	ArtifactCaller ArtifactCallerContext

	// EvidenceBridge is a broker-authenticated, read-only corpus surface. The
	// opaque admission binding never enters guest memory; request JSON cannot
	// choose a principal, repository, source session, ancestry, or plugin
	// namespace. It backs ordinary WASM tools used inside broker-created
	// read-only children and mechanical citation validation in their parent.
	EvidenceBridge EvidenceBridge

	// ApplicationBridge is the broker-owned EP-0064 control plane for this
	// exact plugin/session/generation admission. The bridge retains the opaque
	// binding outside WASM memory; imports expose only typed, capability-gated
	// journal, projection, scheduling, control, and timer operations.
	ApplicationBridge ApplicationBridge
	// ApplicationAnchor is the broker-authenticated lifecycle scope used by
	// generic host primitives that need retry identity in addition to a
	// capability check. It is never populated from guest JSON. Ordinary
	// one-shot plugin tools leave it empty.
	ApplicationAnchor ApplicationAnchor

	// FleetBridge wires retained-agent primitives backing stado_agent_*.
	// EP-0064 splits the historical agent:fleet aggregate into operation-
	// scoped agent:{spawn,list,read,send,cancel} capabilities so installed
	// lifecycle applications can receive only the authority they need.
	// Nil on surfaces without a live runtime fleet (e.g. tool run).
	FleetBridge FleetBridge

	// ApprovalBridge powers explicit plugin-requested human approval
	// prompts. Unlike the old global tool gate, this is opt-in per
	// plugin capability and may be nil on non-interactive surfaces.
	ApprovalBridge ApprovalBridge

	// ChoiceBridge powers stado_ui_choose — operator-facing single /
	// multi-choice prompts. Same nil-means-unavailable contract as
	// ApprovalBridge; gated by ui:choice cap. Q3 (2026-05-07).
	ChoiceBridge ChoiceBridge

	// PrintBridge powers stado_ui_print — plain-text fire-and-forget
	// emit into the operator's view. Same nil-means-unavailable
	// contract as ApprovalBridge; gated by ui:print cap. Drop-on-floor
	// when nil so plugins on non-interactive surfaces don't block
	// (fire-and-forget by EP-0064 and the host-import reference).
	PrintBridge PrintBridge

	// RenderBridge powers stado_ui_render — structured panel emit
	// across TUI / ACP / MCP / headless. Same nil-means-unavailable
	// contract as PrintBridge: a nil bridge here yields silent
	// success so plugins on disconnected channels stay non-blocking
	// (fire-and-forget by EP-0064 and the host-import reference).
	RenderBridge RenderBridge

	// ToolHost is the runtime host surface native tool wrappers call
	// through when a plugin uses the public built-in tool imports.
	// Nil is valid in non-session contexts like `stado tool run`;
	// imports that require it return an error payload.
	ToolHost tool.Host

	// DefaultSandboxPolicy is the sandbox policy applied to stado_exec
	// / stado_proc_spawn calls when the wasm guest doesn't supply its
	// own. Nil = legacy "guest opt-in only" posture (the wasm shell
	// runs unsandboxed). AttachToolHost copies it from the caller's
	// tool.SandboxPolicyProvider so the process imports and Executor.Run
	// share one surface decision.
	//
	// Type is *sandboxPolicy (defined in host_proc.go); kept as `any`
	// here to avoid a forward reference in this file's import block.
	// buildSandboxedCmd does the type assertion on the way through.
	DefaultSandboxPolicy any

	// Public built-in tool capability bits. These map thin host
	// wrappers to the underlying native implementation while keeping
	// manifests narrow and auditable.
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
	// EP-0028: ExecBash / ExecSearch / ExecASTGrep removed — their capabilities
	// (exec:bash / exec:search / exec:ast_grep) were dropped in
	// EP-no-internal-tools Step 4, so the fields could never be set and every
	// reader was dead. See the "exec" case in capability parsing below.
	ExecPTY bool
	// ExecPTYGlobs, when non-empty, restricts stado_pty_create to binaries
	// matching one of the exec:pty:<glob> patterns —
	// the PTY analogue of ExecProcGlobs. An empty list means broad exec:pty.
	// Without this the PTY path ran any binary (or /bin/sh via opts.Cmd)
	// regardless of the exec:proc glob, bypassing the cap-confinement layer.
	ExecPTYGlobs []string
	// ExecProc gates stado_proc_* and stado_exec (EP-0038 §B Tier 1).
	// ExecProcGlobs, when non-empty, restricts to any of the listed
	// exec:proc:<glob> patterns. An empty list means broad exec:proc.
	ExecProc      bool
	ExecProcGlobs []string
	// BundledBin gates stado_bundled_bin (EP-0038 §B Tier 1).
	BundledBin bool
	// DNSResolve / DNSReverse gate stado_dns_resolve / stado_dns_resolve_axfr (Tier 2).
	DNSResolve bool
	DNSReverse bool
	// DNSAXFR gates stado_dns_resolve_axfr (zone transfer over TCP).
	// Implies DNSResolve. Declared via dns:axfr in the manifest.
	DNSAXFR bool

	// DNSAXFRPrivate loosens the AXFR dial guard so RFC1918 / loopback /
	// link-local destinations are reachable. Declared via dns:axfr_private
	// in the manifest. Without it, AXFR refuses to dial private addresses
	// even when the underlying dns:axfr capability is held — same posture
	// as net:http_request vs net:http_request_private.
	DNSAXFRPrivate bool
	// DNSResolvePrivate loosens the stado_dns_resolve custom-server guard so a
	// plugin may direct queries at RFC1918 / loopback resolvers. Declared via
	// dns:resolve_private. Without it, a custom server resolving to a private
	// address is refused (else dns:resolve alone could query the host's
	// internal / split-horizon resolver). Mirrors DNSAXFRPrivate for AXFR.
	DNSResolvePrivate bool
	// CryptoHash gates stado_hash and stado_hmac (EP-0038 §B Tier 3).
	CryptoHash bool
	// Compress gates stado_compress / stado_decompress (Tier 3).
	Compress   bool
	LSPQuery   bool
	UIApproval bool
	// UIChoice gates stado_ui_choose — operator-facing single /
	// multi-choice picker. Declared via ui:choice in the manifest.
	// Q3 (2026-05-07).
	UIChoice bool
	// UIPrint gates stado_ui_print — plain-text fire-and-forget
	// emit into the operator's view. Declared via ui:print in the
	// manifest (EP-0064).
	UIPrint bool
	// UIRender gates stado_ui_render — structured panel emit
	// (title + typed sections: text / kv / list / code / table /
	// diff). Declared via ui:render in the manifest (EP-0064 and
	// docs/plugins/host-imports.md).
	UIRender bool

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

	// RegistryCatalog is the bounded, exact-registry fact and session-surface
	// bridge used by signed discovery applications. The access object is built
	// from the concrete registry adapter and caller identity by native runtime
	// composition; guest JSON cannot select either. Calls remain separately
	// gated by registry:catalog and session:tool-surface.
	RegistryCatalog *RegistryCatalogAccess

	// ContextResources is the exact, session-bound context catalog exposed to
	// signed applications through operation+kind-scoped capabilities. It is
	// composed from host facts; guest JSON never supplies paths, trust, source
	// provenance, model visibility, or the effective tool ceiling.
	ContextResources *ContextResourceAccess

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

	// NetMulticast gates the elevated stado_net_setopt keys
	// (broadcast, multicast_join/leave, multicast_ttl, multicast_loopback)
	// on UDP listener handles. Declared via net:multicast:udp in the
	// manifest. EP-0038i.
	NetMulticast bool

	// NetICMP gates stado_net_icmp_echo. Declared via net:icmp in
	// the manifest. Note: requires either an unprivileged ICMP
	// socket allowed by Linux net.ipv4.ping_group_range or CAP_NET_RAW.
	// The host import surfaces a clear
	// "operation not permitted" error when neither path works.
	NetICMP bool

	// Progress is the operator-visible callback for stado_progress
	// emissions. Wired by the host caller (TUI, headless run, plugin
	// invoke shell). When nil the import drops silently — the plugin
	// shouldn't fail because the operator surface isn't connected.
	// EP-0038h. NOT for agent-loop integration; the model only sees
	// the final tool result.
	Progress func(plugin, text string)

	// Provider reservations serialize the admission arithmetic, not provider
	// work. Each call reserves its estimated/native input plus selected maximum
	// output before dispatch, then releases unused headroom and commits actual
	// reported or conservative estimated usage even on failure/cancellation.
	providerBudgetMu       sync.Mutex
	providerTokensUsed     int
	providerTokensReserved int

	// lastFSError carries the most recent stado_fs_* error message for
	// retrieval by the wasm plugin via stado_fs_last_error. Populated
	// when a host-side FS import returns -1 with a structured cause
	// (scope guard, capability deny, IO failure). Cleared on the next
	// successful FS import. Cheap workaround for the negative-return
	// wire format: -1 alone can't carry a string.
	lastFSError string
}

// AttachToolHost installs the caller-facing tool host and copies its default
// process sandbox policy. Keeping these two channels together ensures wasm
// process imports use the same runner and host policy as Executor.Run.
func (h *Host) AttachToolHost(toolHost tool.Host) {
	if h == nil {
		return
	}
	h.ToolHost = toolHost
	h.DefaultSandboxPolicy = nil
	if provider, ok := toolHost.(tool.SandboxPolicyProvider); ok {
		h.DefaultSandboxPolicy = provider.DefaultSandboxPolicy()
	}
}

// SecretsAccess holds the capability gates and backing store for the
// stado_secrets_* host imports. Constructed lazily by NewHost when the
// manifest declares at least one secrets:* capability.
type SecretsAccess struct {
	Store         *secrets.Store
	ReadDeclared  bool                    // secrets:read granted at all (vs. not declared)
	WriteDeclared bool                    // secrets:write granted at all (vs. not declared)
	ReadGlobs     []string                // patterns from secrets:read:<glob>; empty+declared = broad
	WriteGlobs    []string                // patterns from secrets:write:<glob>; empty+declared = broad
	AuditEmitter  func(SecretsAuditEvent) // optional; nil = no-op
	PluginName    string                  // canonical source namespace for storage isolation
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
	if !s.ReadDeclared {
		return false // secrets:read not granted — declaring only secrets:write must not confer read
	}
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
	if !s.WriteDeclared {
		return false // secrets:write not granted — declaring only secrets:read must not confer write
	}
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
	if !s.ReadDeclared {
		return false // secrets:read not granted
	}
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
// session (e.g. `stado tool run` outside a session context); the
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
}

// ArtifactCallerContext contains host-authenticated scope attached to artifact
// broker calls. Identity is injected separately from Host.Identity immediately
// before dispatch, so it cannot be replaced by guest JSON or a stale binding.
type ArtifactCallerContext struct {
	Principal          string   `json:"principal"`
	CanonicalRepoID    string   `json:"canonical_repo_id,omitempty"`
	SessionID          string   `json:"session_id,omitempty"`
	SessionGeneration  uint64   `json:"session_generation,omitempty"`
	AncestorSessionIDs []string `json:"ancestor_session_ids,omitempty"`
}

type ArtifactCaller struct {
	Identity plugins.RuntimeIdentity `json:"identity"`
	ArtifactCallerContext
}

// ArtifactBridge is implemented by the broker client attached to a tool host.
// All methods return a bounded JSON response suitable for the WASM output
// buffer. The bridge must reject missing/stale caller context and remains the
// only route to authoritative artifact storage.
type ArtifactBridge interface {
	Propose(ctx context.Context, caller ArtifactCaller, requestID string, payload []byte) ([]byte, error)
	Query(ctx context.Context, caller ArtifactCaller, requestID string, payload []byte) ([]byte, error)
	Edit(ctx context.Context, caller ArtifactCaller, requestID string, payload []byte) ([]byte, error)
	Observe(ctx context.Context, caller ArtifactCaller, requestID string, payload []byte) ([]byte, error)
}

// ArtifactBridgeBinding is returned opaquely through tool.Host so pluginrun can
// attach an authenticated broker client without teaching pkg/tool about plugin
// runtime types.
type ArtifactBridgeBinding struct {
	Bridge ArtifactBridge
	Caller ArtifactCallerContext
}

// EvidenceBridge transports one fixed, capability-gated evidence operation
// under a native-held broker binding. The broker derives every authority field
// from that binding and returns only bounded JSON.
type EvidenceBridge interface {
	CallEvidence(context.Context, string, []byte) ([]byte, error)
}

type EvidenceBridgeBinding struct {
	Bridge EvidenceBridge
}

// ApplicationBinding is minted by broker admission for one exact plugin,
// session, and generation. The opaque broker token remains inside Bridge;
// lifecycle code receives only the authenticated anchor and typed bridges.
type ApplicationBinding struct {
	Anchor      ApplicationAnchor
	Artifact    ArtifactBridgeBinding
	Evidence    EvidenceBridgeBinding
	Application ApplicationBridge
	Controller  ApplicationControllerBridge
	Events      ApplicationEventTransport
}

// ApplicationBridge transports one already capability-gated, bounded typed
// request under the native-held broker admission binding. Operation names are
// fixed by the host import table; guests cannot provide arbitrary broker RPC
// method names.
type ApplicationBridge interface {
	CallApplication(context.Context, string, string, []byte) ([]byte, error)
}

// ApplicationControllerBridge carries native session-controller operations
// associated with one admitted lifecycle application. It is deliberately a
// distinct interface from ApplicationBridge: no WASM host import can reach
// it, and its broker implementation authenticates the session controller in
// addition to resolving the opaque application binding. Merely hiding an
// operation name from the guest ABI is not an authority boundary.
type ApplicationControllerBridge interface {
	CallApplicationController(context.Context, string, []byte) ([]byte, error)
}

// ApprovalBridge is the interactive UI hook plugins can call when they
// explicitly want a human decision. Surfaces without a user-facing UI may
// leave it nil; the host import returns -1 in that case so the plugin can
// decide how to proceed.
type ApprovalBridge interface {
	RequestApproval(ctx context.Context, title, body string) (bool, error)
}

// ChoiceBridge is the interactive UI hook plugins use to prompt the
// operator to pick from a list of options. Mirrors ApprovalBridge but
// for N-of-M selection instead of yes/no. Nil on surfaces without a
// user-facing UI (headless run, MCP server) — the host import returns
// a structured "interactive UI unavailable" error in that case so the
// plugin can decide what to do (e.g., fail vs. fall back to its
// `default`).
//
// The bridge is single-flight per session: only one outstanding
// request at a time. Implementations should reject a second concurrent
// call with a non-nil error so the plugin sees a clean signal instead
// of stacking drawers.
//
// Cancellation: when ctx is cancelled (turn cancel, session cancel,
// operator quit), the bridge MUST return ChoiceResponse{Cancelled: true}
// promptly so the plugin call doesn't deadlock.
type ChoiceBridge interface {
	RequestChoice(ctx context.Context, req ChoiceRequest) (ChoiceResponse, error)
}

// ChoiceRequest is the operator-facing prompt. Single-choice flows set
// Multi=false; multi-choice flows set Multi=true. Default may
// pre-toggle one or more options by ID — for single mode it
// determines the cursor's initial position; for multi mode every
// listed ID starts toggled on.
type ChoiceRequest struct {
	Prompt  string
	Options []ChoiceOption
	Multi   bool
	Default []string
}

// ChoiceOption is a single picker entry. ID is what the plugin gets
// back in ChoiceResponse.Selected; Label is what the operator sees.
//
// Prefix and Input extend the option to carry an editable
// field — the row renders as `prefix [editable text] label` and the
// operator's typed value comes back in ChoiceResponse.InputValue.
// Both are zero-value-safe; callers omitting them (Prefix="", Input=nil)
// behave exactly as before.
type ChoiceOption struct {
	ID     string
	Label  string
	Prefix string       // Read-only decoration shown alongside the input field.
	Input  *ChoiceInput // Nil means a pure choice row without editable input.
}

// ChoiceInput is the optional editable field on a ChoiceOption.
// Default seeds the field; Validator (when non-nil) is checked
// runtime-side before the response is returned to the plugin.
type ChoiceInput struct {
	Default   string
	Validator *ChoiceValidator
}

// ChoiceValidator describes a runtime-side check applied to the
// operator's typed input before the choice resolves. Kind selects
// the validator family; Spec carries kind-specific parameters
// (e.g. "0,80" for length, the pattern for regex). Unknown kinds
// are rejected at decode time.
type ChoiceValidator struct {
	Kind string
	Spec string
}

// ChoiceResponse carries the operator's answer back to the plugin.
// Cancelled=true means no decision was made (Esc / session cancel);
// Selected is empty in that case. InputValue carries the text typed
// into the chosen option's input field, or "" when the chosen
// option had no input.
type ChoiceResponse struct {
	Selected   []string
	InputValue string
	Cancelled  bool
}

// PrintBridge is the operator-facing surface for stado_ui_print.
// Implementations route the text + opts to whatever channel the
// host runs on (TUI scrollback, ACP session/update, MCP tool
// result, etc.). EP-0064 and docs/plugins/host-imports.md own the
// durable contract; each supported surface must wire it explicitly.
//
// Returning a non-nil error surfaces the import call as a negative
// payload to the plugin (size violation, channel rejection); a nil
// return is a successful fire-and-forget emit. Implementations
// should NOT block on operator visibility — print is non-blocking
// by spec, so a backed-up channel should drop or buffer rather
// than block the plugin.
type PrintBridge interface {
	Print(ctx context.Context, text string, opts PrintOpts) error
}

// PrintOpts is the optional decoration on a stado_ui_print call.
// Severity is one of "" (default = info) | "info" | "warn" | "error";
// renderers may style accordingly or ignore. EOL appends a newline
// (default true). StreamID is an opaque label so a renderer can
// coalesce successive calls with the same id into one block (TUI
// inline scrollback may render same-stream prints as a continuation).
type PrintOpts struct {
	Severity string
	EOL      bool
	StreamID string
}

// RenderBridge is the operator-facing surface for stado_ui_render —
// structured panels with typed sections. Implementations route the
// Panel to whatever channel the host runs on (TUI bordered widget,
// ACP session/update kind=panel, MCP tool-result envelope, headless
// NDJSON). Returning a non-nil error surfaces the import call as a
// negative payload to the plugin (channel rejection); nil = silent
// success. Implementations should NOT block on operator visibility —
// render is non-blocking by spec, so a backed-up channel should drop
// or buffer rather than block the plugin. EP-0064 and
// docs/plugins/host-imports.md own this contract.
type RenderBridge interface {
	Render(ctx context.Context, panel Panel) error
}

// Panel is the wire-decoded structured-panel payload emitted by a
// plugin via stado_ui_render. Variant carries optional styling intent
// ("info" / "ok" / "warn" / "error" / "recommendation") that
// renderers may colour, prefix, or ignore. ID is an opaque label
// useful when a later choice references this panel ("re. the diff
// shown above"). Footer is a short trailing line (status, hint).
//
// Target selects the display surface (#21 part 2): "" / "viewport"
// (default, conversation scrollback), "sidebar" / "footer" (a plugin-
// owned, addressable panel — the operator references Panel.ID in
// [tui.sidebar].sections / [tui.footer].segments to show it), or "log"
// (one line appended to the shared, bounded notification log). For
// "sidebar" / "footer" the ID is REQUIRED (it's how the operator
// addresses the panel). Re-rendering the same id replaces that panel
// (last-write-wins). Non-TUI render channels ignore Target.
type Panel struct {
	Title    string
	Sections []Section
	Variant  string
	ID       string
	Footer   string
	Target   string
}

// Section is one body of a Panel. Exactly one of the body-shape
// fields is meaningful per Kind; the wire decoder validates that
// the right field is populated for the declared kind so renderers
// can switch on Kind safely.
type Section struct {
	Kind    string // text | kv | list | code | table | diff
	Heading string
	// Per-kind body fields. The decoder ensures only the matching
	// field is populated for a given Kind; renderers MUST switch on
	// Kind rather than peeking at field zero-ness.
	Text  string
	KV    []KVPair
	List  ListBody
	Code  CodeBody
	Table TableBody
	Diff  DiffBody
}

// KVPair is one row of a kv-kind Section body.
type KVPair struct {
	Label string
	Value string
}

// ListBody is the body of a list-kind Section. Marker controls
// renderer styling: "bullet" (default), "numbered", or "check"
// (operator-facing checklist).
type ListBody struct {
	Marker string
	Items  []string
}

// CodeBody is the body of a code-kind Section. Language is an
// optional renderer hint (TUI may apply syntax-tinted colouring,
// ACP / MCP carry it through verbatim).
type CodeBody struct {
	Language string
	Content  string
}

// TableBody is the body of a table-kind Section. Columns names
// the header row; Rows is a list of cell-value rows. The decoder
// caps Rows × Cols at maxPluginRuntimeUIRenderTableRows ×
// maxPluginRuntimeUIRenderTableCols. Renderers truncate narrower
// terminals at their own discretion.
type TableBody struct {
	Columns []string
	Rows    [][]string
}

// DiffBody is the body of a diff-kind Section. Renderers compute
// or display a before/after view; Plain Before/After strings keep
// the wire shape simple and let each renderer choose its own
// algorithm (TUI uses Myers via go-difflib; ACP carries strings
// verbatim and lets the client diff).
type DiffBody struct {
	Before string
	After  string
}

// FleetBridge is the capability-checked generic agent-operation surface used
// by plugins through stado_agent_* imports. Nil on surfaces without a live
// runtime fleet.
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
	Prompt               string       `json:"prompt"`
	Provider             string       `json:"provider,omitempty"`
	Model                string       `json:"model,omitempty"`
	Thinking             string       `json:"thinking,omitempty"`
	ThinkingBudgetTokens int          `json:"thinking_budget_tokens,omitempty"`
	ReasoningEffort      string       `json:"reasoning_effort,omitempty"`
	Async                bool         `json:"async,omitempty"`
	Ephemeral            bool         `json:"ephemeral,omitempty"`
	ParentSession        string       `json:"parent_session,omitempty"` // empty = use caller's session
	AllowedTools         []string     `json:"allowed_tools,omitempty"`
	SandboxProfile       string       `json:"sandbox_profile,omitempty"`
	Role                 string       `json:"role,omitempty"`
	Mode                 string       `json:"mode,omitempty"`
	Ownership            string       `json:"ownership,omitempty"`
	WriteScope           []string     `json:"write_scope,omitempty"`
	MaxTurns             int          `json:"max_turns,omitempty"`
	TimeoutSeconds       int          `json:"timeout_seconds,omitempty"`
	Source               *AgentSource `json:"source,omitempty"`
	ToolProfile          string       `json:"tool_profile,omitempty"`
	NarrowTools          []string     `json:"narrow_tools,omitempty"`
	TokenBudget          int          `json:"token_budget,omitempty"`
	Execution            string       `json:"execution,omitempty"`
	Supervision          string       `json:"supervision,omitempty"`
	MaxRestarts          int          `json:"max_restarts,omitempty"`
	// ChildToolOwner is injected from this Host's authenticated package
	// identity on every WASM spawn. Guest JSON cannot claim another package's
	// signed agent_child_only helpers.
	ChildToolOwner string `json:"-"`
	// IdempotencyKey names one logical spawn attempt. Lifecycle application
	// hosts scope it to the authenticated plugin/session/generation and reject
	// reuse with a different normalized request. It grants no authority.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Caller is injected by the native host after decoding. Guest JSON cannot
	// select or widen this scope.
	Caller AgentSpawnCaller `json:"-"`
	// Persona names the operating manual the child runs under.
	// Empty = inherit the parent's active persona. Empty +
	// no parent = bundled "default". EP-0038i.
	Persona string `json:"persona,omitempty"`
}

// AgentSpawnCaller is the authenticated lifecycle application scope attached
// to an idempotent spawn. Keeping it out of the JSON contract prevents a guest
// from choosing another plugin, session, or generation as its deduplication
// namespace.
type AgentSpawnCaller struct {
	PluginID   string
	SessionID  string
	Generation uint64
}

// AgentSource selects an authorized immutable point in a historical session.
type AgentSource struct {
	SessionID string `json:"session_id"`
	At        string `json:"at,omitempty"`
}

// AgentSpawnResult is the output of FleetBridge.AgentSpawn.
type AgentSpawnResult struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	// FinalText is populated when Async=false and the agent completed.
	FinalText string                 `json:"final_text,omitempty"`
	Terminal  *AgentTerminalMetadata `json:"terminal,omitempty"`
}

// AgentTokenUsage is host-collected terminal accounting. Currency estimates
// are deliberately absent from this application-facing contract.
type AgentTokenUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// AgentCleanupDiagnostic is separate from the semantic result. Fingerprint is
// safe to compare or journal; raw provider cleanup errors are not exposed.
type AgentCleanupDiagnostic struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type AgentTerminalMetadata struct {
	Usage         AgentTokenUsage         `json:"usage"`
	UsageComplete bool                    `json:"usage_complete"`
	Cleanup       *AgentCleanupDiagnostic `json:"cleanup,omitempty"`
}

// AgentListEntry is one entry from FleetBridge.AgentList.
type AgentListEntry struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"session_id"`
	Status     string  `json:"status"`
	Model      string  `json:"model"`
	StartedAt  string  `json:"started_at"`
	LastTurnAt string  `json:"last_turn_at,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

// AgentMessages is the result of FleetBridge.AgentReadMessages.
type AgentMessages struct {
	Messages []AgentMessage         `json:"messages"`
	Offset   int                    `json:"offset"`
	Status   string                 `json:"status"`
	Terminal *AgentTerminalMetadata `json:"terminal,omitempty"`
}

// AgentMessage is one item in AgentMessages.
type AgentMessage struct {
	Role    string `json:"role"` // "assistant" or "external_input"
	Content string `json:"content,omitempty"`
	Source  string `json:"source,omitempty"` // for external_input events
	Offset  int    `json:"offset,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// NewHost is the compatibility constructor for tests and identity-free ABI
// inspection. Executable plugin loaders must derive a source-bound identity
// and call NewHostWithIdentity; this helper has no verified source path.
func NewHost(m plugins.Manifest, workdir string, logger *slog.Logger) *Host {
	identity, _ := plugins.RuntimeIdentityForLocal(m)
	return NewHostWithIdentity(m, identity, workdir, logger)
}

func NewHostWithIdentity(m plugins.Manifest, identity plugins.RuntimeIdentity, workdir string, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Host{
		Manifest: m,
		Identity: identity,
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
				// #054: reject cfg:* sub-paths containing ".." at
				// parse time so a traversal cap (e.g.
				// fs:read:cfg:state_dir/../../keys) never enters the
				// allow-list. expandFSEntry also guards containment at
				// check time; this is defense-in-depth.
				if cfgSubEscapes(path) {
					continue
				}
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
			// On systems where the workdir crosses a symlink (notably
			// Fedora Atomic / Silverblue / Bazzite, where /home →
			// /var/home), the cap-path stored above is the symlink
			// form (e.g. /home/user/repo) but actual file reads run
			// through realPath(), which resolves to /var/home/user/.
			// Without aliasing both forms here the cap-glob compare
			// fails and fs:read:. silently denies every file. Append
			// the realpath alongside the literal so either form
			// matches at allow-time.
			scopes := []string{scope}
			if !strings.HasPrefix(scope, "cfg:") {
				// #016: alias only the workdir *prefix* and re-append
				// the cap's relative suffix literally. Resolving the
				// whole cap path with EvalSymlinks would follow a
				// repo-controlled symlink in the suffix (e.g. a manifest
				// fs:read:src where the repo makes src → ~/.ssh), baking
				// the escape target into the allow-list and sidestepping
				// realPath()'s per-access symlink defense. Absolute caps
				// have no workdir prefix to alias, so they keep only the
				// literal form.
				if alias := workdirSymlinkAlias(workdir, path); alias != "" {
					scopes = append(scopes, alias)
				}
			}
			switch parts[1] {
			case "read":
				h.FSRead = append(h.FSRead, scopes...)
			case "write":
				h.FSWrite = append(h.FSWrite, scopes...)
			}
		case "net":
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
			// "net:multicast:udp" — enables the elevated setopt keys
			// (broadcast, multicast_join/leave, multicast_ttl,
			// multicast_loopback) on UDP listener handles. EP-0038i.
			if parts[1] == "multicast" && len(parts) == 3 && parts[2] == "udp" {
				h.NetMulticast = true
				continue
			}
			// "net:icmp" — gates stado_net_icmp_echo. EP-0038i.
			if len(parts) == 2 && parts[1] == "icmp" {
				h.NetICMP = true
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
			// "net:dial:..." / "net:listen:..." are parsed by
			// parseNetSocketCap below.
			if parts[1] == "dial" || parts[1] == "listen" {
				break // out of the switch; parser block below handles it
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
		case "provider":
			// provider:invoke:<tokens> is exact: no bare default, malformed
			// suffix, compatibility alias, or silent clamping before v1.
			if len(parts) != 3 || parts[1] != "invoke" {
				continue
			}
			if budget, err := strconv.Atoi(parts[2]); err == nil && budget > 0 && budget <= maxProviderInvokeCapabilityTokens {
				h.ProviderInvokeBudget = budget
			}
		case "artifact":
			if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
				continue
			}
			scope := strings.TrimSpace(parts[2])
			switch parts[1] {
			case "propose":
				h.ArtifactPropose = append(h.ArtifactPropose, scope)
			case "read":
				h.ArtifactRead = append(h.ArtifactRead, scope)
			case "edit":
				h.ArtifactEdit = append(h.ArtifactEdit, scope)
			case "observe":
				h.ArtifactObserve = append(h.ArtifactObserve, scope)
			}
		case "evidence":
			if len(parts) == 2 && parts[1] == "validate" {
				h.EvidenceValidate = true
				continue
			}
			if len(parts) != 3 || (parts[2] != "artifact" && parts[2] != "session") {
				continue
			}
			if h.EvidenceCatalog == nil {
				h.EvidenceCatalog = make(map[string]bool)
				h.EvidenceSearch = make(map[string]bool)
				h.EvidenceOpen = make(map[string]bool)
			}
			switch parts[1] {
			case "catalog":
				h.EvidenceCatalog[parts[2]] = true
			case "search":
				h.EvidenceSearch[parts[2]] = true
			case "open":
				h.EvidenceOpen[parts[2]] = true
			}
		case "exec":
			switch parts[1] {
			// EP-no-internal-tools Steps 4+5: exec:bash / exec:shallow_bash /
			// exec:search / exec:ast_grep all dropped — their delegate host
			// imports are gone. Use exec:proc:<glob> instead.
			case "pty":
				h.ExecPTY = true
				if len(parts) == 3 && parts[2] != "" {
					h.ExecPTYGlobs = h.appendExecGlob(h.ExecPTYGlobs, parts[2], "exec:pty")
				}
			case "proc":
				h.ExecProc = true
				if len(parts) == 3 && parts[2] != "" {
					h.ExecProcGlobs = h.appendExecGlob(h.ExecProcGlobs, parts[2], "exec:proc")
				}
			}
		case "bundled-bin":
			h.BundledBin = true
		case "dns":
			switch parts[1] {
			case "resolve":
				h.DNSResolve = true
			case "resolve_private":
				h.DNSResolve = true // resolve_private implies resolve
				h.DNSResolvePrivate = true
			case "axfr":
				h.DNSResolve = true // axfr implies resolve
				h.DNSAXFR = true
			case "axfr_private":
				h.DNSResolve = true // axfr implies resolve
				h.DNSAXFR = true
				h.DNSAXFRPrivate = true
			case "reverse":
				h.DNSReverse = true
			}
		case "crypto":
			if parts[1] == "hash" {
				h.CryptoHash = true
			}
		case "compress":
			h.Compress = true
		case "lsp":
			if parts[1] == "query" {
				h.LSPQuery = true
			}
		case "ui":
			switch parts[1] {
			case "approval":
				h.UIApproval = true
			case "choice":
				h.UIChoice = true
			case "print":
				h.UIPrint = true
			case "render":
				h.UIRender = true
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
				h.Secrets.ReadDeclared = true
				if len(parts) == 3 && parts[2] != "" {
					h.Secrets.ReadGlobs = append(h.Secrets.ReadGlobs, parts[2])
				}
				// len(parts) == 2 → broad read; ReadGlobs stays empty (match-all)
			case "write":
				if h.Secrets == nil {
					h.Secrets = &SecretsAccess{}
				}
				h.Secrets.WriteDeclared = true
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
				h.State.ReadDeclared = true
				if len(parts) == 3 && parts[2] != "" {
					h.State.ReadGlobs = append(h.State.ReadGlobs, parts[2])
				}
			case "write":
				if h.State == nil {
					h.State = &StateAccess{}
				}
				h.State.WriteDeclared = true
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
	// Namespace authority-bearing stores at construction time so direct
	// lifecycle/operator dispatchers cannot forget the binding. The stores
	// themselves are attached later by the runtime or surface.
	if h.Secrets != nil {
		h.Secrets.PluginName = identity.Namespace
	}
	if h.State != nil {
		h.State.PluginName = identity.Namespace
	}
	return h
}

// AuditIdentity returns the exact canonical runtime identity used to attribute
// authority-bearing host activity. Manifest.Name is display metadata only.
func (h *Host) AuditIdentity() string {
	if h == nil || h.Identity.Validate() != nil {
		return ""
	}
	return h.Identity.Canonical
}

// AttachAuthorityStores provisions the two host-owned stores whose namespaces
// carry plugin authority. Executable loaders call this exactly once after
// deriving RuntimeIdentity and before installing imports. Keeping the wiring
// here prevents direct tool, lifecycle, headless, and background loaders from
// accidentally falling back to the display-only Manifest.Name.
//
// Instance state is process-lifetime and belongs to the owning Runtime.
// Secrets are durable under stateDir. A missing stateDir deliberately leaves
// the secrets store unavailable rather than inventing another persistence
// location.
func (h *Host) AttachAuthorityStores(stateDir string, instanceStore *InstanceStore, audit func(SecretsAuditEvent)) {
	if h == nil {
		return
	}
	if h.Secrets != nil {
		h.Secrets.PluginName = h.Identity.Namespace
		h.Secrets.AuditEmitter = audit
		if stateDir != "" {
			h.Secrets.Store = secrets.NewStore(stateDir)
		}
	}
	if h.State != nil {
		h.State.PluginName = h.Identity.Namespace
		h.State.Store = instanceStore
	}
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

func (h *Host) NeedsArtifactBridge() bool {
	return len(h.ArtifactPropose) != 0 || len(h.ArtifactRead) != 0 ||
		len(h.ArtifactEdit) != 0 || len(h.ArtifactObserve) != 0
}

func (h *Host) NeedsEvidenceBridge() bool {
	return h != nil && (len(h.EvidenceCatalog) != 0 || len(h.EvidenceSearch) != 0 ||
		len(h.EvidenceOpen) != 0 || h.EvidenceValidate)
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
		if pathPrefixMatch(abs, expanded) {
			return true
		}
		// cfg:* templates resolve at check time, so the symlink-alias
		// that literal cap paths receive at parse time (see NewHost's
		// symlinkAlias append) has to be applied here. abs arrives
		// EvalSymlinks-resolved via realPath, while StateDir is the
		// symlink form straight from os.UserHomeDir(); on Fedora Atomic
		// (/home → /var/home) the literal compare above misses. Alias the
		// expanded value and retry against the resolved form.
		if strings.HasPrefix(a, "cfg:") {
			if alias := symlinkAlias(expanded); alias != "" && pathPrefixMatch(abs, alias) {
				return true
			}
		}
	}
	return false
}

// pathPrefixMatch reports whether abs equals entry or lies under it
// (entry treated as a directory prefix). Shared by the literal and
// cfg-template allow-list checks.
func pathPrefixMatch(abs, entry string) bool {
	return entry == abs || strings.HasPrefix(abs, strings.TrimRight(entry, "/")+"/")
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
	expanded := filepath.Clean(value + "/" + sub)
	// #054: verify the cleaned result stays under the cfg value. Without
	// this, a manifest cap like fs:read:cfg:state_dir/../../keys cleans to
	// an allow-list root OUTSIDE the state-dir subtree, and
	// pathAllowedExpanded would then treat that escaped prefix as
	// authorized. Mirror the pathInRoot guard in host_fs.go: reject when
	// the relative path escapes (".."-prefixed) or resolves to an
	// absolute path. cfgSubEscapes also rejects ".." segments at parse
	// time as defense-in-depth (see NewHost).
	rel, err := filepath.Rel(filepath.Clean(value), expanded)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return expanded
}

// cfgSubEscapes reports whether a cfg:* capability sub-path contains a
// ".." traversal segment. #054 defense-in-depth: such caps are dropped
// at manifest-parse time (NewHost) so they never enter the allow-list,
// in addition to the check-time containment guard in expandFSEntry.
func cfgSubEscapes(rawCfg string) bool {
	if !strings.HasPrefix(rawCfg, "cfg:") {
		return false
	}
	_, sub, _ := strings.Cut(rawCfg[len("cfg:"):], "/")
	if sub == "" {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(sub), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
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

// workdirSymlinkAlias aliases the *workdir prefix* of a manifest fs
// capability path, preserving the cap's relative suffix literally.
//
// #016: the previous code aliased the fully-resolved cap path via
// symlinkAlias(<workdir>/<sub>), which ran EvalSymlinks over the final
// components too. A malicious repo could then make a scoped cap target
// (e.g. fs:read:src) a symlink to a sensitive directory (~/.ssh); the
// resolved escape target got appended to the allow-list, defeating the
// per-access realPath() symlink check. Here we only resolve symlinks in
// the workdir (which the host controls, not the repo) and re-join the
// literal relative suffix, so the Fedora-Atomic /home → /var/home
// aliasing still works while repo-controlled suffix symlinks are never
// followed at parse time.
//
// Returns "" for absolute caps (no workdir prefix to alias), when the
// workdir doesn't resolve differently, or when the workdir can't be
// evaluated — the caller falls back to the literal entry.
func workdirSymlinkAlias(workdir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return ""
	}
	resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return ""
	}
	resolvedWorkdir = filepath.Clean(resolvedWorkdir)
	if resolvedWorkdir == filepath.Clean(workdir) {
		return ""
	}
	// Re-attach the literal (un-resolved) relative suffix.
	return filepath.Clean(filepath.Join(resolvedWorkdir, path))
}

// symlinkAlias returns the EvalSymlinks-resolved form of an absolute
// path when it differs from the literal, or "" when the path doesn't
// resolve differently / doesn't exist / can't be evaluated. Used to
// alias cfg:* path-template entries (expanded at check time) on systems
// where the state-dir crosses a symlink (Fedora Atomic /home →
// /var/home is the canonical case). Best-effort: a missing path or
// EvalSymlinks failure is not fatal — the caller falls back to the
// literal entry, which may still match if the runtime's realPath also
// fails on the access side.
func symlinkAlias(absPath string) string {
	if absPath == "" || !filepath.IsAbs(absPath) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return ""
	}
	resolved = filepath.Clean(resolved)
	if resolved == filepath.Clean(absPath) {
		return ""
	}
	return resolved
}
