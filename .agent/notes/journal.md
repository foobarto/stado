# Journal — refactor+quality-2026-q2

Append-only. Newest at the bottom.

---

## 2026-05-07 — session start
- Mode: Feature / Phase 1.1 (bridge contract tests).
- Plan committed (3 rounds of consult+fix with codex/gemini).
  Last commit: `ef55cb9`.
- About to read existing bridges (host_session.go, host_memory.go,
  host_ui.go, host_agent.go, host.go) and existing test files
  (bridge_test.go, memory_bridge_test.go, host_ui_choose_test.go,
  session_caps_test.go, host_test.go) before sketching the harness.
  Several test files already exist that overlap with 1.1's scope —
  need to figure out what's covered vs. what 1.1 adds.

## 2026-05-07 — context loaded; harness design

What's already covered in `internal/plugins/runtime/`:
- `host_test.go` — NewHost manifest parsing.
- `session_caps_test.go` — session/llm cap parsing + a `fakeBridge`
  with call counters; explicitly notes "we can't invoke wasm host
  imports without a wasm module" and tests indirectly.
- `memory_bridge_test.go` — LocalMemoryBridge implementation
  round-trip (not the host import gate).
- `host_ui_choose_test.go` — `decodeChooseRequest` validator only.

What 1.1 needs to add: tests for the host-import dispatch logic
itself — capability-gate, nil-bridge, exact forwarding, cancel.

Design call: bridge contract tests use a real wazero runtime +
`InstallHostImports`. After registration, the host module's
exports are reachable via `rt.Wazero().Module(NamespaceStado)
.ExportedFunction(name)` and callable from Go.

Two-tier tests:
- **Memory-free** (capability-gate, nil-bridge): the closure
  short-circuits before touching `mod.Memory()`. Calling the
  exported function with arbitrary i32 args produces the -1
  return without dereferencing memory. Test directly.
- **Memory-using** (exact forwarding): need a thunk wasm module
  that imports the host imports + exports a caller. Hand-write
  bytes — minimal module per import, embed in the harness.

Cancel contract: where the bridge takes ctx, test that the
bridge's recorded ctx is the same one passed to the host import,
and that ctx cancellation reaches the bridge. Where the bridge
doesn't take ctx (SessionBridge.ReadField), the contract is N/A
for that method.

Starting with the harness file now.

## 2026-05-07 — Phase 1 round-4 review fixes

Codex + gemini round-4 review caught real gaps in Phase 1 work.
All "must-fix" items applied:

- Sandbox T2 false-positive risk: `Available()=LookPath` isn't
  enough (codex saw bwrap on PATH but T2 still failing in their
  env). Added `tier2ReadyRunners(t)` probe that runs a benign
  `true` through the runner and skips T2 with a clear log if
  the probe fails. Negative control test now uses the probed
  list.
- Sync spawn cancel orphan: plan promised "runtime state cleaned
  (no orphan record)"; implementation deliberately doesn't (spawn
  goroutine uses RootCtx, not caller ctx). Added test asserting
  the actual behaviour (entry remains in registry with running
  status after caller cancel). Plan edited to match.
- ReadMessages offset gaps: added since>current, since<0, and
  since-forwarded tests. Documents current behaviour where since
  is echoed through unchanged; plan edited to note alignment is
  out of scope.
- FleetBridge cancel-prop: added cancel-prop tests for AgentList,
  AgentSendMessage, AgentCancel (previously only Spawn and
  ReadMessages were covered).
- Forwarding fields: AgentSpawn forwarding now covers Persona /
  ParentSession / AllowedTools / SandboxProfile / Ephemeral.
  LLMInvoke covers Persona / System / Temperature.
  AgentReadMessages asserts forwarded Since (was 0; now 5).
- Plan edits: Phase 1.1 SessionBridge / MemoryBridge / Approval /
  Choice / Fleet specifics rewritten to match HEAD interfaces.
  Phase 1.3 cancellation, message-offset, and missing-agent
  sections updated to reflect actual behaviour.

Deferred (per Bartosz's instruction):
- WASM encoder hardening (duplicate names, negative NumParams,
  zero-result thunks). No real hazard at current scale.
- Channel-signalled cancel pattern (replacing time.Sleep). Works
  today; opportunistic refactor.
- Phase 2.1 path-traversal hardening — fold into A2 spec at start.

## 2026-05-07 — Phase 1.3 complete

FleetBridgeAdapter contract tests cover the layer the plan was
specifically worried about (no test file existed for fleet_bridge.go;
fleet_test.go covered the underlying *Fleet, not the adapter).

17 tests covering all 5 adapter methods + concurrency (20-way
parallel spawn). Stable under -count=10 -race.

Plan-vs-reality: Fleet.Cancel is documented idempotent (returns
nil for unknown IDs). Plan called for typed not-found error.
Aligning would be a behavior change; existing callers depend on
idempotency. Kept current behavior; documented divergence in the
test for future-fix follow-up.

Race detector caught a latent issue in the package's existing
fakeSpawner (gotPrompt write isn't atomic). Existing tests run
serially so they don't hit it. My concurrency test would have, so
I added a tiny race-safe `concurrentSpawner` for that test only —
didn't touch fakeSpawner per "don't fix things you weren't asked
to fix."

Phase 1 is now complete. Ready for merge checkpoint #1.

## 2026-05-07 — Phase 1.2 complete

Runner contract test in
`internal/sandbox/runner_contract_test.go` follows the round-3
plan revision: Tier 1 covers command-construction + exec
allow-list semantics (every available runner); Tier 2 covers
runtime FS enforcement (only BwrapRunner here on Linux, would
extend to SbxRunner on macOS).

Tier 2 design choice: subprocess result inspected via
`cmd.Run()` returning `*exec.ExitError` for denied writes and
checking the host-visible filesystem for the un-created file.
This matches the round-3 reframe ("subprocess exit code, not
return value from `Command`") because `Runner.Command` returns
only `*exec.Cmd`, never a denial — denial happens inside the
spawned bwrap/sandbox-exec process.

Composition parked as
`.agent/specs/open/sandbox-multilayer-composition.md`. Verified
against source: `runner_linux.go::detectList` returns only
`BwrapRunner` and `NoneRunner`; landlock/seccomp exist as modules
but aren't wired.

## 2026-05-07 — Phase 1.1 complete

All 5 bridges have contract tests for all 4 contracts. Pattern:
recordingFooBridge with mu+atomic counters, ctx capture, blocking
flags. cap-gate / nil-bridge tests run with allBridgeImports
thunk shape; forwarding tests stage payloads in thunk memory and
assert recorder + return buffer. Cancel tests use context.WithTimeout
and assert on the recorder's captured ctx.Err() (the closure return
races with wazero's CloseOnContextDone).

Test counts: SessionBridge 11, MemoryBridge 9, ApprovalBridge 6,
ChoiceBridge 6, FleetBridge 13. Total 47 (the harness probe is
counted in the SessionBridge file).

Plan-vs-reality findings logged here for B3:
- SessionBridge ops in plan ("Append-block, get-block-by-id,
  list-blocks, set-metadata") don't match reality. Actual:
  NextEvent / ReadField / Fork / InvokeLLM. Tests adapted.
- ChoiceBridge return convention is bytes-encoded (positive ok,
  negative error msg via encodeToolSidePayload). Tests assert
  the sign + the error message text on cap-deny / nil-bridge
  paths. Other bridges use plain -1 sentinel.

## 2026-05-07 — probe failed, need wasm encoder

Wazero panics with "calling ExportedFunction is forbidden on host
modules" when you try to invoke a host import directly. The probe
test confirmed this. So the harness can't take the easy path.

Path forward: a small in-Go wasm encoder that builds a thunk
module per test session. The thunk imports the stado host imports
we want to test and exports a Go-callable thunk per import. The
thunk has its own linear memory the harness can pre-populate
before invoking, satisfying the forwarding-contract tests too.

This is more infrastructure but covers all four contracts with
the same shape. Estimating ~150 LoC for a focused encoder
supporting i32 params/results + memory + simple thunks. Wasm
binary format is straightforward enough.
