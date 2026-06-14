# stado — Threat model

> Last reviewed: 2026-05-22 (re-walked against the all-wasm tool
> architecture). **Every tool is now a signed wasm plugin** dispatched
> through capability-gated host imports — there are no in-process native
> tools (the pre-EP-0037/0038 `internal/tools/*` surface is gone). See
> `docs/eps/0037-tool-dispatch-and-operator-surface.md`,
> `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`,
> `docs/eps/0005-capability-based-sandboxing.md`, and
> `docs/eps/0030-security-research-default-harness.md`. The central shift
> since the 2026-04-25 review: filesystem and network access are gated by
> per-plugin **capabilities** enforced at the host-import boundary, not
> left to in-process trust. Update when re-walking.

## Overview
stado is a local CLI/TUI coding agent that integrates with LLM providers (Anthropic/OpenAI/Google/OAI‑compatible), maintains a git‑sidecar session state, and executes tools — all of which are **signed wasm plugins** (fs read/write/edit/glob/grep, shell exec + PTY sessions, ripgrep/ast‑grep, webfetch, LSP, agent spawn, plus user plugins) reached through capability-gated host imports. Sessions are stored in a sidecar bare repo with signed audit logs; mutations are materialized in a worktree and only applied to the user repo when `session land` is invoked. It supports a JSON‑RPC ACP server, headless `stado run`, and a WASM plugin runtime with signed manifests. Two **distinct** controls — don't conflate them: (1) the broker (an evolution of `stado daemon`, EP-0050) projects a per-session **capability ceiling** (FS/net/exec policy) that the runner enforces on **every orchestrator surface** (TUI, `stado run`, `stado headless`, `stado acp`, `stado mcp-server`) as of v0.57.0; (2) **OS process-containment** (bwrap namespace re-exec + landlock/seccomp on Linux, sandbox‑exec on macOS; Windows unsandboxed) is applied by `stado run` (the `[sandbox]` re-wrap) and by the `mcp-server`/`daemon`/`acp`/`headless` default exec policy for `stado_exec`/`stado_proc_spawn` — but bash on the bare TUI/`run` surface deliberately runs against the operator's own filesystem (it is the operator's own session). Opt out per-run via `stado run --no-sandbox`, or for the whole process via `STADO_BROKER_ATTACH=0`. Network allow‑listing via a local CONNECT proxy is best‑effort.

## Threat model, Trust boundaries and assumptions
**Attacker‑controlled inputs**
- LLM responses: tool names/args, assistant text, reasoning blocks (prompt‑injection is realistic).
- Repository contents, including AGENTS.md/CLAUDE.md, source files, and untrusted artifacts read by tools.
- Web content fetched by `webfetch`.
- MCP servers (stdio or HTTP) and any tools they expose.
- Plugin wasm binaries/manifest signatures before verification; plugin outputs once executed.
- Network responses from LLM providers or other HTTP endpoints.
- External binaries on PATH (rg/ast‑grep) and their outputs.

**Operator‑controlled inputs**
- `config.toml`, environment variables (API keys, provider endpoints, `STADO_BROKER_ATTACH`), CLI flags (e.g., `--no-sandbox`), tool allow/deny lists, budgets, telemetry endpoints, plugin trust store, and MCP capability manifests.
- Decisions to enable/disable tools, plugins, or network access, and whether to “land” changes into the user repo.

**Developer‑controlled inputs**
- Built‑in tool implementations, provider integrations, audit/signing logic, and release build pipeline.

**Assumptions / constraints**
- stado runs as a single local user; there is no multi‑tenant or network‑exposed service surface.
- The OS user is the security boundary; tool execution inherits user privileges unless a sandbox is enabled.
- **Containment is capability-based, not approval-based.** There is no automatic per-tool-call approval prompt (the old native-tool approval loop was removed in EP-0017 — a prompt was a poor containment boundary). What a tool can touch is bounded by (a) which plugins are registered/enabled, (b) the FS/net/exec capabilities each plugin's manifest declares, enforced at the host-import boundary, and (c) the sandbox policy. Human approval still exists but as an **opt-in capability** (`stado_ui_approve`) a plugin invokes deliberately, not a blanket gate.
- Sandboxing is platform‑dependent (Linux landlock + bwrap, macOS sandbox‑exec; Windows unsandboxed). As of v0.57.0 it is **default-on for every orchestrator surface** — TUI, `stado run`, `stado headless`, `stado acp`, `stado mcp-server` all attach to the broker on launch and run under its projected ceiling. Direct `stado run` writes are confined to launch cwd + `/tmp` via Landlock; `stado_exec`/`stado_proc_spawn` under `stado mcp-server`/`stado daemon` retain the EP-0030 host-default policy (PID+uid namespace isolation, restricted FS, network passthrough). Opt out per-run via `--no-sandbox`; opt out process-wide via `STADO_BROKER_ATTACH=0` (development scenarios only).

## Attack surface, mitigations and attacker stories
### Tool execution & filesystem access
**Surface:** the `fs.*` (read/write/edit/glob/grep), `shell.*` (exec + PTY sessions), `rg`/`astgrep`, `readctx`, and `lsp.*` wasm tools. FS-touching host imports resolve the requested path (symlink-aware, EP-0031) and gate it against the calling plugin's declared `fs:read:`/`fs:write:` capability scopes via `host.allowRead` / `host.allowWrite` (`internal/plugins/runtime/host_fs.go`). A plugin with no `fs:read:<path>` capability covering the resolved path is denied — the gate is not "join to workdir," it is an explicit per-path capability check.

**Risks/attacker stories:**
- Prompt‑injected instructions try to make `fs.read` access `~/.ssh`, cloud credentials, or other non‑repo secrets; or `shell.exec` exfiltrate data. The reach is bounded by the capability scopes the enabled tools actually hold — a tool granted only `fs:read:<workdir>/**` cannot read `~/.ssh` regardless of what the model asks.
- A broadly-scoped capability (e.g. an operator granting `fs:read:/` to a convenience plugin) re-widens this; the trust then rests on the manifest the operator approved.
- Malicious repo content coerces the agent into destructive `shell.exec` commands within whatever the exec sandbox allows.

**Mitigations:**
- **Capability gating at the host-import boundary** (EP-0005, paths EP-0031): FS/exec/net reach is bounded by each plugin's declared, operator-approved capabilities — this is the primary containment, not a future plan.
- Work is done in a sidecar worktree; user repo stays pristine until `session land`.
- Output truncation budgets (`internal/tools/budget`) limit bulk exfiltration.
- Operator tool filters (`[tools] enabled/disabled`) remove a tool from the registry entirely.
- `stado run` (v0.57.0+) applies Linux landlock by default, restricting writes to launch cwd + /tmp (reads remain broad at the landlock layer — the capability gate is the tighter read control). `--no-sandbox` is the explicit per-run opt-out.
- Residual risk: capabilities are declared per plugin and approved at install/trust time; an over-broad grant or a trusted-but-coerced tool still operates within its granted scope. There is no per-call confirmation by design (EP-0017).

### OS sandboxing & network control
**Surface:** `internal/sandbox` runners (bwrap, sandbox‑exec), landlock/seccomp, HTTPS proxy allow‑list.

**Risks/attacker stories:**
- On Windows, or hosts without bwrap/sandbox‑exec, subprocesses run unsandboxed even where a host-default policy is requested (the policy is a no-op without an enforcing runner).
- Pre-v0.57.0 `stado run` defaulted to NO sandbox (`run.go`: default policy NONE). v0.57.0 reverses that — direct `stado run` is sandbox-by-default. Operators who keep `--no-sandbox` set (or who set `STADO_BROKER_ATTACH=0`) re-take the pre-v0.57.0 risk: exec from an interactive session inherits full user privileges.
- An explicit per-call `sandbox` field on `stado_exec` can opt out of the host default.

**Mitigations:**
- **Host-default protective policy** (EP-0030, v0.48.0): under `stado mcp-server` / `stado daemon`, `stado_exec`/`stado_proc_spawn` calls that don't supply their own `sandbox` field get bwrap/sandbox‑exec PID+uid namespace isolation, a restricted FS view (`/bin /sbin /tmp /var/tmp /run` reads, writes to `/tmp /var/tmp` + workdir), and network passthrough — applied by default rather than opt-in.
- Network allow‑listing via a local CONNECT proxy (host allow‑list).
- Operators running the higher-risk surfaces (MCP server for untrusted clients, daemon) get the protective default without configuration; direct-`stado run` operators must opt in.

### Network access and web fetching
**Surface:** LLM provider HTTP clients, OAI‑compat endpoints, `webfetch`.

**Risks/attacker stories:**
- `webfetch` can reach internal services (SSRF‑like behavior) and return data to the model.
- Base URL overrides can redirect traffic to untrusted endpoints; API keys may be exposed to a malicious proxy.

**Mitigations:**
- `webfetch` can be disabled via tool allowlist or stripped in air‑gap builds.
- Providers use HTTPS by default; operator should treat baseURL as trusted configuration.

### Plugins (WASM) and MCP extensions
**Surface:** plugin manifest/signature, trust store, wasm runtime host imports; MCP stdio/HTTP servers.

**Risks/attacker stories:**
- Malicious plugin signed by an untrusted key; or trust‑store tampering enabling rogue plugins.
- MCP HTTP server returns tool definitions that execute sensitive actions or exfiltrate data.

**Mitigations:**
- Ed25519‑signed manifests; trust store with fingerprint pinning and rollback protection.
- Optional CRL/Rekor verification paths for plugins.
- Capability‑gated host imports for plugin FS/net/session/LLM access.

### ACP JSON‑RPC server
**Surface:** stdin/stdout RPC for editor integrations (`internal/acp`).

**Risks/attacker stories:**
- Local process with access to the ACP connection can send prompts that trigger tool execution.

**Mitigations:**
- Designed for local IPC; no network listener. Operators should ensure only trusted clients spawn/use ACP.

### Audit log, signed commits, and sidecar state
**Surface:** `internal/state/git` + `internal/audit` commit signing.

**Risks/attacker stories:**
- Attacker modifies the sidecar to hide traces or replays tool calls; stolen signing key could forge history.

**Mitigations:**
- Every tool call produces a signed commit in `trace` and (for mutations) `tree`.
- `stado audit verify` detects tampering; signatures cover commit metadata and hashes.
- Reproducible builds and dual signing (cosign/minisign) reduce release tampering.

### Telemetry and logging
**Surface:** OpenTelemetry exporters, slog logs, hook outputs.

**Risks/attacker stories:**
- Enabling OTel can send tool names, model usage, and performance metadata to external collectors.
- Hook commands run with full user privileges and receive turn payloads.

**Mitigations:**
- Telemetry is opt‑in (`STADO_OTEL_ENABLED` / config).
- Hooks are operator‑configured; execution is time‑bounded and output is isolated to stderr.

**Out‑of‑scope / low‑relevance classes:** CSRF, XSS, SQL injection, and multi‑tenant authz are largely inapplicable because stado is a local CLI without a web server. The primary threats are local execution, data exfiltration, and trust boundary violations between untrusted content and privileged tooling.

## Criticality calibration (critical, high, medium, low)
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
