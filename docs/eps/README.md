# Enhancement Proposals (EPs)

This directory holds **Enhancement Proposals** — numbered design
records that capture the *what* and *why* of non-trivial changes to the
project. See [EP-1](./0001-ep-purpose-and-guidelines.md) for the full
process, conventions, and lifecycle.

## When to write one

Write an EP if you're introducing a new public contract, touching a
load-bearing invariant, reversing a prior decision, or answering a
"should we do X or Y?" question that isn't obvious from the code. Skip
it for bug fixes, dep bumps, and contained refactors.

## How to write one

1. Copy `0000-template.md` to `NNNN-short-kebab-title.md` with the next
   unused number (see the index below).
2. Fill in the frontmatter and the expected sections.
3. Open a PR. Iterate. When accepted, update `status: Accepted` and
   merge.

## Index

| #    | Title | Type | Status |
|------|-------|------|--------|
| 0001 | [EP Purpose and Guidelines](./0001-ep-purpose-and-guidelines.md) | Process | Accepted |
| 0002 | [All Tools as WASM Plugins](./0002-all-tools-as-plugins.md) | Standards | Implemented |
| 0003 | [Provider-Native Agent Interface](./0003-provider-native-agent-interface.md) | Standards | Implemented |
| 0004 | [Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md) | Standards | Implemented |
| 0005 | [Capability-Based Sandboxing](./0005-capability-based-sandboxing.md) | Standards | Implemented |
| 0006 | [Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md) | Standards | Implemented |
| 0007 | [Conversation State and Compaction](./0007-conversation-state-and-compaction.md) | Standards | Implemented |
| 0008 | [Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md) | Standards | Implemented |
| 0009 | [Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md) | Standards | Implemented |
| 0010 | [Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md) | Standards | Implemented |
| 0011 | [Observability and Telemetry](./0011-observability-and-telemetry.md) | Standards | Implemented |
| 0012 | [Release Integrity and Distribution](./0012-release-integrity-and-distribution.md) | Standards | Implemented |
| 0013 | [Subagent Spawn Tool](./0013-subagent-spawn-tool.md) | Standards | Implemented |
| 0014 | [Multi-Session TUI](./0014-multi-session-tui.md) | Standards | Implemented |
| 0015 | [Memory System Plugin](./0015-memory-system-plugin.md) | Standards | Implemented |
| 0016 | [Learning and Self-Improvement Plugin](./0016-learning-self-improvement-plugin.md) | Standards | Implemented |
| 0017 | [Tool Surface Policy and Plugin Approval UI](./0017-tool-surface-policy-and-plugin-approval-ui.md) | Standards | Implemented |
| 0018 | [Configurable System Prompt Template](./0018-configurable-system-prompt-template.md) | Standards | Implemented |
| 0019 | [Model and Provider Picker UX](./0019-model-provider-picker-ux.md) | Standards | Implemented |
| 0020 | [Inline Context Completion](./0020-inline-context-completion.md) | Standards | Implemented |
| 0021 | [Assistant Turn Metadata](./0021-assistant-turn-metadata.md) | Standards | Implemented |
| 0022 | [Theme Catalog and Picker](./0022-theme-catalog-and-picker.md) | Standards | Implemented |
| 0023 | [TUI Status Modal](./0023-status-modal.md) | Standards | Implemented |
| 0024 | [TUI Footer Density](./0024-footer-density.md) | Standards | Implemented |
| 0025 | [Thinking Display Modes](./0025-thinking-display-modes.md) | Standards | Implemented |
| 0026 | [Command Input Ergonomics](./0026-command-input-ergonomics.md) | Standards | Implemented |
| 0027 | [Repo-Root Discovery](./0027-repo-root-discovery.md) | Standards | Implemented |
| 0028 | [`plugin run --with-tool-host` + HOME-rooted MkdirAll](./0028-plugin-run-tool-host.md) | Standards | Implemented |
| 0029 | [Config-introspection host imports — `cfg:*`](./0029-config-introspection-host-imports.md) | Standards | Implemented |
| 0030 | [Security-research default harness](./0030-security-research-default-harness.md) | Standards | Placeholder |
| 0031 | [`fs:read:cfg:state_dir/...` path templates](./0031-fs-cap-path-templates.md) | Standards | Implemented |
| 0032 | [ACP client — wrap external coding-agent CLIs](./0032-acp-client-wrap-external-agents.md) | Standards | Draft |
| 0033 | [Responsive frontline — supervisor + worker lanes](./0033-responsive-supervisor-worker-lanes.md) | Standards | Draft |
| 0034 | [Background agents + fleet registry](./0034-background-agents-fleet.md) | Standards | Implemented |
| 0035 | [Project-local .stado/ directory](./0035-project-local-stado-dir.md) | Standards | Implemented |

<!-- Add new entries in numerical order. Keep the table tidy. -->

## Status legend

- **Placeholder** — triage-approved idea with a reserved number and
  a problem statement + open questions. Not yet a worked design.
  Low review bar to merge; the point is rapid capture. See EP-1
  §"Placeholders."
- **Draft** — author is iterating. Design space being actively
  worked out. Content may change.
- **Accepted** — approved for implementation, or for Informational and
  retrofitted EPs, approved as the canonical record of an already
  shipped decision. Content is append-only.
- **Partial** — one or more scoped slices have shipped, but the EP's
  stated goals are not fully implemented yet.
- **Implemented** — a Standards EP that has shipped and now describes
  the current runtime contract. Optional
  `implemented-in: vX.Y.Z` in frontmatter points at the release.
- **Superseded** — replaced by a later EP. Frontmatter points forward
  via `superseded-by: [N]`.
- **Withdrawn** — author pulled it before acceptance.
- **Rejected** — maintainers declined it. Kept for historical context.

## Types

- **Standards** — defines code, on-disk layout, CLI, API, or
  user-visible behaviour that the runtime is expected to implement.
- **Informational** — documents background, conventions, decision
  records, or historical context. It does not carry the main runtime
  architecture.
- **Process** — changes how contributors work (e.g., this process doc).

## Frontmatter fields

Required:

```yaml
ep: N
title: Short, descriptive title
author: Name <email@example.com>
status: Placeholder | Draft | Accepted | Partial | Implemented | Superseded | Withdrawn | Rejected
type: Standards | Informational | Process
created: YYYY-MM-DD
```

Optional, added as relevant (see EP-1 for full semantics):

```yaml
updated: YYYY-MM-DD
requires: [N, M]          # must-read-first dependencies
supersedes: [N]           # this EP replaces these
superseded-by: [N]        # this EP has been replaced
extended-by: [N, M]       # later EPs build on this one
see-also: [N, M]          # loosely related
implemented-in: vX.Y.Z
discussion-at: <URL>
```

All EP-reference fields are YAML lists even when holding a single
value (`superseded-by: [8]`), for tooling consistency.

## Bidirectional links

When a new EP supersedes an older one, or when a strong extension
relationship is recorded via `extended-by`, **the same PR must update
the older EP's frontmatter** so navigation works in both directions.
Loose `see-also` links do not need reciprocal updates. See EP-1
§"Updating EPs" for the full rule.

## Conventions

- **Filename:** `NNNN-short-kebab-title.md`. Titles under ~60 chars.
- **Numbers:** sequential, four-digit, claimed at merge time. Rebase on
  conflict.
- **Decision log:** every Standards EP must have one. Informational
  EPs should have one when they capture rationale (most do).
- **Append-only after Acceptance:** substantive edits go in a new EP
  that supersedes the old.
