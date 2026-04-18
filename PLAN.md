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

---

## Phase 0 — Demolition

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

## Phase 1 — Coding-Agent Provider Interface

**Goal:** ~200 LOC seam in `pkg/agent` encoding what the agent loop actually needs; 4 direct implementations. No third-party LLM abstraction library.

### 1.1 `pkg/agent/agent.go` — the interface

```go
type Provider interface {
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

## Phase 2 — Git-Native State Core

**Goal:** Sidecar repo with alternates; dual-ref; turn tags; diff-then-commit.

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
| `stado session attach <id>` | Reattach to existing session |
| `stado session delete <id>` | Remove session + worktree |
| `stado fork <session>` | New worktree + both refs forked (parallel agent) |
| `stado land <session>` | Push `refs/sessions/<id>/tree` to user repo as `<branch-name>` |
| `stado revert <commit-or-turn>` | `git reset --hard` on a new child session branch |

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

## Phase 3 — Sandbox Layer

**Goal:** Platform-abstracted policy enforcement. Capabilities declared, OS enforces.

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

## Phase 4 — Tool Runtime Overhaul

**Goal:** Replace bespoke context engine with solid search primitives + LSP; wire diff-then-commit.

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

## Phase 5 — Tamper-Evident Audit

**Goal:** Signed git refs as the audit primitive.

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

## Phase 6 — OpenTelemetry from Boot

**Goal:** Traces/metrics/logs across every boundary; off by default, one-line enable.

| # | Action |
|---|--------|
| 6.1 | `internal/telemetry` — `go.opentelemetry.io/otel` + OTLP/gRPC + OTLP/HTTP exporters. Resource attrs: `service.name`, `service.version`, `session.id`, `repo.id`, `agent.name`. |
| 6.2 | **Span hierarchy:** `stado.session` → `stado.turn` → `stado.tool_call` → `stado.sandbox.exec` → `stado.provider.stream` |
| 6.3 | **Metrics:** `stado_tool_latency_ms` (tool, outcome), `stado_tokens_total` (provider, model, direction), `stado_cache_hit_ratio` (provider, model), `stado_approval_rate` (tool, decision), `stado_sandbox_denials_total` (tool, reason) |
| 6.4 | `slog` + `otelslog` — structured logs correlated with spans. |
| 6.5 | Config `[otel]` section — exporter, endpoint, sampling, headers, insecure, timeout. |

**Verify:** local docker-compose with jaeger + otel-collector; run a TUI session; see full trace hierarchy in Jaeger UI.

---

## Phase 7 — WASM Plugin Runtime + Signed Manifest

**Goal:** Third-party plugins run in wazero, capability-gated, signed.

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

## Phase 8 — MCP Hardening + ACP Server

**Goal:** MCP as client (tool interop), ACP as server (editor interop, Zed).

| # | Action |
|---|--------|
| 8.1 | **MCP client hardening** — each MCP server launch goes through sandbox layer with per-server policy. Server capability manifest declares caps in config; out-of-scope asks prompt user. Server output is audited to trace ref. |
| 8.2 | **ACP server** — `internal/acp/server.go`. Implement Zed's Agent Client Protocol (`github.com/zed-industries/agent-client-protocol`). Stdio transport, JSON-RPC framing, `session`/`newSession`/`prompt`/`cancel` lifecycle. Editor connects to `stado --acp` as its agent backend. Tool calls from Zed route through the same sandboxed tool runtime. |
| 8.3 | Header blurb on `stado acp` explaining capabilities (permission grants, file edits, etc.) |

**Verify:**
- Zed configured with stado as ACP agent → new session → edit file → approved/denied
- MCP server can't exceed its declared caps

---

## Phase 9 — Headless + Parallel Agents

**Goal:** Same core, multiple surfaces. True parallel agents.

| # | Action |
|---|--------|
| 9.1 | Extract headless core: `internal/core/runtime.go` — session manager, agent loop, tool executor, state committer — all UI-independent. |
| 9.2 | `stado headless` — JSON-RPC over stdio surface matching TUI events. Enables scripting, CI integration, and TUI-as-client-of-daemon pattern. |
| 9.3 | `stado run --prompt "..." --agent claude-code-acp --max-turns 20 --json` — non-interactive; exit code reflects outcome; emits structured events. |
| 9.4 | **Parallel agents** — `stado fork <session>` creates new worktree + branches → independent agent runtime. Manager multiplexes I/O, keeps a supervisory OTel trace per fork. TUI gets an "agents" pane showing all forks of current session. |
| 9.5 | `stado agents list/attach/kill` |

**Verify:**
- 3 agents on same repo in parallel don't clobber each other (separate worktrees)
- kill one → others unaffected; trace preserved

---

## Phase 10 — Release & Reproducibility

**Goal:** Signed, reproducible, airgap-installable single binary.

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

## Suggested PR Sequence

| PR | Content |
|----|---------|
| 1 | Phase 0 (demolition only) |
| 2 | Phase 1.1 interface + 1.5 OAI-compat (proves interface shape with simplest provider) |
| 3 | Phase 1.2–1.4 (three SDK-backed providers) |
| 4 | Phase 2.1–2.6 (git-native core) |
| 5 | Phase 2.7–2.8 + CLI (`stado session/fork/land/revert`) |
| 6 | Phase 3 (sandbox layer) |
| 7 | Phase 4 (tool runtime) |
| 8 | Phase 5 (audit signing) |
| 9 | Phase 6 (OTel) |
| 10 | Phase 7 (WASM plugins) |
| 11 | Phase 8 (MCP + ACP) |
| 12 | Phase 9 (headless + parallel) |
| 13 | Phase 10 (release) |

**Rough estimate:** 8–12 weeks of focused work for a single strong Go developer to PR13, with Phase 3 (sandbox) and Phase 7 (WASM plugins) being the gnarliest.

---

## Offline / Airgap Honesty

Be honest in docs about what "works offline" means at the model capability level. A Claude Sonnet-class coding experience is not replicated by Qwen2.5-Coder-32B or Llama-3.3-70B on a laptop — they're genuinely useful but distinctly weaker at long agentic tool-use loops. The airgap wedge is real for users who legally can't send code to a cloud provider; it's a lie for users who just want to save money and expect frontier-model quality from a 7B model on their MacBook. Setting expectations in the README saves angry issues.
