## Approval prompts in the TUI

- Don’t treat tool approval as a fully modal keypath if the goal is “keep typing while deciding.” The safe split is:
  - `y` / `n` work as global shortcuts for fast resolve.
  - a small approval card owns `Up`, `Left/Right`, and `Enter` only after it has focus.
  - normal text editing continues in the textarea while approval is pending.
- Reserve approval UI height from the actual rendered card (`lipgloss.Height`) instead of subtracting a fixed row count. The earlier hardcoded `-2` made the left column height drift and produced the “duplicated/misaligned input box” rendering bug from the screenshot.
- For regression tests, assert against stable stripped UI markers, not raw textarea bodies. `ansi.Strip` + the inline input-status row is much more reliable than matching the placeholder/cursor-rendered textarea output directly.
