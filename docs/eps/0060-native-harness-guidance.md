---
ep: 60
title: Bounded Lifecycle Guidance
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
requires: ["EP-0050", "EP-0052", "EP-0054", "EP-0055", "EP-0056", "EP-0057", "EP-0059", "EP-0064"]
extended-by: ["EP-0062", "EP-0066"]
see-also: ["EP-0009", "EP-0030", "EP-0051", "EP-0058"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Replaced native cross-surface guidance with a TUI-only signed-lifecycle-application contract; native code now exposes bounded facts and one append-only contribution primitive, while the application owns classifiers, thresholds, wording, and ordering.
  - date: 2026-08-14
    status: Partial
    note: EP-0066 identified native guidance policy as placement drift and required migration to WASM.
  - date: 2026-08-12
    status: Accepted
    note: Accepted for implementation after the repository-wide documentation audit.
  - date: 2026-08-12
    status: Implemented
    version: v0.79.0
    note: Shipped the superseded native implementation in PR #253.
---

> **Relationships:** **Requires:** [EP-0050](./0050-broker.md), [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0054](./0054-addressable-context-and-research-agents.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0064](./0064-wasm-lifecycle-applications.md) · **Extended by:** [EP-0062](./0062-harness-enforced-supervised-work.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md) · **See also:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0030](./0030-security-research-default-harness.md), [EP-0051](./0051-lua-lifecycle-hook-contract.md), [EP-0058](./0058-measured-adaptive-retrieval.md)

# EP-0060: Bounded Lifecycle Guidance

## Problem

Stado's application architecture includes approved artifact retrieval,
isolated research, trajectory learning, retained children, and durable
mailboxes. The first three are available only through their explicit signed
packages; core stado has no native imitation. Tool descriptions make an
installed mechanism discoverable, but availability alone does not teach an
agent when another search, learning review, or coordination check is warranted.

The original implementation answered that problem with native Go policy and
injected its prose across every agent surface. That placement was wrong. Signal
types, broker sequence, child lifecycle, unread counts, and the actual tool
ceiling are facts that only the host can establish. Deciding which combination
deserves a nudge, the threshold, ordering, and wording is
application policy. EP-2 and EP-66 put that policy in signed WASM.

## Goals

- Expose small, deterministic, broker-authenticated quality facts without raw
  payloads, prose, paths, or guest-selectable scope.
- Let an explicitly enabled signed lifecycle application turn those facts into
  bounded advisory context at `pre_llm`.
- Keep classifiers, thresholds, wording, item ordering, and within-contribution
  deduplication in plugin source.
- Make the contribution strictly weaker than lifecycle decision authority: it
  cannot deny, replace the system prompt or model, or mutate history.
- Advertise research and coordination only when the relevant tool is present
  under the live session registry ceiling.
- Fail open. A missing, trapping, stale, or malformed guidance application must
  contribute nothing and must not block unrelated work.

## Non-goals

- Making guidance a security feature or a quality gate. It is untrusted advice
  below operator and repository instructions.
- Automatically activating memory, approving a lesson, widening tools, or
  treating model/plugin output as operator intent.
- Giving WASM raw WAL access, a caller-selected session, signal attributes,
  evidence text, mailbox payloads, learning focus/errors, or artifact bodies.
- Claiming parity on CLI `run`, headless, ACP, MCP, retained children, or
  recursive agent loops. The accepted v1 composition surface is the TUI only.
- Making the official package mandatory or automatically enabled.

## Design

### Native bounded facts

The broker import `stado_session_context_read` requires
`session:context:read`. It takes an empty request. Native code resolves the
logical session from the opaque EP-50 application binding and returns one
bounded snapshot:

- active deterministic signal type, detector version, confidence, timestamps,
  and host-recorded detection sequence;
- child lifecycle status/generation and unread message count;
- truncation flags, broker sequence, schema, and canonical snapshot digest.

The request cannot name a session, generation, repository, path, principal, or
plugin. The response omits raw attributes, origins, evidence, prompts, errors,
mailbox bodies, artifact data, and application prose. IDs are opaque facts and
need not enter guidance text.

The TUI's `pre_llm` payload also supplies bounded volatile quality facts: the
current input plus digest/truncation and fast-context presence plus digest.
Both are explicitly untrusted content. Neither establishes operator origin,
instruction priority, permission, or semantic truth.

`stado_registry_catalog` projects the exact live registry through the current
TUI `ToolSurfaceController`. The application may therefore mention a research
or coordination tool only if it is actually present beneath the current
registry/config/session ceiling. Catalog access does not activate tools.

### Append-only contribution

A manifest must hold both:

```text
lifecycle:observe:pre_llm
lifecycle:contribute:pre_llm
```

It may then return exactly:

```json
{
  "decision": "contribute",
  "contribution": {"system_append": "bounded advisory text"}
}
```

The host accepts only a non-empty `system_append` of at most 16 KiB and appends
it to the current system text. This is a distinct capability and wire decision,
not a restricted spelling of `mutate`. It cannot carry a reason, model,
replacement system, history edit, or deny. The ordinary lifecycle payload
ceiling remains independently enforced.

Lua hooks run first in configured order. Enabled lifecycle applications follow
in canonical signed-identity order, so a contribution observes the result of
earlier valid mutations and later participants observe the appended system.
Native fact-only observers remain last.

### Application-owned policy

The official guidance application owns the initial policy:

1. active mechanical signal shapes may recommend preserving reusable
   corrections and asking the operator to run `/learn`;
2. historical or recurring-memory-shaped input may recommend isolated research
   when the matching research tool is available and fast retrieval did not
   already answer the recurring-memory case;
3. active retained work or unread messages may recommend the available agent
   inspection and follow-up tools.

It owns the fixed templates, classifier vocabulary, maximum three items,
1,200-byte output limit, sorting, and within-contribution deduplication. These
values are package policy, not ABI constants. Another signed application may
implement a different policy against the same facts. The generic context
snapshot deliberately excludes the memory application's learn/review journal;
guidance therefore cannot claim that a signal was reviewed or suppress advice
from application-private completion state.

### Failure and authority

The official manifest declares `failure: open`. Callback traps, timeouts,
malformed JSON, attempted denial without decision authority, unavailable facts,
and decode failures are logged by the host and become `continue` with the
current system unchanged. This posture is resolved inside the WASM application
adapter before the shared Lua runner sees an error; a global fail-closed Lua
policy setting cannot silently turn advisory guidance into a provider barrier.

Installed-package verification, source-scoped signer pins, manifest capability
checks, broker admission, opaque binding, and the live tool ceiling remain the
security boundary. The application chooses advice only. Even correct guidance
does not prove a lesson, approve an artifact, grant a tool, or authorize a
recipient.

### Supported surface

The TUI is the only v1 surface that admits persistent lifecycle applications,
owns the exact session controller, and supplies live per-turn volatile facts
and registry ceiling. Native guidance composition is deleted from CLI `run`,
headless, ACP, subagents, and the generic `AgentLoop`. Those surfaces receive no
implicit substitute. Future parity requires a real lifecycle-application
composition and controller contract on each surface, not another native prompt
callback.

The official source is maintained in `foobarto/stado-plugins`. Source staged
during development is intentionally unsigned; release requires the ordinary
offline signing, installation, pinning, and explicit background-plugin opt-in.

## Safety invariants

- Only the broker-selected current logical session is projected.
- Guest requests contain no authority selector; response prose/payloads are
  excluded and all lists and output are bounded.
- Contribution authority never implies decision, scheduling, artifact,
  provider, fleet, or tool-surface authority.
- Guidance never activates, edits, rejects, or deletes an artifact and never
  claims that a candidate was approved.
- Application/configuration failure cannot block a provider turn outside the
  separately supported lifecycle-admission scope; runtime failure-open always
  contributes nothing.
- Native code contains facts, validation, and composition mechanics only—not
  guidance templates, classifiers, thresholds, or recommendation policy.

## Acceptance criteria

- The context import rejects missing native logical scope and leaks none of the
  injected signal attributes, evidence references, tool args, or mailbox body.
- Session-context-only children use explicit `active`; retained children expose
  only the closed retained lifecycle enum.
- Another lifecycle application's learn/review journal never enters the
  context snapshot or becomes implicit guidance authority.
- Research and coordination names appear only when catalogued under the live
  TUI session ceiling.
- Contribution cannot deny or mutate model/history/system replacement fields,
  and is bounded to 16 KiB.
- Canonical multi-application order is deterministic and contribution chains
  after an earlier decide-capable mutation.
- With the shared runner configured fail-closed, a trapping, malformed, or
  deny-attempting contribute-only failure-open application still permits the
  turn and leaves the system unchanged.
- No native guidance builder or application-specific prompt callback remains in
  run, TUI, headless, ACP, subagent, or generic agent-loop code; only the TUI's
  generic lifecycle composition invokes the contributor.
- The official unsigned development source passes unit, race, vet, and two-build
  reproducibility checks; publication and signing remain release work.

## Related

- [What Survives the Window](../articles/adaptive-context.md)
- [Lua lifecycle hooks](../features/lifecycle-hooks.md)
- [WASM lifecycle applications](./0064-wasm-lifecycle-applications.md)
