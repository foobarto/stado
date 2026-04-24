---
ep: 25
title: Thinking Display Modes
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
implemented-in: v0.12.0
see-also: [3, 21]
history:
  - date: 2026-04-24
    status: Implemented
    note: TUI thinking blocks can be shown fully, hidden, or rendered as a recent tail.
---

# EP-25: Thinking Display Modes

## Problem

Reasoning-capable providers can stream long thinking blocks. Full
thinking is useful for debugging and trust, but it can dominate the chat
viewport and make the final answer harder to scan. Users need a quick
TUI control that changes only the rendering policy, not the provider
request or the saved transcript.

## Goals

- Keep full thinking visible by default to preserve existing behavior.
- Let users hide thinking blocks from the TUI viewport.
- Let users show only the recent tail of long thinking blocks.
- Preserve provider-native thinking in conversation persistence
  regardless of display mode.
- Make the feature discoverable from key help and the command palette.

## Non-goals

- Changing `[agent].thinking` provider-request behavior.
- Removing thinking from saved transcripts or audit data.
- Adding per-session persistent display preferences in the first slice.

## Design

The TUI owns a display-only `thinkingMode` with three values:

- `show` renders complete thinking blocks.
- `tail` renders a bounded recent tail of each thinking block.
- `hide` suppresses thinking blocks from the chat viewport.

`Ctrl+X H` cycles `show -> tail -> hide -> show`. `/thinking` cycles the
same way, while `/thinking show`, `/thinking tail`, and `/thinking hide`
set a specific mode. The command lives in the View group because it
changes rendering, not inference behavior.

Render caching includes the thinking display mode for thinking blocks so
switching between `show` and `tail` cannot reuse stale rendered output.
While a model is streaming, toggling does not append a system block to
the transcript; it only re-renders the viewport so the current turn is
not split by UI feedback.

## Migration / rollout

Default to `show`, matching prior behavior. No config migration is
required.

## Failure modes

- A user may confuse display `hide` with provider thinking being off.
  Docs and command copy state that display modes do not change capture
  or persistence.
- Tail mode can cut through a paragraph if the provider emits one very
  long line. This is acceptable for a first display-control slice.

## Test strategy

- Unit tests cover show/tail/hide rendering.
- Slash and keybind tests cover direct setting and cycling.
- A streaming-state regression test ensures toggling does not append
  transcript blocks mid-turn.

## Open questions

- Should the selected display mode persist across TUI restarts?
- Should resumed sessions reconstruct thinking blocks as separate
  viewport blocks instead of assistant placeholders?

## Decision log

### D1. Make thinking mode display-only

- **Decided:** `hide` and `tail` only affect rendering.
- **Alternatives:** mutate saved conversation blocks or disable provider
  thinking.
- **Why:** users need viewport control without losing auditability or
  changing model behavior.

### D2. Do not append feedback while streaming

- **Decided:** streaming toggles re-render silently.
- **Alternatives:** always append a system block with the new mode.
- **Why:** inserting UI feedback into `m.blocks` during a provider turn
  can split the visible assistant response.

## Related

- EP-3 Provider-Native Agent Interface
- EP-21 Assistant Turn Metadata
