---
ep: 50
title: Broker — privileged session-construction and policy-validation layer
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Draft
type: Standards
created: 2026-05-27
see-also: [30, 32, 37, 38, 42]
history:
  - date: 2026-05-27
    status: Draft
    note: >
      Phase 1 of stado's v1 security architecture rollout. Plumbing only —
      sandbox enforcement (phase 2), mount table (phase 3), capability
      ceiling (phase 4), trust-root + audit-trace writer (phase 5),
      provenance/taint (phase 6), ssh-agent + git sub-agent (phase 7),
      session-tree-as-broker-client (phase 8) follow.
---

# EP-50: Broker

## Summary

stado v1 introduces a **broker** process responsible for two
concerns:

1. **Policy validation.** Every request to create a session or
   construct a sandbox is checked against a loaded policy file
   before any action is taken.
2. **Session-creation mediation.** Orchestrator surfaces (TUI,
   `stado run`, `stado headless`, `stado acp`, `stado mcp-server`,
   `stado tool run`) ask the broker for a session over a typed
   IPC; the broker projects the request to a ceiling, constructs
   the (eventual) sandbox, and returns a handle.

The broker is implemented as an evolution of the existing
long-running per-user `stado daemon` process (`internal/daemon/`).
Today's daemon hosts PTY sessions, browser cookie jars, LSP
connections, and cached wasm modules across single-shot
`stado tool run` invocations. The broker grows out of it by
picking up policy validation + session construction in addition
to that state-hosting role — same UDS, same JSON-RPC transport,
new `broker.v1.*` method namespace alongside the existing
`tool.call` / `daemon.*` methods.

## Motivation

DESIGN.md §"Broker" sets the architectural model in detail; the
short version: the LLM is in the hot path of indirect prompt
injection, so anything that holds raw privilege adjacent to the
model is one prompt-injection turn away from misuse. Splitting
out a small, narrow, privilege-holding component that the
orchestrator can only *request* from — never directly invoke —
bounds the blast radius. A fully compromised orchestrator (model
+ plugins + TUI + headless transport) can still only obtain what
the operator's global policy allows.

## Scope (phase 1)

This EP records what landed in phase 1 of the v1 security
architecture rollout (sub-phases 1a–1h on branch
`sec/v1-architecture`). Subsequent phases iterate; this EP is
the canonical reference for the broker's surface as it stands at
the end of phase 1.

**In scope:**

- `internal/broker/` package: `Service`, `Policy`, `SessionHandle`,
  `Purpose`, `Profile`, `CapabilityRequest`, `Decision`,
  `DecisionWriter`/`DecisionRecord`, `DispatchError`.
- `broker.v1.*` JSON-RPC method namespace:
  - `broker.v1.session.create`
  - `broker.v1.session.terminate`
  - `broker.v1.toolrun.sandbox`
  - `broker.v1.policy.query`
- `internal/daemon/` integration: `ServerOpts.BrokerDispatcher`
  field; `MethodBrokerPrefix` for routing; broker error codes
  `-32020..-32027`.
- `cmd/stado/broker_bridge.go` + `broker_client.go`: the
  daemon-side bridge from `broker.Service.Dispatch` to
  `daemon.BrokerDispatcher`, and the orchestrator-side
  `attachToBroker` / `BrokerSession` helpers.
- Policy format: TOML at `$XDG_CONFIG_HOME/stado/policy.toml`,
  with a binary-embedded permissive default (every purpose /
  profile / no-plugin-override admits) so phase 1 is a
  behavioural no-op for existing users.
- CLI surface change: `--sandbox-fs` removed, `--no-sandbox` added
  with inverted polarity. Pre-1.0 breaking change, no deprecation
  alias (per the operator's "no kid gloves" rule).
- Orchestrator entry-point wiring: TUI / `stado run` / headless /
  ACP / mcp-server all attach via `attachToBroker` when
  `STADO_BROKER_ATTACH=1` is set (phase-1 opt-in; phase 2 flips
  the default).
- End-to-end tests in `cmd/stado/broker_e2e_test.go` exercising
  the full attach round-trip + deny path + test-binary refusal
  Skipped path.

**Out of scope for phase 1 (deferred to later phases):**

- Actual sandbox enforcement of the projected ceiling (phase 2).
- Mount-and-namespace invariant table in code + CI assertion
  (phase 3).
- Effective-set narrowing during a session (phase 4).
- Trust-root mounted read-only inside the agent's namespace +
  broker-owned trace writer (phase 5).
- Provenance/taint tagging at ingestion (phase 6).
- ssh-agent socket + git sub-agent flow (phase 7).
- `stado session tree` ported to broker client (phase 8).

## Design

### Trust model

The orchestrator is treated as **hostile**. The LLM is assumed
reachable by indirect prompt injection through any of the
content-bearing surfaces it touches; the plugin host runs
operator-installed code which is signed but is not the broker's
trust root; the user-facing surfaces marshal arbitrary content
for display. A fully compromised orchestrator — model, plugins,
TUI, headless transport — can only do what the broker permits.

In particular: a fully compromised orchestrator can still only
*request* sessions, and the broker can still only grant what the
global policy already permits.

### IPC contract

Transport: existing newline-delimited JSON-RPC 2.0 over UDS at
`$XDG_RUNTIME_DIR/stado/daemon.sock` (mode 0700). Same socket as
today's daemon — the broker shares it.

Invariants (DESIGN.md §"Broker" → "IPC channel"):

- **Narrow.** Speaks in typed request/response messages for
  enumerated broker operations. Not a general RPC surface.
- **Typed.** Messages are structured, validated, and rejected on
  unknown fields. The broker uses `DisallowUnknownFields` strict
  unmarshal at every entry point — a typo'd field is an error,
  not a silently ignored value.
- **Un-spoofable.** Mode 0700 UDS — only the broker's owner
  (the operator's stado process group) can write to it.

Method namespace: `broker.v1.*`. The `v1` prefix leaves room for
v2 schema evolution without breaking v1 clients.

### Methods

#### `broker.v1.session.create`

Constructs a session for an orchestrator (TUI / run / headless /
ACP / mcp-server) or sub-agent (`spawn_agent` — wired in phase 4).

Request:

```jsonc
{
  "purpose": "main-chat" | "subagent" | "tool-run",
  "profile": "default" | "hardened" | "no-sandbox",
  "cwd": "/abs/path",
  // sub-agent only:
  "role": "explorer" | "worker",
  "mode": "read_only" | "workspace_write",
  "write_scope": ["./pkg/foo", "./pkg/bar"],
  "plugin_name": "fs.read"  // tool-run only
}
```

Response (admit):

```jsonc
{
  "session_id": "<32-char hex>",
  "purpose": "main-chat",
  "ceiling": {"FSRead": ["/"], "FSWrite": ["/work", "/tmp"], ...},
  "trace_ref": "refs/sessions/<id>/trace",
  "created_at": "2026-05-27T11:00:00Z",
  "rule": "purpose:main-chat"
}
```

Response (deny): JSON-RPC error with code
`ErrCodeBrokerPolicyDeny` (-32020) and a message identifying the
rule that fired.

#### `broker.v1.session.terminate`

Idempotency contract:
- First terminate → `{ok: true}`.
- Second terminate against same SessionID → error
  `ErrCodeBrokerSessionTerminated` (-32024).
- Terminate against unknown SessionID → error
  `ErrCodeBrokerSessionNotFound` (-32023).

#### `broker.v1.toolrun.sandbox`

Used by `stado tool run` (and equivalents) to ask the broker to
construct a transient sandbox for a single agent-less plugin
invocation. No session is allocated; no `trace` ref is opened.
Phase 1 returns an opaque handle; phase 2 wires real sandbox
construction.

#### `broker.v1.policy.query`

Debug + future-use endpoint. Returns the broker's decision for a
given `CapabilityRequest` without actually creating anything.
Used by `stado audit` / debug tooling.

### Error codes

Reserved range `-32020..-32029` for broker errors:

| Code     | Name                              | Meaning |
|----------|-----------------------------------|---------|
| -32020   | ErrCodeBrokerPolicyDeny           | Policy refused the request. |
| -32021   | ErrCodeBrokerInvalidPurpose       | Purpose value is not one of the declared enums. |
| -32022   | ErrCodeBrokerInvalidProfile       | Profile value is not one of the declared enums. |
| -32023   | ErrCodeBrokerSessionNotFound      | SessionID was never minted by this broker. |
| -32024   | ErrCodeBrokerSessionTerminated    | SessionID was minted but has been terminated. |
| -32025   | ErrCodeBrokerPolicyLoad           | Policy file failed to load at broker startup. |
| -32026   | ErrCodeBrokerInvalidParams        | Params validation failed (typo'd field, malformed JSON, etc.). |
| -32027   | ErrCodeBrokerInternal             | Broker-internal failure (mint failed, etc.). |

The codes are mirrored at the `internal/daemon/protocol.go` layer
so daemon-side handlers can reference them without importing
`internal/broker` (avoids upward dependency from the protocol
layer to the policy layer).

### Policy file format

TOML at `$XDG_CONFIG_HOME/stado/policy.toml`. Binary-embedded
default at `internal/broker/policy_default.toml`. Schema:

```toml
default = true       # global fallback when no more-specific rule fires.

[purpose]
main-chat = true
subagent = true
"tool-run" = true

[profile]
default = true
hardened = true
"no-sandbox" = true

[plugin]
# operator-set per-plugin overrides; default is empty (no overrides).
# example denying a plugin:
#   "shell.spawn" = false
```

Resolution order (first matching rule wins): plugin override (for
tool-run requests with `plugin_name` set) → purpose → profile →
`default`. The Decision's `Rule` field names which fired:
`plugin:<name>`, `purpose:<name>`, `profile:<name>`, or
`default`.

Unknown purpose/profile keys at load time are errors (no silent
typo tolerance). Plugin keys are NOT validated against an enum
(forward declarations of not-yet-installed plugins are
legitimate).

### Daemon convergence

Today's `stado daemon` already provides what the broker needs:
long-running per-user process, typed IPC over UDS, auto-spawn
pattern, dispatcher infrastructure, idle timeout, PTY watchdog.
Phase 1 adds the new `broker.v1.*` methods as a peer to the
existing `tool.call` / `daemon.*` / `session.*` methods. Phase 2
extends the daemon's role further (constructing real sandboxes,
not just routing dispatch).

What's deliberately NOT changed in phase 1:
- The internal/sandbox proxy code (`internal/sandbox/proxy.go`)
  per operator ruling — preserved byte-equal.
- Existing `daemon.*` methods.
- PTY hosting, browser cookie jars, LSP connections, wasm caching.
- Idle timeout + watchdog reaping.

### Orchestrator-side helper

`cmd/stado/broker_client.go`:

- `attachToBroker(ctx, purpose, profile, cwd)` → `(*BrokerSession,
  error)`. Auto-spawns the broker via `daemon.EnsureRunning`,
  dials, issues `broker.v1.session.create`, returns the handle.
- `BrokerSession.Close()` issues `broker.v1.session.terminate` +
  closes the connection. Idempotent on nil, Skipped, or
  already-closed.
- `STADO_BROKER_ATTACH=1` gates whether orchestrator entry points
  attach. Default off in phase 1 (existing tests stay green);
  phase 2 flips the default. Accepts `1`/`true`/`on`/`yes`.
- Test-binary refusal: `daemon.EnsureRunning` refuses to spawn
  itself as a daemon when running inside a Go test binary; the
  helper translates this into a Skipped session so existing
  entry-point tests don't need to build a real stado binary.

### `--sandbox-fs` → `--no-sandbox`

Pre-1.0 breaking change. The retired flag's polarity meant
"default = unsandboxed", which v1 reverses. The new flag's
polarity means "default = sandboxed; opt out to disable". No
deprecation alias — operators who reach for `--sandbox-fs` see a
"unknown flag" error rather than a misleading alias.

In `stado run`:
- Default (no flag) → `BwrapRunner` via `sandbox.Detect()` +
  Landlock writes-confined to launch cwd + /tmp. Broker is
  invoked with `ProfileDefault`.
- `--no-sandbox` → `NoneRunner` + no Landlock. Broker is invoked
  with `ProfileNoSandbox` (the broker still mediates the request;
  it just records the operator's explicit opt-out decision).

The launch cwd is the writable boundary in BOTH modes — operators
expect `cd ~/projects/foo && stado run` to operate on
~/projects/foo, not on a per-session scratch worktree (DESIGN.md
§Sandbox → "Launch cwd is the default read-write boundary").

## Implementation phases

The phased rollout that produced this EP, on branch
`sec/v1-architecture` between 2026-05-27 commit `62e6aef` (phase
0 doc pass) and TBD (phase 1h, this EP). Commits:

- `62e6aef` — Phase 0: DESIGN.md + PLAN.md doc pass.
- `348f6d6` — Phase 1a: internal/broker/ skeleton + daemon
  BrokerDispatcher wiring.
- `85abf57` — Phase 1b/c + partial 1d: policy.toml format +
  loader + Service-level tests.
- `95bc1f8` — Phase 1d/e: daemon-IPC integration tests +
  brokerDispatcherBridge + daemon wiring.
- `a01a1ca` — Phase 1f-partial: broker_client.go + stado run
  attach.
- `10be819` — Phase 1f-complete: TUI / headless / ACP /
  mcp-server attach.
- `c15f61f` — Phase 1g: --sandbox-fs retired, --no-sandbox added.
- *(this commit)* — Phase 1h: e2e tests + this EP.

## Testing

- `internal/broker/*_test.go`: unit tests for Policy.Evaluate
  precedence, Service.CreateSession/TerminateSession/LookupSession,
  decision logging, atomic SetPolicy, TOML loader strict
  validation.
- `cmd/stado/daemon_broker_test.go`: 8 integration tests over
  real UDS daemon (start in goroutine, dial, dispatch broker.v1.*
  methods, assert codes/payloads). Covers admit, deny,
  invalid-purpose, unknown-field-rejection, terminate cycle,
  toolrun.sandbox admit + plugin-level deny, policy.query
  round-trip, nil-dispatcher backward compat.
- `cmd/stado/broker_client_test.go`: 5 unit tests for opt-in
  parsing, Skipped paths, Close idempotency.
- `cmd/stado/broker_e2e_test.go`: 4 e2e tests that exercise the
  full attachToBroker → daemon → broker → response → Close cycle
  through the orchestrator-side helper. Covers admit,
  no-sandbox profile, deny-by-policy, no-broker-running Skipped.

## Future phases (not in this EP)

| Phase | Scope |
|-------|-------|
| 2 | Sandbox-first default execution. `STADO_BROKER_ATTACH` flipped to default-on; broker's returned ceiling threaded back into the Executor/Runner; TUI startup announcement. |
| 3 | Mount-and-namespace invariant table enforced in code + CI assertion. `~/.ssh/config` default-profile decision point resolved. |
| 4 | Session capability model in code. Ceiling/effective vocabulary applied to `Policy`. Drop-only effective set. Widening-via-fork. |
| 5 | Trust-root + audit-trace writer invariants. Trust ring + signing keys mounted RO. Broker owns sole writable handle to sidecar. Broker-decision log at canonical path. |
| 6 | Provenance / taint tagging at ingestion. |
| 7 | ssh-agent + git sub-agent. (Last per operator ruling — most tuning expected.) |
| 8 | `stado session tree` as broker client. |

## References

- DESIGN.md §"Broker" — architectural model.
- DESIGN.md §"Sessions and sub-agents" — security atom + ceiling
  vocabulary.
- DESIGN.md §"Sandbox" — sandbox-first default, mount-and-
  namespace invariant table, launch cwd boundary, `--no-sandbox`
  flag.
- PLAN.md §"v1 security architecture rollout" — phased schedule.
- `.agent/specs/open/v1-phase1-broker.md` — phase 1 spec with
  AC1.1–1.11 acceptance criteria.
- `.agent/notes/brainstorms/v1-phase1-broker.md` — design choices
  + open questions.
- `.agent/notes/brainstorms/v1-phase1-daemon-integration-map.md`
  — daemon integration surface map.
- `.agent/decisions/2026-05-27-v1-security-architecture.md` —
  operator decision record for the v1 architecture.
