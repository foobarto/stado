---
ep: 41
title: Shell PTY tool naming — read_until and screenshot for tool-selection affordance
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-05-22
implemented-in: v0.49.0
see-also: [37, 38]
history:
  - date: 2026-05-22
    status: Implemented
    note: >
      Renamed the shell plugin's `shell.expect` → `shell.read_until` and
      `shell.snapshot` → `shell.screenshot`; added cross-referencing
      descriptions to `shell.read`. Shipped in v0.49.0. Old names removed
      outright (no aliases) per the pre-1.0 no-kid-gloves posture. Promoted from
      the implementation plan that previously lived under
      `docs/superpowers/plans/`.
---

# EP-0041: Shell PTY tool naming — read_until and screenshot

> **Revision (v0.74.1, EP-0043):** The agent-facing `shell.screenshot` tool
> was removed. Screen capture is now `shell.read` with `mode: "screen"` or
> `mode: "auto"`. This EP documents the v0.49.0 rename only; current PTY UX
> is authoritative in [EP-0043](./0043-shell-pty-ux-rethink.md).

## Problem

The shell plugin exposes three "get output from a PTY session" tools. As
originally named they were `shell.read` (drain buffered output), `shell.expect`
(block until a pattern matches), and `shell.snapshot` (render the terminal
screen via vt100). LLM agents pick tools by **name first**, description second.
`read` is the universal default verb, while `expect` (jargon from `expect(1)`)
and `snapshot` are not words an agent reaches for unprompted. The observed
failure: driving a full-screen TUI, an agent calls `shell.read`, gets a stream
of raw ANSI escape sequences it can't interpret, and muddles through instead of
switching to `shell.snapshot`. It also rarely reached for `shell.expect` to wait
on a prompt, hand-rolling read-loops instead. This is an affordance problem, not
a capability gap.

## Goals

- Make agents reach for the right PTY-output tool by name, without prompting.
- Keep all three behaviors and their output shapes unchanged.
- Add cross-references so the default tool (`shell.read`) redirects the agent to
  the right sibling.

## Non-goals

- Changing matching, rendering, or attach/lock semantics.
- Renaming the host imports `stado_terminal_expect` / `stado_terminal_snapshot`
  (host↔wasm ABI; internal, not agent-facing). Only the agent-facing tool names
  and the wasm exports changed.
- Merging `read` and `read_until` into one tool (considered and rejected — see
  D2).
- Backward-compatibility aliases (rejected — see D3).

## Design

Three tools, renamed for discoverability; behavior identical:

| Old | New | Behavior |
|---|---|---|
| `shell.read` | `shell.read` (unchanged) | Drain buffered output, return immediately. |
| `shell.expect` | `shell.read_until` | Block until a pattern matches / timeout / EOF. |
| `shell.snapshot` | `shell.screenshot` | vt100-rendered screen: `{text, cols, rows, cursor, title, svg?}`. |

`shell.read_until` carries the word `read`, so it clusters next to `shell.read`
in the agent's mental model and is reachable without knowing `expect(1)`.
`shell.screenshot` is the universally intuitive term for "capture what's on
screen now" (and the tool already emits an optional SVG). `shell.read`'s
description now names both siblings: use `shell.screenshot` when output is ANSI
escape garbage from a full-screen program; pass it to `shell.read_until` to
block until a prompt appears.

Implementation was a pure rename across: the wasm exports
(`plugins/bundled/shell/main.go`), tool registrations + descriptions
(`internal/runtime/bundled_plugin_tools.go`), canonical metadata
(`internal/runtime/tool_metadata.go`), both PTY-bound refusal lists
(`cmd/stado/tool_run.go`, `internal/tui/model_commands.go`), tests, the
`expect-demo-go` demo prose, and `CHANGELOG.md`. The host imports were left
untouched; `shell.read_until` routes to the existing `stado_terminal_expect`
host import.

## Migration / rollout

Old names (`shell.expect`, `shell.snapshot`) were **removed outright** — no
deprecation aliases — and all in-repo callers updated in the same change. This
follows the pre-1.0 no-kid-gloves posture (clean breaking changes; no shims).
External callers (e.g. the htb-toolkit, project skills) update on their own
cadence. Shipped in v0.49.0 with a breaking-change CHANGELOG note.

## Failure modes

- A missed caller would leave a dangling reference to a now-deleted tool name.
  Guarded by a repo-wide grep in the done-definition and by the registration /
  metadata contract tests. (One such miss — a second wire-name switch in
  `tool_run.go` — was caught by the final grep, not the canonical-name tests.)

## Test strategy

Behavior is unchanged, so the existing round-trip tests are the safety net,
re-pointed at the new names: `TestShellScreenshotE2E` (wasm round-trip for the
screen capture) and `TestBundledShellReadUntil_RoundTripsThroughWasm`
(pattern-wait round-trip), plus metadata-resolution cases for the canonical and
wire forms. Real-binary check confirmed the new names are listed + PTY-gated and
the old names 404.

## Open questions

None. A follow-on idea — wrapping over-long descriptions to a second line rather
than truncating — is unrelated to naming and out of scope here.

## Decision log

### D1. Rename rather than re-document

- **Decided:** rename the tools (`read_until`, `screenshot`) instead of only
  rewriting descriptions.
- **Alternatives:** keep the names, improve descriptions and rely on the agent
  reading them.
- **Why:** agents select on the name first; a better description under a
  non-evocative name doesn't move selection behavior much. The name is the
  affordance.

### D2. Keep three tools — do not merge `expect` into `read`

- **Decided:** `read_until` stays a separate tool from `read`.
- **Alternatives:** fold the wait-for-pattern behavior into `shell.read` via an
  optional `patterns` arg (one fewer tool).
- **Why:** the merge would force a discriminated output union on `shell.read`
  (`expect`'s `before` ≠ `read`'s `data`; `before` excludes the match) — exactly
  the agent-confusion the rename is trying to remove. Three tools each with one
  clean output shape is the better trade.

### D3. No deprecation aliases

- **Decided:** delete the old names outright.
- **Alternatives:** register `shell.expect` / `shell.snapshot` as deprecated
  aliases for a release or two.
- **Why:** pre-1.0, no external install base to protect; aliases are dead weight
  that obscures the real tool surface (no-kid-gloves posture).

### D4. `read_until` over `expect_read` / keep `expect`

- **Decided:** `shell.read_until`.
- **Alternatives:** `expect_read`, `read_expect`, or keeping `expect`.
- **Why:** `read_until` describes the behavior ("read until a pattern") and is
  reachable cold without knowing `expect(1)`, while still carrying `read` so it
  clusters with `shell.read`.

## Related

- EP-0037 (tool dispatch and operator surface) — canonical/wire tool naming.
- EP-0038 (ABI v2 + bundled wasm) — the bundled shell wasm wrapper + host
  imports this builds on.
- Design record + handoff: `.agent/specs/done/shell-tool-affordance.md`.
