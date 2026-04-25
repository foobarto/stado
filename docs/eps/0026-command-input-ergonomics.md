---
ep: 26
title: Command Input Ergonomics
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
implemented-in: v0.12.0
see-also: [19, 20, 24, 25]
history:
  - date: 2026-04-25
    status: Implemented
    note: Inline slash suggestion rows are now covered by tests that assert command IDs and secondary shortcut hints render together.
  - date: 2026-04-25
    status: Implemented
    note: Inline slash suggestions now retain command group labels from the modal palette.
  - date: 2026-04-24
    status: Implemented
    version: v0.12.0
    note: Slash suggestions moved inline, Ctrl+P remained the modal command palette, and the input rail now reflects the active mode.
---

# EP-26: Command Input Ergonomics

## Problem

The editor had too many command-discovery paths doing the same thing.
Typing `/` opened the same modal command palette as `Ctrl+P`, which made
slash commands feel detached from the message draft and prevented the
input area from carrying immediate mode feedback.

## Goals

- Keep `Ctrl+P` as the full modal command palette.
- Make an empty-prompt `/` open compact fuzzy slash suggestions above
  the chat input.
- Preserve typed slash-command execution for submitted text such as
  `/model <id>`.
- Keep Do, Plan, and BTW visually distinct in the input rail.
- Avoid stealing literal `/` characters from non-empty drafts.

## Non-goals

- Replacing the command palette.
- Replacing the `@` context completion surface.
- Changing slash-command names or semantics.

## Design

The TUI uses the same command catalog and fuzzy matcher for both command
surfaces:

- `Ctrl+P` opens the modal command palette.
- `/` at an empty prompt opens an inline suggestion box above the input.
- Inline suggestions keep compact group labels so Quick, Session, and
  View commands do not collapse into one undifferentiated list.
- Rows show the slash command ID and any secondary keyboard shortcut
  together so users can run a command either way.
- `/` in a non-empty prompt remains literal text.
- `Enter` on a suggestion fills and runs the selected slash command.
- `Esc` closes the active suggestion/palette surface and returns focus
  to the textarea.

The chat input rail color is mode-derived:

- Do uses the normal user rail color.
- Plan uses the thinking/plan rail color.
- BTW uses the accent rail color.

## Test Strategy

- In-process TUI tests cover `/` opening inline suggestions and `Ctrl+P`
  opening the modal palette.
- Key registry tests ensure `/` is no longer bound to the command-list
  action.
- UAT coverage checks slash suggestion close behavior and resumed typing.
- Render-focused tests cover the mode-derived input rail tone.

## Open Questions

- None.
