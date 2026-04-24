# Context management

Every LLM has a finite context window. Stado tracks input tokens per
turn and warns before you're close to the limit. In the TUI, the hard
cap first asks the bundled `auto-compact` background plugin to recover
by forking a compacted child session, then replays the blocked prompt
there. Headless surfaces context warnings to clients instead of
blocking them on its own.

## The two thresholds

```toml
[context]
soft_threshold = 0.70   # advisory — flash a system note + offer /compact
hard_threshold = 0.90   # TUI block; headless warns and leaves policy to clients
```

Both values are **fractions of the provider's reported
`max_context_tokens`**. Defaults: `0.70` / `0.90`. The numbers above
mean:

- At 70% you get a one-line system advisory under the current turn
  and a `/compact` suggestion in the sidebar. Nothing is blocked.
- At 90% the TUI tries bundled auto-recovery first. If a child session
  is forked, stado switches to it and replays the blocked prompt there.
  If no child is produced, the input stays in the editor and you
  recover manually with `/compact`, `/retry`, `/clear`, or an explicit
  session fork. Headless emits `session.update { kind: "context_warning",
  level: "hard" }` and leaves blocking to the client.

## Why two thresholds (not one)

A single hard cap is too late — you hit it mid-task, the call fails,
and you're stuck figuring out what broke. A single soft warning is
too easy to ignore — the next turn slides past the ceiling and the
provider rejects it with an opaque message.

Two values give a graceful degradation:

1. Soft (70%): "think about winding down or compacting".
2. Hard (90%): "you need to do something before the next turn".

10% headroom between the two means a single 2-3K-token assistant
reply usually doesn't bridge them — you have time to act.

## How context usage is computed

Provider-reported usage from streaming events is the source of truth
once a turn completes. Providers also implement `agent.TokenCounter`
where practical so stado can warn when context percentage may be
unreliable before usage arrives:

- Anthropic uses the Messages count-tokens endpoint.
- OpenAI and OpenAI-compatible providers use the offline tiktoken path
  for prompt-side estimates and provider usage when the server reports
  it.
- Google uses Gemini's `CountTokens` endpoint.

If a provider cannot report a usable `MaxContextTokens`, stado shows
`context: unavailable` and skips threshold enforcement instead of
displaying a fake percentage.

The sidebar percentage is `input_tokens / max_context_tokens`. Crosses
to red past the hard threshold; amber past the soft.

## `/context` — one-stop status

`/context` in the TUI (or the pill in the sidebar) prints:

```
context: 42.3K / 200K (21%)  soft 70%  hard 90%
session: 1a2b…
cost: $0.1523
instructions: AGENTS.md
```

When the provider hasn't reported `max_context_tokens` (some local
runners don't), stado shows `context: unavailable` rather than a
misleading 0%. The pill disappears entirely in that case.

## Hitting the limits

### Soft threshold crossed

A system block renders under the last assistant turn:

```
context: 72% used — /compact summarises the conversation and replaces prior
turns with a shorter system message. Your next turn will be much cheaper.
```

The `/compact` suggestion is the only one shown — other recovery
paths (`/clear`, session fork) exist but aren't on the critical path.

### Hard threshold crossed

Input submit first tries automatic recovery:

```
context at 91% (hard threshold 90%) — running bundled auto-compact before
replaying your prompt in a child session.
```

If recovery succeeds, the TUI switches into the compacted child session
and replays the blocked prompt there. If it does not, the input text
stays in the buffer so nothing is lost.

## `/compact` — summarise and replace

Fires a dedicated compaction turn using the same provider + model.
The system prompt asks for a short summary that preserves facts,
decisions, and open questions. Output streams below the last assistant
block with a y/n prompt:

- `y` — replaces the conversation in-memory and on disk. A dual-ref
  compaction marker preserves the original turn range in the sidecar
  audit history.
- `n` — discards the summary, conversation unchanged.
- `e` — edit the summary inline before accepting.

Compaction is idempotent: running it on an already-compacted
conversation produces a new, tighter summary on top.

Persisted-session CLI compaction is intentionally plugin-driven rather
than a built-in core rewrite path. Use a session-aware plugin with
`stado plugin run --session <id> <plugin-id> <tool> [json-args]`; the
canonical source is `plugins/default/auto-compact/`: it uses
fork-based recovery and seeds the child session's persisted
conversation with the generated summary.

## Config

```toml
[context]
soft_threshold = 0.70   # 0 < x ≤ 1; 0 disables the advisory
hard_threshold = 0.90   # 0 < x ≤ 1; 0 disables the block

# Common tweaks:
# Tight budget, blocking early:
# soft_threshold = 0.50
# hard_threshold = 0.75

# Running a 1M-context model and want to fill it:
# soft_threshold = 0.85
# hard_threshold = 0.98
```

Headless mode (`stado headless`) honours the same thresholds, but its
JSON event stream emits `session.update { kind: "context_warning",
level: "soft" | "hard" }` when completed turns sit at or above the
configured threshold. Headless does not block its callers on its own.

## Gotchas

- **max_context_tokens isn't always available.** OAI-compat providers
  that don't implement `/models` with context info leave stado in
  "unavailable" mode. Thresholds are skipped — no warnings, no block.
  Fix the preset to include `max_tokens` or switch to a provider that
  reports.
- **Thinking tokens count toward input on the next turn.** A reasoning
  model producing 5K thinking tokens inflates the next turn's prompt
  by that amount. Compacting is the only reset.
- **The hard cap is client-side.** If the provider does not report a
  usable max-context size, thresholds are skipped and provider errors
  become the failure mode.

## See also

- [commands/tui.md](../commands/tui.md) — TUI viewport + sidebar.
- [features/slash-commands.md](./slash-commands.md) — `/compact`,
  `/context`.
- [features/budget.md](./budget.md) — the cost-tracking sibling.
