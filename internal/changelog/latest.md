## v0.67.0 — autonomous-surface bash confinement + resume broker parity — 2026-06-14

Tightens tool-execution confinement on the autonomous surfaces and brings
`session resume` in line with the bare TUI's broker handling.

### Security

- **`stado acp` and `stado headless` now confine `bash` by default.** Both drive
  the agent autonomously (Zed / ACP clients; external HTTP / MCP clients) like
  `mcp-server` and the daemon — but unlike those peers they applied no sandbox
  policy to `bash` (`shell.exec`), so a tool call could read anything the process
  could, including credential files. They now run `bash` under the same
  protective default policy as `mcp-server` / daemon (working dir + `/tmp` +
  system paths; **no `$HOME`**). Filesystem tools were already confined to their
  granted (workdir-scoped) paths on every surface — this closes the `bash` / exec
  gap. Interactive surfaces (`stado run`, the TUI, `session resume`) keep the
  operator's-filesystem semantics by design: `bash` there runs with your access.
  Behavior change for external acp/headless integrations: a `bash` tool call can
  no longer read outside the working dir + `/tmp` + system paths.

### Fixes

- **`session resume` attaches to the broker like the bare TUI.** Resume now goes
  through the same broker-attach + sandbox-mode-announcement path as launching
  `stado` directly (a shared inline-TUI launch), instead of a separate
  un-attached path — so a resumed session is handled consistently (it requires
  the broker, or an explicit `--no-sandbox` / `STADO_BROKER_ATTACH=0` opt-out,
  the same as the bare TUI).

