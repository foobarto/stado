// Package daemon implements stado's stateful tool host.
//
// `stado tool run` is single-shot — every invocation is a fresh process
// whose pty.Manager (browser cookie jar / LSP connections / cached wasm
// modules) is born and dies with the call. Agents that string together
// `shell.spawn` → `shell.read` over multiple `tool run` calls hit a wall:
// the spawn returns an id that no follow-up call can find.
//
// The daemon fixes that by being a long-running peer over a Unix domain
// socket. `stado tool run` auto-spawns the daemon when missing, dispatches
// the tool call as JSON-RPC, returns the result. State that needs to
// outlive a single call (PTYs, browser sessions, LSP, wasm module cache)
// lives inside the daemon process.
//
// Wire format: newline-delimited JSON-RPC 2.0 over a Unix domain socket
// at $XDG_RUNTIME_DIR/stado/daemon.sock (override via $STADO_DAEMON_SOCKET).
// Mode 0700, owner-only. SO_PEERCRED on Linux rejects connections from
// other uids. No TCP bind by default; loopback bind is opt-in and adds a
// bearer token gate.
//
// The daemon does NOT host the agent loop. Agent loops still happen in
// the calling process; the daemon is a state service for `stado tool run`
// callers. MCP server (`stado mcp-server`) remains an independent stdio-
// per-client surface — a future commit may unify the two over the daemon's
// dispatcher, but v1 keeps them separate.
package daemon

import (
	"encoding/json"
	"time"
)

// Protocol version. Bumped on wire-incompatible changes only — additive
// changes (new methods, new optional fields) keep the same version.
// Clients send their version in the handshake; daemon refuses connections
// from a client that's older than its earliest supported version.
const Version = "1"

// MinClientVersion is the earliest client protocol version this daemon
// will accept. Equal to Version while we're at v1; bumped only when a
// client cannot speak the current wire shape.
const MinClientVersion = "1"

// DefaultIdleTimeout is how long the daemon stays alive with zero live
// sessions and no tool calls before exiting on its own. Operators
// running `stado daemon start --idle-timeout=0` opt out.
const DefaultIdleTimeout = 30 * time.Minute

// HandshakeTimeout caps how long a freshly-connected client has to send
// its daemon.handshake before the daemon closes the connection. Real
// clients send it as their first message; the timeout exists to cap
// resource use from a misbehaving or scanning peer.
const HandshakeTimeout = 5 * time.Second

// MaxRequestBytes caps a single JSON-RPC request payload. tool.call
// arguments can be large (snapshot SVG round-trips, file contents) but
// 32 MiB is two orders of magnitude past anything legitimate.
const MaxRequestBytes = 32 << 20

// JSON-RPC error codes. Standard JSON-RPC reserves -32768 through -32000
// for protocol-level errors; we use -32000…-32099 for stado-specific ones
// so they don't collide with parse-error etc.
const (
	ErrCodeParse             = -32700
	ErrCodeInvalidRequest    = -32600
	ErrCodeMethodNotFound    = -32601
	ErrCodeInvalidParams     = -32602
	ErrCodeInternal          = -32603
	ErrCodeServerShutdown    = -32000
	ErrCodeVersionSkew       = -32001
	ErrCodeToolNotFound      = -32010
	ErrCodeToolDenied        = -32011
	ErrCodeSessionNotFound   = -32012
	ErrCodeProjectMismatch   = -32013
)

// JSON-RPC method names. Every request carries a method from this list;
// unknown methods get ErrCodeMethodNotFound.
const (
	MethodHandshake     = "daemon.handshake"
	MethodStatus        = "daemon.status"
	MethodShutdown      = "daemon.shutdown"
	MethodToolCall      = "tool.call"
	MethodToolList      = "tool.list"
	MethodSessionList   = "session.list"
	MethodSessionKill   = "session.kill"
)

// Request is a JSON-RPC 2.0 request envelope. ID is a raw json.RawMessage
// so we preserve "null" / number / string round-trip without normalising
// to one form. Notifications (no ID) are accepted but currently never
// emitted by the daemon.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response envelope. Exactly one of Result or
// Error is set. Result is interface-typed because each method returns a
// different concrete shape; the client knows which to decode based on
// the method it called.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC error object. Data carries an optional structured
// payload (e.g., the underlying go error wrapped); clients should treat
// Code + Message as the canonical signal.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// HandshakeParams is the payload for daemon.handshake. ClientVersion is
// the daemon protocol version the client speaks; ClientName is a free-form
// identifier ("stado-tool-run", "stado-daemon-cli") for the daemon's logs.
type HandshakeParams struct {
	ClientVersion string `json:"client_version"`
	ClientName    string `json:"client_name,omitempty"`
}

// HandshakeResult tells the client which version the daemon speaks plus
// the daemon's process identity. Mismatched versions cause the daemon to
// reject the request with ErrCodeVersionSkew before this result is sent —
// successful handshakes always have compatible versions.
type HandshakeResult struct {
	ServerVersion string `json:"server_version"`
	DaemonPID     int    `json:"daemon_pid"`
	StadoVersion  string `json:"stado_version"`
}

// StatusResult is the daemon.status payload. Suitable for `stado daemon
// status` rendering and operator-facing observability.
type StatusResult struct {
	ServerVersion string    `json:"server_version"`
	StadoVersion  string    `json:"stado_version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSec     int64     `json:"uptime_sec"`
	DaemonPID     int       `json:"daemon_pid"`
	SocketPath    string    `json:"socket_path"`
	Projects      int       `json:"projects"`
	LiveSessions  int       `json:"live_sessions"`
	TotalCalls    uint64    `json:"total_calls"`
	IdleSec       int64     `json:"idle_sec"`
	IdleTimeoutSec int64    `json:"idle_timeout_sec"`
}

// ShutdownParams is the payload for daemon.shutdown. Force=true skips the
// "wait for active calls" grace period; Reason is logged for the operator.
type ShutdownParams struct {
	Force  bool   `json:"force,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ShutdownResult acknowledges the shutdown decision. Daemon closes the
// connection immediately after sending this; the listener stops accepting
// new connections, in-flight calls finish (or are cancelled if Force).
type ShutdownResult struct {
	OK bool `json:"ok"`
}

// ToolCallParams is the payload for tool.call. ProjectID scopes the
// session registry — calls from project A cannot observe sessions
// created from project B even though both share the daemon. AllowList,
// when non-empty, gates which tool names are permitted to dispatch from
// this call (mirrors the per-process --tools flag); the daemon rejects
// dispatches whose tool isn't in the list.
type ToolCallParams struct {
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	ProjectID string          `json:"project_id"`
	AllowList []string        `json:"allow_list,omitempty"`
	Workdir   string          `json:"workdir,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	TimeoutMs int             `json:"timeout_ms,omitempty"`
}

// ToolCallResult mirrors tool.Result with a flat JSON shape. Content is
// the tool's stdout payload (already JSON-stringified by the wasm side
// for object-shaped tools); Error is the wasm-side error string when the
// tool reported failure. Both empty = a tool that returned no output and
// no error (rare; mostly side-effect-only tools).
type ToolCallResult struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// ToolDescriptor is one entry in the tool.list response. Mirrors the
// shape of `stado tool list --json` so clients can reuse rendering.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Canonical   string          `json:"canonical"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Class       string          `json:"class"`
}

// ToolListResult is the tool.list payload. The list is the unfiltered
// daemon-side registry minus tools disabled in [tools].disabled — the
// caller's --tools allowlist is applied only at dispatch time, not
// here, so operators querying the catalogue see what's available.
type ToolListResult struct {
	Tools []ToolDescriptor `json:"tools"`
}

// SessionDescriptor is one entry in the session.list response. Kind
// distinguishes pty / browser / lsp / etc.; Summary is a short human-
// facing description of what the session represents.
type SessionDescriptor struct {
	Kind      string    `json:"kind"`
	ID        uint64    `json:"id"`
	Summary   string    `json:"summary"`
	Alive     bool      `json:"alive"`
	StartedAt time.Time `json:"started_at"`
	ProjectID string    `json:"project_id,omitempty"`
}

// SessionListParams scopes the session.list response to one project by
// default. AllProjects=true returns every session across the daemon —
// reserved for `stado daemon status --all` operator-facing tooling.
type SessionListParams struct {
	ProjectID   string `json:"project_id,omitempty"`
	AllProjects bool   `json:"all_projects,omitempty"`
}

// SessionListResult is the session.list payload.
type SessionListResult struct {
	Sessions []SessionDescriptor `json:"sessions"`
}

// SessionKillParams identifies one session for destruction. ProjectID
// must match — cross-project kill is rejected (operators with the all-
// projects view use SessionKill with the explicit project_id).
type SessionKillParams struct {
	Kind      string `json:"kind"`
	ID        uint64 `json:"id"`
	ProjectID string `json:"project_id"`
}

// SessionKillResult acknowledges the kill. False here means the session
// was not found (already gone); the daemon does not error on that case
// to keep kill idempotent.
type SessionKillResult struct {
	OK bool `json:"ok"`
}
