# State

**Mode:** Feature (adding test coverage to an existing system)

**Current item:** Phase 1.1 — Plugins/runtime bridges contract tests
**Plan:** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## In flight

Phase 1.1: **complete.** Bridge contract tests in
`internal/plugins/runtime/` for all 5 bridges (Session/Memory/
Approval/Choice/Fleet) covering all 4 contracts. 47 tests total.
Stable under `-count=10 -race`.

Phase 1.2: **complete.** Runner contract test in
`internal/sandbox/runner_contract_test.go`. Tier 1 (every
available runner): command shape, exec allow-list deny/pass.
Tier 2 (BwrapRunner only on this Linux host; sandbox-exec
analogous on macOS): negative control, FS-write denied, FS-write
allowed. 13 sub-test executions. Stable under `-count=10 -race`.
Multi-layer composition parked as
`.agent/specs/open/sandbox-multilayer-composition.md`.

Phase 1.3: **complete.** FleetBridgeAdapter contract tests in
`internal/runtime/fleet_bridge_test.go`. 17 tests covering
AgentSpawn (sync/async/error/cancel), AgentList, AgentReadMessages
(unknown/completed/running/timeout/cancel), AgentSendMessage, and
AgentCancel. Plus a concurrency test (20 parallel spawns).
Stable under `-count=10 -race`.

Phase 1 done — ready for **merge checkpoint #1** (tests-only
diff).

Phase 2.1.aa: **complete.** mcpbridge audit. Verified
`internal/mcpbridge` has zero filesystem operations — pure
JSON-over-RPC bridge to external MCP server process. No
`workdirpath` usage, no `filepath.*`, no `os.Open/ReadFile/...`.
No API impact for A2.

Plan also rewritten this turn for the round-2 design pivot:
4 types (`Resolver` / `UserConfigResolver` / `StrictResolver` /
`RootResolver`) instead of D12's original 2.

Up next: Phase 2.1.a — `Resolver` (workdir) + `RootResolver`
(independent constructor) + security tests.

## Queued (in order, per plan)

1. **Phase 1.1** (in flight) — bridge contract tests
2. **Phase 1.2** — sandbox runner contract test (Tier 1 + Tier 2)
3. **Phase 1.3** — fleet_bridge.go lifecycle tests
4. End of Phase 1 → merge checkpoint #1
5. **Phase 2.1 (A2)** — workdirpath Resolver/RootResolver
6. End of Phase 2.1 → merge checkpoint #2 (cherry-pick to main allowed)
7. **Phase 2.2 (A1)** — Model + render consolidation
8. End of Phase 2.2 → merge checkpoint #3
9. **Phase 2.3 (A3)** — handler_*.go split in package tui
10. End of Phase 2.3 → merge checkpoint #4
11. **Phase 3.1, 3.2, 3.3** — Tier B sweep
12. End of Phase 3 → merge checkpoint #5

## Blocked

Nothing.

## Recent

- 2026-05-07: plan committed (3 commits), three rounds of consult+fix
  with codex/gemini. Last commit `ef55cb9`.
- Now starting Phase 1.1 execution.
