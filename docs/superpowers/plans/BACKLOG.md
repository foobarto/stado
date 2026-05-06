# Backlog — locked decisions awaiting a plan

> **Status (2026-05-06):** All 10 items from the 2026-05-05
> architectural-reset locked-decisions list are resolved.
> 8 shipped, 1 documented deferral, 1 split between #5 (done)
> and #7 (done).

---

## 1. ~~Collapse `spawn_agent` (native) and `agent.spawn` (wasm)~~ — **DONE 2026-05-06** (`feat/collapse-spawn-agent-surface` PR)

Native `spawn_agent` registration dropped; wasm `agent.spawn`
(registered as `agent__spawn`) is the canonical surface. Both
paths went through `SubagentRunner` already; the wasm form is
a strict superset (adds `agent.list`, `read_messages`,
`send_message`, `cancel`, async mode). `subagent.Tool{}` type
deleted; `subagent.ToolName` const retained as a label for
commit metadata.

## 2. ~~`plugin install --autoload`~~ — **DONE 2026-05-06** (`feat/plugin-install-autoload` PR)

`stado plugin install --autoload` now persists the newly-
installed plugin's tools into `[tools].autoload` via
`config.WriteToolsListAdd`. Failure to write the config is
non-fatal (warn to stderr; install still succeeded).

## 3. ~~`plugin doctor` cap-vs-sandbox cross-check~~ — **DONE 2026-05-06** (`feat/plugin-doctor-sandbox-check` PR)

After the existing surface-compatibility report, `plugin doctor`
walks the plugin's caps against `[sandbox]` config. Three
severity tiers (✗ error / ⚠ warn / i info) cover net-blocked,
namespaced-no-proxy, fs paths not in bind_ro/bind_rw, and the
exec:* informational note.

## 4. ~~`plugin reload <name>` (whole-plugin)~~ — **DONE 2026-05-06** (`feat/plugin-reload` PR)

CLI `stado plugin reload <name>` validates + lists tools.
TUI `/plugin reload [<name>]` rebuilds the executor's tools
registry so plugins installed AFTER session start become
visible without restart. Tool calls always re-read plugin.wasm
from disk per invocation (no wasm cache to invalidate); the
CLI subcommand is advisory + informational.

## 5. ~~Slash-command persistence default~~ — **DONE 2026-05-05** (`feat/tui-slash-and-handles` PR)

`/tool enable / disable / autoload / unautoload` shipped with per-session default + `--save` flag flip. See `internal/tui/model_commands.go`.

## 6. ~~`tools.describe(name | names)` batched form~~ — **DONE 2026-05-06** (`feat/tools-describe-batched` PR)

`tools__describe` now accepts `name: "foo"` (single) OR
`names: ["foo","bar"]` (batched). Both forms can be passed
in one call; entries are merged and deduped preserving order.

## 7. ~~Typed-prefix dotted handle IDs (`proc:fs.7a2b`)~~ — **DONE 2026-05-05** (`feat/tui-slash-and-handles` PR)

`FormatHandleID`/`ParseHandleID` + `HandleType` constants in `internal/plugins/runtime/handles.go`.

## 8. ~~EP-0038e — Tier 2 stateful HTTP + secrets~~ — **DONE 2026-05-06** (`feat/ep-0038e-tier2-stateful` PR)

`internal/secrets/` (operator store + `stado secrets` CLI) and
`internal/httpclient/` (cookie jar + redirect policy + dial guard)
shipped, plus `stado_secrets_*` and `stado_http_client_*` wasm
imports gated by `secrets:read[:<glob>]`, `secrets:write[:<glob>]`,
and `net:http_client`. Spec + handoff in
`docs/superpowers/specs/2026-05-06-ep-0038e-tier2-stateful-design.md`.

## 9. ~~`plugin sign` for CI pipelines~~ — **DONE 2026-05-06** (`feat/plugin-sign-ci` PR)

`stado plugin sign --key-env <ENVVAR>` reads the seed from an
env var (hex or base64), eliminating the temp-file dance for
CI runner secrets. `--quiet` suppresses informational stdout
for clean CI logs.

## 10. ~~Delete `buildNativeRegistry()` per EP-0038b Task 5~~ — **DEVIATION DOCUMENTED 2026-05-06**

Decision: retain native `buildNativeRegistry()` indefinitely as the
parity-test backstop + operational fallback. The no-native-tools
invariant holds at the model-facing surface (wasm-side wins when
enabled); deleting the native path would lose the parity cross-check
and the runtime opt-out via `[runtime.use_wasm.<tool>]`.

Full justification + revisit conditions in `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`'s "Deviation: buildNativeRegistry() retained as parity backstop" section.

---

## Done — empty. New work goes through `superpowers:brainstorming` first.
