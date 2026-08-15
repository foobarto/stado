---
ep: 2
title: All Tools as WASM Plugins
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-04-22
implemented-in: v0.80.0
extended-by: ["EP-0038", "EP-0066"]
see-also: ["EP-0005", "EP-0006", "EP-0037", "EP-0038"]
history:
  - date: 2026-08-14
    status: Implemented
    version: v0.80.0
    note: >
      C60 removed the final native stado-owned model tool. Tasks now live in an
      explicit official WASM lifecycle application backed by plugin-defined
      global broker artifacts; the JSON writer, tool, picker, key, static
      command, MCP registration, and default autoload were deleted. The native
      model-registration debt allowlist is empty. Signing/publication remains
      release work and does not restore a fallback.
  - date: 2026-08-14
    status: Partial
    note: >
      Native model-invocable skill registration, prompt listing, exact-name
      handlers, and synthetic user-role injection were deleted. Generic
      operation/kind-scoped context-resource facts retain native visibility,
      provenance, and ceiling enforcement; official opt-in WASM owns search,
      open, formatting, and activation requests. Native research/tasks/guidance
      keep the EP Partial.
  - date: 2026-08-14
    status: Partial
    note: >
      Native llm.invoke, its MCP registration/persona option, and the policy-
      bearing host ABI were deleted. Exact provider:invoke:<tokens> now gates
      only native provider construction, credentials, cancellation, accounting,
      and bounded facts; official opt-in WASM owns the model-facing contract.
      Native skills/research/tasks/guidance work then kept the EP Partial; the
      later skill correction above shrank that list again.
  - date: 2026-08-14
    status: Partial
    note: >
      The eight native registry discovery/activation tools, their native types,
      direct CLI bypass, and exact-name result interceptors were deleted. A
      bounded loader-bound catalog projection and atomic session-surface edit
      are generic host primitives; the official opt-in tool-registry WASM
      package owns search, formatting, grouping, and activation workflow.
      Native skills/research/tasks/guidance work keeps the EP Partial.
  - date: 2026-08-14
    status: Partial
    note: >
      Native supervise commands, model tools, workflow policy, and evaluator
      were removed in favor of the official stado-plugins lifecycle
      application. The EP remains Partial for native skills/research/tasks,
      guidance, and provider-tool surfaces.
  - date: 2026-08-14
    status: Partial
    note: >
      Bundled core tools now load exclusively from verified source-adjacent
      embedded manifests. The generic loader is the sole owner of model name,
      description, schema, class, categories, capabilities, and ABI export;
      native fallback switches and duplicated Go metadata were deleted. The EP
      remains Partial for the separately tracked native meta, research, task,
      guidance, and llm.invoke surfaces.
  - date: 2026-08-14
    status: Partial
    note: >
      Corrected a false Implemented marker during the PR-257 architecture audit.
      Stado still exposes native registry meta-tools, skills/research/tasks, and
      guidance/model-invocation policy. The shrinking native-surface guard
      defines the remaining debt; this EP returns to Implemented only when it
      reaches zero.
  - date: 2026-04-22
    status: Draft
    note: Initial draft — converted from brainstorming session on tools-to-plugins migration.
  - date: 2026-04-23
    status: Accepted
    note: Accepted retroactively once the bundled tool migration and override surface were the shipped default.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Bundled tools now load through the built-in plugin runtime; approval variants ship as plugins.
  - date: 2026-05-05
    status: Partial
    note: >
      Status reduced from Implemented to Partial. The visible tool surface is plugin-shaped via
      newBundledPluginTool wrappers, but the implementation behind a tool name is still native Go
      (internal/runtime/bundled_plugin_tools.go directly registers fs.ReadTool{}, bash.BashTool{},
      etc.). The invariant in §Design that "the implementation behind a tool name is a plugin
      module running in the wazero host" is not currently true. EP-0038 restores the invariant by
      moving every native tool to a wasm plugin and deleting the wrapper layer.
  - date: 2026-05-05
    status: Implemented
    note: >
      EP-0038 introduced real wasm modules for fs/shell/rg/readctx/agent and a
      temporary per-family parity rollout. That transitional dual path has since
      been deleted; the 2026-08-14 entry above records the current architecture.
---

> **Relationships:** **Extended by:** [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md) · **See also:** [EP-0005](./0005-capability-based-sandboxing.md), [EP-0006](./0006-signed-wasm-plugin-runtime.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md)

# EP-2: All Tools as WASM Plugins

## Problem

The runtime needs one extension model for shipped tools, operator
overrides, and approval-aware variants. If bundled tools lived outside
the plugin system, stado would have two different trust and execution
models for the same tool surface, and users could not replace a shipped
tool with a policy-specific implementation by name.

## Goals

- Keep one runtime contract for bundled tools and third-party tools.
- Support override-by-name without recompiling stado.
- Keep approval-aware behavior inside the plugin model instead of
  special-casing native tools.
- Enforce plugin-declared capabilities at the host-import boundary.

## Non-goals

- Changing the tool registry wire contract.
- Requiring one plugin per tool.
- Delegating sandbox enforcement to plugin code.
- Treating shipped tools as a privileged exception to the plugin model.

## Design

The shipped runtime keeps three invariants:

- bundled tools are loaded through the plugin runtime
- override-by-name is a supported contract
- approval variants remain plugins, not hidden native exceptions

The registry surface stays stable. Tools still present `ToolDef`
metadata and return the same result blocks to the agent loop, but the
implementation behind a tool name is a plugin module running in the
wazero host.

Bundled tools and downloaded third-party plugins share the same runtime
and registry surface, but not the same trust-install flow. Bundled tools
ship with stado and load through the built-in plugin path; downloaded
plugins go through the external manifest-signature, trust-store, and
verification workflow described in EP-6.

Override resolution is name-based. A configured override replaces the
bundled implementation for that tool name without changing the agent
contract or the surrounding approval flow:

```toml
[tools.overrides]
# Keys are canonical (dotted) tool names. The pre-EP-0038 bare names
# (webfetch, read) are gone — using them here would not match any tool.
web.fetch = "my-patched-fetch@v1.0.0"
fs.read = "my-read@v2.1.0"
```

Host imports remain the execution boundary. Plugins declare capabilities
in the manifest, and the host grants only the corresponding imports for
filesystem, network, session, logging, LLM, and approval-aware
operations. Plugins do not own sandboxing or raw host access; they
invoke narrow wrappers that the runtime can audit and constrain.

Approval-aware variants such as write-like or exec-like tools stay
inside the plugin system. The runtime does not bypass the manifest or
trust model for "special" mutating behavior. That keeps the approval
surface explicit in plugin metadata and consistent with the rest of the
tool registry.

## Decision log

### D1. All tools as plugins (not just extensible)

- **Decided:** bundled tools load through the built-in plugin runtime;
  native Go code provides host services, not hidden tool
  implementations.
- **Alternatives:** keep a split between built-in native tools and
  optional plugin tools.
- **Why:** one tool model keeps trust, overrides, and auditing coherent.

### D2. Security > performance

- **Decided:** the plugin runtime is the default execution path even for
  bundled tools.
- **Alternatives:** keep latency-sensitive tools outside the plugin
  runtime.
- **Why:** stado optimizes for a uniform trust boundary and override
  surface, not for preserving separate native fast paths.

### D3. Thin wrappers for shared resources

- **Decided:** the host exposes narrow imports for shared services and
  enforces capability checks there.
- **Alternatives:** grant plugins direct syscalls or raw runtime access.
- **Why:** the host stays authoritative for sandboxing, pooling, and
  audit boundaries.

### D4. Keep approval-aware tool behavior inside the plugin model

- **Decided:** approval-aware behavior stays in the plugin contract
  rather than reappearing as hidden native exceptions.
- **Alternatives:** special-case mutating or exec tools outside the
  plugin model.
- **Why:** the registry, trust model, and audit flow remain uniform only
  if approval variants are still plugins.

## Related

- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- DESIGN.md §Tools
- PLAN.md Phase 8 (plugin ecosystem)
