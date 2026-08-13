# Shared Tasks

stado has a shared task store for work that should be visible to both
the user and the agent. The TUI exposes it as a browser/editor, and the
model sees the same store through the `tasks` tool whenever tools are
enabled.

The tool is part of the default model-facing tool surface. stado's default
agent instructions treat the store as a deferred-work inbox: when a user sends
an unrelated request during active work, the agent records it instead of
silently replacing the current task, then revisits open tasks after the active
task finishes. Automatic continuation is limited to task IDs deferred by that
conversation; agents do not claim arbitrary items from the global store.
Requests that correct, constrain, or directly extend the active work remain
part of that work.

When state-mutating tools are unavailable, including Plan mode and `--no-tools`,
or a task-store write fails, the default instructions require the agent to
identify the request as an unpersisted deferred item and disclose that it could
not write the shared store.

## What It Stores

Each task has:

- `id`
- `title`
- optional `body`
- `status`: `open`, `in_progress`, or `done`
- created and updated timestamps

The store lives under stado state:

```sh
$XDG_STATE_HOME/stado/tasks/tasks.json
```

If `XDG_STATE_HOME` is unset, stado uses the platform default from the
config loader.

## TUI

Open the task manager with:

```text
/tasks
Ctrl+X K
```

The task manager lets you browse, filter/search, view detail, create,
edit, delete, and change status without leaving the TUI.

You can also create a quick task from the input:

```text
/tasks add Review release notes
```

## Agent Tool

When tools are enabled, the model can call the `tasks` tool with:

- `create`
- `list`
- `read`
- `update` / `edit`
- `delete`

`tasks` is a state-mutating tool. It updates the shared task store and
writes trace metadata for audit, but it does not create a worktree tree
commit because the task file is external state, not a repo change.

Plan mode only exposes non-mutating tools, so `tasks` is hidden there.
Use Do mode when the model should manage tasks.

This is behavioral guidance rather than a scheduler: the store does not yet
associate tasks with a repository or session, inject the backlog into every
turn, or dispatch tasks automatically when a model loop ends.

`/supervise` adds a stronger host-owned path for high-assurance work: prompts
received during an active run enter a durable inbox, a fresh watchdog classifies
them at a safe boundary, and unrelated/uncertain prompts are written here with
deduplication before the inbox item is acknowledged. The store itself remains
global CRUD rather than a project scheduler.

## Bounds And Safety

The task store is intentionally bounded:

- task id: 128 bytes
- title: 256 bytes
- body: 16 KiB
- total tasks: 1000
- store file: 128 MiB
- model-facing `list`: capped at 50 summaries by default
- model-facing task output: capped before entering model context

Writes use a process mutex plus a lock file next to the store to avoid
lost updates across concurrent TUI/tool/MCP calls. Persisted task files
are validated on load, so oversized or malformed data is rejected before
it can be returned to the model.

## See Also

- [commands/tui.md](../commands/tui.md) - TUI keybinds and slash commands
- [features/slash-commands.md](slash-commands.md) - command palette entries
- [features/supervise.md](supervise.md) - host-enforced single-focus work and durable prompt deferral
- [commands/run.md](../commands/run.md) - enabling tools from scripts
- [commands/mcp-server.md](../commands/mcp-server.md) - exposing the tool to MCP clients
