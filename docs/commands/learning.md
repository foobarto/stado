# `stado learn`

Review completed trajectories and manage evidence-backed operational lessons.

## What It Does

`stado learn --session-id <id>` (or the explicit `stado learn run`) asks an
isolated reviewer to inspect host-recorded mistake and correction signals. A
review may create up to five versioned `lesson` candidates; it never activates
them. One-off failures are deliberately ignored.

The pre-v1 `stado learning` command has been removed. Existing legacy memory
logs can be imported once with `stado learn migrate`.

## Common Flow

```sh
stado learn --session-id sess_...
stado learn run --session-id sess_... --focus "tool argument corrections"
stado learn candidates --session-id sess_...
stado learn artifact art_... --session-id sess_...
stado learn retrieval-report
stado learn migrate
```

In the TUI, `/learn [focus]` reviews the just-completed session trajectory.
Candidate review is available without starting another model turn:

```text
/learn candidates
/learn show art_...
```

Activation is intentionally unavailable through ordinary `stado learn` and is
being removed from the in-process TUI path. An agent can invoke a CLI, and a
hostile orchestrator can forge its own callback, so neither proves operator
intent. Activation remains withheld until a broker-owned predeclared policy or
separately trusted presenter can authorize the exact artifact version, body,
scope, and actor.

## Commands

| Command | Purpose |
|---------|---------|
| `stado learn [run] --session-id <id>` | Review one completed trajectory and record the review job |
| `stado learn candidates` | List visible candidate and migrated legacy lessons |
| `stado learn artifact <id>` | Show one authorized memory or lesson artifact |
| `stado learn migrate` | Archive and idempotently import the legacy memory JSONL store |
| `stado learn retrieval-report` | Show shadow adaptive-retrieval observations |

The legacy lesson lifecycle subcommands remain under `stado learn` for local
audit and migration work (`propose`, `list`, `show`, `edit`, `reject`, `delete`,
`supersede`, `document`, `stale`, and `export`). They operate on the legacy
store and do not bypass the trusted activation boundary.

## Scope and Safety

Review scope is `session` by default; `--scope repo` and `--scope global` are
available when the host can bind the corresponding identity. Session artifacts
are visible to the creating session and its descendants, not siblings or
ancestors.

Lessons are untrusted guidance below operator and repository instructions. An
active lesson cannot grant tools, permissions, or authority. Reviewer outputs
retain their evidence provenance and taint, and secret-bearing material is not
made eligible for normal prompt retrieval.

See [What Survives the Window](../articles/adaptive-context.md) for the persistence,
research-agent, retained-agent, and adaptive-ranking contracts.
