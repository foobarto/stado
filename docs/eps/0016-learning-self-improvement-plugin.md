---
ep: 16
title: Learning and Self-Improvement Plugin
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Accepted
type: Standards
created: 2026-04-24
see-also: [2, 6, 9, 11, 15]
history:
  - date: 2026-04-24
    status: Placeholder
    note: Captures the request for plugin-backed learning and self-improvement.
  - date: 2026-04-24
    status: Accepted
    note: >-
      Defined lesson candidates, approval flow, storage relationship
      to EP-15 memory, retrieval, invalidation, and prompt boundaries.
  - date: 2026-04-25
    status: Accepted
    note: >-
      First implementation slice added typed lesson items in the
      append-only memory store, explicit `stado learning propose/list/show`,
      and a separate approved-lesson prompt section.
  - date: 2026-04-25
    status: Accepted
    note: >-
      Added first-class `stado learning edit|approve|reject|delete|supersede`
      review commands so lesson-specific fields can be revised before
      approval and approved lessons can be replaced explicitly.
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

Learning is a specialization of EP-15 memory with stricter provenance
and review rules. A lesson is not a general fact about the user or repo;
it is operational guidance derived from a completed debugging,
implementation, review, or release cycle. The goal is to preserve
repeatable process improvements without letting the model silently teach
itself bad habits.

Core stado provides the same plugin permission boundary and prompt
placement guarantees as EP-15. The learning plugin proposes lessons and
retrieves approved lessons; durable approval remains a user action.

### Lesson item shape

Lessons may reuse the EP-15 memory storage substrate, but they must be
typed distinctly:

```json
{
  "id": "lesson_01J...",
  "memory_kind": "lesson",
  "scope": "global|repo|session",
  "summary": "short action-oriented rule",
  "lesson": "what should be done differently next time",
  "trigger": "situation where this lesson applies",
  "rationale": "why this lesson is valid",
  "evidence": {
    "session_id": "source session",
    "turns": [7, 8, 9],
    "commits": ["optional sha"],
    "tests": ["commands or gates that validated it"],
    "files": ["paths that were relevant"]
  },
  "status": "candidate|approved|rejected|superseded|expired",
  "confidence": "low|medium|high",
  "created_at": "RFC3339",
  "updated_at": "RFC3339",
  "expires_at": "optional RFC3339",
  "supersedes": ["optional lesson ids"],
  "tags": ["tooling", "release", "tui", "security"]
}
```

The `trigger` field is mandatory. A lesson without a clear applicability
condition is too likely to become vague process noise.

### Candidate generation

The first shipped path is explicit:

- user runs a learning command after a solved issue, or
- user accepts a TUI/headless prompt to review lessons after a commit,
  release, or repeated failure/retry cycle

Automatic background suggestions may observe `turn_complete` and
session/commit metadata, but they may only create `candidate` items.

Good candidate signals:

- the same failure happened more than once before being fixed
- a test or trace revealed a non-obvious invariant
- a release/CI run produced an actionable process warning
- the user corrected a recurring assistant behavior
- a security or auditability issue required a new guardrail

Poor candidate signals:

- one-off facts better suited to repo docs or EPs
- unverified assistant speculation
- private data, credentials, or user secrets
- broad advice with no trigger condition

### Review flow

Candidate review is explicit and edit-first:

1. Plugin proposes one or more candidates with evidence links.
2. User can edit `summary`, `lesson`, `trigger`, `scope`, tags, and
   expiry before approval.
3. User approves, rejects, or marks "document elsewhere".
4. Approved lessons become eligible for retrieval; rejected lessons
   remain as audit records but are never injected.

The review UI must show source session, relevant commits/tests, and the
exact prompt text that could be injected later.

### Retrieval and prompt placement

Lessons are retrieved through EP-15 `memory:read` with an additional
filter:

```json
{
  "memory_kind": "lesson",
  "prompt": "current task",
  "repo_id": "...",
  "session_id": "...",
  "max_items": 4,
  "budget_tokens": 500
}
```

Core injects approved lessons in a separate section after ordinary
memory:

```text
Operational lessons from prior approved sessions. Treat these as
reviewable guidance. Current user instructions, repo instructions, and
the active task override them.

- [repo/tooling lesson_...] When Go tools are missing from PATH, use
  the pinned toolchain path before declaring a linter unavailable.
```

Lessons are never merged into the default system prompt or project
instructions. They are advice with provenance, not identity rules.

### Invalidation and supersession

Lessons must become stale deliberately:

- expiry can be set on approval
- file-linked lessons become candidates for review when referenced
  files are deleted or heavily rewritten
- EP-linked lessons become candidates for review when the EP is
  superseded
- a newer approved lesson can supersede older lessons
- users can manually expire or delete lessons from retrieval

Expired/superseded lessons stay in the audit store but are not returned
by retrieval.

### Relationship to `.learnings/`

The existing `.learnings/` directory remains useful as repo-owned
human-readable notes. The learning plugin can propose candidates from
those files, and it can offer to write a lesson back to `.learnings/`
when the user chooses "document elsewhere". It must not rewrite
`.learnings/` silently.

### Permissions

The plugin uses EP-15 capabilities:

- `memory:propose` to create lesson candidates
- `memory:read` to retrieve approved lessons
- `memory:write` only through explicit user-approved actions
- `session:read` to inspect evidence

No learning plugin receives permission to edit repo instructions,
system templates, or `.learnings/` unless separately granted normal file
write capability by the user.

## Migration / rollout

Start as an explicit command/plugin action that proposes lessons after a
successful debugging session or release cycle. Add automatic suggestions
only after the review path is reliable.

The first implementation shares EP-15 local storage and retrieval with
`memory_kind:"lesson"`, stricter required `lesson`, `trigger`, and
evidence fields, and explicit `stado learning propose/list/show`
commands. The review surface now also includes `stado learning
edit|approve|reject|delete|supersede`, including lesson-specific edits
to guidance, trigger, rationale, tags, expiry, scope, and evidence.
Approved lessons are retrieved through the same opt-in memory config but
rendered in a separate "Operational lessons" prompt section. Global
automatic lesson capture is not shipped in this first release.

## Failure modes

- Bad lessons reinforce incorrect behavior.
- Lessons become stale after code changes.
- Private or sensitive content is stored as a lesson.
- The plugin over-injects process advice and drowns out the user's
  current request.

## Test strategy

- Unit tests for lesson candidate validation, required trigger/evidence
  fields, expiry, and supersession.
- Plugin-host tests proving background plugins can propose but cannot
  approve lessons without explicit user action.
- Prompt assembly tests proving lessons are labeled separately from
  identity, project instructions, ordinary memory, and compaction
  summaries.
- Retrieval tests showing max item/token budgets and trigger-based
  filtering.
- Security tests with malicious lessons attempting to rewrite system
  rules or exfiltrate secrets.

## Open questions

- How should the plugin detect "heavily rewritten" files for
  invalidation without expensive history analysis?
- Should exported lessons be portable between machines by default, or
  require an explicit signed export bundle?

## Decision log

### D1. Separate from raw memory at the proposal level

- **Decided:** capture learning/self-improvement as its own EP even if
  implementation later shares the memory substrate.
- **Alternatives:** fold it entirely into EP-15.
- **Why:** lessons have different review, provenance, and staleness
  requirements than ordinary remembered facts.

### D2. Learning is memory-kind, not a separate authority

- **Decided:** approved lessons reuse the EP-15 substrate as a distinct
  `memory_kind:"lesson"` and prompt section.
- **Alternatives:** create a separate learning database and injection
  path.
- **Why:** EP-15 already defines scope, approval, retrieval budgets, and
  prompt-injection defenses. Lessons need stricter schema and review,
  not a more privileged channel.

### D3. Trigger is mandatory

- **Decided:** every lesson must include the situation where it applies.
- **Alternatives:** store general rules such as "be more careful" or
  "write tests".
- **Why:** triggerless lessons overfit past work and pollute unrelated
  prompts. The model needs a condition for when to use the lesson.

### D4. Explicit review before approval

- **Decided:** learning plugins can propose but cannot approve durable
  lessons on their own.
- **Alternatives:** automatic self-improvement after every successful
  task or failed retry cycle.
- **Why:** bad lessons are more dangerous than missing lessons because
  they systematically bias future sessions.

### D5. Dedicated CLI review first

- **Decided:** the first review surface is explicit CLI commands under
  `stado learning`, with post-turn hooks left for later.
- **Alternatives:** prompt for lesson review automatically after every
  turn, commit, or release command.
- **Why:** the storage and prompt contracts needed a reliable manual
  review path before any background suggestion loop can be trusted.

### D6. Trigger and rank before severity

- **Decided:** lessons do not carry a separate severity or priority in
  the first implementation; trigger text, scope, recency, and retrieval
  rank are the control surface.
- **Alternatives:** add priority/severity fields to the lesson schema.
- **Why:** priority is easy to overfit and hard to calibrate. Triggered
  relevance keeps the prompt budget tied to the active task.

## Related

- EP-15 Memory System Plugin
- EP-9 Session Guardrails and Hooks
