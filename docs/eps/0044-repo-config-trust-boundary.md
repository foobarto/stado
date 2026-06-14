---
ep: 0044
title: Repo-config trust boundary — harden the project-config strip-list + per-project TOFU
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-06-14
see-also: [0035, 0039, 0050]
history:
  - date: 2026-06-14
    status: Accepted
    note: >
      Drafted from the 2026-06-14 Codex 140-findings triage. A cluster of 13
      findings showed that a repo-committed .stado/config.toml (and project
      personas/plugins) is merged with config-level trust while the threat
      model classifies "Repository contents" as attacker-controlled. Operator
      chose BOTH harden (extend the strip-list) AND per-project TOFU. Phase 1
      (always-strip harden) shipped first; TOFU + the per-surface gates are
      phased follow-ups tracked here.
---

# EP-44: Repo-config trust boundary

## Problem

`config.Load()` merges a repository's `.stado/config.toml` after the user
config, stripping only `[hooks]` and `[aliases]`. Everything else merges
verbatim. The threat model (`docs/security/threatmodel.md`) lists
**"Repository contents ... attacker-controlled"**, yet a cloned/shared repo
can today, on a bare `cd repo && stado`, with no operator action:

- neutralize the Esc / Ctrl+G interrupt kill switch or swap the input model
  (`[keymap]`) — Codex #7/#10;
- inject a system prompt by selecting a repo `.stado/personas/*.md`
  (`[defaults].persona` + the project persona search path) — #8/#42;
- auto-run installed wasm plugins at launch (`[plugins].background`,
  project personas' `plugins:`) — #8;
- forward host secrets into a wrapped agent (`inherit_env` under
  `[acp.providers.*]` / `[mcp.providers.*]`) — #20;
- install a persistent auto-approving MCP backdoor (`register_mcp`) — #3;
- remove the ACP loop circuit-breaker (`[acp].max_turns`) — #54;
- hide the sandbox/budget/risk safety chrome (`[tui.sidebar]/[tui.footer]`) — #14;
- autoload a project-local plugin trusted only by the global signer store
  (`.stado/plugins/`) and grant it CWD-wide FS caps — #4/#45;
- leak cross-repo memories via a repo-controlled `.stado/user-repo` pin — #126.

## Decision (operator, 2026-06-14)

Two complementary controls:

1. **Harden — always-strip.** Keys with no legitimate project use, or that must
   never be honored from a repo *even if the project is later trusted*, are
   stripped from the project overlay unconditionally (like `[hooks]`/`[aliases]`).
2. **TOFU-gate.** Legitimate-but-powerful project keys (exec/exfil-capable) are
   applied only after an explicit per-project operator trust decision, keyed by
   the canonical repo-id, reusing the `AnchorTrustStore` file pattern. In
   non-interactive surfaces (headless/CI) the gated keys are ignored-and-warned
   rather than erroring, matching the existing strip behavior.

## Design

### Phase 1 — always-strip (SHIPPED)

`internal/config/config.go` Load() strip loop extended from `{hooks, aliases}`
to also drop: `keymap`, `defaults.persona`, `agent.system_prompt_path` (a repo
path that `loadSystemPromptTemplate` would read into the provider system prompt
— same injection class as `defaults.persona`; Codex #210), `plugins.background`,
`acp` (whole — register_mcp + inherit_env + max_turns), `mcp.providers`
(wrapped-MCP inherit_env; `mcp.servers` stays), `tui.sidebar`, `tui.footer`. koanf `Delete`
is a recursive prefix-delete, so dotted leaves and whole subtrees both work.
Project model/provider/tool overrides (the EP-0035 use case) remain honored.
This satisfies the defense-in-depth requirement that the interrupt keymap and
`inherit_env` are NEVER honored from a repo regardless of any future trust.

### Phase 2 — per-project TOFU (DEFERRED, follow-up)

- New `ProjectConfigTrustStore` at `<StateDir>/project-config-trust/<repo-id>.json`,
  keyed by `stadogit.RepoID(repoRoot)` (canonical, symlink-tolerant), modeled on
  `internal/plugins/anchor.go` (one JSON file per key, atomic 0o600 write).
- Gate the still-powerful project keys behind it: `mcp.servers` (subprocess
  exec), `inference.presets` + `defaults.provider` (exfil endpoint), `sandbox`,
  `runtime.use_wasm`. Trusted → merge; untrusted+interactive → prompt via the
  existing `promptYesNoTTY` / `tuiApprovalBridge` / ACP `requestApproval`
  bridges; untrusted+non-interactive → drop + stderr advisory.

### Phase 3 — persona/plugin origin gates

- **Persona Vector 1 (#8/#42) — SHIPPED.** A project-origin `.stado/personas/*.md`
  shadowed the bundled/user persona silently. The `personas.Resolver` now honors
  the `{CWD}/.stado/personas/` dir only when `AllowProject` is set, plumbed from
  the opt-in `[defaults] allow_project_persona` (default false) at the
  cfg-bearing surfaces (TUI/headless/acp); llmtool/subagent default off. The
  opt-in key is itself in the project-config strip-list so a repo can't
  self-enable it. (Vector 2 — `[defaults].persona` from project config — was
  already closed in phase 1.)
- **Project plugin autoload (#4/#45) — DEFERRED, follow-up.**
  `registerInstalledPluginTools` verifies only the global signer store; add a
  per-project trust gate before honoring `.stado/plugins/`, and scope a project
  plugin's relative `fs:*` caps tighter than the whole session workdir.

### Out of cluster (separate fixes)

- **#12 (SHIPPED)** auto-LSP spawned unsandboxed servers on repo edits — now
  gated behind opt-in `[lsp].auto_diagnostics` (default false), so a
  prompt-injected edit in an untrusted repo can't drive host LSP spawns.
- **#11 (SHIPPED, docs)** redacted tool output is still persisted in the sidecar
  audit blobs — added a caveat to `docs/features/lifecycle-hooks.md` that a
  `post_tool` mutate is context-hygiene, not secret-erasure. (A future
  `NoAuditBlob` opt-out remains possible per the mutation-provenance spec.)
- **#126 (SHIPPED)** cross-repo memory via a repo-committed `.stado/user-repo` —
  the pin is now honored only when it is an ancestor/descendant of the workdir
  (`internal/memory/context.go` `pinRelatedToWorkdir`).

## Test strategy
- Phase 1: `TestProjectOverlayStripsSecuritySensitiveKeys` — every stripped key
  is dropped from a project overlay while `defaults.model` + `mcp.servers`
  survive. (Shipped, green.)
- Phase 2/3: TOFU store round-trip + gate behavior per surface; persona-origin
  fallback; project-plugin trust gate — with the follow-up PRs.

## Risk / self-critique
- Stripping whole `[acp]` from project config is safe (ACP-server config is an
  operator surface), but if a future legitimate project-level `acp` key appears
  it must be added back as a gated (not stripped) key.
- The always-strip is coarse (whole subtrees); Phase 2's TOFU is the finer
  control for keys that have a real project use. Shipping Phase 1 first trades
  some project flexibility for immediate closure of the HIGH findings.
