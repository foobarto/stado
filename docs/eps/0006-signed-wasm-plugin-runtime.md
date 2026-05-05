---
ep: 6
title: Signed WASM Plugin Runtime
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [2, 5, 10, 11, 12, 39]
extended-by: [39]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped plugin authoring, trust, override, and background-runtime surfaces around v0.0.1 to v0.1.0.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Signed manifests, trust pinning, runtime verification, and session-aware plugins are the shipped extension model.
---

# EP-6: Signed WASM Plugin Runtime

## Problem

stado needs a way to extend or replace runtime behavior without asking
users to fork and recompile the host. That extension surface has to be
auditable, capability-bounded, and explicit about trust, otherwise a
"plugin system" just becomes unsigned arbitrary code execution.

The project also needs one runtime that works for simple tool plugins,
bundled-tool overrides, and long-lived session-aware plugins.

## Goals

- Run third-party extensions in a narrow, capability-gated WASM host.
- Make trust decisions explicit through signer pinning and verification.
- Support both one-shot tool execution and persistent background
  plugins.
- Reuse the same runtime for shipped examples, external plugins, and
  bundled-tool overrides.

## Non-goals

- Automatic plugin discovery or a central registry.
- A native dynamic-library plugin model.
- Granting plugins raw process or repository access outside the declared
  host imports.

## Design

Plugins are directories containing a WASM module, a JSON manifest, and a
manifest signature. Verification is layered and shipped:

- manifest signature validation
- WASM digest match against the manifest
- rollback protection keyed by signer plus `name` and `version`
- optional CRL verification
- optional Rekor inclusion checks

Trust pinning is explicit. Users opt into signer trust with
`stado plugin trust`, and install or run flows verify against that local
trust store instead of treating download as trust.

Execution happens inside wazero. The host-import model is narrow by
design: plugins receive only the capability-gated imports required for
filesystem, networking, session, approval, logging, or LLM access.
Plugins do not receive raw syscalls or implicit repository access.

The shipped runtime supports three plugin modes:

- tool plugins executed directly
- tool overrides selected by name
- background or session-aware plugins bound to runtime events

Tool overrides and background modes are part of the same runtime rather
than separate extension systems. Session-aware plugins use explicit
capabilities such as `session:read`, `session:observe`, `session:fork`,
and `llm:invoke`, which keeps extension power visible in the manifest
and auditable in the runtime.

## Decision log

### D1. Use signed manifests plus an explicit trust store

- **Decided:** manifest signatures plus explicit signer pinning are the
  trust contract.
- **Alternatives:** unsigned plugins, blind TOFU by default, or signing
  only the WASM blob.
- **Why:** the manifest is where capabilities and digests live, so that
  envelope needs the signature. Explicit trust makes the security
  boundary visible to the user.

### D2. Layer verification instead of relying on one check

- **Decided:** verification includes signature, digest, rollback, and
  optional CRL/Rekor checks.
- **Alternatives:** signature-only or digest-only verification.
- **Why:** each layer catches a different class of failure or attack, and
  the runtime should not depend on one brittle source of trust.

### D3. Expose narrow host imports instead of raw host access

- **Decided:** wazero host imports are the only host access path.
- **Alternatives:** give plugins direct filesystem, network, or git
  access and rely on convention.
- **Why:** narrow imports keep the host authoritative over policy,
  resource sharing, and audit boundaries.

### D4. Support session-aware and background plugins in the same runtime

- **Decided:** one runtime supports tools, overrides, and
  background/session-aware plugins through explicit capabilities.
- **Alternatives:** separate plugin systems for tools and session
  automation.
- **Why:** one runtime keeps the extension model coherent and makes
  advanced behaviors like auto-compaction or session recording auditable
  through the same trust pipeline.

## Related

- [EP-2: All Tools as WASM Plugins](./0002-all-tools-as-plugins.md)
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [EP-12: Release Integrity and Distribution](./0012-release-integrity-and-distribution.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [docs/commands/plugin.md](../commands/plugin.md)
- [SECURITY.md](../../SECURITY.md)
