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

# Audit additions (2026-05-05 quality pass)

Added after a three-axis audit (consistency / code-quality / spec-vs-code
reconciliation) of the v0.33.0 landings. The original 10 items above
remain accurate. The items below are drift the original BACKLOG didn't
catch — verified against code, not just inferred from agent reports.

## 11. Tier 1 networking primitives missing entirely

- **Locked at:** NOTES §3 / EP-0038 §B Tier 1 — `stado_net_dial(transport,
  addr, opts)`, `stado_net_listen`, `stado_net_accept`, `stado_net_{read,
  write,close}`, `stado_net_icmp_{open,send,recv,close}`.
- **Status today:** No `host_net.go` file exists. Zero `stado_net_*`
  imports registered in `internal/plugins/runtime/`. `host_dns.go`,
  `host_http_request_test.go` are the only network-adjacent files.
- **Effect:** Plugins cannot open raw sockets, listen for callbacks,
  or do raw ICMP. The HTB use case for `net.listen` (callbacks /
  reverse-shell handlers) is unbuildable. Capability vocabulary
  (`net:dial:<transport>:<host>:<port>` etc.) is documented but has
  nothing to gate.
- **Effort:** Substantial — host_net.go scaffolding + handle table
  integration + capability matcher for `<transport>:<host>:<port>`
  globs + ICMP raw-socket privilege check (kernel-side only).
- **Recommendation:** Own EP — call it EP-0038f or similar. Not a
  patch fix.

## 12. `stado_json_parse` / `stado_json_stringify` missing

- **Locked at:** NOTES §3 Tier 3 — strict RFC 8259.
- **Status today:** Not registered in runtime/host_*.go. Plugins
  must roll their own JSON parsers in wasm or use the SDK's
  helpers.
- **Effort:** Small — `host_json.go` wrapping `encoding/json`.
- **Recommendation:** Single PR alongside the Tier-1 networking
  EP, or its own micro-PR.

## 13. `stado_dns_resolve_axfr` missing

- **Locked at:** NOTES §3 Tier 2.
- **Status today:** Only `stado_dns_resolve` present in
  `host_dns.go`. AXFR is a separate import per the locked surface.
- **Effort:** Small — extend `host_dns.go`. Capability
  `dns:axfr:<zone>` is in the vocabulary already.
- **Recommendation:** Bundle with the Tier-1 networking EP.

## 14. `agent.send_message` and `agent.read_messages` are stubs

- **Status today:** `internal/runtime/fleet_bridge.go:99,135` —
  `AgentReadMessages()` returns "best-effort single assistant
  message"; `AgentSendMessage()` is a no-op. Both wired tools are
  registered and visible to the LLM but don't do anything useful.
- **Effect:** Of the locked 5-tool agent surface, only `spawn`,
  `list`, and `cancel` are functional. The two-channel
  communication model (and the architectural payoff of
  agents-as-sessions) is a stub.
- **Effort:** Modest — wire the FleetBridge to the actual session
  message-queue + driver-field plumbing. Probably a fresh plan.
- **Recommendation:** Address as part of resolving Item 1
  (spawn_agent / agent.spawn collapse) — both touch the same
  FleetBridge path.

## 15. CLI flag name drift — `--tools-whitelist` vs locked `--tools`

- **Locked at:** NOTES §10 — *"`--tools <list>` (whitelist;
  lockdown mode)"*.
- **Status today:** `cmd/stado/run.go:38-40,409` use
  `--tools-whitelist`. Comment at `:405-408` acknowledges back-
  compat: "the canonical name agreed in NOTES is `--tools`."
- **Effort:** Tiny — rename flag (this is pre-1.0, no kid gloves
  per NOTES line 1117).
- **Recommendation:** Single PR. Drop `--tools-whitelist`
  entirely; do not keep an alias.

## 16. Silent JSON-parse error swallows in meta-tools

- **Status today:** `internal/runtime/meta_tools.go:43`
  (`metaSearch.Run`) and `:147` (`metaCategories.Run`) use
  `_ = json.Unmarshal(...)`. Malformed args silently default to
  empty query. Inconsistent with `metaDescribe:101` and
  `metaInCategory:186` which check the error.
- **Effort:** Tiny — change two lines.
- **Recommendation:** Single PR; bundle with any meta-tool work.

## 17. `FetchAnchorPubkey` ignores caller context

- **Status today:** `internal/plugins/anchor.go:23` calls
  `cl.Get(url)` with `//nolint:noctx`. Hardcoded 15s timeout, no
  cancellation. Operator can't Ctrl-C an anchor fetch.
- **Effort:** Small — switch to `http.NewRequestWithContext` and
  thread the caller's `ctx`.
- **Recommendation:** Single PR.

## 18. Handle registry collision spins forever

- **Status today:** `internal/plugins/runtime/handles.go:26-40` —
  on collision, `alloc()` retries indefinitely. No max-attempt
  bound. Theoretical deadlock under registry-full or rand-broken
  conditions.
- **Effort:** Tiny — bound the retry loop, return error on
  exhaustion.
- **Recommendation:** Single PR.

## 19. Parity tests cover only `fs` + `shell` families

- **Status today:** `internal/runtime/parity_test.go` gates `fs`
  and `shell` only. EP-0038b migrated `rg`, `astgrep`, `readctx`,
  `web`, `dns`, `agent` — none have parity backstops. The
  `STADO_PARITY_*` env-gate harness is in place but unused for
  these families.
- **Effect:** The parity-test backstop that justified keeping
  `internal/tools/*` (Item 10) only works for two of the seven
  migrated families.
- **Effort:** Modest — one parity test per family, reusing the
  harness.
- **Recommendation:** Plan-worthy; bundle with any work that
  touches the migrated tools.

## 20. `[sessions] auto_prune_after` config schema not wired

- **Locked at:** NOTES §8 — *"`[sessions] auto_prune_after = ""`
  (off by default)"*.
- **Status today:** No `Sessions` struct in
  `internal/config/config.go`. CLI `stado session prune` exists
  for explicit cleanup, but the config knob is absent (operators
  who want time-based retention have no toggle).
- **Effort:** Small — add struct, parse, wire to the existing
  prune codepath as a startup-time hook.
- **Recommendation:** Single PR.

## 21. Manual wire-form munging bypasses helpers

- **Status today:** `internal/tools/tool.go:121` does
  `strings.ReplaceAll(query, ".", "__")` directly. The
  `WireForm`/`ParseWireForm` helpers exist precisely to keep this
  round-trip in one place.
- **Effort:** Tiny — replace with the helper call.
- **Recommendation:** Single PR.

## 22. `nolint:staticcheck` on `fmt.Errorf("%s", msg)`

- **Status today:** `internal/sandbox/wrap.go:99`. Should be
  `errors.New(msg)` or `fmt.Errorf(msg)`. The `nolint` hides a
  valid lint signal.
- **Effort:** Trivial.
- **Recommendation:** Bundle into any sandbox-touching PR.

## 23. Unused exported `ErrLockNotFound`

- **Status today:** `internal/plugins/lock.go:142` exports
  `ErrLockNotFound` but no callsite checks for it. Either delete
  or wire into the install/load paths that should distinguish
  "lock not found" from generic read errors.
- **Effort:** Trivial.

## 24. `defaultAutoloadNames` mixes wire forms and bare names

- **Status today:** `internal/runtime/executor.go:20-34` lists
  both `"read"` (bare native name) and `"fs__ls"`, `"spawn_agent"`
  (wire form). Comment promises a cleanup post-EP-0038; still
  mixed.
- **Effort:** Small — pick wire form throughout, update comment.
- **Recommendation:** Single PR; bundle with Item 1 collapse work
  (touches the same area).

## 25. `golangci-lint` test-file blanket exclusions

- **Status today:** `.golangci.yml` exempts errcheck/unused
  wholesale in `*_test.go`. Hides setup-failure smells (e.g. a
  test that silently fails to spin up a fixture).
- **Effort:** Small — narrow the exclusion to specific rules
  (e.g. `t.TempDir()` cleanup-error suppression only).
- **Recommendation:** Single PR.

---

## Conventions for picking from this backlog

- Each item lists **files** to keep grep-able; treat the list as load-bearing when starting a plan.
- **Originals 2, 4, 6, 9** + **audit additions 12, 13, 15, 16, 17, 18, 20, 21, 22, 23, 24, 25** are tiny enough to land in a single PR without a full superpowers plan.
- **Originals 1, 3, 5, 7** + **audit additions 14, 19** deserve a proper plan or an `AskUserQuestion` first to settle the design choice.
- **Original 8** + **audit addition 11** are each their own EP.
- **Original 10** is best left as a documented deviation rather than executed.

## Severity rollup (audit, 2026-05-05)

- **MUST FIX (operator-visible / functional gap):** 11 (Tier 1 net), 14 (agent messaging stubs), 15 (flag name), 1 (spawn_agent dup).
- **SHOULD FIX (correctness / hygiene):** 12, 13, 16, 17, 18, 19, 20, 21, 7.
- **NIT (cleanup):** 22, 23, 24, 25.
