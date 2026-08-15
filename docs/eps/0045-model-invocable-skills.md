---
ep: 45
title: Model-Invocable Skills (Agent Skills parity)
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Partial
type: Standards
created: 2026-06-17
requires: ["EP-0037", "EP-0038", "EP-0039"]
extends: ["EP-0008"]
see-also: ["EP-0018", "EP-0030", "EP-0044", "EP-0066"]
history:
  - date: 2026-08-14
    status: Partial
    note: >
      Corrected implementation placement and provenance. Native stado now
      exposes only operation/kind-scoped, digest-fenced context-resource facts
      plus the generic registry/session-surface primitive. The explicit
      official skills WASM plugin owns search, matching, formatting, opening,
      and activation requests. Native prompt listing, skills__load
      registration, exact-name interception, and synthetic user-role injection
      are deleted. Operator /skill, slash, and --skill remain native gestures.
  - date: 2026-08-14
    status: Partial
    note: >
      EP-44 did not ship a per-project TOFU store; project skill allowed-tools
      therefore remains unconditionally inert. A future enablement needs a
      separately accepted authority path.
  - date: 2026-06-29
    status: Partial
    note: >
      Initial Phase 1 shipped directory SKILL.md bundles, frontmatter controls,
      persona allowed-tools, a native listing/loader, and cross-surface wiring.
      The 2026-08-14 correction retains the file/operator behavior but replaces
      the native model application and its false provenance path.
  - date: 2026-06-17
    status: Draft
    note: Initial draft.
---

> **Relationships:** **Requires:** [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0039](./0039-plugin-distribution-and-trust.md) · **Extends:** [EP-0008](./0008-repo-local-instructions-and-skills.md) · **See also:** [EP-0018](./0018-configurable-system-prompt-template.md), [EP-0030](./0030-security-research-default-harness.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md)

# EP-45: Model-Invocable Skills (Agent Skills parity)

> **Implementation status (2026-08-14):** The native model application is
> removed and the generic resource/surface ABI plus reproducible official WASM
> source are complete. The package remains unsigned/unpublished. Project
> resource `allowed-tools` stays mechanically inert until a separately accepted
> authority path exists, so the EP remains Partial.

## Problem

EP-8 made skills explicit operator prompt gestures. A project can name a
reusable workflow and the operator can invoke it with `/skill:<name>`, a slash
shortcut, or `stado run --skill`. That does not provide progressive disclosure:
the model cannot discover a relevant skill and request its body when useful.

The first Phase 1 implementation closed that visible gap in the wrong layer.
Native Go appended a skill listing to every system prompt, registered
`skills__load`, recognized that exact tool name in AgentLoop and TUI, removed
the body from the tool result, and appended it as a new user message. This had
three architectural defects:

1. a stado-owned model application lived in native code despite EP-2/38;
2. every execution surface needed a skill-specific interceptor and fallback;
3. a model-selected tool result was relabeled as operator/user provenance.

The goal remains progressive disclosure. The correction changes placement and
provenance rather than retreating to operator-only skills.

## Goals

- Let a model search admitted skill summaries and open one exact body on
  demand.
- Keep search/matching/formatting/invocation policy in an official signed WASM
  plugin under EP-66.
- Keep native stado limited to facts, trust projection, immutable identity,
  bounds, and generic session-ceiling enforcement unavailable inside WASM.
- Preserve explicit `/skill`, slash, and `--skill` operator gestures.
- Preserve the flat Markdown and directory `SKILL.md` formats, including
  `${STADO_SKILL_DIR}` references.
- Make model-selected content remain an ordinary tool-origin result.
- Give run, TUI, headless, ACP, and subagents the same context-bound behavior
  without surface-specific fallbacks.

## Non-goals

- Bundling or default-enabling the official skill plugin. Installation, signer
  trust, `[tools]` admission, and autoload remain explicit operator choices.
- Treating plugin isolation as permission to trust project-controlled prompts.
- Giving project `allowed-tools` authority. EP-44 has no accepted transition
  for it.
- Adding a native prompt-contribution or role-injection extension point.
- Shipping argument substitution, dynamic shell interpolation, personal/global
  skills, plugin-bundled skill directories, forked execution, model overrides,
  path activation, or live reload in the corrected Phase 1.

## Decisions

### Model behavior belongs to an explicit WASM plugin

The official `skills` package lives in `foobarto/stado-plugins` and declares:

| Tool | Class | Effective capability subset | Behavior |
|---|---|---|---|
| `skills__search` | NonMutating | `context:resource:catalog:skill` | Search and format admitted skill facts; cannot open bodies or edit the tool surface |
| `skills__load` | StateMutating | skill catalog + open, `registry:catalog`, `session:tool-surface` | Open one exact opaque resource ID returned by search, optionally request an allowed-tool surface edit, and return the labeled body as the tool result |

The plugin is neither embedded nor enabled merely because stado found a skill
file. Operators install the signed package and expose its tools through normal
tool policy. Omitting or disabling `skills__load` prevents model-driven body
invocation. There is no separate native listing to suppress.

Native stado has no `metaSkillLoad`, static `skills__load` registration,
`FormatModelListing`, `AbsorbSkillLoad`, or exact-name autoload/result branch.
Absence, plugin failure, or filtered tools has no native fallback.

### Generic context-resource ABI

Two generic host imports bridge facts WASM cannot obtain from its garden:

- `stado_context_resource_catalog` under
  `context:resource:catalog:<kind>`;
- `stado_context_resource_open` under `context:resource:open:<kind>`.

Capabilities are exact by operation and kind. Catalog permission for `skill`
does not grant open, another kind, a path, or filesystem authority.
The package top-level capability list is the signed union, while each ordinary
tool's required, presence-preserving signed subset attenuates its actual Host
view. `[]` is explicit zero authority; omission is rejected rather than treated
as inheritance. Admission rejects a duplicated or non-subset value, so
`skills__search` cannot acquire open or surface-edit authority from its more
capable sibling.

`skills__load` requires the opaque `id` returned by search, never a display
name. It obtains a fresh catalog and matches that exact ID before opening under
the catalog digest. Duplicate display names therefore remain independently
loadable, while a fabricated or stale ID fails closed. This follows the same
rule as plugin package identity: human-readable labels do not select
authority.

Catalog pages are strict, bounded, ordered, and fenced by one
`catalog_digest`. Every resource carries an opaque immutable `id`, exact
content `digest`, kind, name, summary, scope, provenance, model visibility, and
effective allowed-tool facts. Open requires kind, ID, and the exact catalog
digest, and repeats the admitted fact with content format and content.

The resource ID binds immutable source metadata, body, and declared tools. The
catalog digest also binds the current effective tool projection. Pagination or
open fails stale rather than combining facts from two source/trust/session
states. No raw source path is exposed as authority.

This ABI is intentionally not named after skills. Other applications may use a
new kind after separately defining native admission and exact capabilities;
the primitive itself contains no search score, matching, prose formatting,
workflow choice, or role policy.

### Native admission owns visibility and trust facts

WASM does not receive every parsed file and decide which ones are safe:

- `disable-model-invocation: true` resources are mechanically omitted from
  both catalog and open;
- rendered content over 128 KiB is omitted from the model catalog so the
  complete labeled result fits the ordinary 1 MiB WASM tool-result channel,
  including worst-case JSON escaping; operator `/skill` and `--skill` retain
  the native loader's separate 1 MiB file allowance;
- project resources report no effective allowed tools;
- persona resources may report at most 64 unique valid exact names;
- those names are intersected with an immutable snapshot of the exact
  config-filtered registry and the live session override ceiling;
- absent, globally disabled, and session-disabled names are omitted;
- deactivate changes active state, not the ceiling, so a permitted name can be
  reactivated later.

The official plugin loads the generic exact registry catalog, checks every
effective name exists in that same projection, and asks
`stado_session_tool_surface_apply` to activate the complete batch under its
registry digest. The host rechecks the whole batch before atomic mutation.
Neither the skill nor the plugin can widen registry, broker, sandbox, or
session authority.

### Tool-origin content stays tool-origin

`skills__load` returns a versioned JSON tool result containing the exact
Markdown body, resource ID/digest, `scope`, `provenance`, content format, and
any activated names. AgentLoop and TUI persist and send it exactly as a
role=tool result. They do not trim the body, append a synthetic user message,
or special-case the tool name.

This differs deliberately from the explicit operator path. `/skill` and
`--skill` truthfully create user input because the operator selected the skill.
Matching body bytes do not imply matching provenance.

### Cross-surface composition is context-bound

`EffectiveSkills` remains the one project/persona resolution path. Each
autonomous surface passes that catalog in the current tool-dispatch context:

- run and headless AgentLoop;
- ACP AgentLoop;
- subagent one-shot AgentLoop rooted at the child worktree;
- TUI direct async tool dispatch.

The generic plugin adapter constructs resource access from that exact context
for installed, bundled, and override dispatch uniformly. No lifecycle tick,
system prompt contribution, global mutable catalog, or hidden per-surface
fallback is required. A one-shot invocation with no bound catalog sees the
primitive as unavailable.

### Skill file and operator contract

The loader recognizes both:

```text
.stado/skills/review.md
.stado/skills/review/SKILL.md
```

Directory skills may carry supporting files. `${STADO_SKILL_DIR}` is expanded
only inside returned body content. Supporting files remain accessible solely
through separately granted filesystem/process tools.

Phase 1 frontmatter remains:

| Field | Meaning |
|---|---|
| `name` | Stable name within the effective catalog |
| `description` | Primary search summary |
| `when_to_use` | Additional matching context |
| `slash` | Explicit operator shortcut |
| `disable-model-invocation` | Omit from the model resource catalog |
| `user-invocable` | Include/exclude operator gestures |
| `allowed-tools` | Persona-only candidate names; project value is inert |

Nearest project skill wins project collisions; project entries win persona
name collisions. Invalid or oversized siblings warn without discarding valid
entries. Existing path confinement, symlink refusal, entry bounds, and file
size limits remain native loader responsibilities.

## Phasing and status

Corrected Phase 1 is implemented in source but the EP remains **Partial**. The
official plugin development artifact is deliberately unsigned and unpublished
until the release gate uses the real offline key. Later phases still require
separate implementation and trust review:

1. **Corrected Phase 1:** model search/open, file formats, invocation controls,
   persona allowed-tool projection, operator paths.
2. **Deferred:** argument substitution and any dynamic context generation.
3. **Deferred:** personal/global and plugin-distributed skill sources.
4. **Deferred:** fork execution, model/effort overrides, paths, settings
   overrides, and live reload.

Dynamic shell interpolation is not an incremental convenience. It is command
execution triggered by project prompt content and needs a dedicated accepted
authority/sandbox design before implementation. This EP no longer pre-accepts
an unimplemented EP-44 trust gate.

## Failure behavior

- Official plugin absent or not surfaced: no model skill discovery; operator
  gestures continue.
- Catalog capability absent or no context bound: plugin call fails closed with
  no native fallback.
- Catalog changes during pagination/open: stale failure; caller searches again.
- Hidden skill: absent from catalog and impossible to open by guessed ID.
- Invalid persona tool declarations: resource projection fails; no partial
  activation.
- Unknown/disabled persona names: omitted from effective facts.
- Registry changes before activation: digest-fenced edit fails atomically.
- Body plus result metadata exceeds the 1 MiB plugin tool-result buffer: load
  returns an explicit size error rather than truncating content.

## Required tests

- strict operation/kind capability separation, JSON/UTF-8/size bounds, cursor
  progress, stale pagination, stale open, and distinct open-envelope sizing;
- resource ID changes with body/source/declaration changes;
- hidden resources cannot catalog or open;
- project tools remain inert; persona tools are unique/bounded and exactly
  intersected with filtered registry/session ceilings;
- AgentLoop and TUI controller ceilings reject absent names and remain stable
  across deactivate/reactivate;
- `skills__search` owns matching/formatting and `skills__load` owns open plus
  one atomic activation request;
- a `skills__load`-shaped result remains an unmodified role=tool message in
  run and TUI;
- native registry/prompt/handler debt remains zero under architecture guards;
- official source passes unit, race, vet, reproducible build, and no-signature
  checks.

## Decision log

### D1. Progressive disclosure, not file shape, defines model invocation

The model must be able to find a cheap summary and request the body. Flat and
directory forms are authoring ergonomics, not different authority classes.

### D2. Use an explicit application over generic facts

A native listing/loader is rejected. A skill-specific privileged import is
also rejected. Generic resource facts preserve the WASM innovation boundary
while native admission retains facts and ceilings unavailable in the garden.

### D3. Preserve initiator provenance

Operator selection produces user input. Model selection produces a tool
result. Stado never changes one into the other based on an exact tool name.

### D4. Project allowed-tools remains inert

EP-44 shipped always-strip for unsafe project authority rather than a TOFU
store. Location alone cannot pre-approve session tools. Persona declarations
are operator-authored and may be intersected with, never widen, the session
ceiling.

### D5. Explicit installation and exposure

The official plugin is not bundled or default-enabled. This keeps stado core
small, makes model behavior a signed replaceable application, and leaves the
operator in control of whether skill discovery consumes model surface.

## Related

- [EP-8](./0008-repo-local-instructions-and-skills.md)
- [EP-37](./0037-tool-dispatch-and-operator-surface.md)
- [EP-38](./0038-abi-v2-bundled-wasm-and-runtime.md)
- [EP-39](./0039-plugin-distribution-and-trust.md)
- [EP-44](./0044-repo-config-trust-boundary.md)
- [EP-66](./0066-canonical-plugin-authority-and-application-placement.md)
- [Skills reference](../features/skills.md)
