# Operational Commands

This guide covers the smaller operator-facing commands that support the main
agent, session, plugin, and audit workflows. Exact flag spelling remains
available through `stado <command> --help`.

## Provider credentials: `stado auth`

`stado auth` records environment-variable references, never credential values.
Use `list` to see redacted availability, `set <provider>` to configure a
reference, and `unset <provider>` to remove it. Use `stado secrets` for plugin
secrets that are not provider credentials.

```sh
stado auth list
stado auth set anthropic --env ANTHROPIC_API_KEY
stado auth unset anthropic
```

## Stateful tool host: `stado daemon`

The per-user daemon retains PTYs, browser cookie jars, LSP connections, and
compiled WASM state across otherwise independent `stado tool run` calls. Tool
execution starts it automatically when needed; manual lifecycle commands are:

```sh
stado daemon start
stado daemon status
stado daemon reload
stado daemon stop
```

The socket defaults to `$XDG_RUNTIME_DIR/stado/daemon.sock` on Linux and a
mode-0700 per-user directory below `$TMPDIR` elsewhere. `STADO_DAEMON_SOCKET`
overrides that location.

## Security harness: `stado harness`

`stado harness init` creates the security-engagement folder layout in the
current project. Select security mode with the TUI or run command's harness
mode controls; project-specific harness text belongs under `.stado/harness/`.

```sh
stado harness init
stado run --mode security --tools --prompt "map the target attack surface"
```

## Install and uninstall

`stado install` copies the running binary into the first writable XDG-style
directory on `PATH`. `--prefix` chooses an exact directory and `--force`
permits replacement of a different binary. `stado uninstall` removes stado
from the same user-writable candidate directories; it does not remove state,
configuration, or an independently located running binary.

```sh
stado install
stado install --prefix "$HOME/.local/bin" --force
stado uninstall
```

## External agent discovery: `stado integrations`

This command detects supported external coding-agent CLIs and reports their ACP
and MCP compatibility. `--json` provides stable machine-readable output.

```sh
stado integrations
stado integrations --json
```

## Scheduled runs: `stado schedule`

Schedules live in the configured state directory and can be executed directly
or installed into the user's OS crontab. `install-cron` and `uninstall-cron`
only manage entries marked as stado-owned.

```sh
stado schedule create --help
stado schedule list
stado schedule run-now <id>
stado schedule install-cron
stado schedule rm <id>
stado schedule uninstall-cron
```

## Operator secret store: `stado secrets`

Plugin secrets are stored as mode-0600 files below the state directory. Values
are supplied through stdin by default and are not written to logs or audit
trails. `get` deliberately emits raw bytes, so avoid terminals and pipelines
that record output.

```sh
printf '%s' "$TOKEN" | stado secrets set service-token
stado secrets list
stado secrets get service-token
stado secrets rm service-token
```

## Tool inspection and execution: `stado tool`

`list`, `categories`, and `info` inspect the effective model-facing tool
surface. `enable`/`disable` change policy, while `autoload`/`unautoload` control
which schemas are sent every turn. `run` invokes one canonical (`fs.read`) or
wire-form (`fs__read`) tool; installed-plugin details are covered in the
[plugin guide](plugin.md).

```sh
stado tool list
stado tool categories
stado tool info fs.read
stado tool enable fs.read
stado tool autoload fs.read
stado tool run fs.read '{"path":"README.md"}'
stado tool reload
```

## Audit-derived usage: `stado usage`

`stado usage` folds signed session commit trailers into all-time model totals.
Use `--since` and `--until` for relative or absolute windows, `--by-session`
for a session breakdown, and `--json` for automation. This differs from
[`stado stats`](stats.md), which is the recent interactive dashboard.

```sh
stado usage
stado usage --since 7d --by-session
stado usage --since 2026-08-01 --json
```
