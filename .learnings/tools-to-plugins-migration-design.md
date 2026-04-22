# Tools-to-Plugins Migration — Design Notes

## Triggered by user request: move ALL tools from native Go to WASM plugins.

## Rationale (from user)
- Security-first approach: the only trusted code is the host
- Users can replace any tool with audited implementations (e.g., web search with sensitive-data scrubbing, custom auth)
- Tools gain certifiability and pluggability; cost is performance overhead
- Acceptable cost: <1sec overhead per call (user explicitly OK)
- Approval gate should apply to ALL tool calls, including NonMutating (e.g. read sensitive files, web search with auth)

## Core Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  stado core (trusted)                                       │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────┐   │
│  │ tools.       │  │ http.Client  │  │ os.ReadFile/    │   │
│  │ Registry     │  │ (pooled)     │  │ WriteFile       │   │
│  │              │  └──────▲───────┘  └────────▲────────┘   │
│  │   Plugin     │         │                     │            │
│  │   overrides  │         │ thin wrappers      │            │
│  │   by name    │         │ (host imports)     │            │
│  └──────┬───────┘         │                     │            │
└─────────┼─────────────────┼─────────────────────┼────────────┘
          │                 │                     │
    ┌─────▼──────┐          │                     │
    │   WASM     │──────────┘                     │
    │  plugin    │  stado_http_get()               │
    │            │  stado_http_post()              │
    │            │  stado_fs_read()   ─────────────┘
    │            │  stado_fs_write()  (with approval gate
    └────────────┘   stado_llm_invoke()  if Mutating class)
```

## Key Principles (from user)
1. **Performance**: <1sec overhead per call is acceptable by default; >1sec is "matter of consideration not immediate dismissal"
2. **Security > Performance**: Stado is "all about security, not performance"
3. **Thinnest possible wrappers**: Host exposes pooled resources (http.Client, fs, config) via host imports; plugins don't own mutable cache state, only access it
4. **Approval for ALL calls**: NonMutating also need approval gates — e.g. reading sensitive files, web search with auth tokens
5. **Manifest-driven capabilities**: Every host import a plugin can access must be declared in the manifest; this is what drives audit/approval
6. **OPA-like admission control**: "approval-based admission control" where policy engine checks call against global/project/session config before allowing
7. **Error propagation**: Return JSON from plugins for structured error info; parse on host side for stack traces
8. **Tool chaining**: Wrappers must provide consistent interface/contract so plugins can chain seamlessly

## Phased Approach (proposed)
Phase 1: `--override-tool <name>=<plugin-id>` flag, NonMutating only (1 week)
Phase 2: Add tool class to manifest, wire approval for plugins (2-3 days)
Phase 3: Migrate built-in tools to WASM plugins (4-6 weeks total)
Phase 4: Admission control / OPA-like policy engine (TBD)

## Open Questions
- How to expose `http.Client` connection pooling to plugins (same pool or per-plugin pool?)
- How to handle plugin cold-start time (100-300ms first call) for latency-sensitive tools
- Whether to ship built-in tools as vendored `.wasm` files in the binary or compile on demand
- How structured errors from plugins map to the `agent.ToolResultBlock` type
- Whether to expose git session state to plugins via a wrapper (for tools like `commit`, `diff`)
- Approval gate UX: inline overlay vs. sidebar notification vs. require explicit `:approve`
- OPA integration: wasm policy engine or sidecar?

## Blockers to Resolve First
All P1 safety bugs must be fixed before starting this refactor — the same code paths are involved.
