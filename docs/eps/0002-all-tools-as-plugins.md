---
ep: 2
title: All Tools as WASM Plugins
author: foobarto
status: Draft
type: Standards
created: 2026-04-22
history:
  - date: 2026-04-22
    status: Draft
    note: Initial draft — converted from brainstorming session on tools-to-plugins migration.
---

# EP-2: All Tools as WASM Plugins

## Problem

Stado's built-in tools (`read`, `write`, `bash`, `web_fetch`, `ripgrep`, `commit`, `diff`, `ls`, etc.) are hard-coded in Go. Users cannot replace a built-in tool with an alternate implementation that adds scrubbing, custom auth, compliance logging, or company-specific policy. If a security-conscious developer wants a `web_fetch` that strips internal domain names before hitting the wire, or a `read` that logs every file access for audit, they must fork stado and edit Go source.

The plugin runtime already exists (wazero, manifest+trust, host imports), and plugins can add *new* tools. But built-in tools are privileged: they run native code with full process capabilities, no manifest audit, and no ability to override.

## Goals

- Every tool the model sees is backed by a plugin (native or WASM).
- Users can replace any tool with an alternate plugin by name.
- Host exposes thin wrappers for shared resources so plugins get performance benefits (connection pooling, fd caching) without owning mutable state.
- All tool calls — Mutating and NonMutating — pass through an approval/admission gate.
- Plugin capabilities are declared in the manifest; the host enforces them at the wrapper level.

## Non-goals

- Removing built-in tools today. Native and plugin tools coexist during transition.
- Changing the `tools.Registry` wire contract (`ToolDef` schema, `agent.ToolResultBlock`). Those stay stable.
- Requiring one plugin per tool. Multi-tool plugins are fine.
- Making plugins responsible for their own sandboxing. Host enforces; plugins declare.
- Achieving zero latency overhead. Performance tradeoffs are explicitly accepted (<1sec per tool-heavy turn).

## Design

### 4.1. Component map

```text
                        User surfaces
        TUI ─────── stado run ────── headless ────── ACP
         │            │                 │              │
         └────────────┴────────┬────────┴──────────────┘
                                │
                    ┌───────────▼───────────┐
                    │   internal/runtime    │
                    │     (AgentLoop)         │
                    └───────────┬───────────┘
                                │
              ┌─────────────────┼──────────────────┐
              ▼                 ▼                  ▼
      ┌──────────────┐  ┌──────────────┐   ┌──────────────┐
      │   Provider   │  │ tools.       │   │ internal/    │
      │   (pkg/agent)│  │   Registry   │   │   state/git  │
      └──────────────┘  └──────┬───────┘   └──────────────┘
                               │
              ┌────────────────┴────────────────┐
              │                                 │
         native tool                        plugin tool
              │                                 │
         (Go function)                     ┌────┴────┐
                                           │  WASM   │
                                           │ runtime │
                                           └────┬────┘
                                                │
                                 ┌──────────────┼──────────────┐
                                 ▼              ▼              ▼
                        ┌────────────┐ ┌────────────┐ ┌────────────┐
                        │stado_http_*│ │stado_fs_*  │ │stado_llm_* │
                        │ wrappers   │ │ wrappers   │ │ wrappers   │
                        │ (pooled)   │ │ (landlock) │ │ (budget)   │
                        └────────────┘ └────────────┘ └────────────┘
```

### 4.2. Host-import surface

Every wrapper is a host function installed once per wasm runtime. The wrapper is native Go code that enforces capability checks *before* calling the real implementation.

| Host import | Capability in manifest | What it wraps | Check |
|-------------|------------------------|---------------|-------|
| `stado_http_get(...)` | `net:http_get` | `http.Client.Do(GET)` on shared `*http.Client` | none by default; future OPA hook |
| `stado_http_post(...)` | `net:http_post` | `http.Client.Do(POST)` | same |
| `stado_fs_read(...)` | `fs:read` | `os.ReadFile` | Path prefix (landlock) |
| `stado_fs_write(...)` | `fs:write` | `os.WriteFile` | Path prefix + **approval gate** |
| `stado_shell(...)` | `exec:shallow_bash` | `bash` tool executor (async) | **approval gate** |
| `stado_llm_invoke(...)` | `llm:invoke:N` | `Provider.StreamTurn` | Budget preflight |
| `stado_git_commit(...)` | `git:commit` | `gitSession.CommitTurn` | **approval gate** |
| `stado_git_diff(...)` | `git:diff` | `gitSession.Diff` | Read-only, no gate |

The wrapper owns the resource; the plugin borrows it through the wrapper API.

### 4.3. Error propagation

Plugin tools return a flat JSON string on stdout. Host parses into `agent.ToolResultBlock`:

```json
{
  "content": "The result text",
  "error": "",
  "metadata": {
    "http_status": 200,
    "bytes_read": 4096
  }
}
```

If `error` is non-empty, host wraps as `agent.ToolResultBlock{Error: err}` and the agent loop handles it exactly like a native tool error.

### 4.4. Approval gate

All tool calls pass through an approval gate. The gate decides based on:
1. Tool class from manifest (`Mutating` | `NonMutating` | `Exec`)
2. Capability scope (e.g., `fs:read` with `allowed_prefixes`)
3. Call-site context (actual path, URL, command string)
4. Policy — hardcoded allowlist today; future OPA WASM policy or sidecar

Config levels (precedence: session > project > global):
- Global: `~/.config/stado/policy.toml`
- Project: `.stado/policy.toml`
- Session: runtime `--policy` flag or `!policy` system prompt directive

### 4.5. Plugin manifest additions

```json
{
  "tools": [{
    "name": "read",
    "description": "Read a file",
    "class": "NonMutating",
    "capabilities": ["fs:read"],
    "schema": "..."
  }],
  "capabilities": ["fs:read", "net:http_get"],
  "allowed_prefixes": ["/tmp", "."]
}
```

- `class`: `"NonMutating"` | `"Mutating"` | `"Exec"`
- `capabilities`: controls which host imports are available
- `allowed_prefixes`: optional stricter scope than raw capability

### 4.6. Registry precedence

```go
// At init/override time
reg.Override("web_fetch", pluginTool{manifest: mf, wasmPath: "...", runtime: rt})

// reg.All() returns built-ins with no override + plugins for which override exists
```

CLI flag:
```
stado --override-tool web_fetch=my-patched-fetch@v1.0.0 \
      --override-tool read=my-read@v2.1.0
```

Config:
```toml
[tools.overrides]
web_fetch = "my-patched-fetch@v1.0.0"
read = "my-read@v2.1.0"
```

## Migration / rollout

| Phase | Scope | Effort | Deliverable |
|-------|-------|--------|-------------|
| 7.1 | Design sign-off + `ToolDef.Class` in manifest | 2 days | Merged EP + manifest schema |
| 7.2 | `--override-tool` flag + registry wiring | 3 days | PR with tests |
| 7.3 | First NonMutating override: `web_fetch` → demo plugin | 2 days | Working demo + latency numbers |
| 7.4 | Add `stado_http_*` host imports | 3 days | PR with pooled client |
| 7.5 | First Mutating override: `write` + approval gate | 4 days | PR + UX review |
| 7.6 | Bulk tool migration (read, ripgrep, ls, diff) | 1-2 weeks | All built-ins have plugin equivalents |
| 7.7 | Remove native tool implementations (keep wrappers) | 3 days | Vendored plugins |
| 7.8 | Policy/admission control (OPA integration) | 2-3 weeks | Optional OPA sidecar |

## Failure modes

| Failure | Impact | Mitigation |
|---------|--------|------------|
| WASM cold-start latency | Medium | Cache runtime + module across calls; AOT compilation |
| Plugin replaces core tool, breaks chaining | High | Integration tests per override; native fallback for 1 release |
| Approval gate UX is annoying | Medium | Start as opt-in (`--strict-approvals`); expressive policy rules |
| Trust store TOCTOU (every plugin install) | High | Fix TOCTOU first; add advisory file lock (`flock`) |
| Plugin ecosystem fragmentation | Low | Registry namespace (`hub.stado.dev/tools/<name>`); semver enforcement |

## Test strategy

- Unit: registry precedence, manifest parsing, wrapper capability checks
- Integration: plugin override round-trip latency vs native
- End-to-end: `--override-tool` with every built-in tool; verify chaining intact
- Security: tampered manifest rejected; capability escalation blocked

## Open questions

1. Should plugins own their own `http.Client` pool, or share the host's? Sharing is faster; per-plugin pools are easier to rate-limit.
2. How do we version host imports? If `stado_fs_read` gains a parameter in v1.1, old plugins must still run.
3. Plugin manifests for native tools that are *not* overridden — do we generate synthetic manifests so the registry is uniform?
4. Approval gate for NonMutating calls — inline overlay, sidebar notification, or require `:approve`? The latter breaks agent autonomy for non-interactive surfaces.
5. `stado plugin install` from URL — how does the user discover and install a tool plugin? Manual install, or add a registry index?

## Decision log

### D1. All tools as plugins (not just extensible)

- **Decided:** migrate every built-in tool to a plugin; native code becomes host wrappers only.
- **Alternatives:** keep native core and allow Go function overrides via interfaces.
- **Why:** native overrides require recompiling stado, losing the sandbox boundary, and provide no audit trail. WASM plugins are independently verifiable and replaceable without touching stado source.

### D2. Security > performance

- **Decided:** accept <1sec overhead per tool-heavy turn; >1sec is "matter of consideration."
- **Alternatives:** keep hot-path tools native for performance; only migrate slow/network tools.
- **Why:** stado's value proposition is security and auditability, not raw speed. LLM inference is already the dominant latency; plugin overhead is negligible in comparison.

### D3. Thin wrappers for shared resources

- **Decided:** host exposes pooled resources (http.Client, fs, git session) via narrow host-import wrappers; plugins borrow, not own.
- **Alternatives:** plugins get raw syscalls (`os.ReadFile`, `net.Dial`) and manage their own pooling.
- **Why:** connection pooling and fd caching are hard to do efficiently across a WASM boundary. Thin wrappers let the host handle performance while the plugin handles logic.

### D4. Approval gate for ALL tool calls

- **Decided:** Mutating and NonMutating calls both pass through the approval gate.
- **Alternatives:** only Mutating/Exec tools get approval; NonMutating runs freely.
- **Why:** reading sensitive files (`/etc/passwd`, `.env`) and web search with auth tokens are NonMutating but high-risk. The gate must inspect call-site context, not just tool class.

## Related

- [EP-1: EP Purpose and Guidelines](0001-ep-purpose-and-guidelines.md)
- DESIGN.md §Tools
- PLAN.md Phase 8 (plugin ecosystem)