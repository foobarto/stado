---
ep: 68
title: Signed Plugin CLI Commands
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Draft
type: Standards
created: 2026-08-15
requires: ["EP-0037", "EP-0039", "EP-0066"]
see-also: ["EP-0028", "EP-0064"]
---

> **Relationships:** **Requires:** [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0039](./0039-plugin-distribution-and-trust.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md) · **See also:** [EP-0028](./0028-plugin-run-tool-host.md), [EP-0064](./0064-wasm-lifecycle-applications.md)

# EP-0068: Signed Plugin CLI Commands

> **Draft scope:** capture a possible future operator surface. This EP does not
> change the v0.80 command set or make lifecycle slash commands available from
> the CLI. In particular, the staged memory/learn application remains TUI-only.

## Problem

Signed WASM packages can own model tools and persistent TUI lifecycle
applications, including dynamic slash commands. They cannot currently expose a
non-interactive `stado <command>` workflow. Moving product policy out of Go is
therefore allowed to narrow a former native CLI surface even when a useful
plugin-owned workflow still exists in the TUI.

Restoring application-specific Go commands would recreate the placement debt
corrected by EP-66. Automatically treating every lifecycle command as a CLI
command would be equally misleading: the CLI has no TUI composition, approval
drawer, live WorkerRun, or application session unless the host deliberately
constructs those facilities.

## Proposed direction

A signed installed package may eventually declare a separate, explicit set of
CLI commands. A declaration is not inferred from `commands`, tools, exports, or
display names. The loader binds it to the package's exact EP-39 runtime identity
and signed manifest.

The first implementation should prefer an always-unambiguous namespaced form,
for example:

```text
stado plugin command <package-selector> <command> [args...]
```

An operator may additionally enable a declared command as a top-level
`stado <command>` alias. Core commands always retain their names. Duplicate,
ambiguous, stale, unsigned, revoked, or project-only claims fail closed; config
order and Go map order never choose a winner.

Execution is a bounded one-shot WASM call with a strict result envelope and
host-selected exit status. The guest receives normalized arguments and factual
invocation context, not raw controller tokens, package identity fields, ambient
environment variables, credentials, or arbitrary stdio handles. `stado.kernel`
installs only the capabilities signed for that exact command and rechecks the
installed package, revocation/transparency policy, host-version floor, and
resource ceilings at invocation.

CLI commands do not automatically receive TUI-only imports, lifecycle state,
an application binding, a logical session, operator-origin authority, or a
provider. A future command that needs one of those must declare a separately
specified execution profile whose native host can truthfully supply it. Missing
facilities are reported; the host never creates a fake TUI session or falls
back to removed native application code.

## Safety invariants

- Installation does not silently activate a CLI command. Top-level exposure is
  an explicit user-owned choice; project configuration cannot grant it.
- Static core commands cannot be replaced or shadowed.
- Friendly package or command aliases are usability selectors only. Canonical
  source identity owns authority, state, idempotency, and audit attribution.
- Per-command capabilities are an exact signed subset of the package ceiling.
- Arguments and result bytes are bounded and strictly decoded. Raw guest text
  is never interpreted as an exit code, terminal control sequence, authority
  field, or host path.
- Non-interactive invocation cannot call interactive TUI approval/choice
  imports. If interactive CLI transport is later added, it needs its own
  explicit contract and provenance model.
- Removing, disabling, revoking, or making the selected package ambiguous
  removes the dynamic command; there is no native compatibility fallback.

## Open questions

1. Should top-level aliases be persisted in the existing user config or in a
   separate source-keyed activation record?
2. Is a namespaced `stado plugin command ...` surface sufficient, making
   top-level aliases unnecessary?
3. Which factual context, if any, may a one-shot command receive: cwd/repository
   identity, a read-only artifact binding, or an explicitly adopted logical
   session?
4. Should interactive terminal rendering be a separate CLI profile, or should
   plugins compose multi-step interaction from repeated non-interactive calls?
5. Can lifecycle packages share code with CLI commands while retaining
   separate instances and authority, or should the manifest require distinct
   exports?
6. What stable exit-status vocabulary is useful beyond success, usage error,
   denied/unavailable, and application failure?

## Decision log

### D1. Capture the surface separately from lifecycle slash commands

- **Draft direction:** CLI registration is explicit signed metadata and a
  separate host profile; it is never inferred from a TUI command.
- **Why:** the two surfaces have different lifetime, interaction, provenance,
  and authority facts.
- **Deferred:** manifest and wire schema, activation storage, session access,
  interactive transport, and implementation/release milestone.
