# stado — Enterprise Security Pivot Plan

## Architectural North Star

stado is a **sandboxed, git-native coding-agent runtime**:

- Tight internal coding-agent interface (not an LLM abstraction); 4 direct implementations
- User repo stays pristine; all agent state lives in an alternates-linked sidecar
- Dual-ref model: `tree` (executable, mutations only) + `trace` (audit, every call)
- Every tool call goes through an OS-level sandbox with a capability manifest
- WASM plugins with capability-bound signed manifests
- TUI + headless both; ACP server for editor integration; MCP client for tool interop
- OTel everywhere; reproducible signed releases (cosign keyless + minisign)

See [`DESIGN.md`](DESIGN.md) for the as-built architecture.

---

## Status snapshot

Legend: ✅ complete · 🟡 partial · ⬜ not yet

| Phase | Status | Notes |
|-------|--------|-------|
| 0 — Demolition | ✅ | |
| 1 — Provider interface + 4 impls | ✅ | Also 11 bundled OAI-compat presets |
| 2 — Git-native state | ✅ | All 8 session subcommands shipped |
| 3 — Sandbox layer | 🟡 | policy + bwrap + landlock + net-proxy ✅ · seccomp/macOS/Windows ⬜ |
| 4 — Tool runtime | ✅ | 14 bundled tools; embed pipeline ⬜ |
| 5 — Tamper-evident audit | ✅ | Ed25519 commit signing + `stado audit` |
| 6 — OTel | ✅ | Exporters + metrics + span instrumentation across `tools.Executor`, `runtime.AgentLoop`, and all 4 providers |
| 7 — WASM plugins | 🟡 | Manifest + trust-store + CLI ✅ · wazero runtime ⬜ |
| 8 — MCP + ACP | ✅ | Both shipped; per-MCP sandbox policy ✅ (capability parser + `transport.WithCommandFunc` → `sandbox.Runner.Command`) |
| 9 — Headless + parallel | ✅ | `stado run/headless/acp/agents` |
| 10 — Release & reproducibility | 🟢 | Reproducible build ✅ · SLSA ✅ · minisign implementation ✅ (offline-key ceremony ⬜) · Homebrew/apt ⬜ |
| 11 — Context management | 🟡 | 11.1 ✅ · 11.2 ✅ (TokenCounter + 4 impls + `[context]` thresholds + TUI warning-pct + capability-probe + headless `session.update{kind:"context_warning"}`) · 11.3 🟡 (TUI `/compact` + `stateCompactionPending` y/n confirmation + `internal/compact` summarisation helper + headless `session.compact` RPC + advisory CLI stub; dual-ref persistence still pending) · 11.4 ✅ · 11.5 ✅. Spec is in [DESIGN §"Context management"](DESIGN.md#context-management); PR sequence is B–F in §"Remaining work". |

---

## Phase 0 — Demolition — ✅

**Goal:** Remove code that doesn't survive the pivot so subsequent phases build cleanly.

| # | Action |
|---|--------|
| 0.1 | Delete `internal/storage/*` and `modernc.org/sqlite` from go.mod |
| 0.2 | Delete `internal/tools/todo/` (app-level todo tool, sessions.db-backed) |
| 0.3 | Delete `internal/context/{engine,lexical,vector,symbols,embeddings,watcher}` + chromem-go, bleve, faiss deps |
| 0.4 | Strip session-resume code from `internal/tui/app.go` (rewritten Phase 2) |
| 0.5 | Strip provider factory switch from `app.go` (rewritten Phase 1) |
| 0.6 | `config.go`: remove `[providers.*]` sections; prepare skeleton for `[inference]`, `[sandbox]`, `[git]`, `[otel]`, `[acp]`, `[plugins]` |

**Verify:** `go build ./...` compiles with a stub agent loop.

---

## Phase 1 — Coding-Agent Provider Interface — ✅

**Goal:** ~200 LOC seam in `pkg/agent` encoding what the agent loop actually needs; 4 direct implementations. No third-party LLM abstraction library.

**Shipped:** all 5 sub-phases (1.1 interface, 1.2 anthropic, 1.3 openai, 1.4 google, 1.5 oaicompat). 1.6 capability-driven branching partial — `Capabilities{}` populated and surfaced via `/provider`, but the agent loop doesn't yet *exploit* differences (e.g. cache_control selection based on `SupportsPromptCache`). Bundled presets added beyond PLAN: `lmstudio`, `litellm`, `groq`, `openrouter`, `deepseek`, `xai`, `mistral`, `cerebras`.

### 1.1 `pkg/agent/agent.go` — the interface

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    StreamTurn(ctx context.Context, req TurnRequest) (<-chan Event, error)
}

type Capabilities struct {
    SupportsPromptCache  bool
    SupportsThinking     bool
    MaxParallelToolCalls int
    SupportsVision       bool
    MaxContextTokens     int
}
```

**Event** is a sum type: `TextDelta` | `ThinkingDelta` (with signature) | `ToolCallStart` | `ToolCallArgsDelta` (raw JSON) | `ToolCallEnd` | `CacheHit` | `CacheMiss` | `Usage` | `Done` | `Error`.

**Critical:** Thinking blocks and `reasoning_content` carry through **verbatim** with provider-native structure preserved in an opaque field. The agent loop and UI decide what to do with them. Normalizing them away breaks extended-thinking tool-use round-trips.

### 1.2 `internal/providers/anthropic` — direct anthropic-sdk-go

- Prompt caching via `cache_control`
- Extended thinking with signature round-trip
- Parallel tool calls
- ~500 LOC, most of it streaming translation

### 1.3 `internal/providers/openai` — direct openai-go

- `reasoning_content` plumbing
- `tool_choice`, `parallel_tool_calls`
- Structured outputs where applicable

### 1.4 `internal/providers/google` — direct google/generative-ai-go

- Gemini streaming, tool calling, thinking (where applicable)

### 1.5 `internal/providers/oaicompat` — hand-rolled OpenAI-compat HTTP, ~300 LOC, no SDK

Covers llama.cpp, vLLM, Ollama, LiteLLM, OpenRouter, Groq, Cerebras, xAI, DeepSeek, Mistral, and any `/v1/chat/completions` endpoint.

**Presets in config:**

```toml
[inference.presets.ollama]
endpoint = "http://localhost:11434/v1"

[inference.presets.llamacpp]
endpoint = "http://localhost:8080/v1"

[inference.presets.vllm]
endpoint = "http://localhost:8000/v1"

[inference.presets.custom]
endpoint = "${user-specified}"
```

Plus a generic `--endpoint` flag.

**Reference test target:** llama.cpp `llama-server` — it's the substrate, ships as a single binary, has the cleanest airgap story, and fewer surprises around tool calling.

**Capability probing on connect:** hit `/v1/models` and any backend-specific capability endpoint to learn context length, tool-calling support, vision support. Fail gracefully: *"this model doesn't support tool calling; switching to ReAct-style prompting."*

**Error messages:** *"Connection refused at localhost:11434 — is Ollama running? Try `ollama serve`."* *"Model llama3.1:8b not found on server — available models: …"*

### 1.6 Agent loop branches on Capabilities

No lowest-common-denominator path. Exploit Anthropic's caching when available; degrade gracefully when not.

**Verify:** Unit tests per provider with recorded golden transcripts. CI smoke test spins up llama.cpp server with Qwen2.5-0.5B and runs a tool-calling turn.

---

## Phase 2 — Git-Native State Core — ✅

**Goal:** Sidecar repo with alternates; dual-ref; turn tags; diff-then-commit.

**Shipped:** 2.1–2.8 complete. Session CLI has all 8 subcommands (`new/list/show/attach/delete/fork/land/revert`). Tree ↔ worktree materialisation is symmetric (`BuildTreeFromDir` + `MaterializeTreeToDir`/`…Replacing`), so `fork` populates the child worktree and `revert` creates a new child session at a historical commit/turn tag.

### 2.1 `internal/state/git` — pure-Go via go-git

- **Sidecar path:** `${XDG_DATA_HOME}/stado/sessions/<repo-id>.git` (bare)
- **Worktree path:** `${XDG_STATE_HOME}/stado/worktrees/<session-id>/`
- `repo-id` = hash of absolute path of user repo root (or cwd if not a repo)

### 2.2 Alternates

`sidecar/objects/info/alternates` → `user-repo/.git/objects`. User repo is read-only from agent's perspective. Sidecar shares object storage — zero duplication.

### 2.3 Dual-ref design

| Ref | Purpose |
|-----|---------|
| `refs/sessions/<id>/tree` | Executable history — commits on mutations only |
| `refs/sessions/<id>/trace` | Audit log — one empty-tree commit per tool call |

Parent chain at fork points shared by both refs.

### 2.4 Commit policy

| Tool class | tree ref | trace ref |
|------------|----------|-----------|
| Pure queries (read/grep/glob/lsp-ref) | — | ✓ |
| Exec (bash/shell/make/test) | ✓ iff diff non-empty (snapshot → run → diff) | ✓ |
| Write/edit/apply_patch | ✓ | ✓ |
| Failed tool call | — | ✓ with error |

- Committed on successful completion only — never during streaming
- Per-tool-call commit granularity (no cross-call batching)

### 2.5 Commit message format (machine-generated, structured)

```
<tool>(<short-arg>): <one-line-summary>

Tool: write
Args-SHA: sha256:...
Result-SHA: sha256:...
Tokens-In: 1234
Tokens-Out: 567
Cache-Hit: true
Cost-USD: 0.0012
Model: claude-sonnet-4-20250514
Duration-Ms: 342
Agent: anthropic
Turn: 3
```

Machine-parseable; human summaries generated at session end by a cheap model.

### 2.6 Turn tags

`refs/sessions/<id>/turns/<n>` at every LLM-turn boundary on `tree`.

Enables `git log turns/5..turns/6` for turn-level diffs.

### 2.7 Session lifecycle

| Command | Action |
|---------|--------|
| `stado session new` | New session, new worktree |
| `stado session list` | List active sessions |
| `stado session show <id>` | Print session refs + worktree + latest commit summary |
| `stado session attach <id>` | Print the worktree path of an existing session |
| `stado session delete <id>` | Remove session + worktree |
| `stado session fork <id>` | New worktree + both refs forked from parent's tree head (parallel agent) |
| `stado session land <id> <branch>` | Push `refs/sessions/<id>/tree` to user repo as `<branch>` |
| `stado session revert <id> <commit-or-turns/N>` | Create a new child session rooted at the target commit; parent untouched |

**trace never gets pushed to user repo** — stays in sidecar as AppSec record.

### 2.8 Git author identity

Per-agent bot identity, e.g. `claude-code-acp <agent@stado.local>`, so `git log --author` can filter. Configurable.

**Verify:**
- edit → commit → revert → worktree clean + new session branch shows revert
- `bash "make test"` with no changes → no tree commit, only trace
- `bash "touch newfile"` → tree commit with diff
- alternates working: sidecar .git smaller than user's .git
- user's refs untouched after 100 sessions

---

## Phase 3 — Sandbox Layer — 🟢

**Goal:** Platform-abstracted policy enforcement. Capabilities declared, OS enforces.

**Shipped:** 3.1 Policy/NetPolicy/Merge, 3.4 bubblewrap runner, 3.2 Linux landlock (pure Go via `x/sys/unix`, regression-tested via subprocess re-exec), 3.7 Linux CONNECT-allowlist proxy, 3.3 seccomp BPF compiler (Linux), 3.5 macOS `sandbox-exec` runner (generates `.sb` profile from Policy), 3.6 Windows v1 (log warning, runs unsandboxed). `stado run --sandbox-fs` narrows the process with `WorktreeWrite`. **Pending:** 3.6 v2 Windows job objects + restricted tokens.

### 3.1 `internal/sandbox/policy.go`

```go
type Policy struct {
    FSRead  []string   // glob/prefix allow-list
    FSWrite []string
    Net     NetPolicy  // DenyAll | AllowHosts([]string) | AllowCIDR
    Exec    []string   // binary allow-list
    Env     []string   // allowed env var names to pass through
    CWD     string
}
```

### 3.2 Linux — Landlock

`golang.org/x/sys/unix` `landlock_restrict_self`. Pure Go, no CGO. Enforces FS read/write. Fails open on kernels < 5.13 with explicit warning.

### 3.3 Linux — seccomp

Hand-rolled BPF filter allow-list via `SECCOMP_SET_MODE_FILTER` syscall (no libseccomp-golang — it's CGO). Small curated allow-list per tool profile.

### 3.4 Linux — bubblewrap (preferred for bash/exec)

Exec bubblewrap with generated argv: `--ro-bind`, `--bind-try workdir`, `--unshare-net` or `--share-net`, `--die-with-parent`. Falls back to landlock+seccomp when bwrap unavailable.

### 3.5 macOS — sandbox-exec

Generate `.sb` profile on the fly (Scheme-ish DSL), spawn with `sandbox-exec`. Covers FS + network.

### 3.6 Windows

Job objects + restricted tokens (v2). In v1: log warning that Windows runs unsandboxed.

### 3.7 Network policies

- Per-tool egress allow-list
- **Linux v1:** `HTTP_PROXY`/`HTTPS_PROXY` to a `goproxy` that enforces host allow-list; deny raw-TCP tools (documented limitation)
- **Mac:** `sandbox-exec` network rules
- **Linux v2:** dedicated net namespace + veth + nftables (requires `CAP_NET_ADMIN`)

### 3.8 `pkg/tool.Host` extended with `Sandbox() → Policy`

All tool executions route through `internal/sandbox.Run(policy, cmd/fn)`.

**Verify:**
- write outside `FSWrite` globs denied + audited
- `bash curl` to disallowed host fails connection
- read of `/etc/shadow` denied on linux even if bash ran as current user
- tests gated on landlock/bwrap availability with skip messages

---

## Phase 4 — Tool Runtime Overhaul — ✅ (v1)

**Goal:** Replace bespoke context engine with solid search primitives + LSP; wire diff-then-commit.

**Shipped:** 4.1 ripgrep tool, 4.2 ast-grep tool, 4.3 LSP client + 4 tools (`find_definition/find_references/document_symbols/hover`), 4.4 `read_with_context` (Go-aware via `go/parser`), 4.5 classification (Mutating/NonMutating/Exec), 4.6 `tools.Executor` with dual-ref commit invariants, 4.7 task stub deleted, 4.1/4.2 binary-embed release pipeline (`hack/fetch-binaries.go` generates per-platform `//go:embed` files gated on `-tags stado_embed_binaries`; goreleaser's before-hook runs the fetcher and every cross-compile sets the tag; dev builds without the tag fall back to PATH). **Pending:** none.

| # | Tool | Details |
|---|------|---------|
| 4.1 | ripgrep | Embed ripgrep binary via `go:embed` (per-OS/arch release assets). Extract to `$XDG_CACHE_HOME/stado/bin/rg` on first use, verify sha256. Tool surface: pattern, path, globs, context lines, case-sensitivity, max-matches. |
| 4.2 | ast-grep | Same embed approach. Structural code queries. Tool surface: AST pattern, language, rewrite (optional). |
| 4.3 | LSP client | Pure Go via `go.lsp.dev/protocol` + `go.lsp.dev/jsonrpc2`. Auto-launch `gopls`/`rust-analyzer`/`pyright`/`tsserver`. Tools: `find_definition`, `find_references`, `document_symbols`, `hover`, `workspace_symbols`. |
| 4.4 | read_with_context | Reads requested files plus their direct imports (language-aware via LSP `document_symbols`). |
| 4.5 | Tool classification | Classify each registered tool at registration time: `Mutating` | `NonMutating` | `Exec` (requires diff-then-commit). |
| 4.6 | Wire tool executor → sandbox.Run → state.Commit (tree if mutating/exec+diff, trace always) |
| 4.7 | Delete `internal/tools/task/` stub — sub-agents become worktree forks (Phase 9). |

**Verify:**
- ripgrep / ast-grep / LSP tool calls roundtrip correctly via TUI
- `read_with_context` on a Go file includes imports
- mutating tool call produces tree commit; query tool call produces only trace commit

---

## Phase 5 — Tamper-Evident Audit — ✅

**Goal:** Signed git refs as the audit primitive.

**Shipped:** all sub-phases. Every commit carries two signatures now: the stado-native `Signature: ed25519:<base64>` trailer in the commit message (used by `stado audit verify`) AND an SSHSIG-format signature in the commit's `gpgsig` header (used by `git log --show-signature` + `ssh-keygen -Y verify`). SSHSIG is Ed25519 over the git-canonical commit bytes, namespace "git", sha512 — per https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.sshsig. 5.5 is currently a slog mirror via `Session.OnCommit`; wiring to OTel logs is a config change once the exporter lands.

| # | Action |
|---|--------|
| 5.1 | Tool-managed signing key. Generated on first run at `${XDG_DATA_HOME}/stado/keys/agent.ed25519`. Chmod 0600. Optional KMS/HSM backend hook (interface only in v1). |
| 5.2 | Sign every commit on both refs via go-git's SSH signature support (uses the ed25519 key as an SSH key in PEM form). `git log --show-signature` becomes an AppSec primitive. |
| 5.3 | `stado audit verify` — walks both refs, verifies signatures and hash chain. |
| 5.4 | `stado audit export` — emits CEF / JSON lines suitable for SIEM ingestion. |
| 5.5 | OTel log exporter mirrors trace commits to centralized logging (configurable). |

**Verify:**
- tamper any commit in trace → `stado audit verify` fails with commit SHA
- SIEM export is valid JSON-lines; each line has required fields

---

## Phase 6 — OpenTelemetry from Boot — 🟢 skeleton

**Goal:** Traces/metrics/logs across every boundary; off by default, one-line enable.

**Shipped:** `internal/telemetry` with OTLP gRPC + HTTP exporters, the 6 metric instruments in PLAN §6.3, span-name constants for the hierarchy, `[otel]` config section, disabled-safe no-op runtime. Span instrumentation now in place at every call site: `tools.Executor.Run` wraps each tool call in `stado.tool_call` (attrs: name, class, outcome, duration, result_bytes); `runtime.AgentLoop` wraps each turn in `stado.turn` (attrs: turn.index, provider, model, message/tool counts); all four providers wrap `StreamTurn` in `stado.provider.stream` (attrs: provider.name, input/output/cache tokens). No-op tracer path runs under every test.

| # | Action |
|---|--------|
| 6.1 | `internal/telemetry` — `go.opentelemetry.io/otel` + OTLP/gRPC + OTLP/HTTP exporters. Resource attrs: `service.name`, `service.version`, `session.id`, `repo.id`, `agent.name`. |
| 6.2 | **Span hierarchy:** `stado.session` → `stado.turn` → `stado.tool_call` → `stado.sandbox.exec` → `stado.provider.stream` |
| 6.3 | **Metrics:** `stado_tool_latency_ms` (tool, outcome), `stado_tokens_total` (provider, model, direction), `stado_cache_hit_ratio` (provider, model), `stado_approval_rate` (tool, decision), `stado_sandbox_denials_total` (tool, reason) |
| 6.4 | `slog` + `otelslog` — structured logs correlated with spans. |
| 6.5 | Config `[otel]` section — exporter, endpoint, sampling, headers, insecure, timeout. |

**Verify:** local docker-compose with jaeger + otel-collector; run a TUI session; see full trace hierarchy in Jaeger UI.

---

## Phase 7 — WASM Plugin Runtime + Signed Manifest — ✅ (v1)

**Goal:** Third-party plugins run in wazero, capability-gated, signed.

**Shipped:** 7.1 wazero runtime host (`internal/plugins/runtime/`): scaffold + lifecycle (7.1a), host imports `stado_log` / `stado_fs_read` / `stado_fs_write` (7.1b), plugin tool adapter + `stado plugin run` CLI (7.1c); 7.2 plugin package layout; 7.3 manifest schema with JCS-style canonical bytes + Ed25519 signing; 7.4 verification pipeline with rollback protection; 7.5 `stado plugin trust/untrust` key management; 7.6 CRL (Ed25519-signed JSON, `[plugins]` config section, `stado plugin verify` consults CRL with airgap-friendly cache fallback); 7.7 Rekor transparency-log integration (`internal/plugins/rekor.go` — hashedrekord v0.0.1 client via direct REST, no sigstore deps; Upload / SearchByHash / FetchEntry / VerifyEntry; `[plugins].rekor_url` config; `stado plugin verify` does a hash-index lookup and asserts the entry's sig / pubkey / digest triple matches the manifest — mismatch is fatal, absence is advisory, airgap stubs out); 7.8 CLI (`stado plugin trust/untrust/list/verify/digest/run`). **Pending:** none at v1 scope — offline publish workflow for plugin maintainers is a cookbook, not code.

### 7.1 `internal/plugins/runtime.go` — wazero host (pure Go, CGO-free)

WASI preview 1 + custom host imports:
- `stado_fs_read`, `stado_fs_write` — proxied through sandbox with plugin's declared caps
- `stado_net_http` — proxied through net policy
- `stado_log` — structured logging into OTel
- `stado_tool_register` — plugins can register tools at init

### 7.2 Plugin package layout

```
plugin.wasm
plugin.manifest.json   # canonicalized JCS-ish
plugin.manifest.sig    # Ed25519 over manifest bytes
```

### 7.3 Manifest schema (signed payload)

| Field | Description |
|-------|-------------|
| `name` | Plugin name |
| `version` | Semver |
| `author` | Author identifier |
| `author_pubkey_fpr` | Ed25519 fingerprint |
| `wasm_sha256` | Hash of the WASM binary |
| `capabilities` | `[]` of `fs:read:<glob>` | `fs:write:<glob>` | `net:<host-or-cidr>` | `exec:<bin>` |
| `tools` | `[]` tool-def |
| `min_stado_version` | Minimum host version |
| `timestamp_utc` | Signing time |
| `nonce` | Anti-replay |

### 7.4 Verification pipeline

1. Load manifest → verify Ed25519 sig with declared author key (or TOFU-cached fingerprint)
2. Verify `wasm_sha256` matches wasm bytes
3. Check `min_stado_version`
4. Check monotonic version against last-seen version for this author (rollback protection)
5. Show capability grant prompt on first install **and** on any capability change

### 7.5 Key management

| Command | Action |
|---------|--------|
| `stado plugin trust <pubkey|fingerprint>` | Pin a signer |
| `stado plugin untrust <fingerprint>` | Remove a pinned signer |
| `stado plugin install <source>[@<version>] [--signer <fpr>]` | Install with TOFU on first trust |

Every upgrade shows fingerprint + warns loudly if changed.

### 7.6 Revocation list

Signed JSON at a well-known URL (configurable), refreshed on install/update. Entries: `(author_fpr, version, wasm_sha256, reason)`. Cached locally; airgap users can import a signed CRL manually.

### 7.7 Optional Rekor attestation

Authors can submit manifest signature to Rekor; `stado plugin install` can verify against Rekor when online. `--keyless` publish path uses cosign OIDC identity as author.

### 7.8 `stado plugin list/install/verify/sign` CLI

**Verify:**
- tampered wasm → install fails
- tampered manifest → install fails
- capability expansion on upgrade → prompt appears
- downgrade attempt → blocked
- plugin attempting out-of-manifest syscall → denied at host-import layer
- revoked plugin → install fails with CRL reason

---

## Phase 8 — MCP Hardening + ACP Server — ✅ (v1)

**Goal:** MCP as client (tool interop), ACP as server (editor interop, Zed).

**Shipped:** 8.1 MCP client wiring via `[mcp.servers]` config; every server's tools auto-register in the executor and benefit from trace-ref audit. 8.2 ACP server over stdio (`stado acp [--tools]`) — text-only without `--tools`, full agent-loop with git audit when `--tools` is set. **Pending:** per-MCP-server sandbox policy (currently they run with the calling process's privileges — once `tool.Host` gets `Sandbox() → Policy`, MCP servers inherit).

| # | Action |
|---|--------|
| 8.1 | **MCP client hardening** — each MCP server launch goes through sandbox layer with per-server policy. Server capability manifest declares caps in config; out-of-scope asks prompt user. Server output is audited to trace ref. |
| 8.2 | **ACP server** — `internal/acp/server.go`. Implement Zed's Agent Client Protocol (`github.com/zed-industries/agent-client-protocol`). Stdio transport, JSON-RPC framing, `session`/`newSession`/`prompt`/`cancel` lifecycle. Editor connects to `stado --acp` as its agent backend. Tool calls from Zed route through the same sandboxed tool runtime. |
| 8.3 | Header blurb on `stado acp` explaining capabilities (permission grants, file edits, etc.) |

**Verify:**
- Zed configured with stado as ACP agent → new session → edit file → approved/denied
- MCP server can't exceed its declared caps

---

## Phase 9 — Headless + Parallel Agents — ✅

**Goal:** Same core, multiple surfaces. True parallel agents.

**Shipped:** all 5 sub-phases. `internal/runtime` is the shared headless core; both TUI and `stado run` compose it. `stado headless` exposes a JSON-RPC 2.0 daemon surface (`session.new/prompt/list/cancel`, `tools.list`, `providers.list`, `shutdown`). `stado run --prompt` is the one-shot variant. `stado agents list/kill/attach` round out the parallel-agent story; every `runtime.OpenSession` drops `<worktree>/.stado-pid` so `agents list` can report liveness.

| # | Action |
|---|--------|
| 9.1 | Extract headless core: `internal/runtime/runtime.go` — session manager, agent loop, tool executor, state committer — all UI-independent. |
| 9.2 | `stado headless` — JSON-RPC over stdio surface matching TUI events. Enables scripting, CI integration, and TUI-as-client-of-daemon pattern. |
| 9.3 | `stado run --prompt "..." --agent claude-code-acp --max-turns 20 --json` — non-interactive; exit code reflects outcome; emits structured events. |
| 9.4 | **Parallel agents** — `stado session fork <id>` creates new worktree + branches → independent agent runtime. Manager multiplexes I/O, keeps a supervisory OTel trace per fork. TUI gets an "agents" pane showing all forks of current session. |
| 9.5 | `stado agents list/attach/kill` |

**Verify:**
- 3 agents on same repo in parallel don't clobber each other (separate worktrees)
- kill one → others unaffected; trace preserved

---

## Phase 10 — Release & Reproducibility — 🟢

**Goal:** Signed, reproducible, airgap-installable single binary.

**Shipped:** 10.1 reproducible builds (verified bit-for-bit with `-trimpath -buildvcs=true -buildid=` + pinned `mod_timestamp`), 10.2 SBOM via syft in goreleaser, 10.3 implementations (cosign keyless ✅ + minisign Ed25519 with BLAKE2b prehashed ✅), 10.4 `stado verify` exposing embedded build-info, 10.5 `-tags airgap` build — strips self-update network path, plugin CRL refresh, and webfetch tool network call; on-disk CRL cache still authoritative, `stado self-update` surfaces an airgap-install hint, 10.6 SLSA 3 provenance via `slsa-framework/slsa-github-generator` in the Release workflow, 10.7 goreleaser `nfpms` (.deb + .rpm) + `brews` tap wiring — goreleaser emits packages + opens a PR against `foobarto/homebrew-tap` on each release (external tap repo setup + repo-sign keys still user-driven), 10.8a `stado self-update` sha256 verify from checksums.txt + atomic swap with `.prev` backup, 10.8b minisign verification layered onto `stado self-update` (pinned `EmbeddedMinisignPubkey` + `checksums.txt.minisig`, enforced when both present, advisory otherwise). **Pending:** 10.3 offline minisign-key ceremony, 10.7 tap-repo + apt/rpm-repo server infra (goreleaser-side is done), 10.8b cosign half — lands alongside Phase 7.7 sigstore wiring.

| # | Action |
|---|--------|
| 10.1 | **Reproducible builds** — `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=true`, fixed build time via `-ldflags`. Matrix: `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`. |
| 10.2 | **SBOM** — `cyclonedx-gomod` per release; attached as artifact. |
| 10.3 | **Signing — dual scheme on every release:**<br>(a) **cosign keyless** via GitHub Actions OIDC → signatures + Rekor attestations<br>(b) **minisign Ed25519** (long-lived key, stored offline) → `.minisig` beside every artifact<br>Both shipped unconditionally. |
| 10.4 | **Binary-embedded trust roots** — compiled-in: minisign release pubkey, Fulcio root, pinned GitHub identity. `stado verify --show-builtin-keys` displays them. `stado verify <artifact>` verifies cosign if online, minisign unconditionally. |
| 10.5 | **Build tags** — Default build: full cosign + Rekor + minisign. `-tags airgap`: strips cosign (~3MB smaller), minisign-only. |
| 10.6 | **SLSA Level 3** provenance via `slsa-github-generator`. |
| 10.7 | **Distribution** — GitHub Releases (primary), Homebrew tap, apt/rpm repos (signed), `stado self-update` with signature verification. |
| 10.8 | **Rotation plan** published in `SECURITY.md`. |

**Verify:**
- Independent rebuild produces identical sha256
- `cosign verify-blob` passes against pinned identity + issuer
- `minisign verify` passes against embedded pubkey
- `stado self-update` refuses tampered download

---

## Phase 11 — Context Management — ⬜

**Goal:** Implement the four-concern context-management model specified
in [DESIGN §"Context management"](DESIGN.md#context-management):
prompt-cache efficiency, overflow handling, user-invoked compaction,
and tool-output curation. Forking (not summarisation) is the preferred
recovery for oversized sessions, so the phase also hardens
fork-from-point ergonomics to the point where reaching for it is
obvious.

**Design invariants** (copied here because they are acceptance criteria,
not just documentation — DESIGN is the single source of truth for wording):

- Append-only conversation history. The agent loop never rewrites prior
  turns. Any transformation that edits a past message invalidates every
  downstream cache entry and is therefore forbidden.
- No automatic compaction. Not on threshold breach, not in the
  background, not via any config flag. Fork-from-point is the recovery.
- Curation and caching are primary. Overflow handling is a safety net.
- Dedup is best-effort optimisation, never a correctness guarantee.

### 11.1 Prompt-cache awareness plumbing

**Scope:** `pkg/agent`, `internal/providers/*`, `internal/runtime`.

| # | Action |
|---|--------|
| 11.1.1 | Add append-only guardrail to `runtime.AgentLoop` — panic (in debug builds) or log+refuse (in release) if an in-place mutation of a prior `Message` is attempted. |
| 11.1.2 | Deterministic `TurnRequest.Tools` — sort by `Tool.Name()`. Any map-iteration source in the prompt-byte path is banned; lint (or a go-arch-lint rule) catches new offenders. |
| 11.1.3 | Cache-breakpoint placement in `providers/anthropic` — set `cache_control: ephemeral` on the final block of the stable prefix when `Capabilities.SupportsPromptCache` is true. |
| 11.1.4 | Capability-driven branching in `runtime.AgentLoop` per PLAN §1.6 — cache hints only populated when the active provider supports them. |
| 11.1.5 | **Cache-stability test** — render the system-prompt prefix twice with identical inputs, assert byte equality. Fails loudly on any clock/UUID/map-iteration leak. |
| 11.1.6 | **Tool-ordering test** — register tools in randomised order, assert serialised `TurnRequest.Tools` bytes are identical across runs. |

### 11.2 Token accounting

**Scope:** `pkg/agent`, `internal/providers/*`, `internal/runtime`,
`internal/tui`, `internal/headless`.

| # | Action |
|---|--------|
| 11.2.1 | Extend `agent.Provider` with `CountTokens(ctx, req) (int, error)` OR add a capability flag + per-provider tokenizer helper. Prefer the helper approach — it avoids round-tripping to the provider for every count. |
| 11.2.2 | Per-provider tokenizer wiring: Anthropic `Messages.CountTokens` (or official tokenizer), OpenAI `tiktoken`, Google genai tokenizer, OAI-compat uses `tiktoken` by default with a config override. |
| 11.2.3 | Capability probe on first provider use. A backend that cannot report counts is a hard error on first turn — **refuse to proceed blind**. |
| 11.2.4 | Soft/hard threshold enforcement as percentages of `Capabilities.MaxContextTokens`. Defaults: soft 70%, hard 90%. Configurable under `[context]` in config. |
| 11.2.5 | Soft threshold surface: TUI shows a dismissable warning indicator; headless emits `session.update { kind: "context_warning" }`. |
| 11.2.6 | Hard threshold surface: next turn blocked. User prompted to fork, `session compact`, or abort. |
| 11.2.7 | **Token-counting fidelity test** — per provider, assert reported count matches the provider's own count for a fixed prompt within 1% tolerance. |

### 11.3 User-invoked compaction

**Scope:** `cmd/stado/session.go`, `internal/runtime`, `internal/state/git`,
`internal/tui`.

| # | Action |
|---|--------|
| 11.3.1 | `stado session compact <id>` CLI subcommand. |
| 11.3.2 | TUI action — command-palette entry + slash command (`/compact`). |
| 11.3.3 | Summarisation call — uses the active provider, cheap-model preference where available (e.g. Anthropic haiku class). **Open question:** should summarisation be pinned to a separate `[context.compaction.model]` config? Deferred until we see real usage. |
| 11.3.4 | Summary-preview-edit-confirm flow. User sees the proposed summary, can edit, can reject. No commit without explicit confirmation. |
| 11.3.5 | Dual-ref compaction commit: `tree` gets the summary-replaces-turns commit, `trace` keeps raw turns unchanged. `checkout tree~1` restores pre-compaction state. |
| 11.3.6 | Compaction-marker metadata surfaced by `stado session show` — which turns, when, summary SHA. |

### 11.4 Tool-output curation + in-turn dedup

**Scope:** `pkg/tool`, `internal/tools/*`, `internal/runtime`.

| # | Action |
|---|--------|
| 11.4.1 | Per-tool default output budgets (tokens). See DESIGN §"Tool-output curation" for the table. Implemented as a `Tool.DefaultBudget() int` method with a sensible base default (4K). |
| 11.4.2 | Truncation markers — explicit `[truncated: X of Y … call with range=... for more]` so the model knows it can request more. |
| 11.4.3 | `read` tool args extended with `start?: int, end?: int` (1-indexed, inclusive, `end=-1` → EOF). Rename `fs.PathArgs` → `fs.ReadArgs` along the way (codebase hygiene, not a spec requirement). |
| 11.4.4 | Extend `pkg/tool.Host` with `PriorRead(key ReadKey) (PriorReadInfo, bool)` and `RecordRead(key ReadKey, info PriorReadInfo)`. See DESIGN §"Tool interface" for exact semantics. |
| 11.4.5 | Ship a `nullHost` helper in the tools package — zero-behaviour Host for tests. `PriorRead` returns `(PriorReadInfo{}, false)`, `RecordRead` is a no-op. ~20 LOC; saves every test double from reimplementing the same stub. |
| 11.4.6 | Executor maintains the read log as `map[ReadKey]PriorReadInfo` behind a `sync.Mutex`. Per-process lifetime. Process-local turn counter increments on each top-level user prompt. |
| 11.4.7 | `read` tool calls `PriorRead` / hashes current file region / compares / returns reference response on match, fresh read otherwise. Hash via `io.MultiWriter` during the read itself (one pass over bytes). |
| 11.4.8 | Range canonicalisation inside the `read` tool — `""` for full-file, `"<start>:<end>"` for ranged. Resolution of any alternative input shape into canonical form happens before `ReadKey` is constructed. |
| 11.4.9 | **Truncation coverage test** — for each bundled tool, assert default budget is respected and truncation marker is present when hit. |
| 11.4.10 | **Read-dedup invariants test** — PriorRead/RecordRead round-trip, staleness check (modified file → fresh read, not reference), range canonicalisation asserted for every input shape, concurrent reads under mutex don't corrupt the log. |

### 11.5 Fork-from-point ergonomics

**Scope:** `cmd/stado/session.go`, `internal/tui/fork` (new package),
`internal/state/git`.

| # | Action |
|---|--------|
| 11.5.1 | `stado session fork <id> --at <turn-ref>` — extends the existing `session fork` (which forks from tree HEAD). `<turn-ref>` accepts `turns/N` or full commit SHA. No-`--at` form preserved. |
| 11.5.2 | `stado session tree <id>` — **standalone cobra subcommand** with its own `tea.Program`. Not a slash command in the main TUI (that may come later as an additional surface). |
| 11.5.3 | `session tree`'s navigable view renders turn boundaries only by default; sub-turn commits reachable via git tooling with the SHA escape hatch. Single keybinding on cursor-selected turn forks into a fresh session rooted there. |
| 11.5.4 | PTY test harness — `github.com/creack/pty` (de facto Go standard, zero non-stdlib transitive deps, neutral to the charm ecosystem). New infrastructure; one-time setup cost. |
| 11.5.5 | **Scripted-path test** — `session fork <id> --at turns/<N>` single invocation → child session whose tree-ref head matches parent's `turns/<N>` tag, worktree materialised to match. |
| 11.5.6 | **Interactive-path test** — drive `session tree` through the PTY harness, navigate to a turn, press the fork keybinding, assert the resulting session's tree-ref and materialised worktree. |

### 11.6 Non-goals (explicit)

A contribution proposing any of these must first justify why
fork-from-point is inadequate for their use case. Landing such a
contribution requires revising DESIGN first, not back-door-ing through
a PR:

- Automatic or background summarisation of any kind.
- Semantic importance scoring of individual turns.
- Vector-store-backed "memory" of prior sessions.
- Sliding-window auto-eviction without user consent.

**Verify (Phase 11 as a whole):**
- A long session with repeated reads of the same unchanged file shows
  a single disk read plus reference responses; modifying the file
  between reads produces fresh reads.
- Cache-hit ratio metric (once Phase 6 span-wrapping lands, PR A)
  reports >80% on the stable-prefix tokens across turns 2+.
- Soft threshold warning fires at 70% on a synthetic session that
  fills context; hard threshold blocks the next turn at 90%.
- `session fork <id> --at turns/5` in one shell, `session tree <id>`
  + keybinding in another, both produce equivalent child sessions
  when rooted at the same turn.
- No automatic compaction path exists — search the codebase for any
  call to `Compact` that isn't gated behind an explicit user action.

---

## Cross-Cutting Decisions

| Decision | Resolution |
|----------|------------|
| LLM abstraction | Tight internal `pkg/agent` interface (~200 LOC) — a coding-agent interface, not a generic LLM interface. 4 direct implementations: Anthropic, OpenAI, Google, OAI-compat HTTP. No third-party abstraction library. |
| Session storage | Sidecar bare repo `${XDG_DATA_HOME}/stado/sessions/<repo-id>.git` with alternates to user's `.git/objects`. Worktrees at `${XDG_STATE_HOME}/stado/worktrees/<session-id>/`. User repo stays pristine. |
| Commit granularity | Dual-ref: `tree` (mutations only, diff-then-commit for exec) + `trace` (every tool call, empty-tree commits). Turn boundaries as tags. |
| Signing | Releases: cosign keyless (primary) + minisign (airgap fallback), both on every release. Plugins: Ed25519 signed manifest envelope with capability binding, rollback protection, optional Rekor attestation. |
| Context engine | Deleted. Replaced with ripgrep, ast-grep, LSP-backed tools, and `read_with_context`. |
| TUI | Keep bubbletea TUI + add headless mode. |
| ACP | MCP as client for tool interop (Anthropic's protocol). Zed's Agent Client Protocol as server for editor interop. |
| Inference | One OAI-compat HTTP client. Three documented presets (ollama, llamacpp, vllm) + custom. llama.cpp `llama-server` as primary reference/test target. |
| Windows sandbox | Minimal in v1 (documented warning); proper job objects + restricted tokens in v2. |
| Agent bot identity | Per-agent (e.g. `claude-code-acp <agent@stado.local>`) so `git log --author` can filter. |
| Approval persistence | Session-scoped remember with explicit "forget approvals" command. |
| Plugin ABI versioning | SemVer on host imports; bump `min_stado_version` when ABI breaks. |

---

## Remaining work

The original greenfield PR sequence (PRs 1–13 covering Phases 0–10)
has landed. What's left, in the order I'd tackle it:

| PR | Content | Phase |
|----|---------|-------|
| A  | ✅ OTel span instrumentation: `tools.Executor.Run` / `runtime.AgentLoop` / all 4 providers' `StreamTurn` wrapped. Phase 6 closed. | 6 |
| B  | ✅ Phase 11.1 — cache-awareness plumbing: append-only guardrails, deterministic tool serialisation, `cache_control` breakpoint placement driven by `Capabilities.SupportsPromptCache`, cache-stability + tool-ordering tests. | 11 |
| C  | ✅ Phase 11.2 — agent.TokenCounter + 4 provider impls (anthropic HTTP, openai/oaicompat tiktoken offline, google HTTP), `[context]` config, TUI warning-coloured ctx%, first-turn capability probe, **hard-threshold turn block (11.2.6): user-submit path refuses fresh turns above `ctx_hard_threshold` and surfaces a /compact-or-fork advisory; in-flight tool continuations unaffected**. | 11 |
| D  | 🟡 Phase 11.3 — shipped: TUI `/compact` + `stateCompactionPending` state + y/n/e confirmation + inline summary editor (`e` key → `stateCompactionEditing`, Enter commits, Esc/'n' cancels, user's in-flight draft preserved) + `internal/compact` package (summarisation prompt + async Summarise) + advisory `stado session compact` CLI stub + full test coverage. Remaining: dual-ref compaction commit on `tree` + `trace` (needs conversation persistence, not yet in stado). | 11 |
| E  | ✅ Phase 11.4 — ranged `read` args, content-hash dedup, Host.PriorRead/RecordRead, ReadLog, NullHost, per-tool output budgets (read/webfetch/bash/grep/glob/ripgrep) with DESIGN-spec'd truncation markers, and full invariants + truncation-coverage test suites. | 11 |
| F  | ✅ Phase 11.5 — shipped: `session fork <id> --at <turns/N\|sha>` scripted path, standalone `session tree <id>` cobra subcommand with its own tea.Program (navigate + press `f` to fork), `Sidecar.ListTurnRefs` helper, scripted + interactive integration tests. PTY harness pending for a future full end-to-end test. | 11 |
| G  | ✅ Phase 3.3 — seccomp BPF compiler: `internal/sandbox/seccomp_linux.go` hand-rolled allow-default + curated kill-list (`mount`/`umount2`/`reboot`/`kexec_*`/`init_module`/`finit_module`/`delete_module`/`keyctl`/`ptrace`/`process_vm_writev`), per-arch syscall tables, `bwrap --seccomp=FD` integration. Non-Linux stubs in `seccomp_other.go`. | 3 |
| H  | ✅ Phase 3.5 — macOS `sandbox-exec` runner: `internal/sandbox/sbx_profile.go` generates Scheme-ish `.sb` DSL from Policy; `runner_darwin.go` SbxRunner writes profile to tempfile + spawns `sandbox-exec -f <profile> -- cmd`. | 3 |
| I  | 🟡 Phase 3.6 — Windows v1 shipped: `runner_windows.go` WinWarnRunner emits one-time warning + runs unsandboxed. v2 (job objects + restricted tokens) still pending. | 3 |
| J  | ✅ Phase 4.1/4.2 — binary-embed release pipeline: `hack/fetch-binaries.go` downloads ripgrep/ast-grep per-(OS,arch) and emits `bundled_<os>_<arch>.go` with `//go:build stado_embed_binaries && <os> && <arch>` + `//go:embed` directives; goreleaser before-hook runs the fetcher and release build passes the tag; dev builds without the tag fall back to PATH. | 4 |
| K  | ✅ Phase 7.1 — wazero runtime host: `internal/plugins/runtime/` with scaffold + lifecycle (7.1a), host imports `stado_log` / `stado_fs_read` / `stado_fs_write` (7.1b), `PluginTool` adapter + `stado plugin run` CLI (7.1c). | 7 |
| L  | ✅ Phase 7.6 — plugin CRL: `internal/plugins/crl.go` (LoadLocal / SaveLocal / Sign / IsRevoked) + `crl_online.go` (Fetch, `!airgap`) / `crl_airgap.go` (ErrAirgap, `airgap`); Ed25519-signed JSON with canonical-bytes invariant, `[plugins]` config section, `stado plugin verify` consults CRL with cache fallback. | 7 |
| L2 | ✅ Phase 7.7 — Rekor transparency log: direct REST client in `internal/plugins/rekor.go` (hashedrekord v0.0.1, PEM-encoded ed25519 pubkeys, no sigstore deps); `Upload` / `SearchByHash` / `FetchEntry` / `VerifyEntry`; `[plugins].rekor_url` config; `stado plugin verify` performs hash-index lookup and asserts entry sig/pubkey/digest match the trust-store-verified manifest. Mismatch is fatal; absence or airgap stubs are advisory. | 7 |
| M  | ✅ Phase 8.1 — per-MCP-server sandbox policy: config.MCPServer gains `capabilities []string`, `mcp.ParseCapabilities` maps forms (fs/net/exec/env) to `sandbox.Policy`, `mcp.ServerConfig` carries a Runner + Policy, and `transport.WithCommandFunc` routes stdio-server spawns through `sandbox.Runner.Command`. Unsandboxed servers warn on stderr. | 8 |
| N  | 🟡 Phase 9.4/9.5 — fork-time `stado.session.fork` span landed (parent id / child id / root commit / at_turn_ref attrs). Cross-process span-link from child sessions back to the parent's context still pending — needs persisted span context, which stado doesn't carry across session-spawn boundaries yet. | 9 |
| O  | Phase 10.3b — offline minisign-key ceremony + pubkey commit to `internal/audit/embedded.go`. | 10 |
| P  | ✅ Phase 10.5 — `-tags airgap` build: splits self-update, plugin CRL Fetch, and webfetch.Run into `!airgap` / `airgap` pairs. Airgap binary physically cannot reach the network from its own control plane; provider HTTP (user's chosen inference target) untouched. | 10 |
| Q  | 🟡 Phase 10.7 — goreleaser `nfpms` (.deb + .rpm) + `brews` tap wiring shipped in `.goreleaser.yaml`; external tap repo + apt/rpm server infra + signing-key setup still user-driven. | 10 |
| R  | 🟡 Phase 10.8b — minisign verification wired: `internal/audit.EmbeddedMinisignPubkey` (ldflags-seedable var, empty default) + `verifyChecksumsMinisig` covers the four (pin × sig) states with advisory-degrade on unpinned builds. 6 tests. Cosign online verification still pending (sigstore deps — lands alongside Phase 7.7). | 10 |

PRs B–F compose Phase 11 and are best landed in order — each builds on
the previous. Everything else (A, G–R) is independent; land in whatever
order matches priorities.

**Rough estimate for PRs A–F (the Phase 6 + Phase 11 arc):** 3–4 weeks
of focused work. Phase 11.5 interactive (PR F) is the longest single
task because the PTY harness is new infrastructure.

---

## Offline / Airgap Honesty

Be honest in docs about what "works offline" means at the model capability level. A Claude Sonnet-class coding experience is not replicated by Qwen2.5-Coder-32B or Llama-3.3-70B on a laptop — they're genuinely useful but distinctly weaker at long agentic tool-use loops. The airgap wedge is real for users who legally can't send code to a cloud provider; it's a lie for users who just want to save money and expect frontier-model quality from a 7B model on their MacBook. Setting expectations in the README saves angry issues.

---

## Architecture

### Package layout

```
pkg/
  agent/        Provider seam (Provider, TurnRequest, Event, Message, Block…).
  tool/         Tool + Classifier interfaces + Host + ApprovalRequest.
internal/
  providers/
    anthropic/  Direct anthropic-sdk-go.
    openai/     Direct openai-go (Chat Completions).
    google/     Direct generative-ai-go.
    oaicompat/  Hand-rolled /v1/chat/completions HTTP + SSE.
  state/git/    Sidecar, dual-ref, commits, tree materialisation.
  audit/        Ed25519 commit signing, walker, JSONL export, minisign.
  sandbox/      Policy, runners (NoneRunner, BwrapRunner), landlock, proxy.
  tools/        Registry, Executor, classification; subdirs per tool:
                bash / fs / webfetch / rg / astgrep / readctx / lspfind.
  lsp/          Pure-Go LSP client (Content-Length framing, process mgmt).
  runtime/      UI-independent core. OpenSession, BuildExecutor, AgentLoop.
  mcp/          MCP client (process/HTTP transports).
  mcpbridge/    MCPTool adapter so MCP servers' tools satisfy pkg/tool.Tool.
  acp/          JSON-RPC 2.0 line-delimited + Zed ACP server.
  headless/     JSON-RPC 2.0 daemon (editor-neutral namespace).
  telemetry/    OpenTelemetry runtime (exporters, metrics, span names).
  plugins/      Manifest + trust-store + signing (runtime pending).
  config/       TOML via koanf; XDG paths; preset lookup.
  tui/
    theme/      TOML-loadable Theme + bundled default.toml.
    render/     text/template engine + per-widget .tmpl files.
    input/      textarea wrapper + history ring buffer.
    keys/       Action enum + Registry + default bindings + overrides.
    palette/    Modal command palette (Ctrl+P).
    overlays/   Help overlay.
cmd/stado/      Cobra CLI: main, run, session, agents, audit, plugin,
                acp, headless, doctor, verify, self-update, config_init.
```

### Dependency rules

- `pkg/` never imports `internal/`.
- `pkg/agent` never imports any concrete provider.
- `internal/state/git` never imports `internal/audit` (signature hook is a
  `CommitSigner` interface; keeps the cycle from forming).
- `internal/runtime` is the only place `internal/tui`, `cmd/stado/run.go`,
  `cmd/stado/acp.go`, `cmd/stado/headless.go` share session/executor
  construction. Every new surface should compose via `runtime.*`.
- `internal/telemetry` never imported from the critical path of
  `state/git` — `Session.OnCommit` is a plain callback so a test or a
  no-op runtime doesn't drag the OTel exporters in.

### Turn lifecycle

One agent turn, top to bottom:

```
user prompt
  │
  ▼
Model.startStream(ctx)                           — TUI
  │  Model.toolDefs()  ─── Plan mode filters out Mutating/Exec tools
  │
  ▼
provider.StreamTurn(ctx, TurnRequest)            — pkg/agent
  │
  ▼
events: TextDelta / ThinkingDelta / ToolCallStart / ToolCallArgsDelta /
        ToolCallEnd / Usage / Done / Error
  │
  ▼
Model.handleStreamEvent — accumulates per-turn text/thinking/tool_calls
  │
  ▼
Model.onTurnComplete — flushes assistant Message into history
  │
  ├── len(tool_calls) == 0 → stateIdle, done
  │
  └── tool_calls > 0 → approval queue
                ├── rememberedAllow[name]=true → auto-execute
                └── else → prompt user (y/n)
                            ▼
                      tools.Executor.Run(name, input, host)
                            │  (1) resolve Tool + Classifier.Class
                            │  (2) NOTE ClassExec: snapshot pre-tree
                            │  (3) tool.Run() — in-process or exec child
                            │  (4) compute post-state (diff for Exec)
                            │  (5) always commit to trace ref
                            │  (6) commit to tree if Mutating (success),
                            │      or Exec-with-diff
                            │  (7) signer (if set) signs commit body
                            │  (8) Session.OnCommit → slog / OTel
                            │
                            ▼
                      ToolResultBlock appended to pending results
                │
                ▼  (queue drained)
        toolsExecutedMsg → Model appends role=tool Message →
        Model.startStream()  (loop until no tool_calls, or max turns)
```

### Key invariants

- **User repo is read-only** from stado's perspective. Every mutation lives
  under `${XDG_DATA_HOME}/stado/sessions/<repo-id>.git` (sidecar) or
  `${XDG_STATE_HOME}/stado/worktrees/<id>/` (session worktree).
- **Alternates link** sidecar → user's `.git/objects`, so session refs
  can reference any commit in the user repo without copying objects.
- **Dual-ref commit policy** (see §2.4): every tool call commits to
  `refs/sessions/<id>/trace` (empty tree); only mutating or exec-with-diff
  tool calls commit to `refs/sessions/<id>/tree`. Turn boundaries tagged
  as `refs/sessions/<id>/turns/<n>`.
- **Thinking blocks round-trip verbatim**: `agent.ThinkingBlock.Signature`
  + `Native` are carried back to the provider on the next turn so
  Anthropic extended-thinking + tool-use sequences don't break.
- **Provider is lazy**: `stado` boots with zero API keys. `ensureProvider`
  runs on first prompt; failures surface in-UI with an actionable hint.
- **Tools are classified at registration**. Mutation class drives commit
  policy; Plan mode drops Mutating/Exec out of `TurnRequest.Tools`
  entirely so the model literally can't request them.
- **Signatures cover `stado-audit-v1` framing** (tree hash + parent hashes
  + body with any preexisting sig trailer stripped). Tampering with any
  commit field the framing covers invalidates the signature — `stado
  audit verify` walks refs and reports first-invalid-at.

### Data paths and XDG

| Purpose | Path | Notes |
|---|---|---|
| Config | `${XDG_CONFIG_HOME:-~/.config}/stado/config.toml` | Scaffolded by `stado config init`. |
| Theme override | `${XDG_CONFIG_HOME}/stado/theme.toml` | Merged over bundled default. |
| Sidecar bare repo | `${XDG_DATA_HOME:-~/.local/share}/stado/sessions/<repo-id>.git` | One per user repo; shared across sessions for that repo. |
| Agent signing key | `${XDG_DATA_HOME}/stado/keys/agent.ed25519` | Chmod 0600; created on first run. |
| Plugin trust store | `${XDG_DATA_HOME}/stado/plugins/trusted_keys.json` | |
| Session worktrees | `${XDG_STATE_HOME:-~/.local/state}/stado/worktrees/<session-id>/` | Volatile; safe to delete. |
| Pid file (running TUI / run) | `<worktree>/.stado-pid` | Consumed by `stado agents list/kill`. |

---

## Implementation notes & conventions

### Error surfaces

1. Boot-time errors (`stado` startup, `stado run --help`) must NOT depend on
   network or API keys. The provider is built lazily.
2. Runtime errors in a tool call populate `res.Error` + set `is_error` on
   the `ToolResultBlock`; the model sees the error content and can adapt.
3. Session/sidecar errors at TUI boot are non-fatal — TUI continues with
   `session=nil` and audit disabled; a stderr line mentions the
   degradation. Rationale: users on read-only repos or in sandboxes
   should still be able to chat.

### go-git constraints

- We never use `git worktree add`. Instead worktrees are plain directories
  whose contents we materialise from a tree hash via
  `Session.MaterializeTreeToDir`. The sidecar's `alternates` makes this
  cheap: no object copying.
- Commits are synthesised directly (go-git `object.Commit.Encode`) rather
  than through the Worktree API; lets us attach signature trailers
  without round-tripping through the worktree.

### Streaming events

- Providers emit `agent.Event` over a buffered channel (cap 16). The
  channel closes on `Done`/`Error`.
- `ToolCall` is tracked across `Start / ArgsDelta / End`. Parallel calls
  are distinguished by `index` (OAI) or by `content_block_index` in
  Anthropic's SDK events.
- ACP + headless re-emit `session.update` notifications for every
  `TextDelta` / `ToolCallEnd` so editor clients can stream progress
  without reimplementing the provider SSE parser.

### Tool execution cost accounting

- Every `tools.Executor.Run` records
  `stado_tool_latency_ms` regardless of outcome.
- Usage tokens from each `EvUsage` / `EvDone` event accumulate in
  `Model.usage`. Cost is provider-specific and currently not computed;
  providers could populate `Usage.CostUSD` from a pricing table on the
  agent side.

### Sandbox policy composition

`Policy.Merge(inner)` is an INTERSECTION:

- FS allow-lists: intersect (restrict-only-further).
- Net: stricter of the two kinds wins (`DenyAll` > `AllowHosts` > `AllowAll`).
- Exec allow-list: intersect.
- Env passthrough: intersect.
- Timeout: shorter positive value wins.

An outer "session" policy (read-everywhere, write-worktree-only) can be
composed with an inner per-tool policy (exec=[rg] only, net=DenyAll) to
narrow further. Never to widen.

### Plan / Do mode

- **Plan mode** (`Tab`): `Model.toolDefs` filters to `NonMutating` only.
  Left border of the input box turns yellow (`warning`). The model
  literally can't request write/edit/bash — principled enforcement, not
  an approval-loop workaround.
- **Do mode**: full toolset. Left border green (`success`).
- Toggle is per-conversation-state and persists across turns until
  changed.

### Approvals

- Every tool call — in Do mode — is queued and shown to the user with
  y/n.  `/approvals always <tool>` auto-approves that tool name for the
  rest of the session; `/approvals forget` clears.
- Denials feed a `"Denied by user"` error back to the model as a
  `ToolResultBlock{IsError: true}` — the model can adapt (ask a
  different question) rather than hanging.

---

## Design notes for remaining sub-phases

Approach, risks, and open questions for the gnarlier pending items.
For the PR-level breakdown of what's left, see §"Remaining work"
above. This section is design-detail only — the kind of notes you'd
want open on the side while writing the PR.

### Phase 3.3 — Linux seccomp BPF

**Approach:** compile a sock_filter[] at startup from a Policy allow-list
of syscalls, then `seccomp_set_mode_filter` after `PR_SET_NO_NEW_PRIVS`.

**Risks:** Go runtime needs a wide-ish syscall set (`futex`, `clone`,
`rt_sigaction`, `mmap`, `mprotect`, `nanosleep`, …). A too-narrow filter
deadlocks the runtime. Proper seccomp should run in a child process
spawned by bwrap (`bwrap --seccomp=FD` accepts a pre-compiled filter fd)
rather than in-process.

**v1 target:** compile a BPF program from Policy, write it to a file
descriptor, and pass via `BwrapRunner`'s `--seccomp` flag. No in-process
seccomp.

### Phase 4.1/4.2 — binary embed pipeline

**Approach:**

1. `hack/fetch-binaries.go`: at build time, download the ripgrep +
   ast-grep release assets for the matrix in `.goreleaser.yaml`,
   verify sha256, stage into `internal/tools/rg/bundled/` +
   `internal/tools/astgrep/bundled/`.
2. `go:embed` each per-GOOS/GOARCH blob via a build-tagged file:
   `rg_linux_amd64.go` etc. embed `bundled/rg-linux-amd64`.
3. First use: extract to `${XDG_CACHE_HOME}/stado/bin/rg-<sha256>[.exe]`,
   verify hash, `exec` from there.
4. `-tags airgap` excludes the non-host-arch blobs (keeps binary small).

**Open questions:** licensing — ripgrep is MIT, ast-grep is MIT, so
bundling is fine. Must include LICENSE files in the extracted cache
directory.

### Phase 7.1 — wazero runtime

**Plan:**

- `internal/plugins/runtime_wazero.go` (build tag `!airgap`): wazero
  runtime preloaded with WASI preview 1 + 4 host imports:
  - `stado_fs_read(path, buf, len) → n` — proxies through sandbox
  - `stado_fs_write(path, buf, len) → n`
  - `stado_net_http(method, url, body, …)` — routes via sandbox net
    proxy
  - `stado_log(level, msg)` — structured slog
  - `stado_tool_register(name, desc, schema)` — plugin exports tools
- Plugin lifecycle: verify manifest → verify wasm sha256 → check
  `min_stado_version` → check rollback → prompt user for capability
  grant → instantiate wazero module → call exported `_stado_init` →
  on exit, flush OTel span, close runtime.
- `internal/plugins/runtime_noop.go` (build tag `airgap`): stub that
  returns "plugins unavailable" — keeps the binary small for airgap.

**Open questions:** host-import ABI versioning scheme. Leaning toward
`min_stado_version` field in the manifest being the canonical gate —
bump it when an import's signature changes. Plugins compiled against
an older host version refuse to load.

### Phase 8.1 — per-MCP-server sandbox

**Plan:** extend `config.MCPServer` with an optional `capabilities`
field:

```toml
[mcp.servers.github]
command = "mcp-github"
capabilities = ["net:api.github.com", "env:GITHUB_TOKEN"]
```

`runtime.attachMCP` maps these to a `sandbox.Policy` and spawns the
server via `sandbox.Runner.Command(policy, cmd)` — bubblewrap on
Linux, sandbox-exec on macOS. Out-of-manifest syscalls fail visibly.

### Phase 9.4/9.5 — supervisory trace across forks

**Plan:** when `stado session fork` runs, emit an OTel span
`stado.session.fork` with attributes `parent.id` + `child.id`. When
child sessions run tools, their spans link back via `trace.Link` to the
parent's span context so a single trace visualises the whole fork graph.

### Phase 10.3/10.4 — release key ceremony

**Plan:**

1. Offline Ed25519 key generated on an air-gapped machine (e.g.
   `minisign -G` on a live-USB).
2. Public key + fingerprint committed to `internal/audit/embedded.go`
   via `go:generate` so `stado verify` can validate without network.
3. Each `v*` tag: CI fetches a pre-signed `release.minisig` that was
   produced offline against the checksums.txt draft (manual step; GA
   later once we have a secure signing service).

### Phase 10.7 — distribution

**Homebrew:** separate tap `foobarto/homebrew-stado` with a Formula
that points at the GitHub release `.tar.gz`, verifies the minisig
against the embedded pubkey, and installs.

**apt/rpm:** use `nfpm` (already goreleaser-compatible) to produce
`.deb` and `.rpm` with the same reproducibility flags. Sign the
repo metadata with the same minisign key.

---

## Testing strategy

- **Unit tests** for pure translation layers (`convertMessages`,
  `parseMatches`, `canonicalise`), error paths (`ResolveBinary` env
  fallbacks), and protocol framing (JSON-RPC, LSP Content-Length).
- **Integration tests** that skip gracefully when a binary isn't
  available (ripgrep integration tests run if `rg` on PATH; ast-grep
  similar; LSP tests gated on `gopls`).
- **Subprocess re-exec** for irreversible process-wide syscalls —
  landlock can't be undone, so the test forks itself with an env
  marker and inspects the child's exit code. See
  `.learnings/testing-irreversible-process-syscalls.md`.
- **End-to-end commit-chain tests** in `internal/audit/integration_test.go`:
  sign real session commits, tamper with the message, prove verify
  detects the tamper.
- **Reproducibility**: two sequential `go build -trimpath -buildvcs=true
  -ldflags='-s -w -buildid='` invocations must produce identical sha256.

---

## Configuration surface

Full `config.toml` shape (scaffolded by `stado config init`):

```toml
[defaults]
provider = "anthropic"              # bundled name or user-defined preset
model    = "claude-sonnet-4-5"

[approvals]
mode      = "prompt"                # "prompt" | "allowlist"
allowlist = ["read", "glob", "grep", "ripgrep", "ast_grep"]

[inference.presets.my-proxy]
endpoint = "https://proxy.example/v1"

[mcp.servers.github]
command = "mcp-github"
args    = ["--readonly"]
env     = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }

[otel]
enabled     = false                  # default off
endpoint    = "localhost:4317"
protocol    = "grpc"                 # "grpc" | "http"
insecure    = true
sample_rate = 1.0
```

**Env-var overrides:** any key prefixed with `STADO_` with underscores
mapping to nested dots — e.g. `STADO_DEFAULTS_PROVIDER=ollama`
`STADO_OTEL_ENABLED=1`.

---

## Release pipeline (as-built + planned)

1. **Tag** `v0.X.Y` pushed to main.
2. **`.github/workflows/release.yml`** runs goreleaser on Linux/darwin/
   windows × amd64/arm64 with reproducible flags.
3. goreleaser produces: per-target archive, `checksums.txt`, SBOM
   (syft), cosign signature over `checksums.txt` (Rekor entry implicit).
4. `slsa-framework/slsa-github-generator` attestation step consumes
   goreleaser's `artifacts.json` and produces a SLSA 3 provenance
   document attached to the release.
5. **Planned:** minisign signing step using offline key (see §10.3);
   Homebrew tap push via `brew-tap` release job; `nfpm` .deb/.rpm
   publish to a signed apt/rpm repo; `stado self-update` verifies both
   cosign (online) and minisign (airgap) before installing.
   
