# session-inspect — Phase 7.1b capability demo

A signed wasm plugin that exercises the session/LLM capabilities from
[DESIGN §"Plugin extension points for context management"](../../../DESIGN.md).
Declares three of the four new capabilities in its manifest, then its
single `inspect` tool pulls the current session's shape through
`stado_session_read` and returns a JSON report.

This is the validating example that proves K2 (Phase 7.1b) works
end-to-end:

- Manifest capability parsing (`session:read`, `session:fork`,
  `llm:invoke:20000`)
- Host import registration
- Capability-bounded host-to-plugin calls
- JSON round-tripping across the wasm ABI

It's not a useful plugin for day-to-day work — think of it as a
`hello-world` for the session API. The auto-compaction plugin that
the Phase 7.1b capabilities were designed for would build on this
scaffold: read history → invoke LLM to summarise → fork with the
summary as seed.

## Why this plugin is session-aware

The manifest declares:

```json
"capabilities": [
  "session:read",
  "session:fork",
  "llm:invoke:20000"
]
```

`session:read` gates `stado_session_read` (used by this demo).
`session:fork` + `llm:invoke` are declared but not exercised — they're
there to show how a real auto-compaction plugin would stack the caps.
Removing any of them from the manifest makes the corresponding host
import return `-1` at runtime.

## Build + run

```sh
# One-time: generate a demo signer key.
stado plugin gen-key session-inspect-demo.seed

# Compile + sign.
./build.sh

# Pin the demo signer (pubkey printed by gen-key).
stado plugin trust <pubkey-hex> "stado example"

# Verify + install.
stado plugin install .

# Run. The output varies by context:
stado plugin run session-inspect-0.1.0 inspect '{}'
```

### Output in different contexts

**`stado plugin run` (one-shot CLI)** — no live session, so
`session:read` returns errors from the bridge and the plugin renders
mostly-empty fields:

```
stado plugin run: session-aware capabilities declared; note that the
one-shot CLI has no live session — session:read returns zeroed fields,
session:fork + llm:invoke are unavailable

INFO inspect: reading session state plugin=session-inspect
WARN stado_session_read failed ... err="history: no MessagesFn wired on bridge"
WARN stado_session_read failed ... err="session_id: no session"
WARN stado_session_read failed ... err="message_count: no MessagesFn wired on bridge"
{"session_id":"","message_count":"","token_count":"0","last_turn_ref":"","history_bytes":0}
```

**TUI `/plugin:session-inspect-0.1.0 inspect`** — the live TUI wires
`MessagesFn`, `TokensFn`, `LastTurnRef` on the bridge, so every field
populates:

```
plugin session-inspect-0.1.0/inspect → {"session_id":"<id>",
"message_count":"7","token_count":"12544","last_turn_ref":
"refs/sessions/<id>/turns/3","history_bytes":892,
"history_head":[{"role":"user","text":"fix the bug..."},...]}
```

## Implementation notes

- **Zero-copy reads**: `readField()` allocates a 64 KiB scratch buffer,
  passes it to `stado_session_read`, and slices to the returned
  length. No fixed size per field — Go's stdlib `encoding/json`
  handles whatever the host hands over.
- **Build size**: ~3 MB, same ballpark as `hello-go`. The Go runtime
  dominates; session:read plumbing adds maybe 2 KiB.
- **Capability budget**: `llm:invoke:20000` caps a real LLM-using
  plugin at 20K tokens per session. This demo never invokes the LLM
  but declares the cap to exercise budget-parsing.

## Iterating

Edit `main.go`, re-run `./build.sh`. The manifest `version` bumps you
bump yourself before re-installing — stado's rollback protection
refuses same-version overwrites. `rm -rf $XDG_DATA_HOME/stado/plugins/session-inspect-0.1.0`
resets if you want to reinstall without a version bump.
