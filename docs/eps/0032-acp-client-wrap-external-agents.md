---
ep: 0032
title: ACP client — wrap external coding-agent CLIs as stado providers
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Phase A landed in v0.27.0 with `gemini --acp` end-to-end working through stado's TUI/run.
  - date: 2026-05-04
    status: Draft
    note: Phase B design locked at A+C (ACP fs capabilities + MCP server mount). Decision log D6/D7 added.
  - date: 2026-05-04
    status: Draft
    note: Phase B implementation v1 landed across 5 commits (c1f235d, cd18daa, 73bc1f1, edc854e, cdd4938) — inbound dispatch, fs/* translator, universal mcp-server upgrade (Executor + sandbox), MCP mount builder, provider wiring under Tools="stado". Terminal/* ACP methods deferred (MCP-mounted bash covers shell routing). End-to-end smoke against a real wrapped agent pending dogfood.
see-also: [0005, 0006]
---

# EP-0032: ACP client — wrap external coding-agent CLIs as stado providers

> **Status: Draft.** Phase A is implemented and shipping in v0.27.0.
> Phase B (tool-host capability) and Phase C (per-call hybrid) are
> design-locked here but not yet implemented.

## Problem

stado already speaks ACP as a server (`internal/acp/server.go`) so
IDEs like Zed can drive stado as their backing agent. The
inverse direction — stado as ACP **client** wrapping other coding-
agent CLIs (claude / gemini / codex / opencode / aider) — was
unimplemented.

There's significant operator value in stado-as-ACP-client:

- **Multi-session UI on top of single-session CLIs.** Most agent
  CLIs hold one conversation per process. Stado already has
  multi-session switching (`stado session`) + persistent state +
  audit refs; wrapping a CLI brings that UX layer to ANY agent.
- **Cross-agent context handoff.** Copy a Claude reply into a
  Gemini session for a "second-opinion" turn without manually
  spawning two terminals.
- **Provider-uniform UX.** One TUI, one keybinding set, one
  status bar regardless of which underlying agent is producing
  the response.
- **Audit boundary.** Stado records "user sent X to Claude,
  Claude returned Y" in the audit log even when the wrapped
  agent's internal tool calls aren't visible.

## Design (phase A — landed)

### Wrapped-agent-owns-tools

The wrapped CLI runs its own tool stack (`gemini --acp` uses
gemini's bash, fs, grep, web_fetch). Stado is a chat-routing UI
on top — it does NOT expose its bundled tool registry to the
wrapped agent in phase A.

Tradeoff: stado's audit trail is less granular for ACP-wrapped
sessions. Stado records the boundary (user prompt → agent
response) but not the agent's internal tool call sequence.
That's the intentional trust boundary: when you hand off to a
third-party agent, you trust it with the granular trail. Stado
audits the BOUNDARY, not the internals.

### Subprocess lifecycle

- Provider instance lazy-spawns the wrapped subprocess on first
  `StreamTurn` call (so a config with N ACP providers doesn't
  fork N processes at boot).
- The subprocess runs `<binary> <args>` (typically `gemini --acp`
  or `opencode acp`). Stdin/stdout become the JSON-RPC 2.0
  transport; stderr is forwarded to stado's stderr so OAuth
  prompts / auth-required errors surface.
- After spawn, stado sends `initialize` then `session/new`. The
  resulting `sessionId` is cached on the Provider and reused for
  every subsequent `session/prompt`.
- `Provider.Close()` sends `shutdown` + reaps the subprocess.
  Without an explicit close, the subprocess is reaped when its
  stdin closes (i.e. when stado exits).

### Wire format — Zed-canonical ACP dialect

Stado's existing ACP server (`internal/acp/server.go`) speaks an
older v0 dialect with flat `agentName` / `agentVersion` /
`capabilities` shape. The new client speaks the **canonical
Zed-spec shape** (`agentInfo: {name, title, version}` /
`agentCapabilities: {...}`) because that's what real-world
agents on the market emit. The two coexist: stado-as-server
keeps the older shape for back-compat with anything depending
on it; stado-as-client uses the canonical shape for
forward-compat with the agent ecosystem.

Method names (canonical):
- `initialize` — handshake, advertise client capabilities
- `session/new` — create session with `cwd` + `mcpServers: []`
  (the empty array is REQUIRED by gemini-cli's strict zod
  validation, NOT optional)
- `session/prompt` — send a turn; text/tool events stream as
  notifications
- `session/update` — server → client notification carrying
  `agent_message_chunk` / `agent_message` / `tool_call` /
  `tool_call_update` / `agent_thought_chunk` / etc.
- `shutdown` — graceful teardown

### Content-block shape variance

Per the canonical spec, `update.content` can be either a single
`{type, text}` block OR an array of blocks. Different agents
emit different shapes for the same `sessionUpdate` kind:

| Agent | `agent_message_chunk` content shape |
|-------|-------------------------------------|
| gemini-cli | single object: `{"type":"text","text":"..."}` |
| canonical spec | array: `[{"type":"text","text":"..."}]` |

`extractTextBlocks` in the provider normalises both. New agents
adding novel content types (image, audio) will require routing
into the appropriate `agent.Event` kind — for phase A non-text
blocks are dropped silently (TODO for phase D multimodal).

### Config schema

```toml
[acp.providers.gemini-acp]
binary = "gemini"
args   = ["--acp"]

[acp.providers.opencode-acp]
binary = "opencode"
args   = ["acp"]
```

Set `[defaults].provider = "gemini-acp"` (or pass
`--provider gemini-acp` on the CLI) to make it the default. The
wrapped agent inherits the parent shell's auth env (e.g.
`GEMINI_API_KEY` for gemini, OAuth tokens stored in
`~/.gemini/`). Stado doesn't manage credentials for wrapped
agents — that stays the operator's job per the trust model.

### Provider implementation

`internal/providers/acpwrap/provider.go` implements
`agent.Provider` over `internal/acp/client.go`. Capabilities()
returns conservative defaults (no prompt cache, no thinking,
unknown context window) — the wrapped agent and its underlying
model handle their own capability negotiation; stado just routes.

## Phase B (planned) — opt-in tool-host capability

Add a `tools = "stado"` option in `[acp.providers.<name>]`:

```toml
[acp.providers.gemini-acp-stado-tools]
binary = "gemini"
args   = ["--acp"]
tools  = "stado"   # default: "agent" (phase A)
```

When set, stado advertises tools to the wrapped agent through
**both** standard ACP channels — the bounded
`fs.readTextFile`/`fs.writeTextFile`/`terminal` client
capabilities at `initialize`, AND a stado-hosted in-process MCP
server mounted via `session/new.mcpServers` exposing the full
stado tool registry. Two channels means full audit granularity
without the capability cliff that fs/terminal-only would impose.

### Why both channels (D6)

ACP defines two distinct mechanisms for client-side tool exposure:

1. **`initialize.clientCapabilities`** — fixed capability flags
   (`fs.readTextFile`, `fs.writeTextFile`, `terminal`) advertised
   at session start. Spec-compliant agents prefer these when
   advertised because the spec positions them as the canonical
   "trusted client filesystem/shell" path. Bounded surface; every
   ACP-compliant agent supports them.
2. **`session/new.mcpServers`** — per-session array of MCP server
   descriptors. Wrapped agent connects, calls MCP `tools/list`,
   gets full tool definitions (name, description, JSON schema)
   that flow into the LLM's system prompt as standard MCP tools.
   Tool calls flow over MCP, not ACP.

Channel 1 covers the canonical fs+shell path with audit. Channel 2
covers everything else stado offers (grep, glob, edit, web fetch,
project-aware tools) so a wrapped agent forced to use only
stado-routed tools has a useful surface, not three primitives.

### Tool-call routing semantics

ACP-mounted capabilities and MCP-mounted tools are **additive,
not exclusive** — the wrapped agent's built-in tools remain
available unless its CLI is invoked with built-in-disabling flags.
The agent's LLM sees its built-ins PLUS stado's advertised
capabilities PLUS stado's MCP-mounted tools, and decides per-call
which to use. Spec-compliant agents bias toward calling ACP
capabilities (canonical client-trusted path); MCP tools are
treated as parity with built-ins.

Phase B does **not** force the wrapped agent to route through
stado. To achieve "all tool calls audited through stado," the
user adds the wrapped agent's own built-in-disabling flag to the
`[acp.providers.<name>] args` array (gemini, claude, opencode all
expose such a flag in their CLI; the exact form is per-agent and
out of stado's control). With built-ins disabled, the agent has
only the ACP+MCP channels stado provides — every tool call is
audited.

### Implementation hooks

- `ClientCapabilities` in `internal/acp/client.go:220` grows
  `fs.readTextFile`, `fs.writeTextFile`, `terminal` capability
  fields. Populated at `initialize` when `tools = "stado"`.
- `acp.Client.readLoop` (`client.go:95`) currently handles
  inbound responses (matched to pending Calls) and inbound
  notifications (`session/update`). Phase B adds a third case:
  inbound **requests** (method + id set), dispatched to a tool
  handler that returns a JSON-RPC response with the same id. The
  handler routes by method:
  - `fs/read_text_file` → stado's read tool
  - `fs/write_text_file` → stado's write tool
  - `terminal/create` + lifecycle (`terminal/output`,
    `terminal/release`) → stado's bash/process-execution tool
- New `internal/acp/toolhost.go` packages the stado-tool-side
  dispatch, reusing the inverse-direction logic already in
  `internal/acp/server.go` where applicable. Stado was always
  going to be both an ACP server (Zed-as-client → stado-as-agent)
  and now an ACP client with capabilities (wrapped-agent → stado);
  the dispatch logic is symmetric.
- A stado-internal MCP server (reuses EP-0010's MCP server
  infrastructure) starts when `tools = "stado"` is set on a
  provider, exposing the full tool registry. Its connection
  descriptor populates `session/new.mcpServers`.
- Tool calls flowing through either channel pass through stado's
  existing permission/sandbox stack: capability rules
  (`Bash(...)` allowlist, `Edit(...)` paths), sandbox detection,
  audit log emission. The wrapped agent is treated as an untrusted
  caller — same policy semantics as a remote MCP client of stado.
- Audit gains per-tool-call granularity for the calls that DO
  route through stado. Calls the wrapped agent makes via its own
  built-ins remain opaque (D1's trust boundary still applies for
  the agent's own internal stack).

## Phase C (planned) — per-call hybrid

`tools = "merge"` exposes BOTH stado's tools AND the agent's
native ones; the agent picks per call. This is the most
flexible mode but also the most behaviourally complex (two
implementations of `bash` available; which one fires depends on
the wrapped agent's policy).

Defer until B has been in use long enough to know whether merge
is genuinely useful or just confusing.

## Non-goals

- **Stado as Zed editor's backing agent for ACP-wrapped sessions.**
  When stado wraps gemini-acp and Zed connects to stado-acp-server,
  Zed would talk to gemini transitively through stado. Possible
  but not in scope; phase A keeps stado-as-server and
  stado-as-client cleanly separate.
- **Auth management for wrapped agents.** Each agent handles its
  own credentials. Stado will surface auth-required errors via
  forwarded stderr but won't store tokens or run OAuth flows.
- **Multimodal blocks.** Image/audio content blocks land in
  notifications but get dropped silently in phase A. Phase D
  routes them through stado's existing multimodal handling.
- **Multiple sessions per wrapped subprocess.** Each Provider
  owns one subprocess + one session; switching stado sessions
  means spawning another subprocess. Could batch later if it
  becomes a real cost.

## Test strategy

- Unit: `provider_test.go` covers translateUpdate's full
  payload-shape matrix (single-block, array-block, tool-call
  breadcrumb, agent_thought_chunk, available_commands_update
  drop, malformed JSON drop, empty text drop).
- Integration: smoke-tested against `gemini --acp` end-to-end:
  `stado run --provider gemini-acp --prompt "Reply HELLO"`
  produces "HELLO" in stado's stdout. Reproducible test config
  in the dev loop, not auto-run in CI (requires gemini auth).
- Regression: detection-only counterpart in
  `internal/integrations/` ensures `stado integrations` reports
  gemini/opencode as ACP-capable so users can discover what's
  available before configuring.

## Decision log

### D1. Phase A: wrapped-agent-owns-tools as the default

- **Decided:** Phase A ships with no tool-host capability
  exposed by stado. Wrapped agents use their own tools.
- **Alternatives:** ship phase A + phase B simultaneously (tool
  host opt-in from the start); ship phase A but with a
  half-implementation of phase B.
- **Why:** the simplest end-to-end path that preserves audit
  semantics. Tool-host capability adds a meaningful surface area
  (inbound RPC handling, tool-call audit-log routing,
  capability-negotiation back-channel) that's better as its own
  iteration. Operators who want stado's tools can keep using
  native providers; the wrapped path is for operators who want
  the wrapped agent's tools + stado's UI.

### D2. Speak Zed-canonical ACP, not stado's older v0 dialect

- **Decided:** the new client speaks the `agentInfo` /
  `agentCapabilities` canonical shape per
  https://agentclientprotocol.com/.
- **Alternatives:** make stado's existing server speak both
  dialects, then have the client speak the older one too;
  upgrade everything to canonical at once.
- **Why:** the wrapped-agent ecosystem (gemini, opencode, future
  zed-compatible claude wrappers) already uses canonical. The
  client MUST match what's on the wire today. Stado's server can
  be upgraded later without breaking the client; the inverse
  isn't true.

### D3. `mcpServers: []` is REQUIRED in session/new

- **Decided:** the SessionNewParams struct sends `mcpServers`
  as a non-omitempty empty array.
- **Why:** gemini-cli uses zod with `.array(...)` validation
  and rejects undefined. The canonical spec lists mcpServers as
  optional but real-world agents disagree. Sending `[]`
  satisfies both.

### D4. `extractTextBlocks` accepts both single-block and array

- **Decided:** the content-extraction helper tries single-object
  shape first, falls back to array.
- **Why:** gemini emits single-object for `agent_message_chunk`;
  the canonical spec uses array; opencode (untested) likely
  uses array. Accepting both maximises agent compatibility
  without wire-spec interpretation choices we can't enforce.

### D5. Subprocess stderr forwarded to stado's stderr

- **Decided:** wrapped agent's stderr is wired straight to
  os.Stderr.
- **Alternatives:** capture and discard; route through stado's
  log infrastructure; surface in TUI status bar.
- **Why:** wrapped agents print critical info on stderr —
  gemini's "Log in with Google" OAuth URL, opencode's auth-key
  prompts, all error diagnostics. Discarding hides setup
  failures; routing through stado's log adds layout latency.
  Forwarding direct is the lowest-friction-for-operator option;
  later we can add a `--quiet-wrapped-stderr` flag if the
  noise becomes a real problem.

### D6. Phase B uses both ACP capability advertisement AND MCP server mount

- **Decided:** when `tools = "stado"`, advertise standard ACP
  client capabilities (`fs.readTextFile`, `fs.writeTextFile`,
  `terminal`) at `initialize` AND mount a stado-hosted MCP server
  exposing the full tool registry via `session/new.mcpServers`.
- **Alternatives:** ACP capabilities only (bounded fs+shell
  surface; agents forced to use only stado-routed tools have
  3 primitives — capability cliff); MCP mount only (loses the
  spec's bias toward client-trusted fs/terminal path); custom
  ACP extension methods (non-standard, agents won't know about
  them, the wrapped LLM doesn't see the tools).
- **Why:** the user goal is "all tool calls audited through
  stado" with the option to force the wrapped agent into that
  mode (via the agent's own built-in-disabling CLI flag). ACP
  capabilities alone make that mode useless — the agent has only
  fs/shell, no grep/glob/edit/web. MCP-mount fills the gap with a
  standard mechanism every spec-compliant agent supports. Doing
  both takes the canonical fs/terminal-trust-bias path AND keeps
  the broader registry available, so forcing-stado-only doesn't
  cripple the agent.

### D7. Wrapped agent treated as untrusted caller; stado permission stack applies

- **Decided:** every tool call routed through stado (via either
  ACP capabilities or the MCP-mounted server) passes through
  stado's existing permission/sandbox stack — capability rules,
  Bash/Edit allowlists, sandbox detection, audit emission.
- **Alternatives:** trust the wrapped agent implicitly because
  the user opted into `tools = "stado"`; lighter-weight
  permission scope just for ACP-routed calls.
- **Why:** the wrapped agent is a third-party process whose
  decisions stado cannot audit at the LLM level. Trusting it
  to bypass stado's sandbox/permission model would defeat the
  audit goal that motivated phase B in the first place. Same
  policy semantics as a remote MCP client of stado — the
  permission stack is provider-neutral. If the user wants the
  wrapped agent to operate with broader rights, that's an
  explicit permission rule (e.g. `Bash(git *)` allowlist), not
  an ambient trust grant.

## Related

- EP-0005 — capability-based sandboxing. Phase B's tool-host
  capability needs to honor the same sandbox the agent loop's
  bundled bash already uses.
- EP-0006 — signed WASM plugin runtime. Tool-host capability is
  conceptually similar to the wasm host imports — both expose
  stado-side helpers to an external runtime.
- `internal/integrations/` — detection registry for
  ACP-speaking agents. Phase A's wrapped providers map 1:1 to
  registry entries; future TUI work could surface "wrap this
  installed agent as a stado provider with one click" by
  combining detection + provider config write.
