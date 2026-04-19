# stado — Design

Companion to [`PLAN.md`](PLAN.md). PLAN is the phased roadmap + intent;
DESIGN is the concise as-built reference. When something contradicts,
PLAN describes where we're going, DESIGN describes where we are.

---

## One-paragraph description

stado is a sandboxed, git-native coding-agent runtime. A thin
provider interface (`pkg/agent`) fronts four direct LLM integrations
(Anthropic, OpenAI, Google, and a hand-rolled OpenAI-compatible client
that covers ollama/llama.cpp/vLLM/groq/openrouter/…). The agent loop
owns a git sidecar repository per user repo; every tool call the model
makes is committed to a per-session `trace` ref (audit log) and — if
mutating — to a `tree` ref (executable history). Signatures on every
commit make the refs tamper-evident. The TUI, an ACP server (for Zed),
a JSON-RPC headless daemon, and a single-shot `stado run` CLI all
compose the same runtime core.

---

## Component map

```
  ┌──────────────────── User surfaces ────────────────────┐
  │                                                       │
  │   TUI      stado run      stado acp     stado headless│
  │    │           │              │                │      │
  └────┼───────────┼──────────────┼────────────────┼──────┘
       │           │              │                │
       └───────────┴──────┬───────┴────────────────┘
                          │
                          ▼
              ┌───────────────────────┐
              │   internal/runtime    │
              │      (AgentLoop)      │
              └───────────┬───────────┘
                          │
         ┌────────────────┼────────────────┐
         ▼                ▼                ▼
   ┌───────────┐  ┌───────────────┐  ┌──────────────────┐
   │ pkg/agent │  │ internal/tools│  │internal/state/git│
   │(Provider) │  │  (Executor +  │  │  (Sidecar, refs, │
   │           │  │   Registry +  │  │    signatures,   │
   │           │  │   classifier) │  │   materialisation│
   └─────┬─────┘  └───────┬───────┘  └──────────────────┘
         │                │
   ┌─────┴─────┬──────┬───┴────────┐
   ▼           ▼      ▼            ▼
anthropic   openai  google     oaicompat
                                    │
                                    ▼
                        ┌────────────────────┐
                        │  internal/sandbox  │
                        │  (Policy, Runner,  │
                        │   landlock, proxy) │
                        └────────────────────┘
```

- **Provider interface**: one streaming method (`StreamTurn`) emitting a
  discriminated `Event` type. Opaque `Native` fields preserve
  provider-specific payloads (thinking signatures, reasoning content) so
  round-trips don't lose state.
- **Agent loop** (`runtime.AgentLoop`): turn-based — stream, collect
  tool calls, execute via `Executor`, append `role=tool` message, next
  turn, repeat until no tool calls. Bounded by `MaxTurns`.
- **Executor**: looks up tool by name, classifies (Mutating / NonMutating
  / Exec), runs it, writes trace commit always, tree commit conditionally.
  Metrics recorded via OTel instruments.
- **Sidecar**: one bare repo per user repo at
  `$XDG_DATA_HOME/stado/sessions/<repo-id>.git`, alternates-linked to the
  user's `.git/objects`. Zero object duplication.
- **Worktree**: per-session directory at
  `$XDG_STATE_HOME/stado/worktrees/<session-id>/` — plain file tree,
  materialised from and back to sidecar tree objects via
  `BuildTreeFromDir` / `MaterializeTreeToDir`.

---

## Request path: single user prompt → streamed turn

```
User types in TUI input
  └─ Enter
     └─ Model.startStream
        └─ ensureProvider (lazy, errors here are in-UI)
        └─ provider.StreamTurn(ctx, req)
            │
            ├── text deltas  → viewport blocks
            ├── thinking     → thinking block (with signature kept raw)
            └── tool_call_end→ pendingCalls queue

[stream done]
  └─ Model.onTurnComplete
     └─ flush assistant message (text + thinking + tool_uses)
     └─ any pending calls?
        ├─ yes → advanceApproval
        │        ├─ remembered-allow → execute immediately
        │        └─ prompt user y/n
        │            └─ executor.Run
        │                ├─ resolve tool + class
        │                ├─ run (in-proc or spawn via sandbox)
        │                ├─ write trace commit (always)
        │                ├─ write tree commit (if mutating/exec+diff)
        │                ├─ session.OnCommit → slog
        │                └─ return ToolResultBlock
        │        (queue drained) → toolsExecutedMsg
        │            └─ append role=tool Message
        │            └─ Model.startStream (next iteration)
        └─ no → stateIdle
```

---

## Provider interface (`pkg/agent`)

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    StreamTurn(ctx, TurnRequest) (<-chan Event, error)
}
```

**Messages** are lists of typed `Block`s (Text / ToolUse / ToolResult /
Image / Thinking). Exactly one pointer field per block. Ordering
matters — assistant messages often interleave text, thinking, and
tool_use blocks, and providers (especially Anthropic) reject rearranged
sequences.

**Events** are a discriminated union via `EventKind`:
`EvTextDelta · EvThinkingDelta · EvToolCallStart · EvToolCallArgsDelta
· EvToolCallEnd · EvCacheHit · EvCacheMiss · EvUsage · EvDone · EvError`.

**Capabilities** surface what a model supports — `SupportsPromptCache`,
`SupportsThinking`, `MaxParallelToolCalls`, `SupportsVision`,
`MaxContextTokens`. The agent loop can branch on these (not yet
exploited across all code paths; see PLAN §1.6).

`EvCacheHit` / `EvCacheMiss` and `Usage.CacheReadTokens` /
`CacheWriteTokens` are the canonical surface for prompt-cache telemetry;
§"Context management" defines the invariants around what may appear in
the cached prefix and how the hit/miss counts feed OTel metrics. The
events and usage fields are emitted today by every provider; the OTel
instruments they feed (`stado_cache_hit_ratio` etc.) are declared in
`internal/telemetry` but not yet wrapped at the call sites — see PLAN §6
for the remaining span-instrumentation work.

---

## Git-native state (`internal/state/git`)

### Refs

| Ref | What | Commit policy |
|---|---|---|
| `refs/sessions/<id>/tree` | executable history | mutating OR exec-with-diff |
| `refs/sessions/<id>/trace` | audit log | every tool call (empty tree) |
| `refs/sessions/<id>/turns/<n>` | turn boundary tag | tagged via `Session.NextTurn` |

### Commit message format

```
<tool>(<short-arg>): <summary>

Tool: write
Args-SHA: sha256:…
Result-SHA: sha256:…
Tokens-In: 1234
Tokens-Out: 567
Cache-Hit: true
Cost-USD: 0.0012
Model: claude-sonnet-4-5
Duration-Ms: 342
Agent: stado-tui
Turn: 3
Signature: ed25519:<base64>
```

Machine-parseable trailers; the `Signature` trailer is generated by
signing canonical bytes `stado-audit-v1\ntree <hash>\nparent <p1>\n…\n\n<body>` (body = message with any preexisting Signature
trailer stripped). Tampering with any of the covered fields invalidates
the signature — `stado audit verify` walks a ref and reports the first
invalid commit.

### Fork semantics

`stado session fork <parent-id>`:

1. Create child session id.
2. Resolve parent's tree-ref head (may be zero if parent never committed).
3. Seed child's tree-ref at the parent's head hash.
4. Materialise parent's tree into child's worktree.

The trace ref is NOT shared — it's session-local, an audit record of
that particular agent's actions.

`stado session revert <id> <commit-or-turns/N>` is the same mechanism
but rooted at an earlier point in history; produces a new child session,
leaves the parent untouched.

The user-facing contract for "forking from an earlier point" — the
two required paths (`session fork --at` and `session tree`), the
turn-reference syntax, and the promise that the parent is never
modified — is specified in §"Fork-from-point ergonomics" under
§"Context management".

---

## Tool runtime (`internal/tools`)

### Tool interface

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any       // JSON Schema for the model
    Run(ctx, args json.RawMessage, h Host) (Result, error)
}

// Optional — tools that want explicit mutation class.
type Classifier interface { Class() Class }
```

`Host` is the read-write surface tools use to reach the runtime.
`PriorRead` / `RecordRead` are the extensions required by §"Context
management" → "In-turn deduplication".

```go
type Host interface {
    Approve(ctx, ApprovalRequest) (Decision, error)
    Workdir() string
    PriorRead(key ReadKey) (PriorReadInfo, bool)
    RecordRead(key ReadKey, info PriorReadInfo)
}

// ReadKey identifies a read for deduplication.
type ReadKey struct {
    Path  string
    Range string
}

// PriorReadInfo is what Host.PriorRead hands back on a match.
type PriorReadInfo struct {
    Turn        int    // 1-indexed turn number when the prior read occurred
    ContentHash string // sha256 of the bytes returned to the model in that turn
}
```

`Host.PriorRead` returns the MOST RECENT prior read when multiple
exist. Implementations live in the TUI and headless surfaces and
delegate to a session-scoped read log maintained by the Executor.
Both the `ReadKey` input and `PriorReadInfo` output are structs so
future fields (hash algorithm, compression marker, …) don't force
signature churn.

`RecordRead` is the symmetric write side of `PriorRead`. Only the
`read` tool calls it; this is a convention enforced by documentation,
not by the interface itself. Other tools (`ripgrep`, `bash`, …) must
not call `RecordRead` even when they incidentally read files. The
Executor's in-memory log is the sole consumer; there is no
persistence.

**Return-value contract for `PriorRead`.** On `ok=true`, all fields of
`PriorReadInfo` must be populated (non-zero `Turn`, non-empty
`ContentHash`). On `ok=false`, callers must treat the returned
`PriorReadInfo` as undefined and inspect only `ok`. Future fields
added to `PriorReadInfo` follow the same rule — populated on success,
undefined on failure.

The `read` tool computes the content hash incrementally while reading
(via `io.MultiWriter` into both the output buffer and a `sha256.New()`
hasher), not as a post-read pass. Hash scope is the **targeted region
only**, not the full file for ranged reads — a range request + range
match is independent of bytes outside the range. One pass over the
bytes, one hash.

`ReadKey.Range` is a canonical form produced by the read tool: `""`
for a full-file read, `"<start>:<end>"` for a ranged read (both
inclusive, 1-indexed to match the tool's user-facing args). The read
tool is responsible for resolving any alternative input shapes into
this canonical form before constructing the `ReadKey`. Tests must
assert canonicalization for each input shape the tool accepts.

### Bundled tools (14)

| Tool | Class | Notes |
|---|---|---|
| `read` | NonMutating | args: `{path: string, start?: int, end?: int}`. `start`/`end` are 1-indexed, inclusive. Omit both for full-file read. `end` may be `-1` to mean EOF. |
| `write` | Mutating | |
| `edit` | Mutating | |
| `glob` | NonMutating | |
| `grep` | NonMutating | simple Go substring |
| `ripgrep` | NonMutating | shells out to `rg --json` |
| `ast_grep` | NonMutating | shells out to `ast-grep run --json` |
| `bash` | Exec | snapshot → run → diff |
| `webfetch` | NonMutating | HTTP GET |
| `read_with_context` | NonMutating | Go-aware import resolution |
| `find_definition` | NonMutating | LSP textDocument/definition |
| `find_references` | NonMutating | LSP textDocument/references |
| `document_symbols` | NonMutating | LSP textDocument/documentSymbol |
| `hover` | NonMutating | LSP textDocument/hover |
| *(MCP servers)* | varies | auto-registered from `[mcp.servers]` |

### Executor invariants

Per call, unconditionally:
1. classify → `Mutating | NonMutating | Exec`
2. time the call
3. `Registry.Get(name).Run(ctx, args, host)`
4. record `stado_tool_latency_ms` (instrument declared; call-site wrapping pending, see PLAN §6)
5. build `CommitMeta` trailers

Then:
- **trace ref**: always committed (even on failure; `Error:` trailer).
- **tree ref**: committed iff `Mutating` (success) OR `Exec` AND
  post-run tree hash differs from pre-run tree hash.

---

## Context management

Four separate concerns that must not be conflated: (1) prompt-cache
efficiency, (2) context-window overflow handling, (3) compaction, (4)
tool-output curation. Each has a different answer, and the answers
sometimes trade against each other.

**Philosophy.** Curation and caching are primary. Overflow handling is a
safety net. Compaction is strictly user-invoked — there is no automatic
summarizer, no background compactor, no threshold-triggered eviction.
When a session becomes unwieldy, the preferred recovery is
fork-from-an-earlier-point into a fresh session (see §"Fork semantics"),
not lossy in-place summarization. Forking must stay cheap and obvious so
users reach for it instead.

### Prompt-cache awareness

The turn prefix (system prompt + tool definitions + any session-static
header) is treated as a **stable byte-identical artefact** across
successive turns. Cache breakpoints — where the provider supports
explicit ones, as with Anthropic's `cache_control: ephemeral` via
`agent.TurnRequest.CacheHints` — are placed at the end of this prefix.

Rules, enforced at the code level:

- **Append-only history.** The agent loop never rewrites prior turns
  in place. `Model.msgs` / `runtime.AgentLoop`'s message slice grows
  monotonically within a session. Any transformation that would edit a
  prior message invalidates every downstream cache entry and is
  therefore forbidden.
- **Deterministic tool serialization.** `TurnRequest.Tools` must emit
  tools sorted by name. Map iteration order is banned from any code
  path that produces prompt bytes. Applies equally to the wire
  serialisation inside provider packages (tool-call ids, JSON field
  order).
- **No dynamic content in the prefix.** Timestamps, per-run UUIDs,
  token counters, random nonces, wall-clock clocks — none may appear
  inside the cached bytes. The test below is the gate: the rendered
  prefix for identical inputs must be byte-identical.
- **Cache telemetry round-trips through the provider seam.** The
  existing `EvCacheHit` / `EvCacheMiss` events on `pkg/agent.Event`,
  plus `Usage.CacheReadTokens` / `CacheWriteTokens`, are the canonical
  way providers surface hit/miss. These feed the `stado_cache_hit_ratio`
  histogram defined in the telemetry spec.

Cross-refs: §"Provider interface" (events + usage fields); PLAN.md §6.3
(`stado_cache_hit_ratio`).

### Token accounting

Token counts come from the provider's own tokenizer — never from
estimation. Per-backend:

| Backend | Tokenizer |
|---|---|
| Anthropic | `Messages.CountTokens` pre-flight, or the official tokenizer |
| OpenAI + OAI-compat | `tiktoken` (or server-reported if available) |
| Google / Gemini | genai SDK tokenizer |

Capability probing on first provider use returns a boolean indicating
whether token counting is available. A configured backend that cannot
report counts is a hard error on first turn: **we refuse to proceed
blind**.

Two configurable thresholds, expressed as percentages of the active
model's `Capabilities.MaxContextTokens` (percentages, not absolute —
context windows vary wildly):

- **Soft (default 70%).** TUI shows a dismissable warning indicator;
  headless emits a `session.update { kind: "context_warning" }`
  notification. Recommendation is to fork. No automatic action.
- **Hard (default 90%).** The next turn is blocked. User is prompted
  to fork, run `session compact` explicitly, or abort. There is no
  path by which the agent silently compacts mid-session.

### Tool-output curation

Every tool declares a default output-size budget, expressed in tokens.
Defaults:

| Tool | Default budget | Notes |
|---|---|---|
| `read` | 4K | `start` / `end` line-range args request specific regions |
| `ripgrep` | first 100 matches | truncation marker appended |
| `grep` | first 100 matches | same |
| `bash` | 8K combined stdout+stderr | head + tail preserved; middle elided with byte count |
| `glob` / `list_dir` | 200 entries | |
| `webfetch` | 4K | |
| *other bundled* | 4K | unless the tool overrides |

Truncation is **visible to the model** — truncated output carries an
explicit marker so the model knows to request more:

```
[truncated: 14823 of 15000 lines elided — call with range=... for more]
```

The override is per-call via tool arguments, never a global config
knob. The model, not the user, decides when full output is warranted.

**In-turn deduplication (SHOULD).** The tool layer should detect when
a `read` call targets a path+range already read earlier in the current
session and return a reference response in the *current* turn's
`tool_result` rather than re-reading from disk. The prior turn is not
modified — its `tool_result` bytes remain unchanged, so the prompt
cache stays valid. The current turn's `tool_result` carries the
reference; the model learns of the duplicate from the new turn's
payload.

Reference responses include the canonical range in the citation — e.g.
`"already read lines 10:20 at turn 5"` for ranged matches,
`"already read at turn 5"` for full-file matches — so the model can
disambiguate ranged from full-file hits.

Dedup is keyed on **path + range + content hash**, via
`Host.PriorRead(ReadKey) (PriorReadInfo, bool)` (see §"Tool interface"):

1. Build `ReadKey{Path, Range}` from the current call.
2. Call `Host.PriorRead(key)` — if `ok=false`, read from disk normally.
3. On `ok=true`, compute sha256 of the file region the current call
   targets.
4. If the current hash ≠ `PriorReadInfo.ContentHash`, the file has
   changed since the prior read — read from disk normally. The fresh
   bytes are what the model sees; staleness is surfaced, not masked.
5. If the hashes match, return the reference response.

Exact path-and-range match only; a ranged read of a previously
full-file read (or vice versa) is a distinct key and does not dedup.
Hash algorithm pinned to sha256 — same algorithm the audit layer
already uses (§"Audit") so a session's artefacts share one hash
vocabulary.

**Scope — `read` tool only.** This SHOULD applies only to the `read`
tool. Tools that read files as implementation details of other
operations (`ripgrep`, `ast_grep`, `read_with_context`, `bash`) do not
participate in the read log and do not dedup against it. The read log
tracks content delivered to the model via the `read` tool only; tools
do not record against the read log on each other's behalf.

**Scope — per-process.** The read log is maintained by the Executor
in memory for the lifetime of the current `stado` invocation. A
session resumed in a new process starts with an empty read log.
Persistent cross-process deduplication is explicitly not a goal —
restoring an old session into a fresh process should behave as
day-one.

The Executor maintains a process-local turn counter (incremented on
each top-level user prompt, independent of `Session`). When a
`Session` is available, the counter tracks `Session.Turn()`; when
running in the no-session fallback, the counter is authoritative.
`PriorReadInfo.Turn` is always populated from this counter and is
never zero for a successful prior-read match. A turn spans one user
prompt plus all tool-result iterations that follow it, up to but not
including the next user prompt. Agent-internal re-streams after tool
execution do not increment the turn counter.

**Concurrency.** When multiple `read` calls execute in parallel
(provider `MaxParallelToolCalls > 1`), `PriorRead` and `RecordRead`
are serialised against the Executor's read log. "Most recent" is
defined as **`RecordRead`-call-order**, not issue-order. Concurrent
reads of the same key issued before either records will both read
from disk; subsequent reads see whichever recorded last. This is
acceptable — deduplication is a best-effort optimisation, not a
correctness guarantee.

### Compaction

User-invoked only. Ship as `stado session compact` (CLI) and a TUI
action.

Invariants:

- **No automatic trigger.** Not on threshold breach, not in the
  background, not via any config flag. If a contributor proposes an
  auto-compaction path, the onus is on them to explain why
  fork-from-point is insufficient.
- **Confirmation required.** Compaction produces a proposed summary,
  shows it to the user, permits edit, and only commits on explicit
  confirmation.
- **Original turns survive on `trace`.** The `tree` ref receives a
  compaction commit that replaces the conversation-view with the
  summary; the `trace` ref keeps the raw turns unchanged. The
  compaction itself is a commit on both refs, so
  `git checkout refs/sessions/<id>/tree~1 -- …` recovers the
  pre-compaction state exactly. See §"Git-native state" for the
  ref model.
- **Compaction marker.** The session's metadata (surfaced by
  `stado session show`) records which turns were compacted and when.

### Fork-from-point ergonomics

Both paths must exist for the fork-as-preferred-recovery premise to
hold; a single surface is not sufficient.

- **Scripted.** `stado session fork <id> --at <turn-ref>` forks into
  a fresh session rooted at the specified turn in one invocation.
  This extends today's `stado session fork <id>` (which forks from
  tree HEAD); the no-`--at` form is preserved for backward
  compatibility.
- **Interactive.** `stado session tree <id>` is a **standalone cobra
  subcommand** that opens a `tea.Program` of its own — not a slash
  command inside the main TUI. It renders the session's turn history
  in a navigable view; a single keybinding on the cursor-selected
  turn forks into a fresh session rooted at that turn. Standalone,
  because the primary fork-from-point journey is post-session
  recovery, which must work from any shell independent of whether
  the main TUI is running. A slash-command entry point inside the
  TUI may be added later as an additional surface, but the
  standalone subcommand is load-bearing.

Both paths land the user in a new session whose `tree` ref is seeded
at the selected turn's commit and whose worktree has been
materialised to match. The parent session is never modified (see
§"Fork semantics").

**Turn reference syntax.** The canonical user-facing turn identifier
is `turns/<N>`, where `<N>` is the 1-indexed turn number within the
session. This is the form displayed in `session tree`, accepted by
`session fork --at`, and emitted in error messages. Full commit SHAs
on the session's `tree` ref are also accepted anywhere a turn
reference is valid, for scripting and sub-turn precision.
`session tree`'s default view shows turn boundaries (`turns/<N>`)
only; sub-turn commits are not rendered by default. Users who need
sub-turn fork precision obtain the relevant SHA via
`git log refs/sessions/<id>/tree` and pass it to `session fork --at`.

### Non-goals

Explicitly out of scope **for the core agent loop**. A contribution
that proposes any of these as core behavior must first justify why
fork-from-point is inadequate:

- Automatic or background summarization of any kind.
- Semantic importance scoring of individual turns.
- Vector-store-backed "memory" of prior sessions.
- Sliding-window auto-eviction without user consent.

**Plugins may implement any of these behaviors** via documented
extension points — in particular, by forking to a new session
rather than rewriting conversation history in place. A plugin that
rewrites history in place violates the append-only invariant
regardless of where the code lives. See §"Plugin extension points
for context management" below.

### Plugin extension points for context management

The core agent loop is closed to automatic context manipulation, but
the plugin layer is not. A signed, capability-bounded plugin can
observe turn boundaries, read the session's state, and fork into a
new session whose first message is a plugin-provided summary. The
append-only invariant is preserved because nothing in the parent
session is rewritten — the plugin's recovery move is the same move
stado's core offers (fork-from-point), just initiated programmatically.

This subsection defines what a context-management plugin can request
and the invariants it must honour. The canonical motivating case is
auto-compaction, but the surface is deliberately broader.

**Capabilities a context-management plugin may request.** In addition
to the existing `fs:*`, `net:*`, and `exec:*` capabilities, four
session/LLM capabilities gate the host imports below:

| Capability | Purpose | Host import |
|---|---|---|
| `session:observe` | Subscribe to turn-boundary events and receive notifications when a turn completes. | `stado_session_observe(callback_ref)` |
| `session:read` | Read the current session's conversation history, token counts, and metadata. Read-only — no mutation. | `stado_session_read(field, buf, len) → n` |
| `session:fork` | Initiate a fork-from-point programmatically, seeding the child session with a plugin-provided message (e.g. a summary). Returns the new session ID. | `stado_session_fork(at_turn_ref, seed_message, buf, len) → n` |
| `llm:invoke` | Call an LLM with a prompt and receive the response. Uses the active provider by default; plugin manifest may declare a preferred backend. Subject to rate-limiting and budget caps set in plugin config. | `stado_llm_invoke(prompt_ptr, prompt_len, out_buf, out_len) → n` |

> **ABI note.** The signatures above are *indicative* and will be
> finalised with the 7.1b host-import PR. Several shapes are
> under-specified against stado's existing `(ptr, len)`-pair ABI
> convention — notably the `callback_ref` parameter (wasm has no
> native closure/callback type), the `field` encoding in
> `stado_session_read`, and whether `stado_llm_invoke` streams or
> aggregates. Consult the PR when it lands for the authoritative
> shapes; this table documents the *capability semantics*, not the
> wire format.

**Invariants plugins must respect.** Non-negotiable:

1. **Append-only in the parent session.** A plugin must never rewrite
   conversation history in any session — parent or child. Summaries
   are expressed by forking to a new session whose first message is
   the summary, not by editing an existing session's messages.
2. **Capability-bounded.** A plugin's manifest declares every
   capability it uses. Runtime denies capabilities not declared.
   `llm:invoke` specifically carries a token budget per session; a
   plugin that exhausts the budget is killed and reports the denial
   via the audit log.
3. **All plugin-triggered actions are audited.** Any fork initiated
   by a plugin, any LLM call made by a plugin, any tool invocation
   on the plugin's behalf lands on the session's `trace` ref with a
   `Plugin:` trailer identifying the plugin by name + signature
   fingerprint.
4. **User-visible by default.** When a plugin forks a session,
   the TUI surfaces the fork (inline notification; not a silent
   operation). Headless mode emits `session.update { kind:
   "plugin_fork", plugin: "<name>", reason: "<plugin-provided>" }`.

**Canonical example: the auto-compaction plugin shape.** Walking
through how an auto-compaction plugin uses the four capabilities
together:

- At startup, declare capabilities: `session:observe`, `session:read`,
  `session:fork`, `llm:invoke`.
- Subscribe to turn-boundary events via `session:observe`.
- On each turn boundary, check token usage via `session:read`. If
  below configured threshold, do nothing.
- If threshold crossed: read conversation history via `session:read`,
  invoke LLM via `llm:invoke` to produce a summary of the oldest N
  turns, call `session:fork` with the summary as seed message rooted
  at the turn boundary being compacted.
- Return. User sees the fork notification; new session continues with
  the summary as the seed context. Parent session is untouched and
  remains resumable.

This plugin shape is explicitly allowed because it never rewrites
history; it only forks. Per §"Non-goals", the core prohibition is
on *in-place* summarization — not on this fork-based pattern. A
plugin that edited prior turns on the parent session would violate
invariant 1 above and the runtime would refuse the action.

### Testing requirements

These tests gate the invariants above. They are **Phase 11 acceptance
criteria** — none exist in CI today. Each maps to a sub-phase under
PLAN §11:

- **Cache-stability test** (PLAN §11.1). Render the system-prompt
  prefix twice with the same inputs, assert byte equality. Fails
  loudly on any clock / UUID / map-iteration leak.
- **Tool-ordering test** (PLAN §11.1). Register tools in randomised
  order, assert the serialised `TurnRequest.Tools` bytes are identical
  across runs.
- **Token-counting fidelity** (PLAN §11.2). For each supported
  provider, assert the agent's reported token count matches the
  provider's own count for a fixed prompt to within 1% tolerance.
- **Truncation coverage** (PLAN §11.4). For each bundled tool, assert
  the default output budget is respected and the truncation marker is
  present when hit.
- **Read-dedup invariants** (PLAN §11.4). `PriorRead` / `RecordRead`
  round-trip; staleness check rejects dedup when content hash diverges;
  canonicalisation of `ReadKey.Range` asserted for every input shape
  the `read` tool accepts.
- **Fork-from-point ergonomics — scripted** (PLAN §11.5). Assert that
  `stado session fork <id> --at turns/<N>` in a single invocation
  produces a fresh session whose tree-ref head matches the parent's
  `turns/<N>` tag, and whose worktree has been materialised to match.
- **Fork-from-point ergonomics — interactive** (PLAN §11.5). End-to-end
  test that `stado session tree <id>` renders a navigable view, and a
  single keybinding on a specific turn forks into a fresh session at
  that turn — asserted by the resulting session's tree-ref and its
  materialised worktree. Runs against a headless/PTY harness
  (`github.com/creack/pty`) so it fires on CI.

---

## Sandbox (`internal/sandbox`)

```go
type Policy struct {
    FSRead, FSWrite, Exec, Env []string
    Net      NetPolicy  // DenyAll | AllowHosts{[]string} | AllowAll
    CWD      string
    Timeout  time.Duration
}
```

`Policy.Merge(inner)` is the INTERSECTION — never widens.

### Runners

- `NoneRunner` — no sandbox, filtered env.
- `BwrapRunner` (Linux) — translates Policy to bubblewrap flags
  (`--ro-bind` FSRead, `--bind-try` FSWrite, `--unshare-net` on
  `NetDenyAll`, `--setenv` per Env entry, `--chdir` CWD).
- Non-Linux: falls back to `NoneRunner`.

`sandbox.Detect()` picks the most capable available runner.

### Landlock (`internal/sandbox/landlock_linux.go`)

`ApplyLandlock(Policy)` restricts the CURRENT process via Linux
Landlock (`PR_SET_NO_NEW_PRIVS` → `landlock_create_ruleset` →
per-path `add_rule` PATH_BENEATH → `restrict_self`). Irreversible by
design. Returns `ErrLandlockUnavailable` on kernels <5.13 so callers
can fail open.

Typical use: `stado run --sandbox-fs` applies
`WorktreeWrite(session.WorktreePath)` which reads-everywhere but
confines writes to the worktree + /tmp.

### Net proxy (`internal/sandbox/proxy.go`)

HTTPS CONNECT allowlist proxy. Spins up on 127.0.0.1:kernel-assigned.
Matches destination host against `NetPolicy.Hosts` (exact names,
`*.example.com` wildcards, CIDR for IPs). Caller wires it into a
child process via `EnvForProxy(proxy)` which returns the four
HTTP_PROXY/HTTPS_PROXY env assignments.

---

## Audit (`internal/audit`)

- `LoadOrCreateKey(path)` — Ed25519 agent key; auto-generated 0600 PEM.
- `NewSigner(priv)` → satisfies `state/git.CommitSigner`. Interface lives
  in `state/git` to avoid an import cycle.
- `Walker.Verify(refName, head)` — walks first-parent chain, verifies
  each commit's signature; returns counts + first-invalid-at.
- `ExportJSONL(w, storer, refName, head)` — one JSON record per commit,
  with title + trailers parsed out (Signature trailer excluded).
- `MinisignSign / MinisignVerify` — BLAKE2b-prehashed Ed25519 in
  minisign `.minisig` format. For release-artifact signing; interop with
  the `minisign` CLI.

---

## TUI (`internal/tui`)

### Architecture

`Model` (bubbletea) owns everything:

- Conversation state: `[]block` (UI blocks) + `[]agent.Message` (wire
  history for next TurnRequest). Duplication on purpose — wire history
  survives replays; UI blocks track expand/collapse and per-block
  rendering metadata.
- Per-turn accumulators: `turnText / turnThinking / turnThinkSig /
  turnToolCalls`. Reset on `startStream`, flushed to wire history in
  `onTurnComplete`.
- Provider lazy-init via `buildProvider` closure. `ensureProvider`
  called on first prompt; errors surface as a `kind="system"` block.
- Executor + Session optional — TUI runs without sidecar, logging a
  stderr warning; tool calls still work, just without audit.

### Rendering

- Theme in `internal/tui/theme/theme.go`, palette + layout in
  `default.toml`, override at `$XDG_CONFIG_HOME/stado/theme.toml`.
- Per-widget templates in `internal/tui/render/templates/*.tmpl`,
  loaded via `embed.FS`. Overlay dir supported for user overrides.
- FuncMap: `color · bg · bold · italic · underline · muted · wrap ·
  wrapHard · indent · markdown · marker · todoMarker · todoColor`.
- Widgets: `message_user / _assistant / _thinking / _tool`,
  `sidebar`, `status`, `input_status`.

### Input box + mode

- Single rounded-border panel containing textarea + inline status
  (`<Mode> · <Model> <Provider> · <Hint>`).
- Left border tint = mode colour (yellow in Plan, green in Do) via
  `BorderLeftForeground`.
- Bottom strip: muted, right-aligned
  `<tokens> (<pct>) · $<cost>  ctrl+p commands`.
- **Plan mode**: `toolDefs()` filters `NonMutating` only into
  `TurnRequest.Tools` — model literally can't request
  `write/edit/bash`. No approval-loop workaround.

### Command palette (Ctrl+P)

Modal popup (not inline drop-down): own search input, grouped command
list, each row has a right-aligned shortcut/slash-id hint. While
visible, ALL keypresses route to the palette — characters build the
modal's own Query; arrow keys navigate; Enter executes; Esc closes.

---

## Extension points

### New provider

Implement `pkg/agent.Provider`. Add a case in
`internal/tui/app.go:buildProvider` (or a `builtinPreset` row for an
OAI-compat service).

### New built-in tool

1. Implement `pkg/tool.Tool` (+ `Classifier` for non-NonMutating).
2. Register in `internal/runtime.BuildDefaultRegistry`.
3. Add an entry to `internal/tools.Classes` for a per-name class.

### New MCP server

Declare in config:

```toml
[mcp.servers.github]
command = "mcp-github"
args    = ["--readonly"]
env     = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }
```

`runtime.attachMCP` auto-registers every tool the server exposes.

### New plugin (future, once Phase 7.1 lands)

Ship a `plugin.wasm` + `plugin.manifest.json` + `plugin.manifest.sig`
directory. Author's public key must be pinned via
`stado plugin trust <pubkey>`. Manifest version must monotonically
increase (rollback protection).

Context-management plugins (auto-compaction, second-opinion routing,
session-replay exporters) have a dedicated set of capabilities —
`session:observe`, `session:read`, `session:fork`, and `llm:invoke`.
See §"Plugin extension points for context management" for semantics
and the required invariants.

### Custom theme

TOML file at `$XDG_CONFIG_HOME/stado/theme.toml`. Override individual
colour / layout fields; the bundled default fills the rest.

### Custom templates

Override any `.tmpl` in
`$XDG_CONFIG_HOME/stado/templates/<name>.tmpl`. Loaded via
`render.NewWithOverlay` (not yet wired into `stado`'s TUI entry point;
small change pending).

---

## Build & test

- **Build**: `go build -trimpath -buildvcs=true -ldflags="-s -w
  -buildid=" -o stado ./cmd/stado`. Bit-for-bit reproducible.
- **Test**: `go test ./...`. 194 unit tests across 25 packages. Tests
  that depend on external binaries (`rg`, `ast-grep`, `gopls`) skip
  gracefully if the binary is missing.
- **Release**: `.github/workflows/release.yml` builds the matrix via
  goreleaser, produces SBOM + cosign signature + SLSA 3 provenance.
- **CGO**: disabled. Pure Go for the entire module including go-git,
  wazero-ready, landlock syscalls via `x/sys/unix`.

---

## Cross-references

- Roadmap + detailed phase breakdown: [`PLAN.md`](PLAN.md)
- Learnings from non-obvious design/debug: [`.learnings/`](.learnings/)
- Per-package notes: each package has a header comment explaining its
  role. See `pkg/agent/agent.go`, `internal/state/git/sidecar.go`,
  `internal/tools/executor.go`, `internal/sandbox/policy.go`.
