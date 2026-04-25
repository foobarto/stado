---
ep: 22
title: Theme Catalog and Picker
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [19]
history:
  - date: 2026-04-25
    status: Partial
    note: Added stado-rose to the bundled theme catalog.
  - date: 2026-04-25
    status: Partial
    note: Custom theme.toml files can now choose markdown renderer style with [markdown].style.
  - date: 2026-04-25
    status: Partial
    note: The theme picker now shows the currently loaded custom theme.toml row.
  - date: 2026-04-25
    status: Partial
    note: Markdown rendering now switches between dark and light Glamour styles based on theme background luminance.
  - date: 2026-04-25
    status: Partial
    note: Direct /theme light, /theme dark, and /theme toggle shortcuts shipped on top of the picker.
  - date: 2026-04-24
    status: Partial
    note: Bundled dark, light, and contrast themes plus a TUI picker shipped; richer theme authoring remains future work.
---

# EP-22: Theme Catalog and Picker

## Problem

Stado supports a user-authored `theme.toml`, but users had to leave the
TUI and edit a file to try a different visual mode. Opencode exposes
theme switching in-app, which makes appearance an ordinary command
surface instead of a configuration chore.

## Goals

- Ship a small catalog of bundled themes.
- Include at least one light theme and one high-contrast dark theme.
- Expose theme switching from the TUI command palette and keybindings.
- Keep `theme.toml` as the durable override path for power users.

## Non-goals

- A full theme editor.
- Importing external theme packs.
- Reworking every legacy subpackage to accept explicit theme handles in
  this slice.

## Design

The bundled catalog starts with:

- `stado-dark`
- `stado-light`
- `stado-contrast`
- `stado-rose`

`/theme` and `Ctrl+X T` open a searchable picker. `/theme <id>` switches
directly. `/theme light`, `/theme dark`, and `/theme toggle` are mode
shortcuts for users who do not need the picker. Selecting a bundled
theme updates the running TUI and writes the bundled TOML to
`$XDG_CONFIG_HOME/stado/theme.toml`, preserving the existing load path
on the next run.

The runtime applies the selected theme through the explicit renderer
theme and the legacy package-level theme globals so existing picker and
editor components move together.

Markdown rendering uses the `[markdown].style` setting from
`theme.toml`. Supported values are `auto`, `light`, and `dark`; `auto`
uses the active theme background luminance. The renderer clears its
markdown cache when a theme is switched.

If the loaded theme name does not match a bundled catalog entry, the
picker appends it as the current custom `theme.toml` row. Selecting that
row is a no-op; selecting a bundled row still materializes that bundled
TOML into the durable theme override path.

## Test Strategy

- Unit tests load every bundled catalog entry.
- Theme tests cover custom `[markdown].style` loading and fallback merge.
- Picker tests cover current marker and fuzzy filtering.
- TUI tests cover `/theme`, `Ctrl+X T`, command-palette dispatch, block
  cache invalidation, and persisted `theme.toml` output.

## Open Questions

- Should named themes be stored as config keys instead of materialized
  `theme.toml` files?
