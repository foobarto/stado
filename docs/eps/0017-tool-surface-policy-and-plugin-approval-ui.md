---
ep: 17
title: Tool Surface Policy and Plugin Approval UI
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
implemented-in: v0.1.0
see-also: [2, 5, 6, 9]
history:
  - date: 2026-04-24
    status: Implemented
    version: v0.1.0
    note: Records the shipped move from native bundled-tool approvals to tool filtering plus explicit plugin approval capability.
---

# EP-17: Tool Surface Policy and Plugin Approval UI

## Problem

The original TUI approval loop prompted before native bundled tool
calls, while plugins had their own capability model. That split made
policy harder to reason about: a prompt could be mistaken for a
containment boundary, and approval-wrapper plugins duplicated a host
feature that was also available natively.

stado now needs one clearer contract: the native tool surface is shaped
by registry visibility and sandbox capabilities, while human approval is
an explicit plugin behavior that a plugin declares and invokes.

## Goals

- Make `[tools].enabled` and `[tools].disabled` the native bundled-tool
  policy surface.
- Keep Plan mode as a hard tool-visibility filter rather than a
  post-hoc approval workaround.
- Let plugins request human approval through a declared `ui:approval`
  capability.
- Keep the TUI responsive while a plugin approval card is pending.

## Non-goals

- Treating a UI prompt as an OS sandbox or capability boundary.
- Reintroducing a native prompt-before-every-tool loop.
- Letting plugins open approval UI without a manifest capability.

## Design

Native bundled tools execute when they are visible in the current tool
registry. Visibility comes from three controls: Do/Plan mode, the
configured `[tools]` filter, and plugin overrides. Calls for tools that
are not visible to the current turn are rejected before execution.

Human approval is a plugin capability. A plugin that declares
`ui:approval` can call the host approval import and receive an
Allow/Deny result. The TUI renders that request as a dedicated approval
card, supports keyboard approval/denial, and keeps the chat input
editable while the approval is pending. Plugins without `ui:approval`
cannot open the approval UI.

The old `/approvals` slash command remains only as a compatibility hint
that explains the current model. Existing config files with
`[approvals]` still load, but that section is no longer the native tool
execution gate.

## Migration / rollout

Users who previously relied on native approval prompts should narrow
the native surface with `[tools].enabled` or use the shipped
approval-wrapper plugin examples for tools that need a human gate.

## Failure modes

- A user expects `[approvals]` to gate native tools and misses that it is
  now compatibility-only.
- A plugin requests approval without declaring `ui:approval`; the host
  rejects the request instead of opening UI.
- A plugin approval prompt appears while the user has a draft; the TUI
  must preserve draft editing state.

## Test strategy

- TUI tests cover plugin approval allow/deny, card focus, draft
  preservation, and `/approvals` compatibility text.
- Plugin runtime tests require `ui:approval` before the approval import
  can succeed.
- Tool filtering tests verify invisible tools are not available to the
  model in Plan mode or `[tools]` allowlist mode.

## Decision log

### D1. Use tool visibility as the native policy surface

- **Decided:** native bundled tools are controlled through registry
  visibility, Plan mode, and `[tools]` filtering.
- **Alternatives:** keep prompting on every native tool call.
- **Why:** visibility is deterministic and testable; prompts are UX, not
  containment.

### D2. Keep human approval plugin-scoped

- **Decided:** plugins may request approval only when their manifest
  declares `ui:approval`.
- **Alternatives:** expose approval UI to every plugin or every native
  tool call.
- **Why:** capability-gated host imports keep approval requests explicit
  and auditable.

## Related

- [EP-2: All Tools as WASM Plugins](./0002-all-tools-as-plugins.md)
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [docs/commands/tui.md](../commands/tui.md#approvals)
- [docs/commands/plugin.md](../commands/plugin.md)
