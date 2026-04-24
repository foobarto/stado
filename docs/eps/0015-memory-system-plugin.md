---
ep: 15
title: Memory System Plugin
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Placeholder
type: Standards
created: 2026-04-24
see-also: [2, 6, 7, 8, 9, 11]
history:
  - date: 2026-04-24
    status: Placeholder
    note: Captures the request for plugin-backed persistent memory.
---

# EP-15: Memory System Plugin

## Problem

stado has repo instructions, skills, conversation persistence, and
compaction, but no general memory layer for long-lived facts that should
survive across sessions and be retrieved only when relevant. Without
that layer, important user/project preferences either bloat prompts or
are lost between sessions.

## Goals

- Provide a plugin-backed persistent memory system.
- Separate durable memory from transient conversation history.
- Retrieve and inject only relevant memories.
- Make memory creation, review, edit, and deletion user-visible.

## Non-goals

- Silent surveillance of every user message.
- Unbounded prompt injection from stored memories.
- A single mandatory storage backend for all users.

## Design

Placeholder. The design needs to define memory item shape, storage,
retrieval API, user controls, plugin permissions, and prompt-injection
defenses.

The likely shape is a background plugin with a constrained host API for
recording candidate memories and retrieving relevant memory snippets
for a turn. The core runtime should treat retrieved memories as a
distinct prompt section, not as project instructions.

## Migration / rollout

Start disabled or opt-in. Ship with a local file/SQLite-like backend
before considering remote or vector backends.

## Failure modes

- Irrelevant memories pollute the prompt and degrade answer quality.
- Stale memories override current repo instructions.
- Sensitive data is stored without user intent.
- Malicious memory content becomes prompt injection.

## Test strategy

- Unit tests for memory item validation and retrieval filtering.
- Plugin-host tests for permission boundaries.
- Prompt assembly tests showing memories are separated from identity and
  project instructions.

## Open questions

- What is the memory schema?
- Who approves durable memory writes: model, plugin, user, or all three?
- Should retrieval be keyword, embedding, hybrid, or delegated to a
  plugin-defined strategy?
- How are memories scoped: global, repo, session tree, or user profile?

## Decision log

### D1. Plugin-first capture

- **Decided:** capture memory as a plugin-backed feature, not core-only.
- **Alternatives:** bake a memory database directly into runtime.
- **Why:** storage and retrieval strategy are likely to evolve, while
  the core should enforce permissions and prompt boundaries.

## Related

- EP-2 All Tools as WASM Plugins
- EP-6 Signed WASM Plugin Runtime
- EP-8 Repo-Local Instructions and Skills
