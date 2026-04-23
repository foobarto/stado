# Retroactive EP Sweep Design

Date: 2026-04-23
Status: approved in chat
Owner: Codex

## Summary

Treat the existing `docs/eps/` worktree changes as the starting point,
not a false start. Normalize the current EP set into a coherent
as-built architecture record, then add the missing top-level decisions
that are already visible in the shipped system.

The outcome should be a small, defensible EP catalog that matches the
current runtime and release model rather than a large historical archive
or a pile of partially retrofitted drafts.

## Context

The repository already contains:

- a process EP: `0001`
- a tools/plugins EP: `0002`
- an in-progress retroactive batch for `0003` through `0010`

Those drafts cover most of the major architectural decisions, but they
currently skew too historical in tone and too weak in classification.
Several of them document current runtime contracts and constraints, so
leaving them as `Informational` would understate their role.

There are also two missing top-level decisions that are clearly part of
the shipped architecture:

- observability and telemetry
- release integrity and distribution trust

## Goals

- Convert the current EP batch into a coherent retrospective
  architecture set.
- Reclassify EPs so current invariants are documented as
  `Standards`, not just `Informational`.
- Add EPs for missing top-level shipped decisions when they are clearly
  visible in the code, docs, and release machinery.
- Keep the sweep narrow and factual.

## Non-goals

- Rewriting the EP system from scratch.
- Turning EPs into user guides or command references.
- Reworking unrelated docs outside the EP tree.
- Inventing new future-facing decisions that are not already reflected
  in the repository.

## Chosen approach

Use the current `0002`-`0010` set as the base, normalize it, and extend
it only where obvious gaps remain.

Rejected alternatives:

- Full rewrite from scratch: too much churn for little value.
- Editorial pass only: does not fix the classification problem or the
  missing top-level decisions.

## Target EP set

The implementation should converge on this set:

| EP | Title | Target type | Target status |
|---|---|---|---|
| 0001 | EP Purpose and Guidelines | Process | Accepted |
| 0002 | All Tools as WASM Plugins | Standards | Implemented |
| 0003 | Provider-Native Agent Interface | Standards | Implemented |
| 0004 | Git-Native Sessions and Audit Trail | Standards | Implemented |
| 0005 | Capability-Based Sandboxing | Standards | Implemented or Accepted, depending on final wording around known platform gaps |
| 0006 | Signed WASM Plugin Runtime | Standards | Implemented |
| 0007 | Conversation State and Compaction | Standards | Implemented |
| 0008 | Repo-Local Instructions and Skills | Standards | Implemented |
| 0009 | Session Guardrails and Hooks | Standards | Implemented |
| 0010 | Interop Surfaces: MCP, ACP, and Headless | Standards | Implemented |
| 0011 | Observability and Telemetry | Standards | Implemented |
| 0012 | Release Integrity and Distribution | Standards | Implemented |

## Document design rules

### Classification

The rule for this sweep is simple:

- if an EP defines a current runtime contract, invariant, or supported
  architectural boundary, it should be `Standards`
- if an EP documents repository process, it should be `Process`
- `Informational` should be reserved for context that is explanatory but
  not normative

For this project, most of `0003`-`0012` should read as standards
because they describe shipped system boundaries that other code and docs
already rely on.

### Tone

The EPs should be retroactive, but not speculative.

That means each EP should:

- describe the current invariant in present tense
- call out shipped exceptions or partial edges directly
- keep the retroactive history in frontmatter and decision log, not by
  making the whole document read like a postmortem

### Frontmatter normalization

Normalize frontmatter across the set:

- `ep`
- `title`
- `author`
- `status`
- `type`
- `created`
- `implemented-in` where applicable
- `history`
- `see-also` where useful

History entries should reflect retroactive acceptance and
implementation, not pretend the document predated the code.

### Cross-linking

EPs should point to:

- related EPs
- `DESIGN.md` for as-built architecture detail
- `README.md` or feature docs when operational behavior is already
  documented there

The EP should record the decision and invariant, not duplicate every
user-facing command example.

## New EP scope

### EP-11: Observability and Telemetry

This EP should cover:

- OpenTelemetry as a first-class runtime concern
- span boundaries around turns, provider streams, tool calls, and
  session lifecycle events
- metrics as part of the provider/runtime/tool contract
- disabled-safe no-op behavior
- cross-process trace continuity for session forks and resumes

Primary evidence base:

- `internal/telemetry`
- instrumented provider/runtime/tool call sites
- `PLAN.md` phase 6 and 9.4/9.5
- `README.md` observability sections

### EP-12: Release Integrity and Distribution

This EP should cover:

- reproducible build stance
- signed checksum-manifest flow
- role split between cosign keyless and minisign
- `self-update` trust model
- airgap build stance
- distribution surfaces: GitHub Releases, Homebrew tap, packages

Primary evidence base:

- `.github/workflows/release.yml`
- `cmd/stado/selfupdate*.go`
- `internal/audit/minisign.go`
- `README.md` install/update/security sections
- `PLAN.md` phase 10

## Editing boundaries

The implementation pass should:

- edit `docs/eps/README.md`
- edit existing `docs/eps/0001` through `0010`
- add `docs/eps/0011-*`
- add `docs/eps/0012-*`

It should avoid unrelated edits unless a tiny correction is required to
keep an EP reference accurate.

## Verification plan

Before completion:

- read back the edited EP files for internal consistency
- check that statuses, types, and `implemented-in` usage match `0001`
- compare claims against `DESIGN.md`, `README.md`, `PLAN.md`, and the
  relevant code paths
- run `git diff --check -- docs/eps`

## Done criteria

The task is done when:

- the EP index is complete and internally consistent
- each major shipped architectural decision has one clear EP home
- the promoted EPs no longer read like loose informational drafts
- the new EPs cover the missing telemetry and release/trust decisions
- the final set does not obviously contradict current code or docs
