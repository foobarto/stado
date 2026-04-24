# `stado agents`

Inspect and manage parallel agent sessions for the current repo.

## What it does

`stado agents` is the narrow operational surface around session-backed
parallel worktrees:

- `agents list` shows the sessions that look alive or have real history
- `agents attach <id>` prints a worktree path for shell composition
- `agents kill <id>` terminates the owning process if known and removes
  the worktree

This is intentionally smaller than `stado session`. `session` is the
full lifecycle and history surface; `agents` is the “what is currently
running and how do I get into or stop it?” view.

## Why it exists

Once `session fork` landed, parallel sessions were cheap, but the
ergonomics were poor: you could create another worktree, but there was
no focused operational view for live or stale agents. `stado agents`
solves that without turning the richer `session` UI into a process
manager.

## Subcommands

### `agents list`

Shows the sessions that are worth looking at right now.

Default behavior:

- rows with a live PID are shown
- rows with committed tree/trace refs are shown
- stale + empty rows are hidden and summarized in a footer

Use `--all` to include the stale/empty rows too.

Output shape:

```text
<id>    <pid-or->    tree=<short-sha-or->    trace=<short-sha-or->
```

Examples:

```sh
stado agents list
stado agents list --all
```

### `agents attach <id>`

Prints the worktree path for a session so you can `cd` into it from a
shell without launching the TUI.

```sh
cd "$(stado agents attach <id>)"
```

### `agents kill <id>`

Reads `<worktree>/.stado-pid` if present, sends a termination signal to
that pid when it still exists, then removes the worktree directory.

It is an operational cleanup tool, not a ref-deletion command: the
session history in the sidecar repo is left alone.

```sh
stado agents kill <id>
```

## Relationship to `stado session`

- Use `stado session fork` to create another agent session.
- Use `stado agents list` to see what is currently active.
- Use `stado session show` / `logs` / `export` when you need history,
  usage, or conversation detail.

## Gotchas

- `agents kill` removes the worktree, not the sidecar refs. If you want
  to fully delete a session, use `stado session delete`.
- IDs are validated as local session IDs before joining paths, so path
  traversal forms like `../../foo` are rejected.
- `agents list` hiding stale rows by default is intentional; use
  `--all` when you are debugging cleanup behavior.
