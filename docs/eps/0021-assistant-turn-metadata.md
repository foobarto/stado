---
ep: 21
title: Assistant Turn Metadata
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [11, 13, 19]
history:
  - date: 2026-04-24
    status: Partial
    note: Assistant responses now render compact per-turn footers; richer tool/status drilldowns remain future work.
---

# EP-21: Assistant Turn Metadata

## Problem

The status bar shows current aggregate usage, but users often need to
understand the just-finished assistant response in context: which agent
answered, which model/backend handled it, how long it took, whether it
used tools, and what the token/cost delta was.

Without per-turn metadata, that information is scattered across the
sidebar, logs, and provider traces.

## Goals

- Attach compact metadata to completed assistant responses.
- Keep the footer quiet enough not to dominate the transcript.
- Use per-turn usage deltas when providers report them.
- Preserve aggregate status-bar and sidebar behavior.

## Non-goals

- Rendering a full trace viewer in the chat log.
- Showing hidden provider-native payloads.
- Reconstructing exact metadata for old persisted transcripts.

## Design

Completed assistant blocks receive a muted footer containing:

- active agent
- model and provider used for the turn
- elapsed wall-clock duration
- tool-call count
- input/output token delta when available
- cost delta when available

The TUI captures model/provider/agent at stream start so toggles made
while a response is streaming do not rewrite the footer after the fact.

Future work should add a richer status modal for provider/plugin/MCP
health and possibly expandable per-turn trace details.

## Test strategy

- Unit tests for footer composition.
- Render tests that assistant body and footer both survive formatting.
- Real-PTY UAT remains focused on broader TUI regressions; add specific
  footer assertions once deterministic provider usage fixtures exist.

## Open questions

- Should tool counts include failed or rejected calls separately?
- Should footers show cache read/write deltas?
- Should persisted transcripts store footer metadata or reconstruct it
  from trace refs on resume?
