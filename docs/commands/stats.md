# `stado stats`

Aggregate cost and usage from the signed session trace refs.

## What it does

`stado stats` walks the sidecar repo's `trace` refs and reads the
machine trailers written for tool calls: `Tokens-In`, `Tokens-Out`,
`Cost-USD`, `Duration-Ms`, `Tool`, and `Model`.

The source is git history, not OpenTelemetry, so it works offline,
after the fact, and in airgapped builds.

## Usage

```sh
stado stats
stado stats --days 30
stado stats --session <id>
stado stats --model claude-sonnet-4-6
stado stats --tools
stado stats --json | jq
```

Default output is a per-model table plus totals. `--tools` adds a
per-tool call/time table. `--json` emits a stable object for scripts.

## Flags

| Flag | Meaning |
|------|---------|
| `--days N` | Aggregate commits newer than N days; default 7 |
| `--session <id>` | Restrict to one exact session id |
| `--model <id>` | Restrict to commits whose `Model:` trailer matches |
| `--tools` | Include per-tool call/time breakdown |
| `--json` | Emit JSON instead of the human table |

## Gotchas

- Sessions with no trace ref or no tool calls in the window are skipped.
- Model filtering is exact string matching against the audit trailer.
- Provider-only `stado run` calls without tools do not create trace
  commits, so there is nothing for `stats` to count.

## See also

- [session.md](session.md) — session storage and refs.
- [audit.md](audit.md) — lower-level signed trace inspection.
- [features/budget.md](../features/budget.md) — live cost guardrails.
