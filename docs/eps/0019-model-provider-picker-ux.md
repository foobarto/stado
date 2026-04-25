---
ep: 19
title: Model and Provider Picker UX
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [3, 10]
history:
  - date: 2026-04-25
    status: Partial
    version: v0.22.0
    note: Model picker gained Ctrl+A provider setup/remediation hints for selected rows; full provider connect/OAuth remains future work.
  - date: 2026-04-24
    status: Partial
    note: Current marker, recents, provider labels, and model favorites have shipped; provider connection actions remain future work.
  - date: 2026-04-24
    status: Partial
    version: v0.12.0
    note: Model selection gained a direct shortcut and now persists the chosen provider/model as the new config default.
---

# EP-19: Model and Provider Picker UX

## Problem

The TUI can switch models, but model/provider choice is a high-frequency
workflow and should be fast, searchable, and stateful. Users need a
picker that keeps common models close, makes provider routing obvious,
and offers clear next actions when credentials or local runners are not
ready.

## Goals

- Show the current model/provider clearly.
- Persist user intent with favorites and recent selections.
- Surface provider labels so picking a local-runner model also switches
  the backend users expect.
- Leave room for provider connect/credential actions.

## Non-goals

- Replacing config-file defaults.
- Hiding provider identity from advanced users.
- Storing secrets in picker state.

## Design

The first shipped slices are:

- `/model` with no args opens a fuzzy picker.
- Catalog rows carry provider labels and switching provider-aware rows
  updates the active backend.
- The current model is marked.
- Recent model/provider selections are persisted under stado state and
  appear near the top.
- `Ctrl+X M` opens the picker without going through `/model`.
- Accepting a model row, or running `/model <id>`, updates
  `[defaults].provider` and `[defaults].model` in config so the choice
  becomes the next startup default.
- `Ctrl+F` inside the picker toggles a persistent favorite; favorites
  appear before recents.
- `Ctrl+A` inside the picker shows provider-specific setup for the
  selected row: missing API-key env vars, configured preset endpoints,
  or local-runner startup hints. The picker does not store secrets.

Future work should add true provider connect/OAuth flows where
providers support them, richer empty states, and a clearer distinction
between configured providers and detected local runners.

## Test strategy

- Unit tests for current/favorite/recent markers and ordering.
- TUI update-flow tests for selection, provider switching, and favorite
  toggling.
- Real-PTY UAT once provider-empty states and connect actions exist.

## Open questions

- Should favorites live in config for syncability or state for
  per-machine ergonomics?
- Should provider credentials be launched from the picker or a separate
  status modal?
- How should favorites behave when the provider is unavailable?
