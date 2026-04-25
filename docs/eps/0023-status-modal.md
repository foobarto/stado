---
ep: 23
title: TUI Status Modal
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
see-also: [11, 17, 19, 22]
history:
  - date: 2026-04-25
    status: Implemented
    note: Added cached plugin load/tick issue and MCP attach snapshots to close the read-only status modal scope.
  - date: 2026-04-25
    status: Partial
    note: Kept status rows read-only with inline action hints; focusable row actions are deferred until a concrete workflow needs them.
  - date: 2026-04-25
    status: Partial
    note: The modal now includes a compact configured MCP server-name summary.
  - date: 2026-04-25
    status: Partial
    note: The modal now shows active-provider credential env var health.
  - date: 2026-04-25
    status: Partial
    note: The modal now shows the current OTel trace id when the TUI context carries one.
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
- Show provider, model, current agent, provider capabilities, and
  provider credential health.
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

The credentials row uses stado's conventional provider environment
mapping. Remote providers show whether their API key variable is present
or missing; local presets show that no key is required by default.

The MCP row summarizes configured server names from `config.toml`
without connecting to them. It caps the displayed name list so the modal
stays compact.

After the runtime has attempted MCP attachment, the MCP row switches to
the cached attach snapshot: configured count, connected count, attached
tool count, and the latest attach error if one occurred. Rendering the
modal never starts or reconnects MCP clients.

The plugin row summarizes active background plugins and cached lifecycle
state. It shows whether a background tick is running or queued and keeps
the latest load/tick issue surfaced by the TUI plugin lifecycle. Rendering
the modal never loads, ticks, or verifies plugins.

When OTel is enabled and the TUI run context has a valid span context,
the Extensions section includes the trace id for copy/paste into a
collector or trace UI.

## Test Strategy

- Unit-style TUI tests cover slash opening, keybinding opening, closing,
  and expected rendered sections.
- Unit-style TUI tests cover provider credential health, configured MCP
  summaries, cached MCP live summaries, and cached plugin issue/tick
  state.
- Runtime tests cover MCP attach status snapshots for setup errors.
- Existing tmux UAT continues to cover modal routing regressions around
  the shared command palette and help overlay.

## Open Questions

- None for the read-only status modal. Focusable actions or live probes
  should be designed separately when a concrete workflow needs them.
