---
ep: 66
title: Canonical Plugin Authority and Application Placement
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-14
requires: ["EP-0002", "EP-0037", "EP-0038", "EP-0039", "EP-0050"]
extends: ["EP-0002", "EP-0037", "EP-0038", "EP-0060", "EP-0063", "EP-0064"]
extended-by: ["EP-0067"]
see-also: ["EP-0010", "EP-0017", "EP-0028", "EP-0045"]
history:
  - date: 2026-08-14
    status: Accepted
    note: >
      Accepted during the PR-257 corrective audit to make canonical identity
      and the native-primitive/WASM-application boundary uniform across every
      runtime and authority surface.
---

> **Relationships:** **Requires:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0039](./0039-plugin-distribution-and-trust.md), [EP-0050](./0050-broker.md) · **Extends:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0060](./0060-native-harness-guidance.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md), [EP-0064](./0064-wasm-lifecycle-applications.md) · **Extended by:** [EP-0067](./0067-session-controller-and-application-selection.md) · **See also:** [EP-0010](./0010-interop-surfaces-mcp-acp-headless.md), [EP-0017](./0017-tool-surface-policy-and-plugin-approval-ui.md), [EP-0028](./0028-plugin-run-tool-host.md), [EP-0045](./0045-model-invocable-skills.md)

# EP-0066: Canonical Plugin Authority and Application Placement

## Problem

The EP-2/37/38 plugin boundary and EP-39 identity model are sound, but the
implementation applied them unevenly. `Manifest.Name` remained both a display
alias and a durable namespace for secrets and instance state. Some production
paths constructed a local identity for an installed or bundled executable.
Native registry, skills, research, tasks, guidance, and supervise code remained
model-visible applications while top-level design claimed that stado-owned
model tools were WASM.

Those are not isolated compatibility bugs. A display-name collision can cross
authority namespaces, and a native application can bypass exactly the plugin
sandbox and innovation boundary the architecture was designed to provide.

## Decisions

### One runtime identity

Every production plugin instance receives a loader-authenticated
`RuntimeIdentity` before host imports are installed. The identity is:

- the exact EP-39 canonical source/version and resolved commit for installed
  plugins;
- a release-bound `stado.dev/bundled/...` identity for executable content
  reproducibly embedded in a stado build;
- an explicitly unstable, canonical-source-path-derived identity for local
  development.

Local identity is not derived from `Manifest.Name` alone. Two unrelated source
directories using the same display name never share a namespace. A production
path may not silently fall back from missing installed/bundled identity to a
local identity; it fails closed.

`Manifest.Name` remains presentation metadata only. Canonical identity or its
stable unversioned namespace owns secrets, process/session instance state,
artifact kinds and descriptors, lifecycle application instances and event
cursors, broker admission bindings, idempotency namespaces, and audit
attribution.

Changing an alias does not move authority. Changing source identity does not
inherit authority without an explicit broker/operator migration.

### Broker admission is scoped authority, not authorship proof

For authority-bearing imports, the broker independently verifies installed
package bytes, pinned signature, lock identity, capabilities, active session
generation, ceiling, and policy. It mints an opaque scoped binding and resolves
authority fields from that binding on every call. WASM never receives it and
guest JSON cannot widen it.

Because EP-50 treats the orchestrator/plugin host as hostile, such a binding is
not a cryptographic claim that prose was authored by an uncompromised runtime.
It proves only that the request stayed within broker-admitted identity,
capability, scope, and generation. Generated artifacts remain candidates;
operator grants and independent evaluators retain their own authority.

### Native primitives, WASM applications

All stado-owned model-visible product behavior is implemented in WASM. Native
Go may expose generic primitives that WASM cannot obtain from its garden:
filesystem/process/network operations, provider invocation, deterministic
session facts, broker state transitions, operator UI transport, durable event
delivery, and sandbox enforcement.

The current native registry meta-tools, skills loader, research dispatchers,
tasks tool, guidance policy, and supervise tools are migration debt, not
permanent exceptions. A shrinking architecture guard enumerates them until the
allowlist is empty.

EP-60 remains correct about which deterministic signals are useful. This EP
replaces its implementation placement: native code may compute authenticated
facts and availability, while learn/research/coordination wording, thresholds,
recommendation choices, and composition policy move to a WASM lifecycle
application.

EP-10 external MCP tools are not stado-owned native applications. They are
typed adapters to tools whose implementation and authority live in an external
operator-enabled server. The adapter must still apply ordinary broker,
sandbox, taint, audit, schema, and tool-surface policy; “external” is not a
privilege exemption.

## Migration

1. Carry exact runtime identity through installed, bundled, override,
   background, TUI, headless, ACP, MCP, and direct tool-run paths.
2. Move secrets, instance state, session bridges, progress/audit attribution,
   artifacts, and lifecycle keys off display aliases.
3. Make local development identity source-path-derived and reject implicit
   local fallback in production dispatch.
4. Move each stado-owned native model tool/application to an official signed
   WASM package, adding generic host primitives only where the garden lacks a
   capability.
5. Delete native registrations, dispatch interceptions, aliases, and fallback
   writers as each migration lands; shrink the guard to zero.

This is a pre-v1 one-way correction. Durable data gets an explicit auditable
namespace migration where necessary; obsolete identity and application paths
receive no compatibility wrapper.

## Required tests

- two plugins with the same display name cannot share secrets, state,
  artifacts, lifecycle state, or broker bindings;
- every production host constructor rejects missing/mismatched canonical
  identity;
- broker admission rejects self-consistent but uninstalled identity/manifest
  pairs and expires on session generation change;
- aliases can change without changing authority namespace;
- the native model-surface allowlist cannot grow and reaches zero before v1;
- external MCP adapters remain tainted, policy-bounded integration endpoints.
