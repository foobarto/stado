# Overview
stado is a local CLI/TUI coding agent that integrates with LLM providers (Anthropic/OpenAI/Google/OAI‑compatible), maintains a git‑sidecar session state, and executes tools (read/write/edit/grep/glob, bash, ripgrep/ast‑grep, webfetch, LSP/MCP/plugin tools). Sessions are stored in a sidecar bare repo with signed audit logs; mutations are materialized in a worktree and only applied to the user repo when `session land` is invoked. It supports a JSON‑RPC ACP server, headless `stado run`, and a WASM plugin runtime with signed manifests. Optional OS sandboxing (bwrap + landlock/seccomp on Linux, sandbox‑exec on macOS; Windows is currently unsandboxed) and network allow‑listing exist but are best‑effort and sometimes opt‑in.

# Threat model, Trust boundaries and assumptions
**Attacker‑controlled inputs**
- LLM responses: tool names/args, assistant text, reasoning blocks (prompt‑injection is realistic).
- Repository contents, including AGENTS.md/CLAUDE.md, source files, and untrusted artifacts read by tools.
- Web content fetched by `webfetch`.
- MCP servers (stdio or HTTP) and any tools they expose.
- Plugin wasm binaries/manifest signatures before verification; plugin outputs once executed.
- Network responses from LLM providers or other HTTP endpoints.
- External binaries on PATH (rg/ast‑grep) and their outputs.

**Operator‑controlled inputs**
- `config.toml`, environment variables (API keys, provider endpoints), CLI flags (e.g., `--sandbox-fs`), tool allow/deny lists, budgets, telemetry endpoints, plugin trust store, and MCP capability manifests.
- Decisions to enable/disable tools, plugins, or network access, and whether to “land” changes into the user repo.

**Developer‑controlled inputs**
- Built‑in tool implementations, provider integrations, audit/signing logic, and release build pipeline.

**Assumptions / constraints**
- stado runs as a single local user; there is no multi‑tenant or network‑exposed service surface.
- The OS user is the security boundary; tool execution inherits user privileges unless a sandbox is enabled.
- Tool approvals are currently auto‑allow in the codebase (TUI and headless), so safety relies on operator tool‑filtering and sandboxing.
- Sandboxing is platform‑dependent and optional (Linux `--sandbox-fs` landlock, bwrap for exec; macOS sandbox‑exec; Windows unsandboxed).

# Attack surface, mitigations and attacker stories
## Tool execution & filesystem access
**Surface:** `read/write/edit/glob/grep`, `bash`, `ripgrep`, `ast-grep`, `read_with_context`, LSP tools. Paths are joined with workdir but accept absolute/`..` paths; in-process tools do not enforce an allow‑list.

**Risks/attacker stories:**
- Prompt‑injected instructions cause `read` to access `~/.ssh`, cloud credentials, or other non‑repo secrets; or `bash` to exfiltrate data.
- Malicious repo content coerces the agent into modifying files outside the intended worktree or running destructive shell commands.

**Mitigations:**
- Work is done in a sidecar worktree; user repo stays pristine until `session land`.
- Output truncation budgets in `internal/tools/budget` limit bulk exfiltration.
- Operator tool filters (`[tools] enabled/disabled`) can remove `bash`/`webfetch`.
- Optional Linux landlock with `stado run --sandbox-fs` restricts writes to the worktree + /tmp (reads remain broad).
- Future approval workflow is planned but not active; treat tool calls as trusted only when operating in a trusted repo/model.

## OS sandboxing & network control
**Surface:** `internal/sandbox` runners (bwrap, sandbox‑exec), landlock/seccomp, HTTPS proxy allow‑list.

**Risks/attacker stories:**
- On Windows or hosts without bwrap/sandbox‑exec, subprocesses run unsandboxed.
- Misconfigured or missing capability manifests for MCP servers allow full host access.

**Mitigations:**
- Capability policy format for MCP servers; enforcement via sandbox runner when provided.
- Network allow‑listing via local CONNECT proxy (host allow‑list).

## Network access and web fetching
**Surface:** LLM provider HTTP clients, OAI‑compat endpoints, `webfetch`.

**Risks/attacker stories:**
- `webfetch` can reach internal services (SSRF‑like behavior) and return data to the model.
- Base URL overrides can redirect traffic to untrusted endpoints; API keys may be exposed to a malicious proxy.

**Mitigations:**
- `webfetch` can be disabled via tool allowlist or stripped in air‑gap builds.
- Providers use HTTPS by default; operator should treat baseURL as trusted configuration.

## Plugins (WASM) and MCP extensions
**Surface:** plugin manifest/signature, trust store, wasm runtime host imports; MCP stdio/HTTP servers.

**Risks/attacker stories:**
- Malicious plugin signed by an untrusted key; or trust‑store tampering enabling rogue plugins.
- MCP HTTP server returns tool definitions that execute sensitive actions or exfiltrate data.

**Mitigations:**
- Ed25519‑signed manifests; trust store with fingerprint pinning and rollback protection.
- Optional CRL/Rekor verification paths for plugins.
- Capability‑gated host imports for plugin FS/net/session/LLM access.

## ACP JSON‑RPC server
**Surface:** stdin/stdout RPC for editor integrations (`internal/acp`).

**Risks/attacker stories:**
- Local process with access to the ACP connection can send prompts that trigger tool execution.

**Mitigations:**
- Designed for local IPC; no network listener. Operators should ensure only trusted clients spawn/use ACP.

## Audit log, signed commits, and sidecar state
**Surface:** `internal/state/git` + `internal/audit` commit signing.

**Risks/attacker stories:**
- Attacker modifies the sidecar to hide traces or replays tool calls; stolen signing key could forge history.

**Mitigations:**
- Every tool call produces a signed commit in `trace` and (for mutations) `tree`.
- `stado audit verify` detects tampering; signatures cover commit metadata and hashes.
- Reproducible builds and dual signing (cosign/minisign) reduce release tampering.

## Telemetry and logging
**Surface:** OpenTelemetry exporters, slog logs, hook outputs.

**Risks/attacker stories:**
- Enabling OTel can send tool names, model usage, and performance metadata to external collectors.
- Hook commands run with full user privileges and receive turn payloads.

**Mitigations:**
- Telemetry is opt‑in (`STADO_OTEL_ENABLED` / config).
- Hooks are operator‑configured; execution is time‑bounded and output is isolated to stderr.

**Out‑of‑scope / low‑relevance classes:** CSRF, XSS, SQL injection, and multi‑tenant authz are largely inapplicable because stado is a local CLI without a web server. The primary threats are local execution, data exfiltration, and trust boundary violations between untrusted content and privileged tooling.

# Criticality calibration (critical, high, medium, low)
**Critical**
- Arbitrary code execution or file write outside the intended worktree without user intent (e.g., `bash` or plugin sandbox escape).
- Bypass of plugin signature/trust leading to execution of untrusted wasm/native code.
- Remote attacker (via prompt injection or MCP) achieving host‑level privilege escalation.

**High**
- Unauthorized read/exfiltration of sensitive local files (SSH keys, cloud creds) through tool path traversal or missing sandbox.
- Tampering with audit logs or signing keys that hides/misattributes tool actions.
- Unrestricted network egress from tools enabling data exfiltration to attacker‑controlled hosts.

**Medium**
- Denial‑of‑service via large outputs, runaway commands, or resource exhaustion.
- Leakage of sensitive metadata through telemetry/logs or permissive file permissions in XDG state.
- Misconfigured MCP capabilities that unintentionally widen access (but still requires operator setup).

**Low**
- Minor UI/UX issues that misrepresent tool output or auditing.
- Non‑security correctness bugs in prompt/context management that don’t increase privilege or access.

---
