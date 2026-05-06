# Backlog — locked decisions awaiting a plan

These items were **locked in the 2026-05-05 architectural reset**
(`docs/eps/notes/2026-05-05-architectural-reset.md`) but never made it
into a `superpowers:writing-plans` task list. None are blocking, but
each has a paid-in-full design discussion and just needs the
implementation step.

Listed in rough priority order (highest = most operator pain).

---

## 1. Collapse `spawn_agent` (native) and `agent.spawn` (wasm) into one canonical surface

- **Locked at:** NOTES line ~520 (agent surface section), reaffirmed in `defaultAutoloadNames` TODO comment in `internal/runtime/executor.go`.
- **Problem:** Two registered tools that do the same thing. Only `spawn_agent` fires `SubagentEvent` (lifecycle observability). The wasm `agent.spawn` doesn't. The LLM may be unsure which to use; the operator can't filter to just one.
- **Options:**
  - Route `agent.spawn` execution through the SubagentRunner so it emits the same events; drop the bare `spawn_agent` registration.
  - Drop the wasm `agent.spawn` and keep only the native.
  - Document them as intentionally distinct (audit-emitting vs ephemeral).
- **Files:** `internal/runtime/bundled_plugin_tools.go`, `internal/runtime/agentloop_helpers.go`, `internal/runtime/executor.go`, `internal/plugins/runtime/host_agent.go`.

## 2. `plugin install --autoload`

- **Locked at:** NOTES line 1093 — *"pin to autoload at install time, persisted in config. Saves the operator the second `tool autoload` call."*
- **Effort:** Tiny. Add `--autoload` flag to `pluginInstallCmd`; on success, call `config.WriteToolsListAdd(path, "autoload", <newly-installed-tool-names>)`.
- **Files:** `cmd/stado/plugin_install.go`. `WriteToolsListAdd` already exists (`internal/config/write_defaults.go` — landed in `563d251`).

## 3. `plugin doctor` cap-vs-sandbox cross-check

- **Locked at:** NOTES line ~1056 — *"declared caps vs `[sandbox]` constraints (e.g. plugin wants `net:dial:tcp:*:*` but operator's sandbox is `network = "namespaced"` — flag with severity)"*.
- **Effort:** Modest. Read `[sandbox]` config + the plugin's manifest caps; cross-reference and emit warnings.
- **Files:** `cmd/stado/plugin_doctor.go` (existing), `internal/plugins/manifest.go` (cap parse helpers).

## 4. `plugin reload <name>` (whole-plugin)

- **Locked at:** NOTES Q8 — *"both, distinct"* — `tool reload` (per-tool) AND `plugin reload` (whole-plugin module).
- **Status today:** `tool reload` exists; `plugin reload` doesn't.
- **Effort:** Small. `cmd/stado/plugin_reload.go` invalidating cached `*pluginRuntime.Module` for a plugin name.

## 5. ~~Slash-command persistence default~~ — **DONE 2026-05-05** (`feat/tui-slash-and-handles` PR)

`/tool enable / disable / autoload / unautoload` shipped with per-session default + `--save` flag flip. See `internal/tui/model_commands.go`.

## 6. `tools.describe(names: [str])` batched form

- **Locked at:** NOTES Q4 — *"batched, one round-trip"*.
- **Status today:** Single-name path lives in `internal/runtime/meta_tools.go`. Batching not implemented.
- **Effort:** Tiny — accept `string | []string` and dispatch over the slice.

## 7. ~~Typed-prefix dotted handle IDs (`proc:fs.7a2b`)~~ — **DONE 2026-05-05** (`feat/tui-slash-and-handles` PR)

`FormatHandleID`/`ParseHandleID` + `HandleType` constants in `internal/plugins/runtime/handles.go`.

## 8. ~~EP-0038e — Tier 2 stateful HTTP + secrets~~ — **DONE 2026-05-06** (`feat/ep-0038e-tier2-stateful` PR)

`internal/secrets/` (operator store + `stado secrets` CLI) and
`internal/httpclient/` (cookie jar + redirect policy + dial guard)
shipped, plus `stado_secrets_*` and `stado_http_client_*` wasm
imports gated by `secrets:read[:<glob>]`, `secrets:write[:<glob>]`,
and `net:http_client`. Spec + handoff in
`docs/superpowers/specs/2026-05-06-ep-0038e-tier2-stateful-design.md`.

## 9. `plugin install --pubkey-file` already done; `plugin sign` for CI pipelines

- **Locked at:** NOTES quality-pass.
- **Status today:** `--pubkey-file` landed (EP-0039 Task 5). `plugin sign <dir> --key <path>` for CI is implicit (sign is bundled into install/dev today). Worth a separate `plugin sign` verb for headless CI signing.
- **Files:** `cmd/stado/plugin_sign.go` (extend).

## 10. ~~Delete `buildNativeRegistry()` per EP-0038b Task 5~~ — **DEVIATION DOCUMENTED 2026-05-06**

Decision: retain native `buildNativeRegistry()` indefinitely as the
parity-test backstop + operational fallback. The no-native-tools
invariant holds at the model-facing surface (wasm-side wins when
enabled); deleting the native path would lose the parity cross-check
and the runtime opt-out via `[runtime.use_wasm.<tool>]`.

Full justification + revisit conditions in `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`'s "Deviation: buildNativeRegistry() retained as parity backstop" section.

---

## Conventions for picking from this backlog

- Each item lists **files** to keep grep-able; treat the list as load-bearing when starting a plan.
- Items 2, 4, 6, 9 are tiny enough to land in a single PR without a full superpowers plan.
- Items 1, 3, 5, 7 deserve a proper plan or an `AskUserQuestion` first to settle the design choice.
- Item 8 is its own EP.
