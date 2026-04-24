# Active Bugs

> This file should be read at the start of any coding session to maintain context.

---

## ACTIVE BUGS (Unfixed)

- None currently tracked in this file.
- Product gaps such as Windows sandbox v2 live in `PLAN.md`, not here.

---

## RECENTLY FIXED

- **`BUG-001: TUI freezes after LLM thinking response`** — stream deltas now drain through the shared tick-buffer path and are covered by `TestThinkingOnlyStreamDrainsAndReturnsToIdle`.
- **`BUG-002: Slash command popup not working`** — `/` opens the slash palette again; covered by `TestUAT_SlashOpensPalette` and `TestSlashModelTypedFlow`.
- **`BUG-003: Prefix chord ctrl+x ctrl+b unresponsive`** — prefix chords now resolve reliably and display correctly in help.
- **`Exit confirmation modal`** — shipped as the `stateQuitConfirm` popup in the TUI and covered by `TestQuitConfirmAcceptQuits`.
- **`Ctrl+b collision`** — fixed by implementing `ctrl+x` prefix chord system.
- **`modeBTW missing`** — added `modeBTW` constant, `startBtw()`, and `btwResultMsg`.
- **`AppExit regression`** — restored `ctrl+d` from accidental deletion.
- **`ActionDescriptions duplication`** — removed duplicate map.
- **`Render block comments`** — updated `kind` field comment.
