# `stado memory`

Review the local memory store used by plugins that declare `memory:*`
capabilities.

## What It Does

`stado memory` lists, inspects, edits, approves, rejects, deletes,
supersedes, and exports memory items stored under the stado state directory.
Plugin-proposed items start as `candidate`; they are not returned to
memory queries until approved.

Approved memories are only injected into provider prompts when
`[memory].enabled = true` in `config.toml`. Injection is bounded by
`[memory].max_items` and `[memory].budget_tokens`, and the prompt block
is labeled as untrusted context below stado identity and project
instructions. TUI, `stado run`, headless, and ACP use the same prompt
context path.

## Common Flow

```sh
stado memory list
stado memory show mem_...
stado memory edit mem_... --summary "Prefer small diffs" --body "Keep changes focused."
stado memory approve mem_...
stado memory supersede mem_... --summary "Prefer reviewable replacements"
stado memory reject mem_...
stado memory delete mem_...
stado memory export > memories.json
```

Use `stado memory list --json` for scripts.

## Commands

| Command | Purpose |
|---------|---------|
| `stado memory list` | Show the folded memory view |
| `stado memory show <id>` | Print one memory item as JSON |
| `stado memory edit <id>` | Append an edit event for a folded item |
| `stado memory approve <id>` | Promote a candidate to approved |
| `stado memory supersede <id>` | Replace an approved memory with a new approved item |
| `stado memory reject <id>` | Mark a memory rejected |
| `stado memory delete <id>` | Remove a memory from the folded active view |
| `stado memory export` | Export folded items as JSON |

## Notes

The backing store is append-only JSONL. Delete and reject operations add
events; they do not rewrite old events. Edit operations also append a
new event, replacing only the folded active view. Prompt retrieval
remains scoped: only approved, non-secret items matching the requested
global, repo, or session scope are returned through `memory:read`.
Supersede operations append a new approved item and mark the old item
`superseded` in the folded view instead of rewriting the original
event.

Prompt retrieval is opt-in. Candidate, rejected, deleted, expired, and
`secret` memories are never injected into prompts; they remain visible
through review/export surfaces for auditability.
