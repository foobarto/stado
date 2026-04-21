# `[hooks]` — lifecycle shell hooks

Wire a shell command to stado lifecycle events. Runs on every
completed turn; gets the turn's usage numbers as JSON on stdin.
Useful for desktop notifications, Slack pings, custom logging, or
any "react to turn completion" workflow.

MVP scope is **notification-only** — hooks cannot block or modify
a turn. A richer "approve tool call via external policy" form
can grow on top once the simple case has been validated in the
wild.

## Why hooks

Two drivers:

1. **Long turns need out-of-band notification.** Reasoning models
   can take tens of seconds. Users context-switch to other windows
   and miss completion. A 10-line shell script + a `post_turn`
   hook fixes that:
   ```toml
   [hooks]
   post_turn = "notify-send stado 'turn done'"
   ```
2. **Custom telemetry.** The turn trailer (tokens, cost, duration)
   is already written to the session's trace ref, but if you want
   to ship it to a remote system in real time, a hook is the
   escape valve:
   ```toml
   [hooks]
   post_turn = "jq -r '.cost_usd' | curl -X POST my-metrics..."
   ```

## Configuration

Single knob today:

```toml
[hooks]
post_turn = "notify-send stado 'turn complete'"
```

Empty (or absent) → no hook, zero overhead.

## What the hook receives

Stdin is a single JSON object:

```json
{
  "event": "post_turn",
  "turn_index": 7,
  "tokens_in": 12345,
  "tokens_out": 567,
  "cost_usd": 0.0123,
  "text_excerpt": "first ~200 chars of the assistant reply",
  "duration_ms": 3421
}
```

Fields are stable — future additions go at the end and consumers
should tolerate unknown fields.

## Execution

- Invoked as `/bin/sh -c <cmd>`. All shell features work: pipes,
  redirects, background, `&&`, etc.
- Process env inherited from stado. Secrets in env are readable
  inside the hook — no isolation boundary.
- stdout + stderr go to stado's own stderr with a
  `stado[hook:<event>]` prefix so they're distinguishable in a
  shared terminal. No terminal ownership — don't try to run
  interactive commands.
- **5-second wall-clock cap.** `Cmd.WaitDelay` ensures stuck hooks
  (or hooks that spawn grand-children holding the output pipes)
  don't deadlock turn completion.
- Exit codes are recorded to telemetry but not acted on. A failing
  hook is a warning, not a turn-level failure.

## Example hooks

**Desktop notification on turn completion:**
```toml
[hooks]
post_turn = "notify-send stado 'Turn done'"
```

**Slack ping for expensive turns only:**
```toml
[hooks]
post_turn = "jq -r '.cost_usd | select(. > 0.5) | \"🔥 stado turn cost $\\(.)\"' | xargs -r curl -X POST https://hooks.slack.com/... -d"
```

**Log to disk:**
```toml
[hooks]
post_turn = "jq -c . >> ~/.stado/turn-log.jsonl"
```

**Play a sound:**
```toml
[hooks]
post_turn = "paplay /usr/share/sounds/freedesktop/stereo/complete.oga"
```

## Gotchas

- **Synchronous within the TUI.** `FirePostTurn` runs on the main
  bubbletea goroutine. The 5-second cap guarantees no session-wide
  hang, but slow hooks DO delay the next turn's start. For
  expensive work, fork and exit:
  ```toml
  [hooks]
  post_turn = "my-heavy-job & disown"
  ```
- **No error messaging back to the TUI.** A failing hook produces
  a stderr line; nothing surfaces in the chat. If you want visible
  feedback, write your hook to post a system block via `/` RPC
  (not yet supported; design space).
- **Invoked for every turn.** No filtering by cost, role, or
  content. Use jq predicates in the hook script for conditional
  behaviour.
- **Not yet invoked in `stado run`.** MVP wires the hook through
  the TUI path only — headless `stado run` skips it. Expect this
  to land in a subsequent iteration.

## See also

- [features/budget.md](./budget.md) — cost guardrails, same
  shell-hook pattern but gated on thresholds instead of every turn.
- [commands/tui.md](../commands/tui.md) — TUI entry point.
