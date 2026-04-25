# `stado learning`

Propose and inspect reviewable operational lessons from solved work.

## What It Does

`stado learning` is the first EP-16 surface. It stores lessons in the
same append-only memory log as `stado memory`, but marks them with
`memory_kind: "lesson"` and requires a trigger plus evidence. Lessons
start as `candidate`; they only enter prompts after explicit approval
through `stado memory approve <id>`.

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
stado memory approve lesson_...
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

## Notes

Lessons are reviewable guidance, not system-prompt edits. Current user
messages, repo instructions, and the active task override them. Bad or
stale lessons can be rejected, deleted, edited, or superseded with the
same `stado memory` review commands used for ordinary memories.
