Sidebar assertions for the right pane should target signals, not exact full lines.

Why:
- the sidebar is intentionally narrow
- `wrapHard` in the template will split long strings across lines
- `trimSeed` may truncate queued/recovery text before the template wraps it

Practical rule:
- assert section headers and stable substrings like `queued:` or `ctx 82% / hard 90%`
- do not assert the exact full queued-prompt preview or an exact wrapped line shape

This keeps sidebar tests stable across small wording/layout changes while still guarding the useful-at-a-glance behavior.
