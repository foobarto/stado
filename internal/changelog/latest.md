## v0.66.2 — loop/clear/model correctness — 2026-06-13

Three TUI correctness fixes from the UAT-deferred backlog: a `/loop` no longer
runs past the budget/context caps, `/clear` stops the background loop+monitor it
was driving, and `/model` warns on an unknown model id.

### Fixes

- **`/loop` respects the budget hard-cap and context hard-threshold.** A manual
  Enter is blocked at `[budget].hard_usd` / the context hard bound, but a loop
  iteration started its turn directly and bypassed both — so an unattended
  timed/immediate loop could run past the cost cap. A loop iteration now stops
  the loop (with an explanation) when either gate is breached, rather than
  spending past the cap with no one present to `/budget ack` or `/compact`.
- **`/clear` stops the active `/loop` and `/monitor`.** Clearing the
  conversation left the loop running (a stale `↻` indicator over a wiped
  context) and the monitor goroutine streaming into a cleared screen. `/clear`
  now halts both.
- **`/model <id>` warns on an unknown model.** Setting a model id absent from
  the active provider's known catalog now prints a typo warning (the model is
  still set — the catalog isn't exhaustive). Providers without a static catalog
  (local runners, presets, OAI-compat) don't warn.

