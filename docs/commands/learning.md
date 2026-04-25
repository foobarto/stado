# `stado learning`

Propose and inspect reviewable operational lessons from solved work.

## What It Does

`stado learning` is the first EP-16 surface. It stores lessons in the
same append-only memory log as `stado memory`, but marks them with
`memory_kind: "lesson"` and requires a trigger plus evidence. Lessons
start as `candidate`; they only enter prompts after explicit approval
through `stado learning approve <id>`.

Approved lessons are injected in a separate "Operational lessons"
section after ordinary memory when `[memory].enabled = true`.

## Common Flow

```sh
stado learning propose \
  --summary "Use pinned Go toolchain" \
  --lesson "Use the pinned toolchain path before declaring Go unavailable." \
  --trigger "When Go tooling is missing from PATH." \
  --evidence "Local verification used the repo-pinned Go binary." \
  --test "go test ./..."

stado learning list
stado learning show lesson_...
stado learning edit lesson_... \
  --trigger "When Go tooling is missing from PATH in this repo." \
  --rationale "The repo pins a Go toolchain under the module cache."
stado learning approve lesson_...
stado learning supersede lesson_... \
  --summary "Use the current release checklist" \
  --lesson "Run the current release checklist before declaring a release complete." \
  --trigger "When cutting or validating a release." \
  --evidence "The prior release lesson was stale."
stado learning document lesson_...
```

Use `--scope global`, `--scope repo`, or `--scope session`. Repo scope
is the default and resolves the current repo id automatically. Session
scope requires `--session-id`.

## Commands

| Command | Purpose |
|---------|---------|
| `stado learning propose` | Append a lesson candidate to the memory log |
| `stado learning list` | List folded lesson items |
| `stado learning show <id>` | Print one lesson item as JSON |
| `stado learning edit <id>` | Append a lesson-specific edit event |
| `stado learning approve <id>` | Promote a candidate lesson to approved |
| `stado learning supersede <id>` | Replace an approved lesson with a new approved lesson |
| `stado learning reject <id>` | Mark a lesson rejected |
| `stado learning delete <id>` | Remove a lesson from the folded active view |
| `stado learning document <id>` | Write the lesson to `.learnings/` and reject it from prompt retrieval |

## Notes

Lessons are reviewable guidance, not system-prompt edits. Current user
messages, repo instructions, and the active task override them. Bad or
stale lessons can be rejected, deleted, edited, or superseded with
lesson-specific commands. The generic `stado memory` review commands
still operate on the same append-only store for audit and recovery work.

`stado learning document <id>` is the explicit "document elsewhere"
path: it writes a Markdown note under `.learnings/`, refuses to
overwrite an existing file, and marks the lesson rejected so it is not
retrieved for prompts.
