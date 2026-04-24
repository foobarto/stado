---
ep: 18
title: Configurable System Prompt Template
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
implemented-in: v0.1.0
see-also: [3, 8, 10]
history:
  - date: 2026-04-24
    status: Implemented
    version: v0.1.0
    note: Records the shipped editable system prompt template created under the stado config directory on first run.
---

# EP-18: Configurable System Prompt Template

## Problem

stado needs a strong default system prompt that anchors identity and
problem-solving behavior, but users also need to edit that prompt
without recompiling the binary or hiding project instructions in an
unreviewable place.

The runtime must also avoid provider/model identity drift. Local and
OpenAI-compatible models can inherit misleading training-time system
text, so the stado identity and active provider/model metadata need to
be part of the rendered prompt in a stable way.

## Goals

- Ship a strong default problem-solving prompt for stado.
- Create an editable system prompt template on first config load.
- Keep repo-local `AGENTS.md` / `CLAUDE.md` content as an explicit
  template field, not an implicit replacement for stado identity.
- Validate the template at config-load time so broken templates fail
  early.

## Non-goals

- A general prompt package manager.
- Include directives or arbitrary file expansion in the template.
- Letting project instructions override stado's runtime identity unless
  the user explicitly edits the template to do so.

## Design

Config loading creates `$XDG_CONFIG_HOME/stado/system-prompt.md` when
`[agent].system_prompt_path` is unset and the file does not already
exist. The file is written with mode `0600` and contains the default
stado system prompt template.

The template is a Go `text/template` with three fields:

- `{{ .Provider }}` — the active provider name when known
- `{{ .Model }}` — the active model id when known
- `{{ .ProjectInstructions }}` — the nearest loaded `AGENTS.md` or
  `CLAUDE.md` body, if any

Every runtime surface that builds provider requests uses the same
rendering path: TUI, `stado run`, ACP, and headless. If
`[agent].system_prompt_path` points to a custom file, stado expands
`~`, loads it, validates it, and renders it instead of the default
template.

## Migration / rollout

Existing users get the default template created automatically on the
next config load. Users who want the previous behavior can edit that
file or set `[agent].system_prompt_path` to a custom template.

## Failure modes

- A custom template has invalid Go template syntax; config load fails
  before a provider request is made.
- A template omits `ProjectInstructions`; repo instructions load but do
  not reach the model.
- A user edits identity text incorrectly and local models claim to be
  another client; this is visible in the editable template.

## Test strategy

- Config tests cover first-run template creation, custom path loading,
  validation errors, and permissions.
- Instruction tests cover template rendering with provider/model
  metadata and project instructions.
- Surface tests cover TUI/runtime wiring so all provider requests use
  the same composed prompt.

## Decision log

### D1. Use an editable template file in the config directory

- **Decided:** first config load creates `system-prompt.md` under the
  stado config directory.
- **Alternatives:** hardcode the prompt or store it in repo-local files.
- **Why:** users can customize the system prompt globally without
  mixing client identity with project-owned instructions.

### D2. Keep project instructions as a template field

- **Decided:** `AGENTS.md` / `CLAUDE.md` content is injected through
  `{{ .ProjectInstructions }}`.
- **Alternatives:** append instructions outside the template or let them
  replace the whole system prompt.
- **Why:** the prompt structure stays explicit, and stado identity
  remains visible by default.

## Related

- [EP-3: Provider-Native Agent Interface](./0003-provider-native-agent-interface.md)
- [EP-8: Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [docs/features/instructions.md](../features/instructions.md)
- [docs/commands/config.md](../commands/config.md)
