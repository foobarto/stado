# session-recorder — Phase 7.1b second validating plugin

A background plugin that appends one JSONL line per turn to
`<worktree>/.stado/session-recordings.jsonl`. Proves the Phase 7.1b
ABI isn't shaped only for auto-compaction — different plugin, different
capability mix, same host contract.

## Why this plugin exists

`auto-compact` exercises `session:read` + `session:fork` +
`llm:invoke`. `session-inspect` exercises `session:read` only. This
plugin exercises `session:read` + `fs:read` + `fs:write` + the
`stado_plugin_tick` background lifecycle — a genuinely different
slice through the ABI. If you ever want a third-party plugin store,
this is the shape a "telemetry bridge" or "replay writer" plugin
would take.

## Capability declaration

```json
"capabilities": [
  "session:read",
  "fs:read:.stado",
  "fs:write:.stado"
]
```

No LLM invocation, no forking. The plugin cannot read or write outside
`<worktree>/.stado/` — the host rejects any path outside the declared
prefixes with `stado_fs_write denied` in stderr.

## Build + install

```sh
# One-time: generate the demo signer key.
stado plugin gen-key session-recorder-demo.seed

# Compile + sign.
./build.sh

# Pin the demo signer (pubkey printed by gen-key).
stado plugin trust <pubkey-hex> "stado example"

# Verify + install.
stado plugin install .
```

## One-shot invocation

```sh
stado plugin run session-recorder-0.1.0 snapshot '{"note":"pre-deploy checkpoint"}'
# → {"status":"ok","path":".stado/session-recordings.jsonl","recorded_bytes":147}
```

From the TUI:

```
/plugin:session-recorder-0.1.0 snapshot {"note":"before risky refactor"}
```

Inspect the log:

```sh
cat .stado/session-recordings.jsonl
# {"ts":"2026-04-20T10:15:00Z","kind":"snapshot","session_id":"abc…","tokens":4200,"messages":12,"last_turn_ref":"refs/sessions/abc/turns/6","note":"before risky refactor"}
```

## Background (per-turn) mode

Add to `~/.config/stado/config.toml`:

```toml
[plugins]
background = ["session-recorder-0.1.0"]
```

On every turn boundary the plugin fires `stado_plugin_tick` and
appends one `"kind":"tick"` line. Combined with the one-shot
`snapshot` tool, you get automatic per-turn recording plus
hand-placed checkpoints in the same file. Filter with `jq`:

```sh
jq -c 'select(.kind == "snapshot")' .stado/session-recordings.jsonl
```

## Result / log shape

All JSONL entries share one shape:

```json
{
  "ts":            "2026-04-20T10:15:00Z",
  "kind":          "snapshot" | "tick",
  "session_id":    "<uuid>",
  "tokens":        4200,
  "messages":      12,
  "last_turn_ref": "refs/sessions/<sid>/turns/<n>",
  "note":          "optional — set on snapshot args, always empty for tick"
}
```

`tokens` and `messages` come from `stado_session_read` — they reflect
the session state captured at tick/snapshot time, not cumulative
totals. Summing across entries gives you session growth.

## How the read-modify-write works

`stado_fs_write` truncates, so appending to a JSONL file means:

1. `stado_fs_read(.stado/session-recordings.jsonl)` → prior bytes
   (`nil` on first run)
2. Marshal the new recording, append `\n`
3. `stado_fs_write(.stado/session-recordings.jsonl, prior + new)`

That's three host calls per tick. Not efficient for high-volume
logging; fine for one-per-turn cadence. A future host import could add
a dedicated `stado_fs_append` to avoid the read step, but the current
ABI is sufficient.

## Why not just use the tool-classifier trace?

stado's own `refs/sessions/<id>/trace` already records every tool
call. The recorder's value is (a) plain JSONL that `jq` / `awk` can
read without `git` on the path, (b) fields you choose — not whatever
the tool-classifier records — and (c) a template for plugins that
want to record *externally* to an observability backend via fs:write
to a symlinked path.

## Known limitations

- **Read-modify-write contention.** Two concurrent turn boundaries
  can interleave: plugin A reads, plugin B reads, A writes, B writes
  — B's write overwrites A's. For this plugin the concurrency surface
  is one-per-turn on a single session, so it's a non-issue in
  practice. Plugins that need strict append should wait for a
  dedicated `stado_fs_append` host import.
- **JSONL path is per-worktree.** Each session fork creates a fresh
  worktree with its own `.stado/session-recordings.jsonl`. If you
  want a cross-session log, write to an absolute path outside the
  worktree and declare `fs:write:/absolute/path` in the manifest.
- **Token counts are current-turn, not cumulative.** Sum across
  entries to get the growth curve.
