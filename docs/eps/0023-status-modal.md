---
ep: 23
title: TUI Status Modal
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [11, 17, 19, 22]
history:
  - date: 2026-04-24
    status: Partial
    note: First status modal shipped with provider, model, tools, plugins, MCP, OTel, sandbox, and context summaries.
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
- Show session, sandbox, tool count, plugin, MCP, OTel, budget, and
  context summaries.
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

The modal reads existing TUI state only; it does not probe providers or
start MCP/plugin work while rendering.

## Test Strategy

- Unit-style TUI tests cover slash opening, keybinding opening, closing,
  and expected rendered sections.
- Existing tmux UAT continues to cover modal routing regressions around
  the shared command palette and help overlay.

## Open Questions

- Should each section link to a focused command, such as `/tools` or
  `/providers`?
- Should plugin and MCP rows include health/error details once those
  subsystems expose stable status snapshots?
- Should OTel show the current trace id for copy/paste?
