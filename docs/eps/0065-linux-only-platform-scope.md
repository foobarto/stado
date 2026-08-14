---
ep: 65
title: Linux-Only Platform Scope
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-14
requires: ["EP-0012", "EP-0050"]
extends: ["EP-0028", "EP-0037", "EP-0038"]
supersedes: ["EP-0005"]
see-also: ["EP-0006", "EP-0010"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Accepted by operator decision during the complete EP and platform-architecture review.
---

> **Relationships:** **Requires:** [EP-0012](./0012-release-integrity-and-distribution.md), [EP-0050](./0050-broker.md) · **Extends:** [EP-0028](./0028-plugin-run-tool-host.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md) · **Supersedes:** [EP-0005](./0005-capability-based-sandboxing.md) · **See also:** [EP-0006](./0006-signed-wasm-plugin-runtime.md), [EP-0010](./0010-interop-surfaces-mcp-acp-headless.md)

# EP-0065: Linux-Only Platform Scope

## Problem

EP-5 combined two decisions: a capability-based containment model and a
cross-platform product posture that shipped Linux and macOS enforcement while
keeping an explicitly degraded Windows path. The capability decision remains
load-bearing. The platform commitment no longer serves the project.

Stado's security architecture depends on Linux primitives: namespaces,
bubblewrap, Landlock, seccomp, Unix-domain broker IPC, and a Linux-specific
mount model. Treating other operating systems as parallel product targets adds
conditional code, release work, documentation qualifications, and weaker
fallback semantics. It makes the core harder to reason about without advancing
the Linux system that current and v1 work actually target.

## Goals

- Make Linux the only supported platform now and for v1.
- Retain the shared capability vocabulary and intersection-only policy model.
- Optimize the broker, sandbox, plugin runtime, release pipeline, and tests for
  the strongest coherent Linux architecture.
- Remove macOS and Windows roadmap promises and stop treating their existing
  code paths as compatibility constraints.
- State unsupported-platform behavior honestly wherever old artifacts remain.

## Non-goals

- Maintaining feature, security, packaging, or test parity on macOS or Windows.
- Preserving conditional implementations merely because they once shipped.
- Designing every native primitive behind a lowest-common-denominator OS
  abstraction.
- Preventing a future EP from proposing another platform with an independently
  complete threat model, maintainer, and enforcement plan.

## Design

### Support contract

Linux is the sole supported build and runtime target for the current project and
the v1 release. Release artifacts, CI gates, installation guidance, threat-model
claims, and operator documentation are Linux-scoped.

Existing Darwin or Windows files may remain temporarily while the pre-v1
cleanup proceeds. Their presence is not a support promise. They may be deleted
without a compatibility period, and they must not force a weaker interface or
block a Linux-specific improvement.

### Capability and containment model

EP-5's central decision survives unchanged: permissions are explicit
capabilities, policy composition is an intersection, and enforcement belongs in
the runtime or kernel rather than prompt text. Linux uses layered controls
because filesystem, process, syscall, credential, and network isolation are
different problems:

- the broker admits requests and projects immutable ceilings;
- bubblewrap and namespaces construct the process and mount boundary;
- Landlock provides irreversible filesystem narrowing where applicable;
- seccomp reduces the syscall surface;
- network namespaces provide the egress floor, with a typed allowlist proxy as
  a refinement;
- WASM capabilities gate host imports independently of the process sandbox.

The explicit `--no-sandbox` operator override remains distinct from supported
sandboxed execution. An unsandboxed run must identify itself as such and cannot
be described as receiving the v1 containment posture.

### Product and release consequences

The release matrix publishes Linux artifacts only. CI may retain cross-compile
checks briefly when they catch portable Go mistakes at negligible cost, but
such checks are not acceptance gates for a supported product and must not
preserve platform-specific runtime code.

Documentation should say “Linux” rather than enumerate a hierarchy of Linux,
macOS, and degraded Windows behavior. Backlog items for macOS or Windows are
removed rather than parked. Reintroducing either is a new product and security
decision, not routine portability maintenance.

## Migration / rollout

1. Rewrite `DESIGN.md` and `PLAN.md` around the Linux-only contract.
2. Remove macOS and Windows goals from README, threat model, feature docs,
   release workflows, and packaging metadata.
3. Audit platform build tags and abstractions; delete code with no Linux use and
   simplify interfaces where portability was their only purpose.
4. Make the Linux validation matrix the complete release gate before v1.

## Failure modes

- Documentation still advertises another platform: treat as release-blocking
  contract drift.
- A portability wrapper weakens or obscures Linux enforcement: remove or split
  it; Linux semantics win.
- Someone runs an old unsupported binary on another OS: it carries no current
  support or security guarantee even if it happens to start.
- A future platform proposal lacks equivalent enforcement or ownership: reject
  it rather than restoring a warning-only product path.

## Test strategy

- Documentation checks reject new macOS/Windows support or roadmap claims in
  current top-level product documents.
- Linux integration tests exercise the actual broker, mount, namespace,
  Landlock, seccomp, network, and WASM-capability intersections.
- Release tests verify that advertised artifacts and checksums match the
  Linux-only matrix.
- Architecture review confirms platform shims are not controlling native
  primitive design.

## Decision log

### D1. Linux is the only supported platform through v1

- **Decided:** current development and v1 support Linux only.
- **Alternatives:** keep macOS support and warning-only Windows; promise later
  parity; maintain compile-only portability.
- **Why:** stado's containment architecture is Linux-native. Concentrating
  engineering and claims on one coherent platform produces a smaller core and
  a more honest, testable security boundary.

### D2. Existing platform code is not compatibility surface

- **Decided:** unsupported platform remnants may be removed pre-v1 without a
  deprecation path.
- **Alternatives:** freeze them indefinitely; require every refactor to preserve
  compilation; keep them as implicit future promises.
- **Why:** source-tree existence must not silently recreate a product
  commitment the project explicitly dropped.

### D3. Capability policy remains shared across Linux surfaces

- **Decided:** tools, plugins, MCP children, lifecycle applications, and broker
  sessions continue to use explicit capabilities and narrowing composition.
- **Alternatives:** discard EP-5 wholesale; replace the shared policy with
  feature-specific checks.
- **Why:** platform scope changed, not the core containment principle.

## Related

- [Architecture](../../DESIGN.md)
- [Roadmap](../../PLAN.md)
- [Threat model](../security/threatmodel.md)
