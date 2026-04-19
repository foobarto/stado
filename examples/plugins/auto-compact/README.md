# auto-compact — Phase 7.1b validating plugin

The canonical demo for stado's session/LLM plugin capabilities. A single
tool, `compact`, that:

1. Reads the session's current token count via `session:read`.
2. If under `threshold_tokens` (default 10 000), returns skipped.
3. Otherwise, reads the conversation history + last-turn ref, asks the
   active LLM to summarise, and forks into a fresh session seeded with
   that summary.

The fork is the compaction. Per DESIGN §"Plugin extension points for
context management", plugins never rewrite history in place — they
recover via fork-from-point. The parent session stays untouched.

## What the plugin proves

Exercises all four Phase 7.1b host imports in one tool run:

| Host import               | Role in the flow                                   |
|---------------------------|----------------------------------------------------|
| `stado_log`               | Info log lines — visible in stderr                 |
| `stado_session_read`      | Pulls `token_count`, `history`, `last_turn_ref`    |
| `stado_llm_invoke`        | Summarises the history against the active model    |
| `stado_session_fork`      | Roots a new session at the last turn with the summary as seed |

If any one of those fails (capability not declared, budget exhausted,
no session in this context), the plugin surfaces the condition in its
JSON result so the caller sees *why* rather than a silent skip.

## Build + run

```sh
# One-time: generate the demo signer key.
stado plugin gen-key auto-compact-demo.seed

# Compile + sign.
./build.sh

# Pin the demo signer (pubkey printed by gen-key).
stado plugin trust <pubkey-hex> "stado example"

# Verify + install.
stado plugin install .

# Run. Default threshold 10 000 tokens; skipped on under-threshold
# sessions, which includes the one-shot CLI (no session → 0 tokens).
stado plugin run auto-compact-0.1.0 compact '{}'
# → {"status":"skipped","reason":"below threshold","threshold":10000}

# Explicit threshold:
stado plugin run auto-compact-0.1.0 compact '{"threshold_tokens":5000}'
```

From inside a live TUI (where session:read + llm:invoke work):

```
/plugin:auto-compact-0.1.0 compact
```

When the session is above threshold, the TUI will render:

1. A `plugin auto-compact-0.1.0/compact → {...}` system block with the
   compacted result.
2. A `plugin auto-compact forked session → <child-id>  (at turns/N)`
   notification with the summary excerpt + a `stado session attach`
   hint.

Attach to the child session to continue with the summary as context:

```sh
cd $(stado session attach <child-id>) && stado
# → "resumed session — 1 prior message(s) loaded from disk."
```

## Result shapes

All three shapes are wire-stable — consumers (scripts, other plugins,
the TUI's system-block renderer) can rely on them.

```json
// under threshold
{"status":"skipped","reason":"below threshold","tokens":4200,"threshold":10000}

// compacted
{"status":"compacted","tokens":24100,"threshold":10000,"child":"<uuid>","summary_length":1843,"last_turn_ref":"refs/sessions/<parent>/turns/12"}

// error (any capability denial / provider failure)
{"status":"error","reason":"llm:invoke denied / failed / budget exhausted"}
```

## Tuning the budget

The manifest declares `llm:invoke:30000` — 30 K per-session token
budget. Bigger sessions with longer histories may need more; bump the
cap in `plugin.manifest.template.json` and rebuild. The budget is
enforced by stado's runtime, not by the plugin, so a misbehaving
plugin can't blow past it even if the manifest lies.

## Why the prompt looks the way it does

`buildSummarisePrompt` in main.go gives the LLM context about *where
the summary will end up*: not as narration but as a seed for a fresh
conversation. Experienced prompts change with the model; edit the
function, rebuild, and iterate. The summary is what the next session
will read as its first user-turn, so focused action-ready text works
better than a timeline recap.

## Iterating

Edit `main.go`, run `./build.sh`, bump `plugin.manifest.template.json`
version OR `rm -rf $XDG_DATA_HOME/stado/plugins/auto-compact-0.1.0`
before reinstalling (stado refuses same-version overwrites).

## Running as a background plugin

The plugin also exports `stado_plugin_tick` so it can be loaded as a
persistent background plugin that fires on every turn boundary
(rather than requiring explicit `/plugin:...` invocation). Enable it
by adding the installed plugin ID to `[plugins].background` in your
stado config:

```toml
[plugins]
background = ["auto-compact-0.1.0"]
```

On each turn the plugin checks the token count against
`defaultThreshold` (10K); under-threshold ticks are silent (logged
at info level), over-threshold ticks run the full compact + fork
flow. Plugin log lines appear in stado's stderr.

## Known limitations (of the current Phase 7.1b surface)

- **No seed-message auto-replay.** The fork's seed text lands as a
  trace-ref marker but isn't automatically restored into `m.msgs` when
  the child session starts. The resume path (conversation persistence)
  only rehydrates messages the TUI wrote to `.stado/conversation.jsonl`;
  plugin-supplied seeds don't flow through that file yet. Users
  currently see "resumed session — 0 prior messages loaded" in the
  child and need to re-send the summary as their first prompt.
- **Background ticking is turn-boundary granularity only.** The tick
  fires after each assistant turn completes; tool-call round-trips
  inside a turn don't trigger it. If you need sub-turn observation,
  poll via `stado_session_next_event` inside the tick body — the
  event queue receives `{"kind":"turn_complete","turn":N}` on each
  tick firing and you can inspect session state in between.
