<p align="center">
  <img src="assets/logo.png" alt="stado — sandboxed, git-native coding agent for the terminal" width="720">
</p>

<p align="center">
  <a href="https://github.com/foobarto/stado/actions/workflows/ci.yml"><img src="https://github.com/foobarto/stado/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/foobarto/stado/releases/latest"><img src="https://img.shields.io/github/v/release/foobarto/stado?include_prereleases&amp;sort=semver" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/foobarto/stado"><img src="https://goreportcard.com/badge/github.com/foobarto/stado" alt="Go Report Card"></a>
  <a href="https://pkg.go.dev/github.com/foobarto/stado"><img src="https://pkg.go.dev/badge/github.com/foobarto/stado.svg" alt="Go Reference"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/foobarto/stado" alt="Go version">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/foobarto/stado" alt="License"></a>
  <a href="https://securityscorecards.dev/viewer/?uri=github.com/foobarto/stado"><img src="https://api.securityscorecards.dev/projects/github.com/foobarto/stado/badge" alt="OpenSSF Scorecard"></a>
</p>

# stado

A sandboxed, git-native coding agent for the terminal.

Every tool call is committed to a signed audit log. Agent state lives in
a sidecar git repo — your working tree stays pristine until you
explicitly land changes. Tool execution is capability-gated through the
OS sandbox. Releases are reproducible and dual-signed (cosign + minisign)
so you can verify what you're running, including from an airgapped
environment.

> **Status:** pre-1.0. The core agent loop, git-native state, signed
> audit log, sandbox (Linux + macOS), OpenTelemetry instrumentation,
> MCP/ACP integration, signed WASM plugins with CRL + Rekor checks,
> bundled built-in tools loaded through the plugin runtime, and Phase 11
> context management (prompt-cache plumbing, token counting, in-turn
> read dedup, per-tool output budgets, fork-from-point ergonomics, and
> user-invoked compaction) are shipped. Windows sandbox v2 (job objects
> + restricted tokens) is still in flight — see [PLAN.md](PLAN.md) for
> the phased roadmap.

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
confinement plus bubblewrap + seccomp BPF for bash/exec with a
HTTPS-CONNECT-allowlist proxy for network policy. macOS generates a
`sandbox-exec` `.sb` profile from the Policy. Windows runs unsandboxed
in v1 (with a one-time warning); job objects + restricted tokens come
in v2.

**Provider-agnostic by design.** Four direct implementations: Anthropic,
OpenAI, Google, and a hand-rolled OpenAI-compatible client covering
llama.cpp, vLLM, Ollama, Groq, Cerebras, OpenRouter, DeepSeek, xAI, and
Mistral. No third-party LLM abstraction library. Thinking blocks and
reasoning content round-trip verbatim — no lossy normalization.

**Reproducible, signed, airgap-friendly.** Releases are bit-for-bit
reproducible (`-trimpath -buildvcs=true -buildid=`). Each release ships
a `checksums.txt` manifest signed two ways: cosign keyless (via GitHub
Actions OIDC, Rekor-backed) and minisign (Ed25519, long-lived key,
offline-friendly). Archives and packages are verified against that
signed manifest. `stado verify --show-builtin-keys` displays the
minisign trust roots compiled into the running binary.

---

## Install

### Install script (Linux, macOS) — *pending*

A signature-verifying install script (`install.sh`) is planned; in the
meantime use the manual download path or build from source.

### Homebrew

```sh
brew install foobarto/tap/stado
```

### Self-update (existing installs)

```sh
stado self-update --dry-run
stado self-update
```

`self-update` picks the archive matching the current OS/arch, verifies
the downloaded asset against `checksums.txt`, and on release builds with
an embedded minisign root also enforces `checksums.txt.minisig` before
atomically swapping the binary into place.

### Manual download / release assets

Grab the matching archive or package from
[Releases](https://github.com/foobarto/stado/releases) and verify it.
Each tag publishes platform archives
(`stado_<version>_<os>_<arch>.tar.gz` / `.zip`), Linux packages
(`.deb` / `.rpm`), `checksums.txt`, `checksums.txt.sig`,
`checksums.txt.cert`, `checksums.txt.minisig`, and SBOMs.

Manual verification follows the same signed-checksum-manifest flow as
`self-update`: first verify `checksums.txt`, then verify the specific
archive/package against that manifest.

```sh
# keyless cosign verification of the checksum manifest
cosign verify-blob \
  --certificate checksums.txt.cert \
  --certificate-identity-regexp 'https://github.com/foobarto/stado/.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature checksums.txt.sig \
  checksums.txt

# replace <asset> with the archive/package you downloaded
grep " <asset>$" checksums.txt | sha256sum -c -         # Linux
grep " <asset>$" checksums.txt | shasum -a 256 -c -    # macOS

# inspect the minisign root embedded in the stado binary you already trust
stado verify --show-builtin-keys
```

For a fully manual airgapped minisign flow, see
[SECURITY.md](SECURITY.md). For Linux package installs, download the
matching `.deb` or `.rpm` from the release page and install it with
your native package manager.

### From source

```sh
go install github.com/foobarto/stado/cmd/stado@latest
```

Go 1.25+. Pure Go, `CGO_ENABLED=0` works. No native deps — official
release binaries bundle `rg` and `ast-grep` via `go:embed` (extracted
on first use to `$XDG_CACHE_HOME/stado/bin/`, sha256-verified). Source
builds (`go install`) skip the embed and fall back to the system PATH;
`gopls` is optional and always resolved via PATH. Dev/source builds also
do not pin release minisign roots unless you pass the release ldflags,
so `stado verify --show-builtin-keys` will usually report `(not pinned)`.

---

## Quick start

```sh
# Point stado at an LLM provider. Any of:
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
export GOOGLE_API_KEY=...
# Or a local model:
export STADO_DEFAULTS_PROVIDER=ollama     # http://localhost:11434/v1
export STADO_DEFAULTS_PROVIDER=lmstudio   # http://localhost:1234/v1
export STADO_DEFAULTS_PROVIDER=llamacpp   # http://localhost:8080/v1

# Scaffold config (optional — stado works with env vars alone)
stado config init

# Optional preflight: provider keys, sandbox, bundled binaries
stado doctor

# Enter a repo and start a session
cd ~/code/myproject
stado
```

The TUI opens with an input box. Type a request; stado streams the
response, queues tool calls for your approval, and commits every call
to the session's audit log.

### Useful first commands

Core session workflow:

```sh
stado session ls                        # sessions in this repo (ls alias for list)
stado session show <id>                 # refs + worktree + latest commit + usage totals
stado session describe <id> "label"     # attach a human label; surfaces in list + TUI sidebar
stado session resume react              # resume by id, id-prefix, or description substring
stado session logs <id>                 # tool-call audit as a scannable one-line feed
stado session export <id> -o out.md     # conversation as markdown (or --format jsonl)
stado session search "react hook"       # grep across every session's conversation
stado session gc --older-than=24h       # sweep zero-turn sessions (dry-run by default)
stado session fork <id> --at turns/5    # fork from an earlier turn
stado session tree <id>                 # interactive fork-from-turn picker
stado session land <id> <branch>        # push agent's tree to your repo
stado audit verify <id>                 # tamper-check the audit log
```

Run + stats + config:

```sh
stado run --prompt "..."                # one-shot, provider-only
stado run --tools --prompt "..."        # one-shot with the audited tool loop
stado run --session <id> "follow-up"    # continue an existing session from the CLI
stado stats                             # cost + token dashboard (past 7 days)
stado stats --json | jq                 # same, for scripting
stado config show                       # resolved effective config (file + env + defaults)
stado doctor                            # env diagnostic (runners, sandbox, binaries)
```

Plugins:

```sh
stado plugin init my-plugin             # scaffold a Go wasip1 plugin
stado plugin gen-key my-plugin.seed     # one-time signer key
stado plugin sign plugin.manifest.json --key my-plugin.seed --wasm plugin.wasm
stado plugin trust <pubkey-hex> "Alice Example"
stado plugin verify .                   # signature + digest + rollback/CRL/Rekor
stado plugin install .                  # copy into state/plugins/
stado plugin list                       # pinned signer keys
stado plugin installed                  # installed plugin IDs
stado plugin run <id> <tool> '{...}'    # invoke a plugin tool directly
```

`plugin list` shows trusted authors; `plugin installed` shows runnable
plugin IDs (`<name>-<version>`).

Aliases: `ls` → `list`, `rm` → `delete`, `cat` → `export`.

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

See [docs/README.md](docs/README.md) for the current guide index.
Editor-specific ACP setup docs are still sparse, but the command
surface itself is shipped and stable enough to wire into Zed today.

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
`find_references`, `document_symbols`, `hover`). They ship as embedded
signed WASM modules loaded through the same plugin runtime used for
third-party tools, so overrides and capability checks behave the same
way across built-in and external plugins. MCP servers plug in via
config and auto-register their tools.

**Git-native state.** Sidecar bare repo per user repo. Alternates link
to your `.git/objects` so agent sessions reference your history without
copying objects. Dual-ref model (`tree` + `trace`), turn-boundary tags,
and session subcommands for lifecycle, recovery, and introspection:
`new`, `list`, `show`, `describe`, `resume`, `attach`, `delete`,
`fork` (with `--at <turns/N|sha>` to fork from a specific turn),
`revert`, `tree` (interactive turn-history browser — navigate and fork
from a chosen turn), `land`, `export`, `search`, `logs`, `gc`, and
`compact` (advisory; the real flow is `/compact` inside the TUI).

**Sandbox.** Linux ships Landlock for FS confinement, bubblewrap +
seccomp BPF for bash/exec, and a CONNECT-allowlist proxy for egress.
macOS generates a `sandbox-exec` profile from the same `Policy`.
Windows still runs unsandboxed in v1 with a one-time warning while job
objects + restricted tokens land in v2. `stado run --sandbox-fs`
narrows the whole process to worktree-only writes.

**Audit.** Ed25519 commit signatures over a canonical
`stado-audit-v1` framing. `stado audit verify` walks refs and reports
the first invalid commit. `stado audit export` emits JSONL suitable
for SIEM ingestion.

**Surfaces.** Terminal TUI (default), `stado run` (single-shot CLI),
`stado headless` (JSON-RPC 2.0 daemon), `stado acp` (Zed Agent Client
Protocol server), and `stado mcp-server` (stado as an MCP v1 tool
server). All compose the same `internal/runtime` core.

**WASM plugins.** `stado plugin init`, `gen-key`, `sign`, `trust`,
`verify`, `install`, `installed`, `list`, `run`, and `digest` cover the
full author/install loop. `[tools].overrides` can replace bundled tools
with installed plugins, and `[plugins].background` loads turn-boundary
plugins such as auto-compactors or recorders at TUI startup.

**MCP sandbox.** Each `[mcp.servers.<name>]` can declare a
`capabilities` list (fs:read/fs:write/net:<host>/exec:<binary>/env:VAR).
Stado maps these to a sandbox policy and launches the stdio server via
bubblewrap so it can't silently touch anything not in the manifest.
Unsandboxed servers emit a stderr advisory at attach time.

**Plugin CRL.** When `[plugins].crl_url` + `crl_issuer_pubkey` are
configured, `stado plugin verify` fetches an Ed25519-signed revocation
list, caches it on disk (airgap-friendly), and refuses installation of
any (author_fpr, version, wasm_sha256) triple listed — independent
check on top of the trust-store signature + rollback gates.

**Self-update integrity.** `stado self-update` verifies the sha256 from
checksums.txt, and — once a release pubkey is embedded via build
ldflags — also validates `checksums.txt.minisig` before trusting the
checksums. Four (pubkey × signature) states all handled with cleanly
degraded advisories.

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
`checksums.txt` is signed with cosign keyless (Rekor-logged) and
minisign Ed25519; archives/packages are verified against that manifest.
SLSA 3 provenance via `slsa-github-generator`. SBOM via syft.

---

## What's in flight

See [PLAN.md](PLAN.md) for the full roadmap. Headlines:

- **Compaction — CLI-driven + dual-ref persistence** (Phase 11.3
  remainder). The TUI `/compact` flow is shipped; a fully
  CLI-driven `stado session compact <id>` and the dual-ref commit
  that preserves compaction on disk need a conversation-persistence
  layer that doesn't yet exist.
- **Sandbox — Windows v2** (Phase 3.6). Linux (bubblewrap + landlock +
  seccomp + CONNECT-proxy) and macOS (`sandbox-exec`) are shipped;
  Windows runs unsandboxed with a warning until job objects + restricted
  tokens land in v2.
- **Release distribution** (Phase 10.3b / 10.7 tail). The Homebrew tap
  is already live and release archives/packages are built today; the
  remaining work is signed apt/rpm repository hosting plus the release
  ceremony that seeds embedded minisign roots into tagged builds.

---

## Offline / airgap

stado runs fully offline with a local inference backend. Known-good
combinations:

- **llama.cpp** (`llama-server`) — the reference test target. Single
  binary, cleanest airgap story. `STADO_DEFAULTS_PROVIDER=llamacpp`.
- **Ollama** — works via its OpenAI-compat endpoint. Set
  `STADO_DEFAULTS_PROVIDER=ollama`. Note: Ollama's default context
  length is conservative; set `num_ctx` on the model or
  `OLLAMA_CONTEXT_LENGTH` env var.
- **LM Studio** — point-and-click local runner with a GUI. Load a
  model, enable the local server (default port 1234), set
  `STADO_DEFAULTS_PROVIDER=lmstudio`. Override the port via
  `[inference.presets.lmstudio].endpoint` in config if you changed it.
- **vLLM** — for team-scale self-hosted inference. Point at the
  `vllm serve` endpoint. `STADO_DEFAULTS_PROVIDER=vllm`.

Build with `-tags airgap` to strip every outbound-HTTP path that stado
controls: `self-update` refuses to run (pointing operators at the
manual `download → verify → copy` flow), `plugin install` stops
refreshing the CRL and relies on the on-disk cache written by the last
online refresh, and the `webfetch` tool errors on every invocation.
Provider HTTP clients (llama.cpp, Ollama, LM Studio, vLLM, remote APIs)
stay untouched — those are the user's explicit inference target, not
stado's own phone-home. Release verification stays offline-friendly:
`checksums.txt.minisig` can still be validated with the standalone
minisign flow in [SECURITY.md](SECURITY.md), and `stado verify
--show-builtin-keys` still prints the embedded trust roots of the
running binary.

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
model    = "claude-sonnet-4-6"

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
hard_threshold = 0.90   # blocks new turns above this — /compact or /clear to continue
```

Every key is overridable via env var: `STADO_DEFAULTS_PROVIDER=ollama`,
`STADO_OTEL_ENABLED=1`, `STADO_CONTEXT_SOFT_THRESHOLD=0.6`, etc.
Underscores map to nested dots.

Guide coverage is incremental. See [docs/README.md](docs/README.md) for
the current command/feature index; `stado config init`'s scaffolded file
and `stado config show` remain the quickest way to inspect keys that do
not yet have a dedicated guide.

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

## Configuring tools & sandboxing

Stado ships 14 bundled tools by default (read/write/edit, glob/grep/
ripgrep/ast_grep, bash, webfetch, read_with_context, four LSP-backed
symbol tools). All are exposed to the model in every surface: the TUI,
`stado run --tools`, headless JSON-RPC, the ACP server, and
`stado mcp-server`. `stado config show` prints the current effective
surface; `stado doctor` reports which opt-in knobs are set.

### Trim the tool set

Two `[tools]` knobs in `config.toml`:

```toml
[tools]
# Allowlist — only these 3 tools are visible to the model. Every
# other bundled tool is silently omitted from the registry.
enabled  = ["read", "grep", "bash"]

# OR: start from the full default set and strip specific tools.
# enabled takes precedence when both are set.
disabled = ["webfetch", "bash"]
```

Unknown names in either list log a stderr warning and are ignored —
configs survive tool renames across stado versions.

### Approvals

`[approvals]` controls when the TUI prompts before a tool call runs:

```toml
[approvals]
mode      = "prompt"                    # "prompt" | "allowlist"
allowlist = ["read", "glob", "grep",    # auto-approved in allowlist mode
             "ripgrep", "ast_grep"]
```

In the TUI, `/approvals always <tool>` adds a session-scoped
auto-approve for the current run (cleared on restart). `/approvals
forget` removes every session-scoped entry.

### Sandboxing

Linux uses two enforcement layers, both kernel-native:

- **Landlock** (kernel ≥ 5.13) — filesystem confinement. `stado run
  --sandbox-fs` narrows the whole process to writes under the active
  worktree + `/tmp`. Reads stay permitted so globs and greps across
  the rest of the repo still work.
- **Bubblewrap + seccomp BPF** — shell exec. Every `bash` tool call
  and every MCP stdio server is launched inside a bwrap namespace
  with a seccomp profile that strips the usual escape routes
  (`ptrace`, `mount`, `bpf`, `modify_ldt`, etc.).

**Network egress** goes through an in-process CONNECT-allowlist
proxy. The `bash` tool can only reach hosts the capability manifest
permits (`net:<host>` entries in the MCP server's capabilities list,
or `net:allow` for unrestricted — noisy stderr warning when that
broad). `stado doctor` reports Landlock availability and the
sandbox runner in use.

### MCP server isolation

Each `[mcp.servers.<name>]` block attaches an external MCP tool
server. Declare a `capabilities` list to gate what that server can
touch:

```toml
[mcp.servers.github]
command      = "mcp-github"
args         = ["--readonly"]
env          = { GITHUB_TOKEN = "@env:GITHUB_TOKEN" }
capabilities = [
  "net:api.github.com",
  "net:raw.githubusercontent.com",
  "env:GITHUB_TOKEN",
]
```

Capability grammar: `fs:read:<path>` · `fs:write:<path>` · `net:<host>`
· `net:allow` · `net:deny` · `exec:<binary>` · `env:<VAR>`. Empty
capabilities mean unsandboxed (a legacy default); stado logs a loud
advisory on stderr.

HTTP MCP servers (`url = "https://…"`) don't participate in the
bubblewrap sandbox — their network activity is the client's
concern. stdio servers (`command = …`) do.

### WASM plugins

Third-party tools ship as signed wasm binaries, verified against an
Ed25519 trust store (`stado plugin trust <pubkey>`). Capabilities are
declared in the manifest, enforced by the `wazero` runtime — no
kernel-level sandbox needed because wasm already is one. See
[docs/commands/plugin.md](docs/commands/plugin.md) for the operator
workflow and [SECURITY.md](SECURITY.md) for the publish/signing model.

---

## Docs

- [docs/README.md](docs/README.md) — guide index; shows which commands
  and features have standalone docs vs where `stado --help` is still
  authoritative
- [docs/commands/session.md](docs/commands/session.md) — session
  lifecycle, fork/land flow, and export/search/logging
- [docs/commands/plugin.md](docs/commands/plugin.md) — scaffold → sign
  → trust → verify → install → run for WASM plugins
- [docs/features/instructions.md](docs/features/instructions.md) —
  `AGENTS.md` / `CLAUDE.md` resolution and loading rules
- [docs/eps/README.md](docs/eps/README.md) — enhancement proposals and
  retroactive design records for the major shipped decisions
- [DESIGN.md](DESIGN.md) — as-built architecture
- [PLAN.md](PLAN.md) — phased roadmap and remaining work
- [CONTRIBUTING.md](CONTRIBUTING.md) — build, test, contribute
- [SECURITY.md](SECURITY.md) — supply-chain model, key rotation, plugin
  publishing, and vulnerability reporting

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
and Google. The WASM plugin runtime uses
[wazero](https://github.com/tetratelabs/wazero). The Agent Client
Protocol is developed by [Zed](https://github.com/zed-industries/agent-client-protocol).
The Model Context Protocol is developed by [Anthropic](https://modelcontextprotocol.io/).

<p align="center">
  <img src="assets/stado_footer.png" alt="stado" width="100%">
</p>
