# RFC: All Tools as WASM Plugins

**Author:** @foobarto
**Date:** 2026-04-22
**Status:** Draft → Awaiting Discussion
**Related:** DESIGN.md §Tools, PLAN.md Phase 8

---

## 1. Summary

Move **every** built-in tool (`read`, `write`, `bash`, `web_fetch`, `ripgrep`, `commit`, `diff`, `ls`, etc.) out of native Go code and into individually-packaged, signed, sandboxed WASM plugins. The host (stado core) retains only a thin trust boundary and a set of host-import wrappers for shared resources (HTTP, FS, git, LLM). This makes every tool independently auditable, overridable, and policy-governed.

---

## 2. Motivation

### 2.1. Why now?

The plugin runtime is already built (wazero), the manifest-and-trust layer is working (`stado plugin verify/install/trust/run`), and the headless+ACP servers can invoke plugins. The gap is: **plugins can only add new tools, not replace existing ones.** A user who wants a web-search tool that scrubs PII before hitting the wire, or a `read` tool that logs every path for compliance, has no supported path today.

### 2.2. Specific user stories

| User | Need | What they would do after this RFC |
|------|------|-----------------------------------|
| Enterprise admin | Every `read` of files outside the repo must be approved and logged | Ship a custom `read` plugin that wraps `stado_fs_read` with an OPA check |
| Security-conscious developer | Web search leaks my company's internal domain names | Replace `web_fetch` with a plugin that runs request body through a scrubber before calling `stado_http_get` |
| Compliance auditor | Prove that no tool ever touched `/etc/passwd` | Audit plugin manifests for `fs:read` capability scopes; no unscoped native code |
| Tool author | Publish a better `commit` tool | Sign and ship a plugin; users pin it and `--override-tool commit=my-commit@v1` |

---

## 3. Goals & Non-Goals

### Goals
1. Every tool that the model sees is backed by a plugin.
2. Users can replace any tool with an alternate plugin by name.
3. Host exposes thin wrappers for shared resources so plugins get performance benefits (connection pooling, fd caching) without owning mutable state.
4. All tool calls — including NonMutating — pass through an approval/admission gate.
5. Plugin capabilities are declared in the manifest; the host enforces them at the wrapper level, not by trusting the plugin.

### Non-Goals
- Removing built-in tools *today*. The migration is phased; native and plugin tools coexist during transition.
- Changing the `tools.Registry` wire contract (`ToolDef` schema, `agent.ToolResultBlock`). Those stay stable.
- Requiring plugins to handle multiple tools. One plugin = one tool is fine; multi-tool plugins are fine too.
- Making plugins responsible for their own sandboxing. Host enforces; plugins declare.

---

## 4. Architecture

### 4.1. Component map (after RFC)

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

### 4.2. Host-import surface (thin wrappers)

Every wrapper is a host function installed once per wasm runtime. The wrapper is native Go code that enforces capability checks *before* calling the real implementation.

| Host import | Capability in manifest | What it wraps | Check |
|-------------|------------------------|---------------|-------|
| `stado_http_get(url_ptr, url_len, ...)` | `net:http_get` | `http.Client.Do(GET)` on a shared `*http.Client` | none by default; future OPA hook |
| `stado_http_post(...)` | `net:http_post` | `http.Client.Do(POST)` | same |
| `stado_fs_read(path_ptr, ...)` | `fs:read` | `os.ReadFile` | Path must be inside an allowed prefix (landlock) |
| `stado_fs_write(...)` | `fs:write` | `os.WriteFile` | Path prefix + **approval gate** |
| `stado_shell(cmd_ptr, ...)` | `exec:shallow_bash` | `bash` tool executor (async) | **approval gate** |
| `stado_llm_invoke(...)` | `llm:invoke:N` | `Provider.StreamTurn` | Budget counter preflight |
| `stado_git_commit(...)` | `git:commit` | `gitSession.CommitTurn` | **approval gate** |
| `stado_git_diff(...)` | `git:diff` | `gitSession.Diff` | Read-only, no gate |

**Key principle:** The wrapper owns the resource (connection pool, fd, git session), the plugin borrows it. The plugin cannot see mutable host state like caches directly — only through the wrapper's API.

### 4.3. Error propagation

Plugin tools return a flat JSON string on stdout. The host parses it into `agent.ToolResultBlock`:

```json
{
  "content": "The result text",
  "error": "",          // empty on success
  "metadata": {          // optional structured info
    "http_status": 200,
    "bytes_read": 4096
  }
}
```

If `error` is non-empty, the host wraps it as `agent.ToolResultBlock{Error: err}` and the agent loop handles it exactly like a native tool error.

### 4.4. Approval gate

All tool calls — `Mutating` **and** `NonMutating` — pass through an approval gate. The gate decides based on:

1. **Tool class** from the manifest (new field `ToolDef.Class`)
2. **Capability scope** (e.g. `fs:read` with `allowed_prefixes=["/tmp","."]`)
3. **Call-site context** (the actual path, URL, command string)
4. **Policy** — currently a hardcoded allowlist; future OPA WASM policy or sidecar

The gate can be configured at three levels (precedence: session > project > global):
- Global: `~/.config/stado/policy.toml`
- Project: `.stado/policy.toml`
- Session: runtime `--policy` flag or `!policy` system prompt directive

Example policy rule (TOML):
```toml
[[rule]]
tool = "read"
action = "approve"  # or "deny", "prompt"
condition = "path =~ '^\\.\\./|/etc/|/proc/'"
```

### 4.5. Plugin manifest additions

```json
{
  "tools": [
    {
      "name": "read",
      "description": "Read a file",
      "class": "NonMutating",
      "capabilities": ["fs:read"],
      "schema": "..."
    }
  ],
  "capabilities": ["fs:read", "net:http_get"],
  "allowed_prefixes": ["/tmp", "."]
}
```

- `class`: `"NonMutating"` | `"Mutating"` | `"Exec"` — determines approval behavior
- `capabilities`: same as today; controls which host imports are available
- `allowed_prefixes`: optional stricter scope than the raw capability

---

## 5. Registry Precedence

The `tools.Registry` gains a new concept: **plugin override**.

```go
// At init/override time
reg.Override("web_fetch", pluginTool{
    manifest: webManifest,
    wasmPath: "...",
    runtime:  rt,
})

// reg.All() returns:
//   built-in tools for which no override exists
//   plugin tools for which an override exists
//   sorted by name
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

---

## 6. Performance Model

| Metric | Native tool | Plugin tool | Delta |
|--------|-------------|-------------|-------|
| Cold start (first call) | 0 | 100-300ms | one-time |
| Hot-call overhead | 0 | 5-15ms | per call |
| HTTP call (with pooling) | 50-500ms | 55-515ms | negligible |
| File read (small) | 0.1ms | 5-15ms | acceptable |
| `bash sleep 30` | 30,000ms | 30,005ms | negligible |

User acceptance criteria: **<1sec total delta per tool-heavy turn**. With ~10 tool calls per turn, worst-case delta is ~150ms. Well inside the threshold.

---

## 7. Migration Plan (Phased)

| Phase | Scope | Effort | Deliverable |
|-------|-------|--------|-------------|
| 7.1 | Design sign-off + `ToolDef.Class` in manifest | 2 days | Merged RFC + manifest schema |
| 7.2 | `--override-tool` flag + registry wiring | 3 days | PR with tests |
| 7.3 | First NonMutating override: `web_fetch` → demo plugin | 2 days | Working demo + latency numbers |
| 7.4 | Add `stado_http_*` host imports | 3 days | PR with pooled client |
| 7.5 | First Mutating override: `write` + approval gate | 4 days | PR + UX review |
| 7.6 | Bulk tool migration (read, ripgrep, ls, diff) | 1-2 weeks | All built-ins have plugin equivalents |
| 7.7 | Remove native tool implementations (keep wrappers) | 3 days | "Native" tools become vendored plugins |
| 7.8 | Policy/admission control (OPA integration) | 2-3 weeks | Optional OPA sidecar or WASM policy engine |

---

## 8. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| WASM cold-start latency | Medium | Cache runtime + module across calls; ship AOT-compiled plugins |
| Plugin replaces core tool, breaks chaining | High | Integration tests for every override; keep native fallback for 1 release |
| Approval gate UX is annoying | Medium | Start as opt-in (`--strict-approvals`); make policy rules expressive |
| Trust store TOCTOU gets worse (every plugin install) | High | Fix TOCTOU first (ongoing bug); add advisory file lock (`flock`) |
| Plugin ecosystem fragmentation | Low | Registry namespace (`hub.stado.dev/tools/<name>`); semver enforcement |

---

## 9. Alternatives Considered

### 9.1. Just expose tool overrides as Go interfaces
Keep native Go, but allow users to supply a Go function that wraps the built-in one. Rejected: requires recompiling stado; loses sandboxing; no audit trail.

### 9.2. gRPC sidecars instead of WASM
Each tool is a separate process talking gRPC. Rejected: heavier than WASM (process startup ~50-100ms vs module instantiation ~5ms); harder to distribute; no built-in capability boundary.

### 9.3. OPA admission control without plugin migration
Keep native tools but add an OPA check before every call. Rejected: still can't replace the tool implementation; admin is stuck with whatever scrubbing/logging the native tool does (or doesn't do).

---

## 10. Open Questions

1. **Should plugins own their own `http.Client` pool, or share the host's?** Sharing is faster but harder to isolate for per-plugin rate-limiting.
2. **How do we version-host-mappings?** If `stado_fs_read` gains a new parameter in v1.1, old plugins must still run. Version the host import namespace?
3. **Plugin manifests for native tools that are *not* overridden** — do we generate a synthetic manifest so the registry is uniform?
4. **Approval gate for NonMutating calls** — inline overlay, sidebar notification, or require `:approve`? The latter breaks agent autonomy for non-interactive surfaces (headless, run --prompt).
5. **`stado plugin install` from URL** — how does the user discover and install a tool plugin? Same as today (manual install), or add a registry index?

---

## 11. Discussion Log

| Date | Who | What |
|------|-----|------|
| 2026-04-22 | @foobarto | Initial proposal; user accepts <1sec perf hit; approval gate for ALL calls |

---

**Next step:** Discuss and resolve open questions, then vote to accept/revise/defer.
