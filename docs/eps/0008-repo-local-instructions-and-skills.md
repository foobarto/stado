---
ep: 8
title: Repo-Local Instructions and Skills
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
extended-by: [18]
see-also: [9, 10]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped AGENTS.md and .stado/skills conventions introduced in the v0.0.1 documentation surface.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Upward-walk instruction and skill discovery is the current repo-local prompt-input contract.
---

# EP-8: Repo-Local Instructions and Skills

## Problem

Project-specific prompt guidance and recurring workflows decay quickly
when they live in shell aliases, editor settings, or out-of-band chat
snippets. stado needs prompt inputs that travel with the repository,
version with the code, and work the same way across interactive and
non-interactive surfaces.

The feature set also has to work in monorepos, where a subdirectory may
need narrower instructions or skill variants than the repo root.

## Goals

- Keep project instructions in-repo and auto-discovered.
- Make reusable prompt fragments versioned alongside the code.
- Support monorepo-local overrides without extra configuration flags.
- Keep file formats deliberately simple.

## Non-goals

- A templating or parameter-substitution language for skills.
- Include directives or file composition for instructions.
- A config knob that points stado at arbitrary instruction files.

## Design

Project instructions are discovered by walking upward from the current
working directory to the filesystem root. Within each directory, stado
prefers `AGENTS.md` and falls back to `CLAUDE.md`. The first matching
file wins and its body is injected verbatim as the system prompt.

Skills live under `.stado/skills/*.md` and use the same upward walk.
Each markdown file becomes one reusable prompt. Minimal frontmatter can
name and describe the skill; otherwise the filename stem is the skill
name. When multiple directories define the same skill name, the nearest
one wins.

The same nearest-wins resolution rules apply across the TUI and CLI
surfaces. `/skill:<name>` and `stado run --skill <name>` both resolve
the same skill body from the same cwd walk, and the same instruction
lookup feeds interactive and non-interactive turns. The contract is one
repo-local source of truth, not one mechanism per interface.

## Decision log

### D1. Keep prompt inputs in the repo

- **Decided:** instructions and skills live next to the code instead of
  in editor- or machine-local config.
- **Alternatives:** global prompt files, config-path settings, or manual
  flags on every invocation.
- **Why:** version-controlled prompt inputs can be reviewed, discussed,
  and kept in sync with the repository they shape.

### D2. Use upward walk with nearest-wins semantics

- **Decided:** stado resolves instructions and skills by walking upward
  from cwd, with the nearest definition winning.
- **Alternatives:** root-only lookup or merging every layer found on the
  path.
- **Why:** nearest-wins fits monorepos cleanly and keeps scope obvious.

### D3. Prefer simple markdown over a richer DSL

- **Decided:** instructions are plain markdown and skills use only a
  tiny optional frontmatter block.
- **Alternatives:** a template language with variables, includes, or
  nested metadata.
- **Why:** the feature is primarily about discoverability and
  versionability, not about inventing another configuration language.

### D4. Keep the resolution contract stable across surfaces

- **Decided:** TUI and CLI surfaces resolve the same instruction and
  skill files from the same cwd rules.
- **Alternatives:** separate storage locations for interactive and
  scripted usage.
- **Why:** reusable prompt inputs are only valuable if the same file
  means the same thing everywhere stado runs.

## Related

- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [docs/features/instructions.md](../features/instructions.md)
- [docs/features/skills.md](../features/skills.md)
