---
ep: 25
title: Thinking Display Modes
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-04-24
implemented-in: v0.12.0
see-also: [3, 21]
history:
  - date: 2026-06-14
    status: Implemented
    note: "Revised in v0.73.0 — display vocabulary changed to preview/auto/collapsed/expanded (dropping the old show/tail/hide; legacy values still load), a parallel tool_display setting governs tool-output panes, and any block can be overridden between full and one-line by click / Shift+Tab. See the Revision section below."
  - date: 2026-04-25
    status: Implemented
    note: Resumed sessions now reconstruct persisted provider-native thinking as separate viewport blocks instead of assistant placeholders.
  - date: 2026-04-25
    status: Implemented
    note: TUI thinking display mode now persists via `[tui].thinking_display` and is restored on startup.
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

On session resume, persisted provider-native thinking blocks are
reconstructed as TUI `thinking` blocks rather than folded into assistant
placeholder text, so `show`, `tail`, and `hide` remain meaningful across
restarts.

## Migration / rollout

Default to `show`, matching prior behavior. Toggling `/thinking` or
`Ctrl+X H` persists the selected mode to `[tui].thinking_display` in
`config.toml`; existing configs without the key continue to load as
`show`.

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

- None.

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

## Revision: 4-mode vocabulary + tool parity (v0.73.0)

The original three-value `thinkingMode` (show/tail/hide) is replaced by a
shared four-value `displayMode`, and the same control is extended to
tool-output panes:

- **preview** — clip/tail to a few lines (the thinking tail; the tool
  `tool_output_collapsed_height` panel with its "… N more lines" footer).
  This is the new default and reproduces the prior `tail`-style preview.
- **auto** — render the block full while it is streaming/running, then
  collapse it to a single line once it finishes. Backed by a per-block
  `streaming` flag set at thinking/tool creation and cleared when the next
  block appends, the tool result arrives, the turn completes, or the turn
  is cancelled.
- **collapsed** — always a single summary line (`▪ thinking · N lines`,
  `▸ <tool> · N lines`). This replaces the old `hide` — the header stays
  visible rather than suppressing the block entirely.
- **expanded** — always the full body.

**Separate per type.** Thinking uses `[tui].thinking_display` (`Ctrl+X H`,
`/thinking`); tool output uses the new `[tui].tool_display` (`Ctrl+X O`,
`/tool-display`). Both cycle the four modes and persist.

**Per-block override.** In any mode, a mouse click — or `Shift+Tab` on the
focused/latest block — flips that one block between full and one-line for
the session (a tri-state `block.override` that wins over the mode).
Assistant-turn details still use the original `expanded` bool.

**Clean break (pre-1.0).** The canonical values are now
preview/auto/collapsed/expanded; the old `show`/`tail`/`hide` are no longer
written, but still parse on load (mapped to expanded/preview/collapsed) so
existing configs keep working. The original "hide entirely" capability is
gone — `collapsed` shows a one-line header instead of nothing.

This revises the Goals above: "hide thinking blocks" becomes "collapse to
one line", and the feature now covers tool output, not just thinking.

## Related

- EP-3 Provider-Native Agent Interface
- EP-21 Assistant Turn Metadata
