---
ep: 23
title: TUI Status Modal
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [11, 17, 19, 22]
history:
  - date: 2026-04-25
    status: Partial
    note: Rows now include direct next-step hints for focused commands or config files.
  - date: 2026-04-24
    status: Partial
    note: First status modal shipped with provider, model, tools, plugins, MCP, OTel, sandbox, and context summaries.
  - date: 2026-04-24
    status: Partial
    note: Added plain-language LSP readiness that lists detected language-server binaries without starting them.
---

# EP-23: TUI Status Modal

## Problem

The sidebar is intentionally calmer by default, but users still need a
fast way to inspect operational state without opening logs or reading
several slash-command outputs. Opencode exposes this as a status modal;
stado should offer the same scan-speed while keeping its stronger
sandbox, telemetry, and plugin model visible on demand.

## Goals

- Add a modal status surface available from keyboard and slash-command
  paths.
- Show provider, model, current agent, and provider capabilities.
- Show session, sandbox, tool count, plugin, MCP, LSP readiness, OTel,
  budget, and context summaries.
- Keep the default sidebar quiet.

## Non-goals

- A full trace explorer.
- Editing provider, MCP, plugin, or sandbox config from the modal.
- Replacing existing focused commands such as `/providers`, `/tools`,
  `/context`, or `/budget`.

## Design

`/status` and `Ctrl+X S` open a centered modal. `Esc`, `?`, or the same
status key closes it. The modal groups state into:

- Agent
- Runtime
- Context
- Extensions

The LSP row is informational only. It explains that LSP-backed tools
activate when supported files are read and lists known language-server
binaries found on `PATH`.

The modal reads existing TUI state only; it does not probe providers or
start MCP/plugin work while rendering. Rows may include a short
next-step hint, such as `/model`, `/tools`, `/plugin`, `/context`, or
`config.toml`, so the modal remains read-only while still pointing to
the focused command or file that resolves the row.

## Test Strategy

- Unit-style TUI tests cover slash opening, keybinding opening, closing,
  and expected rendered sections.
- Existing tmux UAT continues to cover modal routing regressions around
  the shared command palette and help overlay.

## Open Questions

- Should rows become keyboard-focusable actions, or are inline hints
  enough?
- Should plugin and MCP rows include health/error details once those
  subsystems expose stable status snapshots?
- Should OTel show the current trace id for copy/paste?
