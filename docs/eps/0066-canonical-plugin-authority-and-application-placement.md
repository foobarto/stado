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
  - date: 2026-08-15
    status: Accepted
    note: >
      The Go-side host substrate and its capability-gated WASM bindings are
      named stado.kernel. This is an architectural/source name only; existing
      stado_* wire imports and schemas remain unchanged.
  - date: 2026-08-14
    status: Accepted
    note: EP-60 guidance completed the placement correction: native Go now projects only opaque-binding session facts and the live registry ceiling, while the official TUI lifecycle application owns classifiers, thresholds, ordering, and wording.
  - date: 2026-08-14
    status: Accepted
    note: >
      Model-invocable skills now follow the same boundary. Native stado projects
      only operation/kind-scoped, digest-fenced context facts and exact trust/
      tool ceilings; the explicit official skills WASM package owns search,
      open, formatting, and activation requests. Native prompt and role
      interception paths are deleted.
  - date: 2026-08-14
    status: Accepted
    note: >
      Provider invocation now follows the placement boundary end to end. Native
      stado retains only authenticated provider construction, credentials,
      cancellation, exact token enforcement/accounting, and bounded facts. The
      MCP-only native llm.invoke application and persona flag are deleted; an
      explicit-opt-in official WASM source owns request and output policy.
  - date: 2026-08-14
    status: Accepted
    note: >
      Registry discovery now demonstrates the same placement boundary without a
      native tool-shaped bridge: Go exposes only exact bounded manifest facts
      and digest-fenced atomic session-surface mutation, while an official
      opt-in WASM package owns all discovery and activation workflow.
  - date: 2026-08-14
    status: Accepted
    note: The supervise correction now demonstrates this boundary end to end: official application policy and evaluator source moved to stado-plugins, while native stado retains only generic lifecycle facts and enforcement. Other model-visible native applications remain migration debt.
  - date: 2026-08-14
    status: Accepted
    note: >
      Accepted during the PR-257 corrective audit to make canonical identity
      and the native-primitive/WASM-application boundary uniform across every
      runtime and authority surface.
---

> **Relationships:** **Requires:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0039](./0039-plugin-distribution-and-trust.md), [EP-0050](./0050-broker.md) · **Extends:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0060](./0060-native-harness-guidance.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md), [EP-0064](./0064-wasm-lifecycle-applications.md) · **Extended by:** [EP-0067](./0067-session-controller-and-application-selection.md) · **See also:** [EP-0010](./0010-interop-surfaces-mcp-acp-headless.md), [EP-0017](./0017-tool-surface-policy-and-plugin-approval-ui.md), [EP-0028](./0028-plugin-run-tool-host.md), [EP-0045](./0045-model-invocable-skills.md)

# EP-0066: Canonical Plugin Authority and Application Placement

> **Implementation status (2026-08-14):** Canonical identity propagation and
> every stado-owned native model-application cutover are complete in source;
> the native registration debt allowlist is empty. The EP remains Accepted
> until the staged official packages close their own signing, publication,
> conformance, and authority-presenter gates.

## Problem

The EP-2/37/38 plugin boundary and EP-39 identity model are sound, but the
implementation applied them unevenly. `Manifest.Name` remained both a display
alias and a durable namespace for secrets and instance state. Some production
paths constructed a local identity for an installed or bundled executable.
Native registry, skills, research, tasks, guidance, supervise, and provider-tool code had
remained model-visible applications while top-level design claimed that
stado-owned model tools were WASM. The supervise path, registry
discovery/activation tools, model-invocable skill application, and native
provider-facing `llm.invoke` application are now removed from native stado.
Guidance policy and every native composition callback are removed too. Research
and tasks have now completed the same placement correction in source; their
official package publication remains separately gated.

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

Within the Go implementation, **stado.kernel** names that host-owned substrate:
the capability-gated Go-to-WASM bindings, broker-bound fact projection and
effects, resource ceilings, and Linux enforcement beneath signed applications.
It is not a new WASM module, package identity, capability namespace, or wire
version. Existing `stado_*` imports and their schemas keep their current names;
“kernel” describes the Go-side architectural role.

Tasks completed its placement cutover: the explicit official lifecycle
application owns the dynamic command, ordinary-session model tool, and global
plugin-defined artifact workflow. Native stado retains only generic signed
application projection, broker artifacts, bounded filesystem migration
primitives, and operator choice transport. There is no JSON writer or fallback.
Research is a completed placement cutover: its two outer workflows and six
child-only helpers live in the official WASM source, while native Stado retains
only exact-tool broker bindings, authenticated evidence scope, immutable ranges,
durable bounds/receipts, ordinary child admission, and mechanical citation
integrity.
Supervise is the completed placement cutover: its workflow and tools live only
in the official plugin source, with publication separately gated. Tasks has now
completed the last stado-owned native model-tool cutover, so the architecture
guard's native registration debt allowlist is empty.

EP-60's placement correction is complete in source. Native code computes
authenticated bounded facts and availability; the explicit TUI lifecycle
application owns learn/research/coordination wording, thresholds,
recommendation choices, and composition policy. Its manifest artifact remains
unsigned until the normal release-key ceremony.

Registry discovery is another completed cutover. Native code projects exact
manifest/runtime facts (`name`, deterministic dotted `canonical`, signed
display `plugin`, description, schema, class, categories, and the stable
unversioned source namespace), excludes the caller and every persistent
lifecycle application, and enforces the config/session ceiling. It exposes no
search score, grouping, summary, or activation recommendation. A complete
projection digest fences pagination and an atomic surface edit; the official
WASM package owns the eight `tools.*`/`plugin.*` workflows. Display names and
versioned canonical identities never select mutation authority.

Model-invocable skills are a third completed cutover. Native code parses the
operator/project skill format and projects bounded facts through generic
`context:resource:catalog:<kind>` and `context:resource:open:<kind>`
capabilities. It mechanically enforces model visibility, project fail-closed
tool declarations, persona provenance, opaque immutable IDs, content and
catalog digests, and the exact session ceiling. The explicit official WASM
package owns search/matching/formatting/open workflow and uses the same generic
registry surface edit as any application. Opened content remains an ordinary
tool result. Native system-prompt listings, exact-name result interception, and
synthetic user-role injection are forbidden. Explicit operator `/skill` and
`--skill` gestures remain native because their initiator provenance is true.
Ordinary multi-tool packages declare a signed top-level capability union and
must attenuate each signed tool to a presence-preserving exact subset (`[]`
means zero; omission is rejected). Admission rejects duplicates or authority
absent from the package, and installed/override/bundled/nested dispatch uses the
selected subset for the Host and risk class without changing the
complete-manifest identity or signature binding. Persistent lifecycle
applications must omit per-tool subsets because callbacks and tools share one
long-lived module and package-wide Host; stado does not advertise attenuation
it cannot enforce.

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
