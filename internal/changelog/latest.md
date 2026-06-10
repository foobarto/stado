## v0.61.0 — in-turn steering/queue/interrupt + usability & correctness sweep — 2026-06-11

### TUI

- **In-turn message routing (#16/#17).** A message sent while a turn is in
  flight can be routed three ways, each with a key binding and a slash command:
  **steer** (Enter / `/steer`) injects it into the current turn at the next tool
  boundary; **queue** (alt+enter / `/queue`) defers it to the next turn;
  **interrupt** (ctrl+enter / `/interrupt`) cancels the turn and runs it now.
  Enter-while-busy now steers (was queue-for-next-turn); the queue and fire-now
  roles moved to alt+enter and ctrl+enter. ctrl+enter needs a terminal with
  enhanced-keyboard support — the slash commands are the universal fallback.
- **Click a thinking block to expand it (#14).** Clicking a reasoning block (or
  focusing it with the chord) reveals its full text even when the global
  thinking display is set to tail/hide.
- **Compaction follow-ups (#19).** A proactive one-time advisory when context
  usage crosses the soft threshold, plus auto-recovery when a turn dies with a
  provider context-overflow error (compact + replay the last prompt in a child
  session) when an auto-compact plugin is installed — for both synchronous
  (oaicompat) and EvError-event (Anthropic) error paths.

### Fixes

- **`config show` no longer leaks secrets.** `config show` / `--json` now redact
  OTel auth headers, MCP/ACP server credentials, and proxy `user:pass` (values
  replaced with `<redacted>`, keys/shape preserved). `stado plugin doctor` also
  redacts the proxy in its sandbox cross-check.
- **`stado usage` reports real activity** instead of always "No turns recorded":
  it counts distinct turns even when the agent loop records zero tokens, and
  explains that per-turn token totals aren't recorded yet (a known limitation).
- **A malformed `memory.jsonl` line no longer bricks memory + learning.** The
  store skips and warns on malformed/structurally-invalid lines (including a
  nil-item `supersede` that previously destroyed data) and folds the rest.
- **`session show` / `audit verify` error on a nonexistent id** instead of
  fabricating success (exit 0); real repo/storage errors are surfaced rather
  than masked as "not found".
- **Context threshold `0` disables the gate**, matching the docs — `soft` and
  `hard` are independent knobs (0 disables that one only).

