# EP-0038c: Agent Surface — Retroactive Plan

> **Status:** Retroactive — implementation already landed on `main` between
> commits `21143d4` (`feat(ep-0038c/d): agent surface (FleetBridge +
> stado_agent_* imports + agent wasm module) + sandbox wrap-mode re-exec`)
> and `57f2848` (`feat: agent.* tools (FleetBridge wired via
> tool.AgentFleetProvider)`). This document exists to keep the plan archive
> aligned with the code, per the `2026-04-23-retroactive-eps-design.md`
> convention.
>
> **Owner:** Codex
> **Status:** Implemented
> **Spec:** `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md` §D

**Goal:** Surface a 5-tool `agent.*` family to plugins (and the LLM via
the bundled `agent.wasm`) so wasm code can fork the running stado session,
drive a child agent, exchange messages with it, and cancel it. Decouple
the existing native `spawn_agent` SubagentEvent path from the wasm-callable
surface so both can coexist while we transition.

**Architecture:** A `FleetBridge` interface in the plugin runtime adapts
the existing `runtime.Fleet` (EP-0034) to a wasm-callable shape. The host
side registers `stado_agent_*` imports gated by a new `agent:fleet`
capability. The bundled `agent.wasm` plugin re-exports those imports as 5
model-facing tools. `tool.AgentFleetProvider` is the plumbing that gets
the bridge from a `tool.Host` into the plugin runtime without an import
cycle.

**Tech Stack:** Go, wazero, the existing `internal/runtime/fleet.go`
agent fleet primitives, `internal/runtime/subagent.go` for the parent
SubagentRunner adapter.

---

## File Map (as actually landed)

| File | Status | Purpose |
|---|---|---|
| `internal/plugins/runtime/host_agent.go` | Created | `stado_agent_spawn/list/read_messages/send_message/cancel` host imports |
| `internal/plugins/runtime/fleet_bridge.go` | Created | `FleetBridge` interface, request/result types |
| `internal/runtime/fleet_bridge.go` | Created | `FleetBridgeAdapter` wraps `*Fleet` |
| `pkg/tool/tool.go` | Modified | Added `AgentFleetProvider` interface (avoids cycle) |
| `internal/bundledplugins/modules/agent/main.go` | Created | wasm plugin re-exporting the 5 agent imports as `stado_tool_*` |
| `internal/runtime/bundled_plugin_tools.go` | Modified | Registers `agent.*` tools, wires FleetBridge from the tool host |
| `internal/runtime/tool_metadata.go` | Modified | Maps `agent__spawn` → `agent.spawn`, etc. |

---

## Tasks (retroactive — done)

### Task 1: `FleetBridge` interface + adapter
- ✅ `internal/plugins/runtime/fleet_bridge.go` defines `FleetBridge` interface and `AgentSpawnRequest` / `AgentSpawnResult`.
- ✅ `internal/runtime/fleet_bridge.go` implements `FleetBridgeAdapter` against `runtime.Fleet`.
- ✅ Sync vs async modes: sync polls until done, async returns immediately.

### Task 2: `stado_agent_*` host imports
- ✅ 5 imports gated by `agent:fleet` capability (`Host.AgentFleet`).
- ✅ Capability check at call time via `host.FleetBridge != nil` + `host.AgentFleet`.
- ✅ `host_agent.go:registerAgent*Imports` mirrors EP-0038a's per-resource registration pattern.

### Task 3: Bundled `agent.wasm` module
- ✅ `internal/bundledplugins/modules/agent/main.go` exposes 5 tools:
  `agent.spawn`, `agent.list`, `agent.read_messages`, `agent.send_message`,
  `agent.cancel`.
- ✅ Each maps to a `stado_agent_*` host import 1:1.
- ✅ Built into `internal/bundledplugins/wasm/agent.wasm`.

### Task 4: Wiring through `tool.AgentFleetProvider`
- ✅ `pkg/tool/tool.go` defines the interface (returns `any` to stay
  cycle-free).
- ✅ `internal/runtime/bundled_plugin_tools.go:Run` casts the host to
  `AgentFleetProvider`, gets the bridge, sets `host.FleetBridge`.

### Task 5: `agent.*` tool registrations
- ✅ `bundled_plugin_tools.go:184-261` registers all 5 with `agent:fleet`
  cap. Schemas declared inline.

---

## Open follow-ups (still needed)

These were promised in NOTES but didn't land with the agent surface and
haven't been picked up yet:

- **Collapse `spawn_agent` (native) and `agent.spawn` (wasm) into one canonical
  surface.** Currently both are registered; only `spawn_agent` is in the
  default autoload set, and only `spawn_agent` fires `SubagentEvent`. The
  wasm path doesn't emit lifecycle notifications. Either:
  (a) route `agent.spawn` execution through the same SubagentRunner path
      that `spawn_agent` uses, and drop the duplicate native registration;
  (b) drop the wasm `agent.spawn` and keep only the native; or
  (c) document that they're intentionally distinct (audit-emitting vs
      ephemeral).

- **`/agents` slash command** — locked in NOTES. There's an `agent` field
  in the model status but no dedicated `/agents` view of the spawn tree.
  `/sessions` partly covers this when sessions are agent-driven.

## Verification

```bash
# 1. agent.* tools are registered.
stado tool list | grep '^agent\.'

# 2. host imports gate on agent:fleet cap.
grep -A4 "registerAgentSpawnImport\|host.AgentFleet" internal/plugins/runtime/host_agent.go

# 3. wasm bundle compiles and dispatches the 5 tools.
bash internal/bundledplugins/build.sh
go test ./internal/runtime/... -run TestAgent
```
