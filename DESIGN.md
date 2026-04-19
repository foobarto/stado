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
            ┌────────────────────────────────────────────┐
            │   User surfaces                            │
            │                                            │
            │  TUI   stado run   stado acp   stado headless
            │   │       │           │             │       │
            └───┼───────┼───────────┼─────────────┼───────┘
                └───────┴───────────┴─────────────┘
                                │
                                ▼
                    internal/runtime (AgentLoop)
                    ┌────────────┬──────────────┐
                    │            │              │
                    ▼            ▼              ▼
            pkg/agent     internal/tools   internal/state/git
            (Provider)    (Executor +      (Sidecar, refs,
                │          Registry +       signatures,
                │          classification)  materialisation)
                ▼              │
    ┌───────────┬─────┬────┐   │
    ▼           ▼     ▼    ▼   ▼
  anthropic  openai google oaicompat
                               │
                               ▼
                         internal/sandbox
                         (Policy, Runner,
                          landlock, proxy)
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

### Bundled tools (14)

| Tool | Class | Notes |
|---|---|---|
| `read` | NonMutating | |
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
4. record `stado_tool_latency_ms`
5. build `CommitMeta` trailers

Then:
- **trace ref**: always committed (even on failure; `Error:` trailer).
- **tree ref**: committed iff `Mutating` (success) OR `Exec` AND
  post-run tree hash differs from pre-run tree hash.

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
