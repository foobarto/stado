---
ep: 19
title: Model and Provider Picker UX
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
see-also: [3, 10]
history:
  - date: 2026-04-25
    status: Implemented
    note: >
      The scoped model/provider picker goals are complete; true
      provider connect/OAuth is future provider-specific product work.
  - date: 2026-04-25
    status: Partial
    note: Local-runner detection now distinguishes LM Studio installed models from loaded/runnable models for fallback, doctor, and `/providers`.
  - date: 2026-04-25
    status: Partial
    note: /providers now includes active-provider credential env var health.
  - date: 2026-04-25
    status: Partial
    note: /provider <name> now prints provider setup and remediation guidance directly.
  - date: 2026-04-25
    status: Partial
    note: /providers now includes local-runner remediation when a reachable runner has no models loaded.
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
- `/providers` includes runner-specific next steps when a local endpoint
  is reachable but exposes no loaded models, for example LM Studio's
  developer page or `lms load <model>`.
- LM Studio's OpenAI-compatible `/models` endpoint can list installed
  models that are not loaded. Stado probes LM Studio's local API for
  loaded state and uses only loaded models for automatic local fallback
  and picker rows; doctor and `/providers` still report installed counts
  with load guidance.

Favorites and recents live in stado state rather than `config.toml` so
they remain per-machine ergonomic history. Config remains reserved for
explicit defaults and provider endpoints. Favorites for unavailable
providers remain visible as user intent; provider setup and `/providers`
explain missing credentials, local-runner load state, or startup steps
without silently deleting the favorite.

Provider credentials are not launched or stored by the picker. The
picker and status surfaces show setup/remediation hints; true
provider-specific connect/OAuth flows are future product work outside
this scoped picker EP.

## Test strategy

- Unit tests for current/favorite/recent markers and ordering.
- TUI update-flow tests for selection, provider switching, and favorite
  toggling.
- Real-PTY UAT once provider-empty states and connect actions exist.

## Open questions

- None for the current manual setup model.
