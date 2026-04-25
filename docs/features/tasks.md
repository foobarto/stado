# Shared Tasks

stado has a shared task store for work that should be visible to both
the user and the agent. The TUI exposes it as a browser/editor, and the
model sees the same store through the `tasks` tool whenever tools are
enabled.

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
- [commands/run.md](../commands/run.md) - enabling tools from scripts
- [commands/mcp-server.md](../commands/mcp-server.md) - exposing the tool to MCP clients
