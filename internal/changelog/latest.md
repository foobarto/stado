## v0.62.0 — configurable TUI chrome + plugin display API + landing what's-new — 2026-06-11

### TUI

- **Configurable sidebar sections + footer segments (#21 part 1).**
  `[tui.sidebar].sections` picks which sidebar sections show **and their
  order**; `[tui.footer].segments` picks which footer segments are visible
  (the footer's order is fixed by its template). An empty/absent list means
  "use the defaults" (not "hide everything") — list the ids you want to keep
  to hide the rest. Unknown ids are preserved (they may be plugin panel ids).
  Re-reads on `/reload`. The sidebar width also persists across sessions (#24).
- **"What's new" on the landing page (#22/#23).** The landing screen surfaces
  a brief summary of the latest CHANGELOG entry in the upper-left corner.

### Plugins

- **Plugin display API — sidebar / footer / log render targets (#21 part 2).**
  A `stado_ui_render` panel gains an additive `target` field: `viewport`
  (default, unchanged conversation scrollback), `sidebar` / `footer` (a plugin-
  owned, addressable panel that shows when the operator lists the panel's `id`
  in `[tui.sidebar].sections` / `[tui.footer].segments`; last-write-wins per
  id), or `log` (one bounded line appended to the shared notification log).
  Plugins cannot write to stado's built-in sections/segments; `Variant` maps to
  a SAFE theme tone (never a raw colour); the per-id panel stores are capped.
  Non-TUI render channels (ACP / MCP / headless) ignore `target`.

### Fixes

- **Landing sheep logo no longer deforms on short terminals (#116).** The
  banner renders at its natural aspect when there's vertical room and falls
  back to the compact wordmark when there isn't, instead of being squashed; the
  gap between the logo and the input box is now a fixed margin.

