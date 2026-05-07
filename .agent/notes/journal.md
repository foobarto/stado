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
