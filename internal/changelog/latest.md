## v0.75.2 — Codex triage batches 3+4 — 2026-06-15

Patch follow-up to v0.75.1: closes the next **FIX_NOW** Codex security items
that did not need operator decisions. No new features — resource caps, audit
parity, and host-import hardening.

### Security

- **Plugin state KV is bounded** — `ReadDeclared`/`WriteDeclared` gate
  undeclared keys; entry cap 4096; key bytes count toward totals.
- **Listen-only TCP conns** allow read/write/close on accepted sockets without
  `net:dial`; UDP `sendto` still requires `NetDial`.
- **IPv6 transition addresses** (NAT64/6to4) decode embedded IPv4 and refuse
  private embeds unless the private cap is held.
- **`stado_json_format` output is capped** — 64-level nesting limit + 4 MiB
  formatted output cap (blocks amplification from compact input).
- **AXFR zone transfers are bounded** — 50k record cap, 120s timeout ceiling;
  over-limit transfers abort cleanly (connection drained).
- **Nested `stado_tool_invoke` routes through `Executor.Run`** so inner calls
  get audit trailers and lifecycle hooks (pinned on registry rebuild/reload).

### Fixes

- **TUI `hard_tokens` budget** uses cumulative input tokens across the session
  (not per-turn only).
- **Headless `plugin.run` + background plugins** populate `cfg:state_dir` so
  `stado_cfg_state_dir` works on ACP paths.
- **`/reload` and `/plugin reload`** re-pin invoke executors after registry
  rebuilds.

