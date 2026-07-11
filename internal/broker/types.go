package broker

import (
	"time"

	"github.com/foobarto/stado/internal/sandbox"
)

// Purpose identifies what a session is being created for. The broker
// uses purpose + profile to project a ceiling, and the audit log
// records the purpose for forensic walk-back.
type Purpose string

const (
	// PurposeMainChat is the operator's primary conversation session.
	// Exactly one per stado invocation; lives for the binary's runtime.
	PurposeMainChat Purpose = "main-chat"

	// PurposeSubagent is a child session spawned by an active agent
	// via spawn_agent. Short-lived, ceiling mechanically projected
	// from the spawn request's role/mode/write_scope.
	PurposeSubagent Purpose = "subagent"

	// PurposeToolRun is a transient sandbox for `stado tool run`.
	// Not a session in the agent sense: no trace ref, no message
	// history. The broker mediates the sandbox construction to keep
	// the trust path uniform.
	PurposeToolRun Purpose = "tool-run"
)

// Valid reports whether p is one of the declared purpose values.
func (p Purpose) Valid() bool {
	switch p {
	case PurposeMainChat, PurposeSubagent, PurposeToolRun:
		return true
	}
	return false
}

// Profile selects between blessed sandbox-mount profiles. The
// difference is solely the tightness of the mount table (DESIGN.md
// §"Sandbox" → "Mount-and-namespace invariant table").
type Profile string

const (
	// ProfileDefault is the everyday profile: launch cwd + /tmp
	// read-write, $HOME read-only with credential-bearing subpaths
	// masked. Used unless the operator explicitly selects another.
	ProfileDefault Profile = "default"

	// ProfileHardened is a stricter projection of the same mount
	// model: synthesised minimal ssh config, tighter env scrub,
	// no $HOME mount outside the launch cwd.
	ProfileHardened Profile = "hardened"

	// ProfileNoSandbox is the explicit operator opt-out (via the
	// `--no-sandbox` flag). The broker still mediates the request
	// so the decision is recorded; the returned ceiling configures
	// NoneRunner. Equivalent to today's pre-v1 behaviour.
	ProfileNoSandbox Profile = "no-sandbox"
)

// Valid reports whether p is one of the declared profile values.
func (p Profile) Valid() bool {
	switch p {
	case ProfileDefault, ProfileHardened, ProfileNoSandbox:
		return true
	}
	return false
}

// CapabilityRequest is the input the broker evaluates against the
// loaded policy. It carries everything the broker needs to admit or
// deny without rewinding to defaults.
type CapabilityRequest struct {
	// Purpose is what this session/sandbox is being created for.
	Purpose Purpose

	// Profile selects the mount-table tightness.
	Profile Profile

	// CWD is the absolute working directory the orchestrator was launched in.
	// For subagents it is the already-materialized managed child worktree;
	// relative write_scope entries are resolved against the parent handle's CWD.
	CWD string

	// PluginName, when Purpose is PurposeToolRun, names the wasm
	// plugin the operator asked to run. Empty for other purposes.
	PluginName string

	// Role / Mode / WriteScope, when Purpose is PurposeSubagent,
	// carry the spawn_agent request shape. Empty for other purposes.
	Role       string
	Mode       string
	WriteScope []string

	// SessionID, when set, identifies the requesting session whose
	// taint state should factor into the decision (phase 6). Used by
	// Service.EvaluateWithTaint to apply stricter rules for
	// capability-grant requests from tainted contexts. Empty makes
	// the evaluation taint-agnostic.
	SessionID string
}

// Decision is the broker's verdict on a CapabilityRequest. Admit
// is the binary outcome; Rule is the policy rule that fired (used
// in the decision log + debug surfaces); Reason is operator-facing
// text returned in error responses on Admit=false.
type Decision struct {
	Admit  bool
	Rule   string
	Reason string
}

// SessionHandle is what the broker returns on a successful
// session.create. The orchestrator stores this and applies the
// ceiling to itself; it includes the handle when terminating.
//
// The handle is opaque to the orchestrator beyond the few fields
// it actively uses (SessionID, Ceiling, Effective, TraceRef). The
// broker may internally associate further state with the SessionID.
type SessionHandle struct {
	// SessionID is the broker-minted opaque identifier for this
	// session. Stable for the session's lifetime.
	SessionID string

	// Purpose is echoed from the request for the orchestrator's
	// convenience.
	Purpose Purpose

	// Profile is retained so a child request cannot switch its parent to a
	// weaker sandbox profile.
	Profile Profile

	// CWD is the broker-owned working directory for the handle. For ordinary
	// subagents the broker reserves this directory itself before returning the
	// handle, so the orchestrator cannot redirect a child grant at another
	// session's worktree.
	CWD string

	// Ceiling is the immutable maximal capability set for this
	// session, derived at session-creation from Purpose+Profile
	// (and, for sub-agents, role/mode/write_scope). The ceiling
	// never changes for the life of the session.
	Ceiling sandbox.Policy

	// Effective is the capability set the session currently holds.
	// Initialized equal to Ceiling at session-creation; narrows
	// monotonically via Service.NarrowEffective. Drop-only —
	// widening requires forking a new session (see DESIGN.md
	// §"Sessions and sub-agents" → "Capability ceiling and
	// effective set").
	Effective sandbox.Policy

	// TraceRef is the git ref name the broker will append trace
	// events to (phase 5 wires append-via-broker; phase 1 records
	// the ref name only). Empty for PurposeToolRun.
	TraceRef string

	// ExpiresAt is the deadline beyond which the broker will
	// refuse further operations against this handle. Zero =
	// no broker-enforced expiry (operator-driven only).
	ExpiresAt time.Time

	// CreatedAt is the wall-clock moment of admission.
	CreatedAt time.Time
}
