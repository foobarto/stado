---
ep: 9
title: Session Guardrails and Hooks
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
extended-by: [17]
see-also: [5, 7, 8, 11, 17]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped approvals, budget, and lifecycle-hook surfaces documented during the v0.0.1 cycle.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Budget caps and post-turn hooks are current operator guardrails; native approval handling was later extended by EP-17.
---

# EP-9: Session Guardrails and Hooks

## Problem

A coding-agent session needs more than model capability. Users need ways
to slow the system down around risky actions, cap spend, and wire simple
notifications or telemetry at turn boundaries. Without those controls,
the runtime is technically capable but operationally hard to trust.

These controls also need to stay deliberately small. Once hooks,
approval prompts, or budget acknowledgements become a second policy
engine, the system grows harder to reason about than the problem it
solves.

## Goals

- Keep human approval separate from sandbox policy and scoped to the
  surfaces that explicitly support it.
- Provide a cumulative cost guardrail for long-running sessions.
- Support simple lifecycle notifications and logging hooks.
- Keep every control explicit, scoped, and easy to reset.

## Non-goals

- Replacing the kernel sandbox with approval UX.
- Building a general-purpose policy engine into hooks.
- Making hooks able to rewrite or block completed turns.

## Design

Approvals are a user interaction surface, not the sandbox policy. The
original native bundled-tool approval loop has since been replaced by
EP-17: native tools are controlled through tool visibility
(`[tools]`, Plan/Do mode, and plugin overrides), while explicit human
approval is available to plugins that declare `ui:approval`.

Budget caps are cumulative per session. `[budget].warn_usd` adds a
warning pill and one-time advisory once the session crosses the cap.
`[budget].hard_usd` blocks fresh turns on surfaces that implement the
interactive gate until the user acknowledges or raises the cap. In
`stado run`, the same hard cap maps to a clear runtime error rather than
a silent stop; other non-interactive surfaces may expose the cap through
their own runtime behavior.

Hooks are intentionally narrow. The current shipped hook surface is
`[hooks].post_turn`, which runs one notification-oriented shell
command with a stable JSON payload after a turn completes in the TUI,
`stado run`, and headless `session.prompt`. The hook
inherits environment, writes through stado's stderr, is capped by a
short timeout, and cannot block or rewrite the already-finished turn.
Hook failures are logged and never treated as turn failures.

These controls are scoped so they can be reasoned about locally:
plugin approval requests are explicit capability-gated interactions,
budget caps are cumulative per session even when configured globally,
and budget acknowledgements stay easy to reset.

## Decision log

### D1. Keep approval separate from sandbox policy

- **Decided:** approvals are a user interaction layer on top of the
  sandbox, not a replacement for it. EP-17 refines this by making native
  tool control a visibility policy and plugin approval a declared
  capability.
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

- **Decided:** budget acknowledgements are session-scoped and
  resettable. The older remembered native approval override has been
  replaced by EP-17's plugin-scoped approval capability.
- **Alternatives:** persist overrides globally by default.
- **Why:** operator controls should lower friction for the current task
  without silently weakening future sessions.

### D5. Extended by EP-17

- **Decided:** EP-17 records the later shipped change that removed the
  native bundled-tool approval loop and made human approval an explicit
  plugin capability instead.
- **Why:** EP-9 remains the budget/hooks history, while the changed
  approval contract needs its own current design record.

## Related

- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
- [EP-8: Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [EP-17: Tool Surface Policy and Plugin Approval UI](./0017-tool-surface-policy-and-plugin-approval-ui.md)
- [docs/features/budget.md](../features/budget.md)
- [docs/features/hooks.md](../features/hooks.md)
- [docs/commands/tui.md](../commands/tui.md#approvals)
