# `[budget]` — cost and token guardrails

Optional cumulative caps on session spend. Defaults to unset (no
guard, no pill). USD caps suit hosted APIs; token caps suit any
provider that reports usage.

## Why it exists

A long-running agent session with `--tools` can easily hit $1+ of
provider cost or hundreds of thousands of tokens on a mis-scoped
refactor or an accidental tool loop. Stado tracks `CostUSD` and token
usage on every turn (commit trailers + status bar); `[budget]` turns
passive tracking into active warning and hard caps.

## USD caps

Two thresholds, both in USD:

- **`warn_usd`** — status-bar pill `budget $X/$cap` turns yellow
  once cumulative cost crosses it, AND a one-time system block is
  appended to the conversation. Latched so subsequent turns don't
  spam the same notice.
- **`hard_usd`** — Enter is gated after the cap is crossed.
  `/budget ack` unblocks for the rest of the session; `/budget
  reset` rearms the gate; `/budget` alone shows current state.

`stado run` propagates `cfg.Budget.HardUSD` into the runtime's
`AgentLoopOptions.CostCapUSD`; the loop checks cumulative cost at
every turn boundary and returns `runtime.ErrCostCapExceeded`. The
CLI maps that error to exit code 2 with an actionable stderr
message so CI / scripting pipelines can gate on cost overruns.

## Token caps

Six optional knobs (independent — set only what you need):

| Key | Meaning |
|-----|---------|
| `warn_tokens` | Combined input+output cumulative warn |
| `hard_tokens` | Combined input+output cumulative hard gate |
| `warn_input_tokens` | Input-only cumulative warn |
| `hard_input_tokens` | Input-only cumulative hard gate |
| `warn_output_tokens` | Output-only cumulative warn |
| `hard_output_tokens` | Output-only cumulative hard gate |

**Combined caps** (`warn_tokens` / `hard_tokens`) use session-cumulative
input (`cumulativeInputTokens` across all turns) plus cumulative output
for the combined total. As of v0.75.2, `hard_tokens` gates on that
session sum — not per-turn input only.

**Per-direction caps** (`*_input_tokens` / `*_output_tokens`) gate on
the corresponding cumulative counter. In the TUI, `hard_input_tokens`
compares against last-turn input usage for the gate message; combined
`hard_tokens` uses the session cumulative sum.

`stado run` maps all configured token caps into `AgentLoopOptions` and
checks at every turn boundary. Breach returns `ErrTokenCapExceeded` →
exit code 2.

Misconfigured pairs where `hard ≤ warn` for the same knob are dropped
at config load (with a stderr warning), same as USD caps.

## How to use

### Setting the caps

```toml
[budget]
warn_usd = 1.00
hard_usd = 5.00
warn_tokens = 100000
hard_tokens = 500000
warn_input_tokens = 80000
hard_input_tokens = 200000
```

Fractional USD is fine. Zero (or absent) = disabled for that knob.

### TUI behaviour

**Status bar pill** — renders once a configured warn threshold is crossed:
```
… · budget $1.37/$5.00 · budget 9.0k/10.0k tok · $0.08 · ctrl+p commands
```
Yellow (`warning` theme colour) for budget warnings.

**One-time advisory** — appended as a system block the first time
the pill lights up (USD and/or token, depending on which fired).

**Hard-cap gate** — Enter after crossing any configured hard cap:
```
cost $5.12 ≥ hard cap $5.00 — blocked. Continue with:
  · /budget ack — acknowledge and continue this session
  · edit [budget] in config.toml to raise the cap
```
Draft text stays in the input so `/budget ack` → Enter doesn't lose it.

**`/budget` slash commands:**
- `/budget` — print current state (cost, USD caps, token caps, ack'd?)
- `/budget ack` — set `budgetAcked = true`, unblock for the session
- `/budget reset` — clear `budgetAcked`, rearm the gate

### `stado run` behaviour

USD breach:
```sh
$ stado run --prompt "refactor the billing module" --tools
stado run: runtime: cost cap exceeded: spent $5.0231 of $5.00 cap
$ echo $?
2
```

Token breach follows the same exit-code pattern with a knob-specific
message naming which cap fired.

Partial conversation output is still written to the session before
the exit — the history is self-consistent (the turn that tripped the
cap completed in full; subsequent turns didn't start).

### Doctor surface

```
  ✓ Budget caps         warn=$1.00 hard=$5.00   (ok)
```

When only token caps are set, doctor still reports USD as unset.
Use `stado config show` to verify token knobs loaded.

## Gotchas

- **Session-scoped, not process-scoped.** `/budget ack` lasts until
  the session ends (TUI exit, or process restart). A new session
  starts fresh with the config-file caps.
- **Cost is provider-reported.** Local runners (Ollama, llamacpp,
  etc.) usually report `$0.00`. USD guardrails are a no-op there;
  token caps still apply when the provider reports token usage.
- **Hard cap check is turn-boundary, not stream-boundary.** A single
  very long turn can overshoot the cap — the loop checks after the
  turn completes. Soft real-time budgeting isn't in scope.
- **Provider billing-lag.** Real invoice costs may diverge slightly
  from stado's tracked cost if the provider amortises differently.
  Use `--days 30` in `stado stats` to reconcile.
- **Distinct from `[context]` thresholds.** Context soft/hard are
  fractions of the model window; budget token caps are absolute
  cumulative counts.

## See also

- `stado stats` — historical cost + tokens + per-model breakdown.
  No standalone guide yet; use `stado stats --help`.
- [`[context]` soft/hard thresholds](./context.md) — the cognate
  guardrail on context-window usage
