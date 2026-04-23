---
ep: 11
title: Observability and Telemetry
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [3, 4, 7, 9, 10, 12]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped OpenTelemetry instrumentation and cross-process trace-link design.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: OTel spans, metrics, disabled-safe runtime startup, and fork/resume trace continuity are shipped.
---

# EP-11: Observability and Telemetry

## Problem

stado has multiple long-lived surfaces, multiple tool execution paths,
and session forks that can jump into new processes and worktrees. If
observability is optional glue added around only one surface, operators
cannot answer basic runtime questions such as where latency comes from,
which provider turn caused a failure, or whether a resumed child session
belongs to the same trace as its parent.

The runtime also needs a safe disabled path. Telemetry cannot require a
second execution model, because a feature flag that changes runtime
control flow is harder to trust than one that degrades cleanly to no-op.

## Goals

- Make observability part of the shared runtime contract, not a TUI-only
  or provider-specific add-on.
- Keep span boundaries stable across provider streaming, turns, tools,
  sandboxed execution, and session lifecycle events.
- Expose a baseline metric set that operators and future surfaces can
  rely on.
- Preserve trace continuity when a session is forked and resumed in a
  new process.

## Non-goals

- Tracing every internal helper or turning OpenTelemetry into a general
  logging framework.
- Making telemetry mandatory for local or airgapped use.
- Building a second runtime that exists only for telemetry-disabled
  deployments.

## Design

- telemetry is built into provider, runtime, tool, and session surfaces
- disabled telemetry is a no-op, not a special forked runtime
- spans cover provider stream, turn, tool call, and session lifecycle boundaries
- metrics are part of the runtime contract
- `.stado-span-context` preserves trace continuity across forks and resumes

The implementation follows the shared `internal/telemetry` runtime
rather than surface-specific wrappers. `telemetry.Start` initialises the
OpenTelemetry tracer and meter providers when configured and returns a
runtime whose tracer and meter are still safe to call when telemetry is
disabled. That means `internal/runtime`, `internal/tools.Executor`, and
the provider implementations can instrument the hot path unconditionally
without branching into a separate disabled-only code path.

Span names are a compatibility surface, not incidental strings.
`stado.provider.stream` wraps each provider turn stream, `stado.turn`
wraps each agent loop turn, `stado.tool_call` wraps every tool
invocation, and session lifecycle spans cover fork and resume
boundaries. `stado.sandbox.exec` remains the execution-level child span
for sandboxed process launches. These names are kept stable so dashboards
and traces survive refactors.

Metrics are defined once in `internal/telemetry/metrics.go` and treated
as the baseline runtime instrument surface. That surface currently
declares tool latency, token totals, prompt-cache hit ratio, approval
decisions, sandbox denials, and active sessions, but it should not be
read as claiming that every one of those instruments is emitted with the
same breadth today. The clearly-wired runtime recording path today is
tool-call latency in the executor; the remaining declared instruments
establish the telemetry contract and naming surface that other runtime
paths and future coverage are expected to use rather than inventing
parallel metrics.

Cross-process trace continuity is explicit. When `stado session fork`
creates a child worktree, it writes the parent fork span's W3C
`traceparent` to `.stado-span-context`. When a fresh stado process later
starts from that worktree, `internal/runtime.RootContext` and the resume
path load that file and attach the recovered parent span context before
opening new spans. If the file is missing or malformed, runtime behavior
continues normally and only observability degrades.

The operational rule is graceful degradation. Disabled telemetry, absent
collectors, and best-effort traceparent persistence do not block session
creation, tool execution, or resume behavior. Observability is designed
to be always-on when configured and harmless when not.

## Open questions

No unresolved architecture question blocks the shipped contract in
v0.1.0. Future work can widen coverage, but it should extend the shared
runtime contract instead of adding per-surface instrumentation models.

## Decision log

### D1. Put telemetry in the shared runtime

- **Decided:** instrumentation lives in `internal/telemetry`,
  `internal/runtime`, the provider implementations, and the tool
  executor.
- **Alternatives:** keep telemetry in the TUI only or let each surface
  wire its own instrumentation.
- **Why:** telemetry has to describe one runtime, not several loosely
  similar entry points.

### D2. Make the disabled path no-op rather than separate

- **Decided:** `telemetry.Start` returns safe no-op tracer and meter
  handles when disabled.
- **Alternatives:** conditional instrumentation forks throughout the
  runtime or a telemetry-specific build/runtime mode.
- **Why:** the call sites stay simple, and enabling telemetry does not
  change control flow semantics.

### D3. Treat span names and metric names as contracts

- **Decided:** the shipped span hierarchy and baseline metric set are
  stable runtime surfaces.
- **Alternatives:** ad hoc names per package or silent renames during
  refactors.
- **Why:** observability only helps operators if dashboards and traces
  remain comparable across builds.

### D4. Persist trace context across forks and resumes

- **Decided:** fork writes a W3C traceparent to
  `.stado-span-context`, and child/resumed processes load it.
- **Alternatives:** accept disconnected traces across processes or rely
  on external supervisors to stitch them together.
- **Why:** session fork is a first-class runtime boundary, so trace
  continuity belongs in stado's own contract.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md)
- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [README.md](../../README.md#stado)
- [PLAN.md](../../PLAN.md#phase-6--otel--)
