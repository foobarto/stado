# Tasks Lifecycle Application

Persistent tasks are owned by the explicit official `tasks` WASM lifecycle
application. Core stado has no task store, model tool, picker, MCP registration,
or default autoload. Installing a package does not activate it: the operator
must opt its exact installed store key into the TUI lifecycle-application set.
The unpublished development source is staged at `tasks/` in the official plugin
repository; signing and publication remain release work.

Once admitted, the same persistent application instance owns:

- the dynamic `/tasks` operator command;
- the `tasks` model tool on ordinary Do-mode turns for that exact session;
- the plugin-defined global `task` artifact namespace.

The tool is deliberately absent from Plan and BTW, other lifecycle
applications, worker children, `stado run`, headless, ACP, and MCP. Global
enabled/disabled and per-session disables remain ceilings. If the application
is absent or its callback fails, stado does not fall back to native behavior.

## Data and operations

Each artifact version stores a bounded data object with `title`, optional
`body`, `status` (`open`, `in_progress`, or `done`), and `deleted`. The broker
envelope owns the ID, version, timestamps, provenance, and global principal
binding. Delete is a versioned logical tombstone (`deleted: true`), never a
physical removal or JSON-file write.

The model tool supports `create`, `list`, `read`, `update` / `edit`, and
`delete`. `create` requires a bounded caller-selected `idempotency_key`; the
same logical retry must reuse the same key. Reuse with different data fails
closed. Lists select only live heads, are bounded to 1,000 tasks, and return at
most 50 entries. Updates that normalize to the current data return the current
version instead of minting a no-op version after reply loss.

The command surface is:

```text
/tasks
/tasks list
/tasks add --key <idempotency-key> <title>
/tasks read <id>
/tasks delete <id>
```

Bare `/tasks` uses the generic `stado_ui_choose` bridge. It asks for an explicit
idempotency key before an interactive create. There is no fixed task keybinding
or static palette row; the command appears dynamically only while the signed
application is admitted. `/todo <title>` remains a separate session-local
sidebar note and does not persist a task.

## One-way legacy import

On first admitted use, the application examines only
`cfg:state_dir/tasks/tasks.json`. Missing is not an error. Any unreadable,
malformed, concurrently changing, non-regular, or oversized source fails
closed. The generic guest read ceiling is exactly 16 MiB; larger legacy files
are never read, truncated, overwritten, archived-as-success, or silently
dropped and must be copied aside for manual recovery.

For a valid legacy array, the application:

1. proposes every item as a global `task` artifact with a digest-bound stable
   broker idempotency key and migration tag;
2. re-queries and byte-compares every exact immutable artifact ID/version;
3. writes the exact original bytes to `tasks.json.archive-<sha256>` and reads
   the archive back;
4. re-reads the source immediately to detect a concurrent legacy writer;
5. replaces `tasks.json` with a strict receipt binding the source digest,
   archive, count, and every exact artifact ID/version/data digest, then reads
   that receipt back.

A crash before the receipt leaves the original source available for an
idempotent retry. A completed receipt revalidates the archive and immutable
artifact versions; later ordinary edits and tombstones do not invalidate that
migration proof. There is no dual write, and the deleted native writer cannot
be reactivated.

## See also

- [Lifecycle hooks and applications](lifecycle-hooks.md)
- [Slash commands](slash-commands.md)
- [Plugin authoring](plugin-authoring.md)
- [EP-0063: plugin-defined harness artifacts](../eps/0063-plugin-defined-harness-artifacts.md)
- [EP-0064: WASM lifecycle applications](../eps/0064-wasm-lifecycle-applications.md)
