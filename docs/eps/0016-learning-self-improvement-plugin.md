---
ep: 16
title: Learning and Self-Improvement Plugin
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Placeholder
type: Standards
created: 2026-04-24
see-also: [2, 6, 9, 11, 15]
history:
  - date: 2026-04-24
    status: Placeholder
    note: Captures the request for plugin-backed learning and self-improvement.
---

# EP-16: Learning and Self-Improvement Plugin

## Problem

stado learns informally through repo docs, `.learnings/` notes, and
conversation summaries, but there is no first-class workflow for
extracting lessons from solved issues, validating them, and applying
them to future sessions. Manual notes help but are inconsistent.

## Goals

- Provide a plugin-backed learning workflow for solved issues.
- Turn debugging/implementation discoveries into reviewable lessons.
- Reuse approved lessons in later sessions without bloating every
  prompt.
- Keep the system auditable: what was learned, when, from which session,
  and whether the user accepted it.

## Non-goals

- Letting the model rewrite its own system prompt silently.
- Treating every assistant conclusion as trustworthy memory.
- Replacing project-owned documentation or EPs.

## Design

Placeholder. The design needs to define lesson candidates, review flow,
storage, retrieval, decay/supersession, and interaction with the memory
system.

The likely shape is a background plugin that observes completed turns
or explicit commands, proposes lessons, and stores only user-approved
items. Approved lessons can be retrieved similarly to memory, but should
remain distinguishable from factual memory and project instructions.

## Migration / rollout

Start as an explicit command/plugin action that proposes lessons after a
successful debugging session. Add automatic suggestions only after the
review path is reliable.

## Failure modes

- Bad lessons reinforce incorrect behavior.
- Lessons become stale after code changes.
- Private or sensitive content is stored as a lesson.
- The plugin over-injects process advice and drowns out the user's
  current request.

## Test strategy

- Unit tests for lesson candidate validation and storage.
- Plugin-host tests for explicit approval before durable writes.
- Prompt assembly tests proving lessons are labeled separately.

## Open questions

- Is learning a specialization of EP-15 memory or a separate store?
- What signals indicate a session produced a useful lesson?
- How are lessons invalidated when files or design docs change?
- Should lessons be repo-local, global, or both?

## Decision log

### D1. Separate from raw memory at the proposal level

- **Decided:** capture learning/self-improvement as its own EP even if
  implementation later shares the memory substrate.
- **Alternatives:** fold it entirely into EP-15.
- **Why:** lessons have different review, provenance, and staleness
  requirements than ordinary remembered facts.

## Related

- EP-15 Memory System Plugin
- EP-9 Session Guardrails and Hooks
