## v0.68.0 — TUI-hang fix + EP-audit security/correctness fixes — 2026-06-14

A streaming-hang fix plus a batch of fixes from an EP-vs-codebase audit. The
headline is the TUI freeze; several sandbox/plugin security gaps where a
documented control wasn't actually enforced are also closed.

### Fixes

- **TUI no longer freezes on a cold / after-idle turn.** The OpenAI-compatible
  streaming client (MiniMax and every OAI-compat backend) used the default HTTP
  transport, whose HTTP/2 connection pool has no liveness check — so a pooled
  connection silently dropped during an idle gap (common behind NAT/VM layers)
  was reused on the next turn and blocked forever on the response read, freezing
  the whole UI with no error. The streaming client now sets HTTP/2
  ReadIdleTimeout + PingTimeout so a dead connection is detected and evicted
  (no overall deadline — long generations are unaffected).
- **Scheduled session-resume works.** `schedule` passed `--session-id` but the
  flag is `--session`, so every scheduled session-resume failed with "unknown
  flag". (EP-0036)
- **`stado tool run` honors project-local plugins.** The agent-loop tool
  registry and tool-run resolution now search project-local `.stado/plugins/`
  in addition to the global state dir; a project plugin shadows a global one of
  the same name. (EP-0035)
- **ACP `register_mcp` consent is honored.** The config flag had no field, so
  MCP auto-registration was unreachable. (EP-0032)

### Security

- **The seccomp syscall deny-list is now actually enforced.** The compiled BPF
  filter (kills mount/ptrace/reboot/kexec_load/…) was built but never handed to
  bubblewrap, so the kill-list the docs claim was enforced ran nowhere. It is
  now passed via `--seccomp` on every sandboxed tool call (deny-net / allow-all
  / no-net paths), fail-safe if unavailable. (EP-0005)
- **The meta-tool dispatch kernel can no longer be disabled.** `tools.*` /
  `plugin.load` / `plugin.unload` survive `[tools].disabled` / a narrow
  allowlist (they were silently unregistered, leaving the model unable to
  discover or activate any tool). (EP-0037)
- **Plugins can no longer self-approve their own candidate memories.** The
  memory host import rejected `approve` from a plugin (case/space-normalised) —
  approval is operator-only. (EP-0015)
- **Security harness applies on every surface.** `[harness].mode = "security"`
  now engages on the TUI / ACP / headless surfaces (was honored only by
  `stado run`) and is surfaced in `stado config init`. (EP-0030)
- **Daemon repo-root discovery uses the shared HEAD-checked predicate** (a
  stray HEAD-less `.git` is no longer accepted as a repo root). (EP-0027)

