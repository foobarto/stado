---
ep: 14
title: Multi-Session TUI
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [4, 7, 10, 11]
history:
  - date: 2026-04-25
    status: Partial
    note: >
      Session switching now preserves each session's selected
      provider/model and resets provider capability probes when the
      restored provider changes.
  - date: 2026-04-25
    status: Partial
    note: >
      Session switching now waits for running or queued background plugin
      ticks so session-aware plugin events cannot cross into a different
      active session.
  - date: 2026-04-25
    status: Partial
    version: v0.23.0
    note: In-process session switching now caches per-session editor drafts and chat scroll offsets.
  - date: 2026-04-24
    status: Placeholder
    note: Captures the request for creating, listing, deleting, and switching sessions in one TUI process.
  - date: 2026-04-24
    status: Partial
    note: First TUI slice shipped: searchable switcher plus new-session action.
  - date: 2026-04-24
    status: Partial
    note: Session overlay now supports switch/resume, new, rename, fork, and confirmed delete.
---

# EP-14: Multi-Session TUI

## Problem

The TUI currently behaves like a single active session/worktree view.
Users who want to compare or switch between sessions need separate
processes or shell commands. This makes multi-threaded work awkward and
prevents an opencode-style workflow where one TUI can create, list,
delete, and switch sessions.

## Goals

- Let one TUI process manage multiple stado sessions.
- Support create, list, delete/close, and switch actions.
- Preserve conversation state, scroll state, provider state, and pending
  work per session.
- Keep session identity visible enough that users know where tool calls
  and writes will land.

## Non-goals

- Running every session actively at the same time by default.
- Replacing CLI `stado session` commands.
- Hiding git/worktree boundaries from users.

## Design

The first shipped slice used a command-palette style session overlay:

- `ctrl+x l` opens a searchable overlay of sessions for the current repo
- `ctrl+x n` creates a fresh session and switches to it immediately
- `/switch` opens the same overlay from slash/command-palette paths
- `/new` creates and switches to a fresh session from slash/command-palette paths
- `/sessions` remains an informational list with CLI resume hints

The second shipped slice turns the overlay into a session manager:

- `Enter` switches/resumes the highlighted session
- `Ctrl+N` creates and switches to a fresh session
- `Ctrl+R` renames the highlighted session
- `Ctrl+F` forks the highlighted session and switches to the child
- `Ctrl+D` opens a delete confirmation for an inactive highlighted session
- `Esc` closes the overlay or cancels the current rename/delete action

For this slice, the TUI swaps one active model record rather than
running multiple live tab models. Switching replaces the active
`session`, `executor`, conversation messages, rendered blocks, usage
state, and viewport content, then reloads persisted conversation JSONL
from the target worktree.

Switching is intentionally blocked while there is active work: queued
prompts, streaming turns, approval cards, compaction confirmation/editing,
running tools, and running or queued background plugin ticks must finish
or be cleared first. This avoids hidden background mutation and
wrong-session sends until inactive-session execution policy expands
beyond active-session-only execution.

The TUI keeps lightweight in-process UI state per session: unsubmitted
editor draft, chat scroll offset, selected provider/model, and provider
capability-probe state are cached when a session becomes inactive and
restored when switching back. Restoring a different provider invalidates
the live provider object so the next prompt rebuilds against the active
session's provider.

Background-running inactive sessions remain future work. The current
policy is active-session-only: inactive sessions do not stream, run
tools, or receive background plugin events inside the same TUI process.

## Migration / rollout

The first rollout included list/switch/create. The second adds delete,
rename, and fork. The third adds per-session cached scroll/draft state.
Add inactive background execution only after safety checks are clear.

## Failure modes

- User sends a prompt to the wrong session.
- Deleting a session with uncommitted/generated work causes data loss.
- Background streams keep running after the user switches away.
- Memory use grows with many loaded session models.

## Test strategy

- Unit tests for session list/switch/create state transitions.
- TUI scenario tests for create/switch/delete flows.
- Persistence tests to verify scroll/conversation state survives
  switching and process restart.

## Open questions

- Should inactive sessions continue streaming or pause/cancel?
- Does delete mean hide from the list, remove sidecar refs, remove
  worktree, or all of those with confirmation?
- Should the UI be tab-like, command-palette based, or a dedicated
  session pane?
- How should queued prompts behave when switching sessions?

## Decision log

### D1. Capture as a Standards EP

- **Decided:** multi-session TUI management needs its own Standards EP.
- **Alternatives:** fold it into EP-4 or track as a bug.
- **Why:** EP-4 covers the session/audit substrate; this proposal changes
  the live TUI interaction model and safety contract.

## Related

- EP-4 Git-Native Sessions and Audit Trail
- EP-7 Conversation State and Compaction
