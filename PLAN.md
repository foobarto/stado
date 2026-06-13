# stado — Roadmap

The phased greenfield rollout (Phases 0–11) shipped between v0.1.0
and v0.48.x; that history lives in
[`CHANGELOG.md`](CHANGELOG.md) and the per-EP designs under
[`docs/eps/`](docs/eps/). For the as-built architecture, see
[`DESIGN.md`](DESIGN.md). This file is the forward-looking ledger:
deferred work + product gaps + non-goals.

## Architectural north star

- Sandboxed, git-native coding-agent runtime — not an LLM
  abstraction. A tight `pkg/agent` interface (~200 LOC) with four
  direct implementations (Anthropic, OpenAI, Google, OAI-compat).
- The user's repo stays pristine. Agent state lives in a sidecar
  bare repo with alternates pointing back at the user's objects.
- Dual-ref model: `tree` for executable state + turn/compaction
  boundaries; `trace` for every tool call (the audit log).
- Every tool call runs through an OS-level sandbox with a
  capability manifest. Capabilities are declared by the plugin,
  the kernel enforces.
- WASM plugins with capability-bound signed manifests. Post
  EP-0037/EP-0038, **every tool is a plugin** — a curated set is
  bundled into the binary, the rest are operator-installed.
- TUI + headless + ACP + MCP server all compose the same
  agent-loop core.
- OTel everywhere. Reproducible signed releases (cosign keyless +
  minisign).

## Current product gaps (ranked)

| Rank | Gap | Current state |
|------|-----|---------------|
| 1 | **v1 security architecture rollout** | Largely SHIPPED in v0.57.0 — sandbox-by-default, the privileged broker, session-scoped capabilities with an immutable ceiling, the mount-table CI invariant, and the taint substrate all landed (phase table below + EP-0050). Remaining: phase 6 taint-ingestion wiring and phase 7 ssh-agent + git sub-agent socket bind-mount. Decision record at [`.agent/decisions/2026-05-27-v1-security-architecture.md`](.agent/decisions/2026-05-27-v1-security-architecture.md). |
| 2 | **Windows sandbox v2** | Windows still runs unsandboxed behind `WinWarnRunner`. Job objects + restricted tokens remain the largest security/runtime gap for that platform. Re-open when someone with a Windows dev environment picks it up. |
| 3 | **Signed apt/rpm hosted repos** | goreleaser emits `.deb` / `.rpm` artifacts and the Homebrew tap publishes on every release. External repo hosting (apt/rpm) needs an operator with infra. |

Other surfaces — multi-session switching, alternative sandbox
backends — are net-new capabilities rather than half-shipped
work, so they live in EP backlog conversations, not here.

## v1 security architecture rollout

DESIGN.md specifies v1's security posture; this section tracked the
implementation. Phases 0-5 and 8 shipped in v0.57.0 (CHANGELOG: "v1
security architecture — broker, sandbox-first default, mount table,
ceiling/effective, taint substrate"); phases 6 (taint) and 7 (ssh-agent +
git sub-agent) have their substrate in place, with full enforcement/wiring
still in progress.

| Phase | Scope | Status |
|---|---|---|
| 0 | **Doc pass.** DESIGN.md sections: Sessions and sub-agents, Broker, revised Sandbox (sandbox-first default + `--no-sandbox` flag + mount-and-namespace invariant table + launch-cwd RW boundary + sandbox-mode startup announcement), Context management → Provenance and taint, Audit (trust-root invariant + audit-trace writer invariant + broker-decision log), Sessions → Git sub-agent. PLAN.md phasing (this section). | shipped — 2026-05-27 |
| 1 | **Broker as an evolution of `stado daemon`.** Long-running per-user privileged process picks up policy validation and session construction in addition to today's PTY/state-hosting role. Narrow-typed-unspoofable IPC for session-creation requests. Reverses today's "TUI / `stado run` / `stado mcp-server` host PTYs without the daemon" line: in v1 they all attach to the broker because the broker constructs the sandbox they run in. | shipped — v0.57.0 |
| 2 | **Sandbox-first default execution.** Rename `--sandbox-fs` → `--no-sandbox` with inverted polarity (pre-1.0 breaking, no deprecation alias). Default `BwrapRunner` on Linux / `SbxRunner` on macOS everywhere. Apply Landlock with both reads and writes enumerated per the mount table — retire the `WorktreeWrite` reads-everywhere pattern. TUI startup announcement of sandbox state + mount summary. Reverses the earlier UX-pressured retreat documented in `cmd/stado/run.go:281–290`. | shipped — v0.57.0 |
| 3 | **Mount-and-namespace invariant table in code + CI.** Lift the table from DESIGN.md into the broker's enforced policy. Add a CI assertion that the runtime's actual mount layout matches the table — so a refactor cannot silently widen a mount. Resolve the `~/.ssh/config` default-profile decision point flagged in DESIGN.md. | shipped — v0.57.0 |
| 4 | **Session capability model in code.** Ceiling/effective vocabulary applied to `Policy`; effective set drop-only; widening-via-fork is the only path. Broker validates each `spawn_agent` request against the operator's global policy and projects a ceiling mechanically from the declared `role`/`mode`/`write_scope`. | shipped — v0.57.0 |
| 5 | **Trust-root + audit-trace writer invariants.** Plugin trust ring + anchor-trust + revocation list + signing-verification keys mounted RO into agent namespaces (or compiled-in under hardened profile). Broker (or a dedicated trace-writer subprocess) becomes sole owner of the writable handle to `$XDG_DATA_HOME/stado/sessions/<repo-id>.git`; agent emits trace events over the broker IPC and never opens the sidecar for write. Broker-decision log lands at `$XDG_DATA_HOME/stado/broker/decisions.jsonl` (or equivalent). | shipped — v0.57.0 |
| 6 | **Provenance / taint tagging.** Origin labels assigned at every ingestion point (operator turns → TRUSTED; tool results / file reads / web fetches / plugin output / sub-agent results → UNTRUSTED). Labels are harness-side metadata, never in-band markers. Taint propagates conservatively: any UNTRUSTED span in the turn taints all subsequent tool calls in that turn. Taint feeds the broker's capability-grant policy and (in phase 7) the socket-bearing sub-agent approval prompt. | substrate shipped (v0.57.0); ingestion wiring in progress |
| 7 | **ssh-agent + git sub-agent.** Private key material not mounted into any session. ssh-agent socket bind-mounted only into single-purpose, broker-projected, broker-terminated sub-agents whose ceiling carries only the declared host's key(s) and scoped egress. `git push` denied at the tool-call dispatch point in a fetch-purposed sub-agent regardless of what the socket would sign. Approval-once gated on taint state of the requesting context. Last per operator ruling — most tuning expected. | substrate shipped (v0.57.0); ssh-agent socket bind-mount in progress |
| 8 | **`stado session tree` as broker client.** Standalone cobra subcommand issues session-fork requests over the broker IPC rather than materialising sessions client-side. PTY tests in `cmd/stado/session_tree_pty_test.go` continue to cover user-facing behaviour; implementation moves under the broker. | shipped — v0.57.0 |

### Explicit non-goals for v1

| Non-goal | Rationale |
|---|---|
| **CONNECT / egress proxy as the v1 enforcement floor.** | The Linux network namespace is the enforcement floor (`--unshare-net` for `NetDenyAll`, `pasta --splice-only` private netns for `NetAllowHosts`). The existing HTTPS CONNECT allowlist proxy at `internal/sandbox/proxy.go` is a *refinement layer* above the namespace and is preserved as-is. The proxy will be expanded in a later phase, but v1 does not depend on it for enforcement. |
| **External / relational policy engine** (OPA, Rego, or reimplementation thereof). | v1 policy is expressible as stado's existing capability (CAP) model — declared per-plugin manifests + a global operator policy. An external or relational policy engine is only warranted if policy becomes genuinely relational, which is a later determination. |
| **HTTPS-git credential confinement** (`~/.git-credentials`, `gh` CLI token, `GITHUB_TOKEN`). | These are bearer secrets: any process that can use them can read them. There is no signing-oracle equivalent to ssh-agent for HTTPS. v1 makes ssh remotes confinable and is honest about HTTPS being out of scope. See DESIGN.md §"Sessions and sub-agents" → "Git sub-agent" → "HTTPS-git is a known limitation". |
| **Content-safety classifier in the trust-critical path.** | The decision rule for taint is policy over *origins* — a fact. Content judgment is the same model whose output is being disciplined, deciding whether to discipline itself. If a future phase ever adds an advisory detector, it must be fail-safe and able only to NARROW, never widen; the system must remain fully sound with the detector deleted. |
| **Airgap-mode integration** (`-tags airgap`). | Today's airgap build tag splits self-update / plugin CRL fetch / rekor verification. Integrating the v1 broker/sandbox model with the airgap split is deferred to a future phase. Current `-tags airgap` semantics remain valid. |
| **Per-tool-call approval prompts** as the containment boundary. | v1 keeps tool execution yolo-by-default for the chat session (an approval prompt every turn is unworkable UX). The new approval surface is **capability-grant approval at session-creation time** for socket-bearing sub-agents — a different mechanism that fires at most once per sub-agent grant, gated on taint state. The fence — not the prompt — is what contains. |

## Cross-cutting decisions (still in force)

| Decision | Resolution |
|----------|------------|
| LLM abstraction | Tight internal `pkg/agent` interface (~200 LOC) with 4 direct implementations. No third-party abstraction library. |
| Session storage | Sidecar bare repo at `${XDG_DATA_HOME}/stado/sessions/<repo-id>.git` with alternates to the user's `.git/objects`. Worktrees at `${XDG_STATE_HOME}/stado/worktrees/<session-id>/`. |
| Commit granularity | Dual-ref: `tree` records file-changing mutations plus no-file-change turn/compaction commits; `trace` records every tool call as empty-tree commits. Turn boundaries are tagged. |
| Signing | Releases: cosign keyless (primary) + minisign (airgap fallback) on every release. Plugins: Ed25519 signed manifest with capability binding, rollback protection, optional Rekor attestation. |
| Tooling | All tools are wasm plugins (post EP-0037/EP-0038). Bundled set embedded in the binary; operator-installed plugins via the signed manifest path. No native-tool registry. |
| Inference | One OAI-compat HTTP client. Three documented presets (ollama, llamacpp, vllm) + custom. llama.cpp `llama-server` as primary reference. |
| Sandbox default (v1) | Sandboxed by default across all entry points (TUI, `stado run`, headless, ACP, MCP server, `stado tool run`). `--no-sandbox` is the opt-out (inverted polarity from the retired `--sandbox-fs`). Launch cwd + `/tmp` are the default RW grant; broker extends on operator action. Mount-and-namespace invariant table in DESIGN.md is the source-of-truth for what each profile mounts. |
| Session capability model (v1) | Sessions are the security atom; one agent per session. Each session has an immutable **ceiling** (max capabilities, set at session-creation by the broker) and a drop-only **effective set** (≤ ceiling, narrows during the session). Widening requires forking a new session. Capability never escalates along the spawn tree. |
| Approval — tool-call | Tool execution is yolo-by-default across TUI, `stado run`, and headless. Plugins can request approval at runtime via the `ui:approval` capability; operator filters via `[tools]` allow/deny lists. This row is unchanged from pre-v1. |
| Approval — capability-grant (v1) | A *separate* approval surface fires at **session-creation time** when the requested session needs a high-leverage capability the chat session shouldn't carry — currently the socket-bearing git sub-agent. Approval is **once per sub-agent session** because the session is single-purpose and broker-terminated. Whether the prompt fires is gated on the **taint state** of the requesting context: clean → no prompt (preserves no-nag default); tainted → prompts. The approval is an audit anchor and a speed bump, *not* the containment boundary; the projected ceiling is. |
| Plugin ABI versioning | SemVer on host imports; `min_stado_version` in manifest bumps when ABI breaks. Eager ABI verify at `session/new` surfaces stale plugins with the missing imports. |

## Offline / Airgap honesty

Be honest in docs about what "works offline" means at the model
capability level. A Claude Sonnet-class coding experience is not
replicated by Qwen2.5-Coder-32B or Llama-3.3-70B on a laptop —
they're genuinely useful but distinctly weaker at long agentic
tool-use loops. The airgap wedge is real for users who legally
can't send code to a cloud provider; it's a lie for users who just
want to save money and expect frontier-model quality from a 7B
model on their MacBook. Setting expectations in the README saves
angry issues.

`-tags airgap` build splits self-update, plugin CRL Fetch, and
webfetch.Run into `!airgap` / `airgap` pairs. Airgap binary
physically cannot reach the network from its own control plane;
provider HTTP (user's chosen inference target) untouched.

## See also

- [`CHANGELOG.md`](CHANGELOG.md) — per-release notes covering the
  shipped phases of this plan.
- [`DESIGN.md`](DESIGN.md) — as-built architecture (package layout,
  dependency rules, turn lifecycle, key invariants).
- [`docs/eps/`](docs/eps/) — Enhancement Proposals: per-feature
  design records, indexed in [`docs/eps/README.md`](docs/eps/README.md).
- [`docs/security/threatmodel.md`](docs/security/threatmodel.md) —
  threat model + attack-surface walkthrough.
- [`SECURITY.md`](SECURITY.md) — vulnerability reporting policy.
