---
ep: 9
title: Session Guardrails and Hooks
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [5, 7, 8, 11]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped approvals, budget, and lifecycle-hook surfaces documented during the v0.0.1 cycle.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Session-scoped approvals, budget caps, and post-turn hooks are current operator guardrails.
---

# EP-9: Session Guardrails and Hooks

## Problem

A coding-agent session needs more than model capability. Users need ways
to slow the system down around risky actions, cap spend, and wire simple
notifications or telemetry at turn boundaries. Without those controls,
the runtime is technically capable but operationally hard to trust.

These controls also need to stay deliberately small. Once hooks or
approval overrides become a second policy engine, the system grows
harder to reason about than the problem it solves.

## Goals

- Keep human approval in the loop for risky tool calls.
- Provide a cumulative cost guardrail for long-running sessions.
- Support simple lifecycle notifications and logging hooks.
- Keep every control explicit, scoped, and easy to reset.

## Non-goals

- Replacing the kernel sandbox with approval UX.
- Building a general-purpose policy engine into hooks.
- Making hooks able to rewrite or block completed turns.

## Design

Approvals are the user interaction surface for tool execution, not the
sandbox policy. The TUI can run in prompt-everything mode or allowlist
mode, and it exposes session-scoped `/approvals always <tool>` and
`/approvals forget` commands so a user can loosen or reset behavior
without touching the underlying sandbox rules.

Budget caps are cumulative per session. `[budget].warn_usd` adds a
warning pill and one-time advisory once the session crosses the cap.
`[budget].hard_usd` blocks fresh turns until the user acknowledges or
raises the cap. In non-interactive runtimes, the same hard cap maps to a
clear runtime error rather than a silent stop.

Hooks are intentionally narrow. The current shipped hook surface is
`[hooks].post_turn`, which runs one notification-oriented shell command
with a stable JSON payload after a turn completes. The hook inherits
environment, writes through stado's stderr, is capped by a short
timeout, and cannot block or rewrite the already-finished turn. Hook
failures are logged and never treated as turn failures.

All three controls are scoped so they can be reasoned about locally:
approvals are session-scoped when remembered, budget caps are
cumulative per session even when configured globally, and remembered
overrides stay easy to forget or reset.

## Decision log

### D1. Keep approval separate from sandbox policy

- **Decided:** approvals are a user interaction layer on top of the
  sandbox, not a replacement for it.
- **Alternatives:** rely only on prompts or rely only on sandbox rules.
- **Why:** sandboxing limits what a tool can do, while approvals let the
  user gate whether a permitted action should run right now.

### D2. Budget on cumulative session cost

- **Decided:** cost caps are tracked over the whole session, with warn
  and hard thresholds.
- **Alternatives:** per-turn caps only or passive reporting with no
  guardrail.
- **Why:** the real operational risk is the total session spend from a
  long, wandering agent loop, not just one expensive turn.

### D3. Make hooks notification-only

- **Decided:** hooks receive a post-turn payload and cannot alter the
  just-completed turn.
- **Alternatives:** synchronous policy hooks that can veto or transform
  output.
- **Why:** a narrow notification surface is easy to reason about and far
  less likely to become a hidden execution path.

### D4. Keep overrides easy to forget

- **Decided:** remembered approvals and budget acknowledgements are
  session-scoped and resettable.
- **Alternatives:** persist overrides globally by default.
- **Why:** operator controls should lower friction for the current task
  without silently weakening future sessions.

## Related

- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
- [EP-8: Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md)
- [docs/features/budget.md](../features/budget.md)
- [docs/features/hooks.md](../features/hooks.md)
- [docs/commands/tui.md](../commands/tui.md#approvals)
