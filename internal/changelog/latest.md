## v0.73.0 — configurable thinking + tool display modes — 2026-06-14

Thinking blocks and tool-output panes each get a configurable display mode in
the TUI, with a shared four-value vocabulary and a per-block override. The
default (`preview`) is unchanged from before, so existing setups look the same.

### TUI

- **Display modes for thinking and tool output.** A new vocabulary —
  `preview` / `auto` / `collapsed` / `expanded` — controls how each renders:
  - `preview` (default): clip/tail to a few lines (the prior behavior).
  - `auto`: full while the block is streaming/running, then collapse to a
    single line once it finishes.
  - `collapsed`: always a single summary line (`▪ thinking · N lines`,
    `▸ <tool> · N lines`).
  - `expanded`: always the full body.
- **Separate per type.** Thinking uses `Ctrl+X H` / `/thinking [mode]`
  (`[tui].thinking_display`); tool output uses the new `Ctrl+X O` /
  `/tool-display [mode]` (`[tui].tool_display`). Both persist.
- **Per-block override in any mode.** A mouse click — or `Shift+Tab` on the
  focused/latest block — flips that one block between the mode default and its
  opposite (full ↔ one-line) for the session, then back. Works regardless of
  the global mode.

### Config

- New `[tui].tool_display` key (default `preview`). `stado config show` and the
  `stado config init` template document both display keys.
- **`[tui].thinking_display` values changed** to `preview|auto|collapsed|expanded`.
  The legacy `show`/`tail`/`hide` values still load (mapped to
  `expanded`/`preview`/`collapsed`) so existing configs keep working, but the
  old `hide` (fully suppress thinking) is gone — `collapsed` shows a one-line
  header instead. New values are written canonically.

