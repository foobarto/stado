# `[budget]` — cost guardrail

Two thresholds on cumulative session spend. Opt-in; defaults to
unset (no guard, no pill). Matches the shape you'd expect if you've
used paid hosted LLM tools.

## Why it exists

A long-running agent session with `--tools` can easily hit $1+ of
provider cost on a mis-scoped refactor or an accidental tool loop.
Stado already tracks `CostUSD` on every turn (commit trailer +
status bar); `[budget]` is the guardrail that turns passive
tracking into active warning and a hard cap.

Two caps, both in USD:

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

## How to use

### Setting the caps

```toml
[budget]
warn_usd = 1.00     # yellow pill + one-time advisory at $1
hard_usd = 5.00     # block further turns at $5 until /budget ack
```

Both fractional and integer dollars are fine. Zero (or absent) =
disabled. Misconfigured pairs where `hard_usd ≤ warn_usd` are
dropped (the hard cap wouldn't fire before the warning), with a
stderr warning at config-load time.

### TUI behaviour

**Status bar pill** — renders once `CostUSD ≥ warn_usd`:
```
… · budget $1.37/$5.00 · $0.08 · ctrl+p commands
```
Yellow (`warning` theme colour) so it stands out against muted
metrics.

**One-time advisory** — appended as a system block the first time
the pill lights up:
```
⚠ budget warning: cost $1.00 crossed warn cap $1.00 — hard cap at $5.00
```

**Hard-cap gate** — Enter after crossing `hard_usd`:
```
cost $5.12 ≥ hard cap $5.00 — blocked. Continue with:
  · /budget ack — acknowledge and continue this session
  · edit [budget].hard_usd in config.toml to raise the cap
```
Draft text stays in the input so `/budget ack` → Enter doesn't lose it.

**`/budget` slash commands:**
- `/budget` — print current state (cost, warn, hard, ack'd?)
- `/budget ack` — set `budgetAcked = true`, unblock for the session
- `/budget reset` — clear `budgetAcked`, rearm the gate

### `stado run` behaviour

```sh
$ stado run --prompt "refactor the billing module" --tools
# ... turns proceed ...
stado run: runtime: cost cap exceeded: spent $5.0231 of $5.00 cap
  raise [budget].hard_usd in config.toml or pass a larger budget to continue.
$ echo $?
2
```

Partial conversation output is still written to the session before
the exit — the history is self-consistent (the turn that tripped the
cap completed in full; subsequent turns didn't start).

### Doctor surface

```
  ✓ Budget caps         warn=$1.00 hard=$5.00   (ok)
```

When unset:
```
  ✓ Budget caps         (unset — no cost guardrail)   (ok)
```

So `stado doctor` doubles as "did my config take effect?" verification.

## Gotchas

- **Session-scoped, not process-scoped.** `/budget ack` lasts until
  the session ends (TUI exit, or process restart). A new session
  starts fresh with the config-file caps.
- **Cost is provider-reported.** Local runners (Ollama, llamacpp,
  etc.) usually report `$0.00`. Guardrails are a no-op there; the
  feature is aimed at hosted APIs (Anthropic, OpenAI, Google, etc.).
- **Hard cap check is turn-boundary, not stream-boundary.** A single
  very long turn can overshoot the cap — the loop checks after the
  turn completes. Soft real-time budgeting isn't in scope.
- **Provider billing-lag.** Real invoice costs may diverge slightly
  from stado's tracked cost if the provider amortises differently.
  Use `--days 30` in `stado stats` to reconcile.

## See also

- `stado stats` — historical cost + tokens + per-model breakdown.
  No standalone guide yet; use `stado stats --help`.
- [`[context]` soft/hard thresholds](./context.md) — the cognate
  guardrail on context-window usage
