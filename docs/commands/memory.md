# `stado memory`

Review the local memory store used by plugins that declare `memory:*`
capabilities.

## What It Does

`stado memory` lists, inspects, edits, approves, rejects, deletes,
supersedes, and exports memory items stored under the stado state directory.
Plugin-proposed items start as `candidate`; they are not returned to
memory queries until approved.

Approved memories are injected into provider prompts by default; set
`[memory].enabled = false` in `config.toml` to opt out. Only
user-approved, scoped, non-secret items are ever injected, so the
default-on surface is reviewed context, not silent capture. Injection is
bounded by `[memory].max_items` and `[memory].budget_tokens`, and the
prompt block is labeled as untrusted context below stado identity and
project instructions. TUI, `stado run`, headless, and ACP use the same
prompt context path.

Session-scoped memories are inherited down the fork tree: a session sees
the session-scoped memories of every session it forked from (EP-15), so a
decision recorded in a session survives a compaction/fork into its
descendants. Repo- and global-scoped memories apply to the whole repo and
to every repo respectively.

## Common Flow

```sh
stado memory list
stado memory show mem_...
stado memory edit mem_... --summary "Prefer small diffs" --body "Keep changes focused."
stado memory approve mem_...
stado memory supersede mem_... --summary "Prefer reviewable replacements"
stado memory reject mem_...
stado memory delete mem_...
stado memory compact
stado memory session off
stado memory session on
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
| `stado memory delete <id>` | Hide a memory from retrieval, keeping a `deleted` audit tombstone |
| `stado memory compact` | Rewrite the log to its folded state (one event per live item) |
| `stado memory session [on|off|status]` | Toggle approved-memory retrieval for the current session/worktree |
| `stado memory export` | Export folded items as JSON |

## Notes

The backing store is append-only JSONL. Delete, reject, and supersede
operations append events rather than rewriting old ones. Delete and reject
keep an audit tombstone in the folded view (`deleted` / `rejected`), so the
item stays visible through `list`/`show`/`export`; it is simply excluded
from retrieval. Supersede appends a new approved item and marks the old
item `superseded`. Edit operations also append a new event, replacing only
the folded active view. Prompt retrieval remains scoped: only approved,
non-secret items matching the requested global, repo, or session scope (the
querying session or any of its ancestors) are returned through
`memory:read`.

Because the log only grows, `stado memory compact` rewrites it to its
folded state — exactly one event per live item, tombstones included — to
reclaim space without changing which memories are active. Reads degrade
gracefully past the 128 MB store cap (so an oversized store can still be
inspected and compacted); writes are refused over the cap until `compact`
brings the log back under it.

Prompt retrieval is on by default and can be opted out globally with
`[memory].enabled = false`, or per session with `stado memory session off`,
which creates a `.stado/memory-disabled` marker in the current
session/worktree (`stado memory session on` removes it). Candidate,
rejected, deleted, superseded, expired, and `secret` memories are never
injected into prompts; they remain visible through review/export surfaces
for auditability.

Lesson items created by `stado learning` live in the same log with
`memory_kind: "lesson"`. Approved lessons are retrieved separately from
ordinary memory and rendered in an "Operational lessons" prompt section.
Plain `memory:read` queries without `memory_kind` return ordinary memory
only; callers must request `memory_kind: "lesson"` explicitly.
