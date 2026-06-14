## v0.72.0 — wasm plugin-host capability hardening (security sweep) — 2026-06-14

A scheduled pre-release security sweep (own review + Codex) found and closed a
cluster of wasm plugin-host capability-parity bypasses: newer host-import paths
that skipped a guard their hardened sibling already implements. Each finding was
reproduced as a failing test first. No known exploitation, and a plugin must
already be trusted + installed to reach these paths — but each let a
narrowly-capability'd plugin exceed its declared grant.

### Security

- **PTY spawn now enforces the exec glob and fails closed.** `terminal:open` /
  `exec:pty` spawns checked only the coarse `ExecPTY` bit, so a plugin granted
  `exec:pty` (without `exec:proc`, or with a narrow `exec:proc` glob) could run
  any binary — including `/bin/sh -c`. PTY spawns now resolve the effective
  binary and gate it through the same glob matcher as `exec:proc`. A malformed
  scoped glob fails closed (deny-all) instead of silently widening to broad.
- **DNS-rebinding window closed on the HTTP-stream and DNS host imports.** The
  streaming-HTTP dialer and the custom-server DNS resolve/AXFR paths guarded the
  resolved IP but then dialed the hostname, letting it re-resolve to a private
  address after the check. They now pin and dial the guarded IP (trying every
  guarded A record), matching the hardened `dialIP` sibling.
- **`stado_dns_resolve` against a custom server now requires `dns:resolve_private`.**
  Previously a `dns:resolve` plugin could point at `127.0.0.1:53` and exfiltrate
  split-horizon internal names. Custom-server resolves are now gated behind the
  explicit `dns:resolve_private` capability (the AXFR path already guarded this).
- **`stado_memory_update` can no longer self-approve.** The approve-guard only
  covered the `approve` action; a `memory:write` plugin could inject APPROVED
  (prompt-injectable) memories via `upsert`/`supersede` or a
  `confidence:"approved"` payload. The denylist now covers upsert/supersede and
  empty-confidence upserts — plugin writes stay candidate-only.
- **`MinisignVerify` no longer panics on a wrong-length public key** — it
  validates `len(pub) == ed25519.PublicKeySize` before verifying.
- **Secret-file reads close a symlink-follow + TOCTOU gap.** `readSecretFile`
  now opens with `O_NOFOLLOW` and fstat's the file descriptor (regular-file +
  0600 check on the same fd it reads) instead of `os.Stat` then `os.ReadFile`.

### Plugin ABI migration note

- New capability `dns:resolve_private`. Plugins that call `stado_dns_resolve`
  with a non-default DNS server must now declare it; default-resolver lookups are
  unaffected.
- `exec:pty` / `terminal:open` now honor an optional `:<glob>` scope and enforce
  it on the spawned binary (previously the glob was parsed but not applied to PTY
  spawns).

### Docs

- Threat model: scoped the broker capability-ceiling claim to `stado run` + the
  TUI (the only surfaces that thread it into the runner); `headless` / `acp` /
  `mcp-server` attach to the broker but rely on their host-default exec policy.
- `docs/commands/plugin.md`: documented `plugin remove` / `untrust-anchor` and
  the `install` flags. DESIGN.md git-sub-agent section reframed as target design
  with as-built status.

