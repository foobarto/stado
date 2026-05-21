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
| 1 | **Windows sandbox v2** | Windows still runs unsandboxed behind `WinWarnRunner`. Job objects + restricted tokens remain the largest security/runtime gap. Re-open when someone with a Windows dev environment picks it up. |
| 2 | **Signed apt/rpm hosted repos** | goreleaser emits `.deb` / `.rpm` artifacts and the Homebrew tap publishes on every release. External repo hosting (apt/rpm) needs an operator with infra. |

Other surfaces — multi-session switching, alternative sandbox
backends — are net-new capabilities rather than half-shipped
work, so they live in EP backlog conversations, not here.

## Cross-cutting decisions (still in force)

| Decision | Resolution |
|----------|------------|
| LLM abstraction | Tight internal `pkg/agent` interface (~200 LOC) with 4 direct implementations. No third-party abstraction library. |
| Session storage | Sidecar bare repo at `${XDG_DATA_HOME}/stado/sessions/<repo-id>.git` with alternates to the user's `.git/objects`. Worktrees at `${XDG_STATE_HOME}/stado/worktrees/<session-id>/`. |
| Commit granularity | Dual-ref: `tree` records file-changing mutations plus no-file-change turn/compaction commits; `trace` records every tool call as empty-tree commits. Turn boundaries are tagged. |
| Signing | Releases: cosign keyless (primary) + minisign (airgap fallback) on every release. Plugins: Ed25519 signed manifest with capability binding, rollback protection, optional Rekor attestation. |
| Tooling | All tools are wasm plugins (post EP-0037/EP-0038). Bundled set embedded in the binary; operator-installed plugins via the signed manifest path. No native-tool registry. |
| Inference | One OAI-compat HTTP client. Three documented presets (ollama, llamacpp, vllm) + custom. llama.cpp `llama-server` as primary reference. |
| Approval | Tool execution is yolo-by-default across TUI and headless. Plugins can request approval via `ui:approval`; operator filters via `[tools]` allow/deny lists. |
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
