# stado — Threat model

> Last reviewed: 2026-08-14 (EP-0063/0064 artifact and lifecycle boundary;
> EP-0065 Linux-only platform scope).
> Model-facing product tools are signed WASM plugins dispatched through
> capability-gated host imports; native code supplies the documented runtime
> primitives. The stado-owned native model-tool allowlist is empty under
> EP-0066; the sole exact external MCP adapter is not parallel native
> application policy. See
> `docs/eps/0037-tool-dispatch-and-operator-surface.md`,
> `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`,
> `docs/eps/0005-capability-based-sandboxing.md`,
> `docs/eps/0030-security-research-default-harness.md`, and
> `docs/eps/0044-repo-config-trust-boundary.md`,
> `docs/eps/0063-plugin-defined-harness-artifacts.md`,
> `docs/eps/0064-wasm-lifecycle-applications.md`, and
> `docs/eps/0065-linux-only-platform-scope.md`, and
> `docs/eps/0066-canonical-plugin-authority-and-application-placement.md`. The central shift
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
`stado_exec`, `stado_proc_spawn`, and PTY creation use that executor's Linux
runner: bubblewrap plus seccomp.
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
- Durable TUI broker scopes use a no-follow 0600 recovery bearer bound to the
  broker-recorded canonical repository and current exact git-session subject.
  It prevents guessing and accidental cross-client attachment, but not theft
  or interference by a malicious process already running as the same UID. On
  adoption the broker rotates the live controller and binding version while
  preserving the session/generation application state; the disk bearer stays
  stable because safe rotation would require a two-phase confirmation
  protocol. Automatic compacted-child recovery pre-stages that same bearer and
  atomically moves the broker-recorded subject only after broker-owned direct
  lineage verification; manual forks never inherit it. An ambiguous handoff
  outcome is fail-closed.
- **Containment is capability-based, not approval-based.** There is no automatic per-tool-call approval prompt (the old native-tool approval loop was removed in EP-0017 — a prompt was a poor containment boundary). What a tool can touch is bounded by (a) which plugins are registered/enabled, (b) the FS/net/exec capabilities each plugin's manifest declares, enforced at the host-import boundary, and (c) the sandbox policy. `stado_ui_approve` is an opt-in yes/no workflow interaction, not an authentication primitive or a blanket containment gate.
- Linux is the only supported platform now and through v1 (EP-0065). Darwin
  and Windows are outside the build/runtime contract and carry no current
  containment guarantee. `--no-sandbox` is the explicit Linux override. Broker
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
- Residual risk: capabilities are approved as a signed package ceiling at install/trust time and may be attenuated per signed tool. An over-broad effective tool grant or a trusted-but-coerced tool still operates within that scope. There is no per-call confirmation by design (EP-0017).

### OS sandboxing & network control
**Surface:** the supported Linux `internal/sandbox` runner (bubblewrap),
Landlock/seccomp, namespaces, and the HTTPS proxy allow-list.

**Risks/attacker stories:**
- On a Linux host without the required containment primitives, a requested
  subprocess policy fails because no supported runner can enforce it. The
  explicit `--no-sandbox` override restores direct execution without the v1
  containment posture.
- Pre-v0.57.0 `stado run` defaulted to no sandbox. v0.57.0 reversed
  that. Operators who set `--no-sandbox` retake the direct-execution risk;
  `STADO_BROKER_ATTACH=0` only removes the broker-projected ceiling.
- A plugin's explicit per-call `sandbox` field can tighten the host default but
  cannot opt out of it. Only the operator-level `--no-sandbox` flag removes the
  host default.

**Mitigations:**
- **Host-default protective policy** (EP-0030): on all five top-level
  orchestrator surfaces, process and PTY imports that omit a guest policy get
  bubblewrap isolation, a restricted FS view, and the launch cwd as a
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
- Generic artifact imports are kind- and operation-scoped. The host injects
  canonical plugin identity and session scope; guest JSON cannot forge those
  authority fields, and the broker remains the sole authoritative writer
  (EP-0063).
- Lifecycle applications and watchdogs are quality gates, not security
  boundaries. Their pause/stop requests are enforced only through the broker's
  scoped scheduling primitives and never gain authority from model prose or a
  mailbox message (EP-0064).
- EP-60 guidance is weaker still: an opaque broker binding selects bounded
  current-session facts, the TUI supplies explicitly untrusted input/retrieval
  observations and its live tool ceiling, and the application may only append
  bounded advisory system context. It cannot deny, mutate model/history, or
  widen authority; failure-open faults contribute nothing.
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
- Reproducible builds, Cosign-signed checksum manifests, SBOMs, and provenance
  reduce release tampering. The stricter Minisign path remains inactive until
  its release key is provisioned.

### Telemetry and logging
**Surface:** OpenTelemetry exporters, slog logs, hook outputs.

**Risks/attacker stories:**
- Enabling OTel can send tool names, model usage, and performance metadata to external collectors.
- Hook commands run with full user privileges and receive turn payloads.

**Mitigations:**
- Telemetry is opt‑in (`STADO_OTEL_ENABLED` / config).
- Hooks are operator‑configured; execution is time‑bounded and output is isolated to stderr.

### Supervision reviewers (quality application; signed release available)

EP-0064 places `/supervise` workflow policy in the official WASM application
under `foobarto/stado-plugins/supervise`. The preserved source checkpoint is a
signed Git commit, and the offline-key-signed `supervise/v0.1.1` manifest and
WASM are published for stado 0.80.0 and newer. Native stado contains no
fallback command or policy path. Once the exact signed installed application is explicitly enabled,
it dynamically owns `/supervise` and its three worker tools. The plugin owns contract
setup, review cadence, deterministic
detectors, reviewer/verifier prompts, verdict interpretation, stale-result
policy, retries, plan/completion policy, and recovery workflow. Those are
quality decisions two supervision applications could make differently; they
are not native security primitives.

The host and broker own the facts and effects a sandboxed application cannot
provide for itself: canonical plugin/session identity, broker-stamped current
turn/tree/trace anchors, immutable evidence references, artifact and journal
ordering, attenuated reviewer capabilities, separately issued operator-origin
grants where policy provides them, and enforcement of holds, pause, stop,
cancel, and budget ceilings. Guest JSON and mailbox prose cannot choose those
fields or transitions. A plugin may request an allowed effect; the broker
checks identity, scope, current anchor, capability, and CAS, not whether the
model's semantic judgment was wise. An in-process UI callback is not proof of a
fresh operator gesture against the EP-0050 threat model.

Watchdogs and verifiers consume untrusted repository, transcript, tool, diff,
and child evidence through bounded capabilities. They are not trusted execution
principals and do not become a security boundary merely because the application
calls them reviewers. The accepted stale-result policy is asymmetric: discard
an earlier-anchor approval, deliver an earlier-anchor correction only as
labelled advisory steering, and turn an earlier-anchor pause/stop into a durable
hold plus a fresh current-anchor review. Only a current authorized request may
apply the final scheduling transition.

The operator remains the authority for the supervision contract and high-risk
external boundaries. A steered reviewer can waste tokens, produce bad advice,
or cause a scoped pause request; it cannot widen capabilities, perform a
deployment, or manufacture an anchored completion record. See
[EP-0062](../eps/0062-harness-enforced-supervised-work.md) and
[EP-0064](../eps/0064-wasm-lifecycle-applications.md).

The selected reviewer provider receives requested evidence and is therefore a
data-egress trust choice. This is particularly important when watchdog or
verifier differs from the worker provider. Hook-mutated/redacted tool results
remain mutated in the reviewer view, but stado does not claim it can identify
arbitrary credentials pasted into source, diffs, or conversation text.

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
