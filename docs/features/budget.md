# `[budget]` — token guardrails

Optional cumulative token caps for an agent session. All thresholds default to
unset, which means no gate and no budget warning. Provider-reported cost may
still appear in usage and stats, but it is observational metadata rather than a
budget authority.

## Why it exists

A long-running tool-using session can consume hundreds of thousands of tokens
after a mis-scoped refactor, repeated failure, or accidental loop. Stado already
records input and output usage on every turn. `[budget]` turns those counters
into warnings and hard turn-boundary gates without depending on a provider's
pricing model.

## Token caps

Six optional knobs are independent; set only what you need:

| Key | Meaning |
|-----|---------|
| `warn_tokens` | Combined input+output cumulative warning |
| `hard_tokens` | Combined input+output cumulative hard gate |
| `warn_input_tokens` | Input-only cumulative warning |
| `hard_input_tokens` | Input-only cumulative hard gate |
| `warn_output_tokens` | Output-only cumulative warning |
| `hard_output_tokens` | Output-only cumulative hard gate |

Combined caps use session-cumulative input plus cumulative output. Directional
caps use the corresponding cumulative counter. The loop checks gates at turn
boundaries; a single turn can cross a threshold before the next turn is
refused.

A configured hard threshold must be greater than its matching warning
threshold. Invalid pairs are ignored with a configuration warning. Zero or an
absent key disables that threshold.

## Configuration

```toml
[budget]
warn_tokens = 100000
hard_tokens = 500000
warn_input_tokens = 80000
hard_input_tokens = 200000
warn_output_tokens = 20000
hard_output_tokens = 100000
```

Most sessions need only `warn_tokens` and `hard_tokens`. Directional caps are
useful when unusually large prompts or generations should have their own
ceiling.

## TUI behavior

The status bar renders a warning pill after a configured warning threshold is
crossed:

```text
… · budget 90.0k/100.0k tok · ctrl+p commands
```

The first warning also appends one bounded system advisory. It is latched so
later turns do not repeat the same notice.

After a hard threshold is crossed, new input is gated:

```text
tokens 510000 ≥ hard cap 500000 — blocked. Continue with:
  · /budget ack — acknowledge and continue this session
  · edit [budget] in config.toml to raise the cap
```

Draft input remains intact. `/budget ack` permits further turns for this
session; `/budget reset` rearms the configured gate; `/budget` displays token
usage, configured thresholds, and acknowledgement state.

## `stado run` behavior

`stado run` maps configured token thresholds into the agent loop. A hard breach
returns `ErrTokenCapExceeded`, prints the threshold that fired, and exits with
status 2. The completed turn that crossed the threshold is still persisted, so
the session history remains self-consistent; no later turn starts.

## Gotchas

- **Session-scoped acknowledgement.** `/budget ack` lasts for the current
  session/process. A new session starts with the configured gates armed.
- **Turn-boundary enforcement.** A single long provider turn can overshoot a
  cap. Soft real-time stream cancellation is not part of this contract.
- **Usage reporting is provider-dependent.** A provider that does not report
  token usage cannot be governed accurately by these counters.
- **Cost is not a budget cap.** `stado stats` may report provider cost, but
  current budget policy uses tokens only.
- **Context thresholds are separate.** `[context]` thresholds are fractions of
  the model window; budget thresholds are absolute cumulative token counts.

## See also

- `stado stats` — historical usage and provider-reported cost metadata.
- [`[context]` soft/hard thresholds](./context.md) — context-window pressure.
