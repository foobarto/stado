---
ep: 60
title: Native Bounded Harness Guidance
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-08-12
implemented-in: v0.79.0
requires: ["EP-0052", "EP-0053", "EP-0054", "EP-0055", "EP-0056", "EP-0057", "EP-0059"]
extended-by: ["EP-0062"]
see-also: ["EP-0009", "EP-0030", "EP-0051", "EP-0058"]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted for implementation after the repository-wide documentation audit.
  - date: 2026-08-12
    status: Implemented
    version: v0.79.0
    note: Shipped native bounded harness guidance across agent surfaces in PR #253, with full tests, signed commits, and documentation synchronized.
---

> **Relationships:** **Requires:** [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0054](./0054-addressable-context-and-research-agents.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0056](./0056-agent-mailboxes-and-supervision.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **Extended by:** [EP-0062](./0062-harness-enforced-supervised-work.md) · **See also:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0030](./0030-security-research-default-harness.md), [EP-0051](./0051-lua-lifecycle-hook-contract.md), [EP-0058](./0058-measured-adaptive-retrieval.md)

# EP-0060: Native Bounded Harness Guidance

## Problem

Stado now retrieves approved memory automatically and exposes isolated research,
trajectory learning, retained children, and durable mailboxes. Tool descriptions
make those mechanisms discoverable, but availability does not reliably teach a
model when to use them. Repeated mechanical mistakes can produce strong
host-recorded signals while the agent finishes without proposing a durable
lesson or asking the operator to review one. Historical questions may consume
the main context even though an isolated researcher is available, and retained
children may be left without deliberate coordination.

Lua lifecycle hooks can experiment with prompt nudges, but they receive coarse
payloads, cannot safely interpret broker state, and would make core harness
behavior depend on operator scripting.

## Goals

- Add native, host-derived, situational guidance to every agent surface.
- Prioritize strong mechanical learning signals, especially repeated tool
  failures, corrected arguments, verification recovery, recurring denials, and
  operator corrections.
- Encourage candidate creation through `stado learn` and explicitly ask the
  operator to inspect/approve through `/learn` when candidates may exist.
- Recommend isolated memory or session research only when the current request
  shape makes escalation useful.
- Prompt retained-agent/mailbox coordination when host state shows active work
  or unread data messages.
- Keep guidance short, deterministic, bounded, provenance-safe, and advisory.

## Non-goals

- Automatically activating memory or treating model output as operator intent.
- Letting guidance widen tools, capabilities, corpus access, or prompt priority.
- Sending untrusted signal attributes, mailbox payloads, or artifact bodies into
  the guidance section.
- Replacing tool descriptions, Lua policy hooks, or explicit operator controls.
- Claiming a temporal signal proves the semantic content of a lesson.

## Design

### Host-derived inputs

The guidance builder reads only typed broker projections and fixed host facts:

- active deterministic signal type/id/time for the current session;
- completed learn jobs and their immutable `as_of_sequence`/artifact ids;
- whether fast retrieval selected any prompt context;
- the current operator prompt, used only by a fixed bounded shape classifier;
- host-owned active-child ids/count and mailbox delivery state;
- whether the corresponding model-facing tool is actually present this turn.

Signal attributes, evidence bodies, model-authored state prose, message payloads,
and artifact content are never interpolated. IDs are included only where needed
for auditable candidate evidence and are bounded/validated host identifiers.

### Guidance policy

The builder emits at most three fixed-template nudges under a hard byte limit:

1. **Learning:** an unreviewed strong signal asks the model to preserve the
   mechanical correction as a candidate. If authorized shell execution is
   available it may run `stado learn --session-id <id>`; otherwise it asks the
   operator to run `/learn [focus]` in the TUI or the CLI equivalent. It must
   describe candidates as pending review and must never run/claim approval.
2. **Research escalation:** a historical/recurring-memory request with no fast
   match recommends `memory__research`; a request explicitly about older
   sessions/decisions recommends `session__research`. Raw corpus must remain in
   the child and the parent uses cited synthesis.
3. **Coordination:** active children or unread mailbox data recommend checking
   `agent__list`/`agent__read_messages` and using
   `agent__send_message` for bounded follow-up rather than duplicating work.

The section says it is host advisory workflow guidance below operator/repository
instructions. It cannot authorize a tool call. A completed review job whose
capture boundary includes a signal suppresses that signal's learn nudge. Stable
sorting and templates keep the prompt cache stable while state is unchanged.

### Surface wiring

The TUI builds guidance with each provider turn. `runtime.AgentLoop` accepts a
host callback and re-evaluates it at every internal tool/LLM boundary, so signals
that emerge during a multi-step run can affect the next model turn without
rewriting conversation history. CLI, headless, ACP, retained children, and other
agent-loop callers use the same builder when they have a persisted session.
Pure chat without a session receives only applicable research-shape guidance.

### Lua hooks

Lua `pre_llm` hooks run after native assembly and may further narrow or annotate
the request according to operator policy. They are not used to implement this
feature. Project config cannot define either Lua policy or native guidance
rules.

## Safety invariants

- Guidance never activates, edits, rejects, or deletes an artifact.
- Agent-shell CLI execution is not an operator-origin grant.
- Candidate/active/rejected state remains visibly distinct in wording.
- Only broker-authenticated current-session state is queried; caller-supplied
  foreign ids do not select guidance corpora.
- Failures to read guidance state fail quiet and do not block the agent turn.
- Guidance is bounded independently of memory/context budgets.

## Acceptance criteria

- A repeated identical tool failure produces one bounded learn nudge on the
  next model turn; one-off failure produces none.
- Corrected arguments followed by success recommend a lesson candidate without
  embedding raw arguments.
- A completed learn job covering that signal suppresses the nudge.
- Historical request shapes recommend the correct isolated researcher only when
  that tool is available; ordinary coding prompts do not.
- Active-child/unread-mailbox guidance contains counts/handles but no payload.
- TUI, CLI run, headless, ACP, and retained-agent loops share byte-identical
  guidance for the same host inputs.
- More than three applicable rules remain within the fixed item/byte cap.
- Untrusted signal attributes and mailbox payload injection fixtures cannot
  alter the guidance text.
- Repeated identical signal shapes aggregate during a bounded cooldown, while
  the same mistake after that window remains eligible as new evidence.

## Related

- [What Survives the Window](../articles/adaptive-context.md)
- [Lua lifecycle hooks](../features/lifecycle-hooks.md)
