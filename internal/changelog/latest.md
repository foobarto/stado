## v0.65.0 — configurable keybindings + readable slash output + UAT hardening — 2026-06-13

Adds configurable keybinding schemas and a no-truncation formatter for slash
list output, closes a terminal-escape-injection finding in the LSP diagnostics
sidebar, and lands two autonomous UAT rounds that sweep the display-width
truncation bug class across the TUI and CLI.

### Security

- **LSP diagnostics terminal-escape strip.** The diagnostics sidebar rendered
  the untrusted LSP-server message and path without `StripControlChars`, so a
  malicious or compromised language server could leak OSC/CSI/BEL escapes
  (clipboard hijack, title-bar rewrite) into the terminal. Both fields are now
  stripped like every other untrusted single-line surface.

### TUI

- **Configurable keybinding schemas (Phase 1).** Switch between pre-configured
  keymap schemas and customize bindings via config. `[keymap] schema = "emacs"`
  (default) `| "vscode"` selects the base layout; `[keymap.bindings]` maps an
  action name to comma-separated keys to override any single binding. An unknown
  action name is a non-fatal stderr warning (valid overrides still apply). The
  previously-defined-but-unwired `Messages*` scroll actions (PageUp / PageDown /
  HalfPage / First / Last) are now wired through the registry, so rebinds and
  the ctrl+alt half-page / home-end jumps take effect. Modal `vim`
  (ESC→normal mode) is Phase 2.
- **Readable slash list output (no truncation).** `/tool` / `/tools`, `/skill`,
  `/plugin`, and the `?` help overlay rendered over-compressed lists with
  truncated descriptions. A shared formatter now wraps descriptions
  (display-width aware) and hang-indents them under a dynamic gutter; `/tool ls`
  shows each tool's full description (was name + state only), and `/plugin` no
  longer ellipsis-truncates per-tool descriptions at 120 chars.
- **Typing no longer scrolls the conversation.** The bubbles viewport's default
  keymap bound the text letters `j/k/h/l/b/f/u/d`, `space`, and arrows to
  scroll; since the input is always focused, typing those scrolled the history
  (and `ctrl+u` both deleted-to-line-start and half-paged). Both conversation
  viewports now use a text-safe keymap — only PageUp / PageDown scroll; mouse
  wheel is unchanged.
- **Display-width truncation swept across the TUI.** Pickers, the slash palette,
  the activity panel, and several modal headers truncated by rune count (or
  byte-sliced mid-rune), so wide-CJK / emoji content overflowed the modal
  border, wrapped onto a second interior row (corrupting height accounting), or
  leaked invalid UTF-8. All now truncate display-width- and grapheme-aware
  (`ansi.Truncate`). Covers the tool-call header, approval prompt, status modal,
  choice menu, the tree / fleet / model / agent / persona / theme pickers, and
  the `@`-mention popover.
- **`@`-mention buffer corruption fixed.** `Editor.CursorOffset()` returned a
  display column that was consumed as a byte offset, so accepting an `@`-mention
  after a multibyte / CJK character corrupted the input buffer; the `@`-popover
  also stayed open with a stale anchor after `alt+enter` / `ctrl+enter` / an
  empty submit. Both fixed.
- **Picker overflow + narrow-terminal fixes.** The fleet-picker header and
  detail `SessionID`, the persona / theme right column, and the model / agent
  pickers overflowed the modal border (or, for model / agent, a ≤50-column
  terminal). All now clamp / truncate to the modal width, and the modelpicker
  header no longer slices an SGR escape when narrowed.
- **`/loop` stops cleanly on error.** An errored loop iteration returned without
  clearing loop state, so the `↻ loop` indicator stayed lit forever while
  nothing re-fired. An errored iteration now stops the loop and prompts `/loop`
  to restart.
- **No crash on a malformed plugin table row.** A plugin `table` row with more
  cells than declared columns panicked the render (`index out of range`); the
  extra cells are now dropped instead.
- **Honest local-runner credential save.** The TUI provider modal claimed it
  "recorded a credential ref" for a local-runner no-op save; it now reports the
  no-op honestly, matching the CLI.

### CLI

- **Rune-safe truncation.** `stats`, `plugin info`, `plugin doctor`,
  `session show`, `session tree`, and `doctor` byte-sliced text mid-rune,
  producing mojibake on non-ASCII content; all now truncate on rune /
  display-width boundaries (`textutil.TruncateRunes`). `session search`'s
  match-excerpt window likewise moved off byte offsets to display-column
  windowing (`ansi.Cut`), so a wide-CJK / emoji excerpt is never split.

### Infra

- **CI cross-compile build matrix removed.** The 5-way `go build -o /dev/null`
  matrix was redundant with GoReleaser, which already cross-compiles and signs
  every target at release time and fails the release run on a platform break.
  It is gone from CI — now a fast PR gate plus a post-merge `-race` validation —
  and GoReleaser is the sole release-time build. (#141 first moved it to
  tag-trigger; #148 removed it.)

