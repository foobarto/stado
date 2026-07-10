# stado — Threat model

> Last reviewed: 2026-07-10 (EP-0050 top-level process-ceiling parity).
> **Every tool is now a signed wasm plugin** dispatched
> through capability-gated host imports — there are no in-process native
> tools (the pre-EP-0037/0038 `internal/tools/*` surface is gone). See
> `docs/eps/0037-tool-dispatch-and-operator-surface.md`,
> `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`,
> `docs/eps/0005-capability-based-sandboxing.md`,
> `docs/eps/0030-security-research-default-harness.md`, and
> `docs/eps/0044-repo-config-trust-boundary.md`. The central shift
> since the 2026-04-25 review: filesystem and network access are gated by
> per-plugin **capabilities** enforced at the host-import boundary, not
> left to in-process trust. Update when re-walking.

## Overview
stado is a local CLI/TUI coding agent that integrates with LLM providers
(Anthropic/OpenAI/Google/OAI-compatible), maintains git-sidecar session and
audit state, and executes signed wasm tools through capability-gated host
imports. Tools operate on the launch cwd; the sidecar worktree is the audit and
fork substrate, not a hidden replacement working directory.

Two controls are complementary. First, each wasm host import enforces the
calling plugin's declared FS/net/exec capability. Second, the broker projects a
per-session process ceiling that is composed with OS subprocess containment.
TUI, `stado run`, `stado run --headless`, ACP, and `mcp-server` all derive the
same executor sandbox decision and retain it for every executor they create.
`stado_exec`, `stado_proc_spawn`, and PTY creation use that executor's runner;
on Linux this is bubblewrap plus seccomp, and on macOS it is sandbox-exec.
`--no-sandbox` is the explicit process-containment opt-out on every top-level
surface. `STADO_BROKER_ATTACH=0` skips broker mediation only; it does not select
`NoneRunner` or remove the local host-default process policy. Network
allow-listing via a local CONNECT proxy applies to subprocess policies that use
`NetAllowHosts`.

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
- The OS user remains the outer security boundary. The sandbox narrows what a
  subprocess running as that user can see and mutate.
- **Containment is capability-based, not approval-based.** There is no automatic per-tool-call approval prompt (the old native-tool approval loop was removed in EP-0017 — a prompt was a poor containment boundary). What a tool can touch is bounded by (a) which plugins are registered/enabled, (b) the FS/net/exec capabilities each plugin's manifest declares, enforced at the host-import boundary, and (c) the sandbox policy. Human approval still exists but as an **opt-in capability** (`stado_ui_approve`) a plugin invokes deliberately, not a blanket gate.
- Sandboxing is platform-dependent (Linux Landlock + bwrap, macOS
  sandbox-exec). Windows has no native confinement runner yet: a requested
  subprocess policy fails closed instead of silently using the
  `windows-passthrough` runner; `--no-sandbox` is the explicit override. Broker
  attachment is default-on for TUI, `stado run`, `stado run --headless`, ACP,
  and `mcp-server`; its projected process ceiling reaches all five. Direct
  `stado run` additionally applies Landlock to the stado process itself.

## Attack surface, mitigations and attacker stories
### Tool execution & filesystem access
**Surface:** the `fs.*` (read/write/edit/glob/grep), `shell.*` (exec + PTY sessions), `rg`/`astgrep`, `readctx`, and `lsp.*` wasm tools. FS-touching host imports resolve the requested path (symlink-aware, EP-0031) and gate it against the calling plugin's declared `fs:read:`/`fs:write:` capability scopes via `host.allowRead` / `host.allowWrite` (`internal/plugins/runtime/host_fs.go`). A plugin with no `fs:read:<path>` capability covering the resolved path is denied — the gate is not "join to workdir," it is an explicit per-path capability check.

**Risks/attacker stories:**
- Prompt‑injected instructions try to make `fs.read` access `~/.ssh`, cloud credentials, or other non‑repo secrets; or `shell.exec` exfiltrate data. The reach is bounded by the capability scopes the enabled tools actually hold — a tool granted only `fs:read:<workdir>/**` cannot read `~/.ssh` regardless of what the model asks.
- A broadly-scoped capability (e.g. an operator granting `fs:read:/` to a convenience plugin) re-widens this; the trust then rests on the manifest the operator approved.
- Malicious repo content coerces the agent into destructive `shell.exec` commands within whatever the exec sandbox allows.

**Mitigations:**
- **Capability gating at the host-import boundary** (EP-0005, paths EP-0031): FS/exec/net reach is bounded by each plugin's declared, operator-approved capabilities — this is the primary containment, not a future plan.
- Tools write the launch cwd. Turn-boundary snapshots and trace metadata are
  recorded through the sidecar worktree so sessions can be inspected, forked,
  and landed without changing the tool-visible cwd.
- Output truncation budgets (`internal/tools/budget`) limit bulk exfiltration.
- Operator tool filters (`[tools] enabled/disabled`) remove a tool from the registry entirely.
- `stado run` (v0.57.0+) applies Linux landlock by default, restricting writes
  to launch cwd, `/tmp`, and the exact runtime-owned session worktree/sidecar
  paths required for audit and conversation persistence (reads remain broad at
  the landlock layer; the capability gate is the tighter read control).
  `--no-sandbox` is the explicit per-run opt-out.
- Residual risk: capabilities are declared per plugin and approved at install/trust time; an over-broad grant or a trusted-but-coerced tool still operates within its granted scope. There is no per-call confirmation by design (EP-0017).

### OS sandboxing & network control
**Surface:** `internal/sandbox` runners (bwrap, sandbox‑exec), landlock/seccomp, HTTPS proxy allow‑list.

**Risks/attacker stories:**
- On Windows, or hosts without bwrap/sandbox-exec, sandboxed subprocess calls
  fail because no native runner can enforce the requested policy. The explicit
  `--no-sandbox` override restores direct execution.
- Pre-v0.57.0 `stado run` defaulted to no sandbox. v0.57.0 reversed
  that. Operators who set `--no-sandbox` retake the direct-execution risk;
  `STADO_BROKER_ATTACH=0` only removes the broker-projected ceiling.
- A plugin's explicit per-call `sandbox` field can tighten the host default but
  cannot opt out of it. Only the operator-level `--no-sandbox` flag removes the
  host default.

**Mitigations:**
- **Host-default protective policy** (EP-0030): on all five top-level
  orchestrator surfaces, process and PTY imports that omit a guest policy get
  bwrap/sandbox-exec isolation, a restricted FS view, and the launch cwd as a
  write boundary. The broker ceiling can only narrow that policy.
- Network allow‑listing via a local CONNECT proxy (host allow‑list).
- Every top-level surface gets the protective default without configuration.

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
- Host-import resource caps (v0.75.2): `stado_json_format` output capped at 4 MiB with 64-level nesting limit; `stado_dns_resolve_axfr` capped at 50k records / 120s timeout.
- Nested `stado_tool_invoke` routes through the session `Executor.Run` when pinned (TUI/`/reload`, `stado run`, headless) so inner calls get the same audit + hook + sandbox path as top-level tools.

### Repository configuration (`.stado/config.toml`)
**Surface:** project overlay merged after user config (`internal/config/config.go`).

**Risks/attacker stories:**
- Cloned repo commits operator-domain keys — keymap overrides that neutralize Esc/Ctrl+G, `[plugins].background` autostart, MCP server declarations, `inherit_env` secret passthrough, persona injection, etc.

**Mitigations (EP-0044, v0.75.0):**
- Security-sensitive keys are **always stripped** from project config before merge (`[hooks]`, `[keymap]`, `[plugins].background`, `[mcp.*]`, `[sandbox]`, …). See `docs/commands/config.md`.
- Project personas and project-local plugin autoload require explicit **user-config opt-in** (`allow_project_persona`, `allow_project_plugins`); the opt-in keys themselves are stripped from project config.
- Per-project TOFU for remaining overlay keys is deferred; always-strip is the shipped posture.

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
