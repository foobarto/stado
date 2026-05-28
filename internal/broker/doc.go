// Package broker is stado's privileged session-construction and
// policy-validation layer. It runs inside the long-running per-user
// daemon process and mediates session creation for the orchestrator
// (TUI, `stado run`, headless, ACP, MCP server, `stado tool run`).
//
// See DESIGN.md §"Broker" for the architectural model and
// .agent/specs/open/v1-phase1-broker.md for the phase-1 spec.
//
// Phase 1 wires the plumbing: typed JSON-RPC schema under the
// `broker.v1.*` namespace, a policy file at
// $XDG_CONFIG_HOME/stado/policy.toml with a permissive default, and
// session-handle minting. Subsequent phases tighten policy and
// connect the handle to actual sandbox enforcement, taint state,
// trust-root mediation, and ssh-agent delegation.
//
// The broker does NOT: hold an LLM, run plugin code, execute tools
// directly, or ingest untrusted input. It is small by design.
package broker
