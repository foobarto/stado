---
ep: 20
title: Inline Context Completion
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Partial
type: Standards
created: 2026-04-24
see-also: [8, 13, 14]
history:
  - date: 2026-04-25
    status: Partial
    note: >
      Shell `.sh` and `.bash` top-level function declarations now appear
      as grouped @ picker symbol rows.
  - date: 2026-04-25
    status: Partial
    note: >
      JavaScript and TypeScript top-level class, function, and variable
      declarations now appear as grouped @ picker symbol rows.
  - date: 2026-04-25
    status: Partial
    note: >
      Python top-level class, def, and async def declarations now appear
      as grouped @ picker symbol rows.
  - date: 2026-04-25
    status: Partial
    note: >
      Root Markdown docs and docs/**/*.md now appear as grouped @ picker
      doc rows before ordinary file results.
  - date: 2026-04-25
    status: Partial
    note: >
      Go top-level declarations now appear as grouped @ picker symbol rows
      that insert file:line locations.
  - date: 2026-04-24
    status: Partial
    note: The TUI @ picker now groups built-in agents before repo files; sessions, symbols, docs, and skills remain future work.
  - date: 2026-04-25
    status: Partial
    version: v0.20.0
    note: Session rows now appear after agents; accepting a session-only mention switches sessions, and mixed-prompt mentions insert `session:<id>`.
  - date: 2026-04-25
    status: Partial
    version: v0.21.0
    note: Skill rows now appear after sessions; accepting a skill mention injects that skill's prompt body and removes the mention from the draft.
---

# EP-20: Inline Context Completion

## Problem

The TUI has a useful `@` file picker, but opencode-style workflows use a
single inline completion surface for more than files: agents, sessions,
symbols, docs, and skills can all be discovered without leaving the
message editor.

Separate pickers for every object type make the keyboard model harder to
learn. A unified `@` surface keeps the editor fast while making more
context attachable or selectable in place.

## Goals

- Use `@` as the primary inline completion surface.
- Group heterogeneous results so users can scan by type.
- Keep file-path insertion working as before.
- Let agent rows switch the active agent without leaving stale mention
  text in the draft.
- Leave room for sessions, symbols, docs, and skills.

## Non-goals

- Replacing the slash command palette.
- Loading file contents directly from the picker.
- Building a full symbol index in the first slice.

## Design

The first shipped slice keeps the existing trigger behavior: `@` opens
only at the start of input or after whitespace, so email addresses do not
trigger completion.

Results are now typed internally. Built-in agents appear first:

- Do
- Plan
- BTW

Accepting an agent row switches the active agent and consumes the
`@query` fragment from the draft. File rows still insert the selected
repo-relative path plus a trailing space.

Session rows appear after agents and before files. Accepting a session
row when the mention is the whole draft switches the TUI to that
session and consumes the mention. Accepting a session row inside a
longer prompt inserts `session:<id>` instead of switching, so typed
draft content is not silently moved to another session.

Skill rows appear after sessions and before docs/files. Accepting a skill
row injects the skill body using the same conversation behavior as
`/skill:<name>`, then removes the `@query` fragment from the draft.

Doc rows appear after skills and before symbol/file rows. The first docs
slice indexes root Markdown/MDX files plus `docs/**/*.md` and
`docs/**/*.mdx`, capped to keep the picker responsive. Accepting a doc
row inserts the path, matching normal file-row behavior.

Symbol rows appear after docs and before ordinary file rows. The first
symbol slices index top-level Go declarations with `go/parser` and
top-level Python `class`, `def`, and `async def` declarations with a
bounded line scanner. JavaScript and TypeScript slices index top-level
class, function, and variable declarations with the same bounded scanner.
Shell script slices index top-level `.sh` and `.bash` function
declarations in `name() {}` and `function name {}` forms. Accepting a
symbol row inserts `path:line`, giving the model precise context without
loading file contents.

## Test strategy

- Unit tests for grouped candidate ordering and selected item metadata.
- TUI integration tests for agent accept and file accept regressions.
- Real-PTY UAT after the next non-file category ships.

## Open questions

- Which additional language symbol scanners should be added without
  turning `@` into an unbounded indexer?
