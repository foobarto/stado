---
ep: 36
title: Loop, monitor, and schedule — recurring agent work
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-05-05
see-also: [9, 34, 35]
history:
  - date: 2026-05-05
    note: Implemented — /loop (TUI), /monitor (TUI), stado schedule (CLI)
---

## Summary

Three surfaces for recurring / event-driven agent work:

| Surface | Where | Purpose |
|---------|-------|---------|
| `/loop [interval] <prompt>` | TUI slash cmd | Repeat a prompt automatically (timed or agent-paced) |
| `/monitor <cmd>` | TUI slash cmd | Stream process stdout as session notifications |
| `stado schedule` | CLI | Persistent scheduled runs via cron |

## Motivation

One-shot agent turns cover most interactive work, but engagement loops,
CI monitoring, and recurring reports need a way to keep running without
the operator manually re-typing. Claude Code solves this with `/loop`
(self-paced dynamic loops), `ScheduleWakeup` (timed inter-turn delay),
and `CronCreate` (OS-level persistence). Stado needs the same primitives.

## Spec

### 1. `/loop` — TUI repeating turns

```
/loop <prompt>               immediate-repeat: re-run <prompt> when each
                             turn finishes; stops on /loop stop or
                             [LOOP_DONE] in the agent's response.
/loop <duration> <prompt>    timed: wait <duration> between turns.
                             e.g. /loop 5m "check deploy status"
                             Duration format: Go time.ParseDuration
                             (5s, 2m30s, 1h).
/loop stop                   cancel the active loop (also: /loop off).
```

**Agent stop signal.** When the agent's response text contains the
literal string `[LOOP_DONE]`, the TUI clears the loop state after
the current turn. This lets the agent self-terminate without user
intervention (`/loop stop` remains the manual escape hatch).

**Busy guard.** The next iteration only starts when the TUI is idle
(not currently streaming, no pending approval). A queued tick that
fires while busy is discarded; the next tick is rescheduled.

**UI feedback.** The status bar shows `↻ loop (5m)` when a timed
loop is active, `↻ loop` for immediate-repeat. The block list shows
`[loop ▸ N]` as a separator between iterations.

### 2. `/monitor <cmd>` — process stdout notifications

```
/monitor <cmd>               start <cmd> in a background goroutine;
                             each stdout line → system block in the
                             current session.
/monitor stop                kill the background process.
```

Stderr is discarded (redirect in the command if needed:
`/monitor cmd 2>&1`). Each line is prefixed `[monitor] ` in the
system block so the agent can distinguish monitor lines from regular
system messages. The process is killed when the session ends or
when `/monitor stop` is run.

**No PTY.** Monitor uses a plain `exec.Cmd` with a stdout pipe, not
a PTY — it's a stream consumer, not an interactive shell. Use
`exec:pty` background sessions when interactivity is needed.

**Context.** The monitor goroutine runs under the session's root
context; it stops automatically if the TUI is quit.

### 3. `stado schedule` — persistent scheduled runs

```
stado schedule create --cron "0 9 * * *" --prompt "daily standup"
                      [--session <id>]      # resume existing session
                      [--name <label>]      # human label
stado schedule list
stado schedule rm <id>
stado schedule run-now <id>
stado schedule install-cron                 # write OS crontab entries
                                            # for all active schedules
stado schedule uninstall-cron               # remove stado entries from
                                            # crontab
```

**Storage.** Schedule entries are persisted to
`~/.local/share/stado/schedules.json`. Each entry has:

```json
{
  "id": "<uuid>",
  "name": "daily standup",
  "cron": "0 9 * * *",
  "prompt": "daily standup prompt text",
  "session_id": "",
  "created": "2026-05-05T09:00:00Z"
}
```

**Execution.** `stado schedule run-now <id>` reads the entry and
invokes `stado run --prompt "..." [--session-id ...]` in a subprocess.
Output is appended to `~/.local/share/stado/schedule-<id>.log`.

**OS cron.** `stado schedule install-cron` adds one crontab entry per
active schedule:

```
0 9 * * * /path/to/stado schedule run-now <id>   # stado:<id>
```

The `# stado:<id>` comment is a sentinel for `uninstall-cron` so only
stado-managed entries are touched. `install-cron` is idempotent —
re-running it replaces existing entries for the same ID.

**No daemon.** Execution is delegated to OS cron. Stado does not run
a background daemon. Users who want in-process scheduling can start a
loop in the TUI instead.

## Implementation notes

### TUI changes (loop + monitor)

- `internal/tui/model_loop.go` — new file: `loopState` struct,
  `/loop` command handler, `loopTickMsg` message type, loop injection
  into the turn flow.
- `internal/tui/model_monitor.go` — new file: `monitorState` struct,
  `/monitor` command handler, `monitorLineMsg` message type, goroutine
  lifecycle.
- `internal/tui/model_commands.go` — dispatch `/loop` and `/monitor`.
- `internal/tui/model_update.go` — handle `loopTickMsg` and
  `monitorLineMsg`.
- `internal/tui/model_render.go` — status-bar annotation for active
  loop.

### CLI changes (schedule)

- `cmd/stado/schedule.go` — `scheduleCmd` + subcommands.
- `internal/schedule/schedule.go` — JSON store, crontab read/write.
