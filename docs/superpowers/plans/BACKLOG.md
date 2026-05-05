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

## 5. Slash-command persistence default

- **Locked at:** NOTES Q7 — *"slash = per-session, CLI = persist; `--save` flips slash to write config"*.
- **Status today:** Moot — TUI slash mutating verbs aren't built (the corresponding `stado tool enable/...` CLI verbs landed in `563d251` but no `/tool enable` mirror yet).
- **Action:** When wiring `/tool enable / disable / autoload / unautoload` into TUI, default to per-session, add `--save`.
- **Files:** `internal/tui/model_commands.go`.

## 6. `tools.describe(names: [str])` batched form

- **Locked at:** NOTES Q4 — *"batched, one round-trip"*.
- **Status today:** Single-name path lives in `internal/runtime/meta_tools.go`. Batching not implemented.
- **Effort:** Tiny — accept `string | []string` and dispatch over the slice.

## 7. Typed-prefix dotted handle IDs (`proc:fs.7a2b`)

- **Locked at:** NOTES line 1057-1078 — *"Type-prefix is mandatory ... `/ps`/`/kill` use these"*.
- **Status today:** EP-0038a Task 1 used opaque uint32/uint64 handles. `/ps` / `/kill` work but render bare numbers.
- **Decision needed:** Adopt the dotted format for operator-facing surfaces (renderer + parser, no on-the-wire change), or formally drop from locked decisions.
- **Files:** `internal/plugins/runtime/handles.go`, `internal/tui/model_commands.go` (`/ps` + `/kill` renderers).

## 8. EP-0038e — Tier 2 stateful HTTP + secrets

- **Locked at:** NOTES + EP-0038 §B Tier 2.
- **Originally scoped in:** `EP-0038a` plan §Goal/§File Map; deferred at §Self-Review.
- **Surface:**
  - `stado_http_client_*` — persistent client w/ cookie jar, mux limits, redirect policy.
  - `stado_secrets_*` — operator secret store (`~/.config/stado/secrets/`), audited fetch, never-on-stdout.
- **Effort:** Substantial — handle table, cookie-jar lifecycle, secret store provisioning, capability surface (`net:http_client`, `secrets:read`, `secrets:write`).
- **Recommendation:** Write a fresh `2026-MM-DD-ep-0038e-tier2-stateful.md` plan when this becomes a priority; don't fold into another plan.

## 9. `plugin install --pubkey-file` already done; `plugin sign` for CI pipelines

- **Locked at:** NOTES quality-pass.
- **Status today:** `--pubkey-file` landed (EP-0039 Task 5). `plugin sign <dir> --key <path>` for CI is implicit (sign is bundled into install/dev today). Worth a separate `plugin sign` verb for headless CI signing.
- **Files:** `cmd/stado/plugin_sign.go` (extend).

## 10. Delete `buildNativeRegistry()` per EP-0038b Task 5

- **Locked at:** EP-0038b plan Task 5 (NOT in NOTES — this is plan-vs-code drift).
- **Status today:** Native code in `internal/tools/{fs,bash,webfetch,rg,astgrep,readctx,lspfind}` is alive and registered, then wrapped in a wasm shim at registration time. Plan called for full deletion + relocation under host imports.
- **Risk:** High — would touch every native tool, parity tests need to stay green. Practically the parity wasm/native flag system today gives you the migration knob without forcing the deletion.
- **Recommendation:** Treat as deferred indefinitely; the no-native-tools invariant is satisfied at the model-facing surface, and removing the dual-path harness today loses the parity-test backstop. Document the deviation in `docs/eps/0038`.

---

## Conventions for picking from this backlog

- Each item lists **files** to keep grep-able; treat the list as load-bearing when starting a plan.
- Items 2, 4, 6, 9 are tiny enough to land in a single PR without a full superpowers plan.
- Items 1, 3, 5, 7 deserve a proper plan or an `AskUserQuestion` first to settle the design choice.
- Item 8 is its own EP.
- Item 10 is best left as a documented deviation rather than executed.
