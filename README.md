# stado

A sandboxed, git-native coding agent for the terminal.

Every tool call is committed to a signed audit log. Agent state lives in
a sidecar git repo — your working tree stays pristine until you
explicitly land changes. Tool execution is capability-gated through the
OS sandbox. Releases are reproducible and dual-signed (cosign + minisign)
so you can verify what you're running, including from an airgapped
environment.

> **Status:** pre-1.0. The core agent loop, git-native state, signed
> audit log, sandbox (Linux), OpenTelemetry instrumentation, MCP/ACP
> integration, and Phase 11 context management (prompt-cache
> plumbing, token counting, in-turn read dedup, per-tool output
> budgets, fork-from-point ergonomics, and user-invoked compaction)
> are shipped. macOS/Windows sandbox and WASM plugins are still in
> flight — see [PLAN.md](PLAN.md) for the phased roadmap.

---

## Why stado

Most coding agents treat your repo as a scratch pad and your security
team as an obstacle. stado inverts both:

**Your repo stays read-only.** Every agent action lands in a sidecar
git repo alternates-linked to your `.git/objects` — zero object
duplication, zero pollution of your branches, zero risk of a runaway
agent force-pushing over your work. Changes surface in your repo only
when you run `stado session land`.

**Every tool call is auditable.** Two refs per session: `tree` is the
executable history (mutations only, diff-then-commit for shell), `trace`
is the full audit log (every read, every grep, every LSP call — empty
commits with structured trailers). Both are Ed25519-signed; `stado
audit verify` walks the chain and reports tampering.

**Tool execution is sandboxed.** Capability manifests declare what a
tool can touch; the OS enforces. Linux uses Landlock for filesystem
confinement plus bubblewrap for bash/exec with a HTTPS-CONNECT-allowlist
proxy for network policy. macOS (`sandbox-exec`) and Windows (job
objects) are planned.

**Provider-agnostic by design.** Four direct implementations: Anthropic,
OpenAI, Google, and a hand-rolled OpenAI-compatible client covering
llama.cpp, vLLM, Ollama, Groq, Cerebras, OpenRouter, DeepSeek, xAI, and
Mistral. No third-party LLM abstraction library. Thinking blocks and
reasoning content round-trip verbatim — no lossy normalization.

**Reproducible, signed, airgap-friendly.** Releases are bit-for-bit
reproducible (`-trimpath -buildvcs=true -buildid=`). Every artifact is
signed by both cosign (keyless, via GitHub Actions OIDC, with a Rekor
transparency log entry) and minisign (Ed25519, long-lived key, offline
verification). `stado verify --show-builtin-keys` displays the trust
roots compiled into the binary.

---

## Install

### Install script (Linux, macOS) — *pending*

A signature-verifying install script (`install.sh`) is planned; in the
meantime use the manual download path or build from source.

### Manual download

Grab a binary from [Releases](https://github.com/foobarto/stado/releases)
and verify:

```sh
# cosign (online)
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/foobarto/stado/.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature stado-linux-amd64.sig \
  stado-linux-amd64

# minisign (airgap-safe; public key is compiled into every stado binary)
stado verify stado-linux-amd64
```

### Homebrew, apt, rpm

Pending. Track [the distribution issue](https://github.com/foobarto/stado/issues)
for progress.

### From source

```sh
go install github.com/foobarto/stado/cmd/stado@latest
```

Go 1.25+. Pure Go, `CGO_ENABLED=0` works. No native deps except
optional runtime tools (`rg`, `ast-grep`, `gopls`).

---

## Quick start

```sh
# Point stado at an LLM provider. Any of:
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
export GOOGLE_API_KEY=...
# Or a local model:
export STADO_DEFAULTS_PROVIDER=ollama   # uses http://localhost:11434/v1

# Scaffold config (optional — stado works with env vars alone)
stado config init

# Enter a repo and start a session
cd ~/code/myproject
stado
```

The TUI opens with an input box. Type a request; stado streams the
response, queues tool calls for your approval, and commits every call
to the session's audit log.

### Useful first commands

```sh
stado session list                      # sessions in this repo
stado session show <id>                 # refs + worktree + latest commit
stado session fork <id>                 # new session branched from parent's tree head
stado session revert <id> <commit>      # new child session rooted at an earlier commit
stado session land <id> <branch>        # push agent's tree to your repo
stado audit verify <id>                 # tamper-check the audit log
stado agents list                       # running agents across all sessions
```

Fork-from-an-earlier-turn (`session fork <id> --at turns/5` / interactive
`session tree`) is planned — see Phase 11.5.

### Headless (scripted) use

```sh
# One-shot, exits after the agent finishes
stado run --prompt "add a CHANGELOG entry for v0.4.0" --json

# Long-running daemon; drive from any JSON-RPC 2.0 client
stado headless
```

### Editor integration (Zed, Neovim)

stado speaks Zed's Agent Client Protocol. Configure stado as your agent
backend and drive from the editor:

```sh
stado acp --tools
```

Editor-specific setup docs land under `docs/` as the distribution
channels stabilise.

---

## What's shipped

**Providers.** Anthropic (with prompt caching + extended thinking + signature
round-trip), OpenAI (with `reasoning_content`), Google (Gemini), and a
hand-rolled OpenAI-compat client with 11 bundled presets (ollama,
llamacpp, vllm, lmstudio, litellm, groq, openrouter, deepseek, xai,
mistral, cerebras) plus `--endpoint` for anything else.

**Bundled tools (14).** `read` (supports optional 1-indexed `start`/`end`
line range + in-turn content-hash dedup so repeat reads of an unchanged
file spend no tokens), `write`, `edit`, `glob`, `grep`, `ripgrep`,
`ast_grep`, `bash`, `webfetch`, `read_with_context` (Go-aware import
resolution), and four LSP-backed tools (`find_definition`,
`find_references`, `document_symbols`, `hover`). MCP servers plug in
via config and auto-register their tools.

**Git-native state.** Sidecar bare repo per user repo. Alternates link
to your `.git/objects` so agent sessions reference your history without
copying objects. Dual-ref model (`tree` + `trace`), turn-boundary tags,
ten `session` subcommands: `new`, `list`, `show`, `attach`, `delete`,
`fork` (with `--at <turns/N|sha>` to fork from a specific turn), `land`,
`revert`, `tree` (interactive turn-history browser — navigate and fork
from a chosen turn), `compact` (advisory; the real flow is `/compact`
inside the TUI).

**Sandbox (Linux).** Landlock for FS confinement, bubblewrap for
bash/exec, CONNECT-allowlist proxy for egress, capability-declaration
in `Policy`. `stado run --sandbox-fs` narrows the whole process to
worktree-only writes. macOS and Windows pending.

**Audit.** Ed25519 commit signatures over a canonical
`stado-audit-v1` framing. `stado audit verify` walks refs and reports
the first invalid commit. `stado audit export` emits JSONL suitable
for SIEM ingestion.

**Surfaces.** Terminal TUI (default), `stado run` (single-shot CLI),
`stado headless` (JSON-RPC 2.0 daemon), `stado acp` (Zed Agent Client
Protocol server). All compose the same `internal/runtime` core.

**Context management.** Prompt-cache breakpoints placed automatically
on providers that support them (Anthropic); deterministic tool
serialisation + append-only guardrails keep the cached prefix
byte-stable across turns. Token counting via provider-native
tokenizers (Anthropic `count_tokens`, tiktoken offline for OpenAI and
OAI-compat, Gemini `count_tokens`); soft/hard thresholds are configurable
under `[context]` and the TUI's ctx% indicator colour-codes when crossed.
In-turn read deduplication returns a terse reference when the same
file+range resolves to the same content hash. Per-tool output budgets
(read / webfetch 16K, bash 32K, grep / ripgrep 100 matches, glob 200
entries) with visible truncation markers. User-invoked compaction via
`/compact` in the TUI — summarises the conversation, shows a
preview-and-confirm flow, never touches msgs without explicit y.

**Observability.** OpenTelemetry spans around every tool call
(`stado.tool_call`), every turn (`stado.turn`), and every provider
stream (`stado.provider.stream`). Off by default; enable via `[otel]`
or `STADO_OTEL_ENABLED=1`. Metrics instruments already defined:
`stado_tool_latency_ms`, `stado_tokens_total`, `stado_cache_hit_ratio`,
`stado_approval_rate`, `stado_sandbox_denials_total`.

**Parallel agents.** `stado session fork` creates a new worktree with
its own agent loop — three agents on the same repo don't clobber each
other. `stado agents list` / `attach` / `kill` multiplex them.

**Reproducible, signed releases.** Bit-for-bit reproducible builds.
Cosign keyless (Rekor-logged) + minisign Ed25519 on every artifact.
SLSA 3 provenance via `slsa-github-generator`. SBOM via syft.

---

## What's in flight

See [PLAN.md](PLAN.md) for the full roadmap. Headlines:

- **Compaction — CLI-driven + dual-ref persistence** (Phase 11.3
  remainder). The TUI `/compact` flow is shipped; a fully
  CLI-driven `stado session compact <id>` and the dual-ref commit
  that preserves compaction on disk need a conversation-persistence
  layer that doesn't yet exist.
- **Sandbox — macOS and Windows** (Phase 3). `sandbox-exec` profile
  generation, Windows job objects with restricted tokens.
- **WASM plugins** (Phase 7). Manifest + trust store + CLI shipped;
  the wazero runtime host is the remaining piece.
- **Distribution** (Phase 10). Homebrew tap, signed apt/rpm repos,
  signature verification on `stado self-update`.

---

## Offline / airgap

stado runs fully offline with a local inference backend. Known-good
combinations:

- **llama.cpp** (`llama-server`) — the reference test target. Single
  binary, cleanest airgap story.
- **Ollama** — works via its OpenAI-compat endpoint. Set
  `STADO_DEFAULTS_PROVIDER=ollama`. Note: Ollama's default context
  length is conservative; set `num_ctx` on the model or
  `OLLAMA_CONTEXT_LENGTH` env var.
- **vLLM** — for team-scale self-hosted inference. Point at the
  `vllm serve` endpoint.

Build with `-tags airgap` to strip cosign and produce a smaller binary
for environments that can't verify against Rekor. `stado verify` falls
back to minisign-only in this configuration.

A word of honesty: a Claude Sonnet-class coding experience is not
replicated by Qwen2.5-Coder-32B or Llama-3.3-70B on a laptop. Local
models are genuinely useful for iteration and privacy-critical work;
they're not a drop-in replacement for frontier hosted models on long
agentic loops. See [PLAN §"Offline / Airgap
Honesty"](PLAN.md#offline--airgap-honesty).

---

## Configuration

stado reads `$XDG_CONFIG_HOME/stado/config.toml` (scaffolded by
`stado config init`):

```toml
[defaults]
provider = "anthropic"
model    = "claude-sonnet-4-5"

[approvals]
mode      = "prompt"                     # "prompt" | "allowlist"
allowlist = ["read", "glob", "grep", "ripgrep", "ast_grep"]

[inference.presets.my-proxy]
endpoint = "https://proxy.example/v1"

[mcp.servers.github]
command = "mcp-github"
args    = ["--readonly"]
env     = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }

[otel]
enabled  = false
endpoint = "localhost:4317"
protocol = "grpc"

[context]
soft_threshold = 0.70   # TUI shows a warning indicator above this
hard_threshold = 0.90   # reserved for future blocking UX (pairs with compaction)
```

Every key is overridable via env var: `STADO_DEFAULTS_PROVIDER=ollama`,
`STADO_OTEL_ENABLED=1`, `STADO_CONTEXT_SOFT_THRESHOLD=0.6`, etc.
Underscores map to nested dots.

A full reference document under `docs/` is planned; until then
[DESIGN.md](DESIGN.md) and `stado config init`'s scaffolded file are
the authoritative source.

---

## Paths

| Purpose | Path |
|---|---|
| Config | `$XDG_CONFIG_HOME/stado/config.toml` |
| Sidecar bare repo | `$XDG_DATA_HOME/stado/sessions/<repo-id>.git` |
| Agent signing key | `$XDG_DATA_HOME/stado/keys/agent.ed25519` |
| Session worktrees | `$XDG_STATE_HOME/stado/worktrees/<session-id>/` |
| Plugin trust store | `$XDG_DATA_HOME/stado/plugins/trusted_keys.json` |

Your repo's `.git` is never written to unless you run `stado session
land`. The sidecar repo is safe to delete — it rebuilds on next run.

---

## Docs

- [DESIGN.md](DESIGN.md) — as-built architecture
- [PLAN.md](PLAN.md) — phased roadmap and remaining work
- `CONTRIBUTING.md` — build, test, contribute *(pending)*
- `SECURITY.md` — security policy, key rotation, vulnerability
  reporting *(pending)*
- `docs/` — per-topic guides (ACP integration, MCP servers, sandbox
  policies, telemetry) *(pending)*

---

## Design principles

Four commitments that shape every architectural decision:

1. **The user's repo is read-only until they say otherwise.** Agent
   state lives outside. Landing is always explicit.
2. **Every action is auditable and tamper-evident.** No unsigned
   commits, no un-logged tool calls, no "trust us" on the agent's
   behavior.
3. **Capabilities are declared, the OS enforces.** Not "the agent
   promises not to touch /etc/shadow" — the kernel prevents it.
4. **No lossy abstraction over provider capabilities.** Thinking
   blocks, reasoning content, prompt caching breakpoints round-trip
   verbatim. The agent loop branches on capabilities rather than
   papering over differences.

---

## License

Apache-2.0. See [LICENSE](LICENSE) for the full text.

---

## Acknowledgements

stado builds on [go-git](https://github.com/go-git/go-git),
[bubbletea](https://github.com/charmbracelet/bubbletea),
[koanf](https://github.com/knadh/koanf),
[tiktoken-go](https://github.com/pkoukk/tiktoken-go) (with the offline
BPE loader), and the official provider SDKs from Anthropic, OpenAI,
and Google. The planned WASM plugin runtime will use
[wazero](https://github.com/tetratelabs/wazero). The Agent Client
Protocol is developed by [Zed](https://github.com/zed-industries/agent-client-protocol).
The Model Context Protocol is developed by [Anthropic](https://modelcontextprotocol.io/).
