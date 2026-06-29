---
ep: 45
title: Model-Invocable Skills (Agent Skills parity)
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Partial
type: Standards
created: 2026-06-17
requires: [37]
extends: [8]
see-also: [18, 30, 39, 44]
history:
  - date: 2026-06-29
    status: Partial
    note: >
      Clarification (post-merge) on the `allowed-tools` trust-rule-2 wording
      "pre-approves a prompt for an already-available tool": stado has NO
      native-tool approval prompt — bundled tools run as soon as they are
      surfaced (the old TUI approval loop was removed; only the opt-in plugin
      `ui:approval` card remains, see docs/commands/tui.md). So `allowed-tools`
      in Phase 1 == surfacing the tool onto the per-turn slate, which already
      gives the Claude-Code "runs without asking" behavior for native tools;
      there is no separate native approval step to skip. A 2026-06-29 operator
      decision considered adding an approval-skip and resolved it as
      already-satisfied (no code change); the only residual is suppressing a
      plugin `ui:approval` card, deferred as not currently needed. See
      .agent/decisions/2026-06-29-ep0045-phase1-merge-and-followups.md.
  - date: 2026-06-29
    status: Partial
    note: >
      Phase 1 shipped — model-invocable skills land. Loader recognizes
      directory <name>/SKILL.md bundles (${STADO_SKILL_DIR} expansion) and
      flat .stado/skills/<name>.md, with the os.Root/size/entry-count/symlink
      guards extended to the directory walk. Frontmatter gains when_to_use,
      disable-model-invocation, user-invocable, allowed-tools. The budget-capped
      model listing is appended to the system prompt and skills__load injects
      the body as a user message, wired uniformly across run / TUI / ACP /
      headless / subagents. Trust: project-skill allowed-tools fail closed
      (persona-scoped only until EP-44 TOFU); denying skills__load via
      [tools].disabled disables model invocation wholesale while /skill: keeps
      working (rule 3); inert skills (both invocations off) warn at load. Docs
      synced (features/skills.md, commands/run.md, commands/tui.md, CHANGELOG)
      and the package tests (internal/skills, internal/runtime, internal/tui)
      are green. Phases 2–4 (arguments + dynamic injection, distribution
      scopes, parity tail) remain open — see the phase table above. Status is
      Partial, not Implemented, until they land.
  - date: 2026-06-18
    status: Draft
    note: Phase 1 implementation in flight on feat/ep-0045-model-invocable-skills —
      directory SKILL.md loader, budget-capped model listing, skills__load tool,
      frontmatter knobs (when_to_use / disable-model-invocation / user-invocable /
      allowed-tools), persona-scoped allowed-tools (project fail-closed). Post-review
      fixes — skills__load is deniable per trust rule 3 (NOT a non-disableable
      kernel meta-tool; surfaced on demand only when a model-invocable skill exists,
      and denying it suppresses the listing too); skills wired uniformly across run /
      TUI / ACP / headless / subagents; TUI now injects the loaded body as a user
      message; inert-skill (both invocations off) load warning.
  - date: 2026-06-17
    status: Draft
    note: Initial draft. Closes the gap between stado's user-invoked skill
      prompts and Claude Code's model-invocable Agent Skills, reusing the
      existing deferred-tool search/activate surface (EP-37).
---

# EP-45: Model-Invocable Skills (Agent Skills parity)

## Problem

Stado's skills (EP-8) are **user-invoked prompt injectors**. A skill is
one markdown file under `.stado/skills/<name>.md`; the user pastes its
body into the turn with `/skill:<name>`, `--skill <name>`, or a
`slash:` shortcut. The model never sees that a skill exists and can
never decide to use one. In Claude Code's taxonomy this is a *custom
command*, not a *skill*.

Claude Code (and the [Agent Skills](https://agentskills.io) open
standard it implements) define a skill by the property stado is
missing: **progressive disclosure**. Each skill's `name` +
`description` sit in the model's context cheaply; the model pulls the
full body into context **on its own initiative** when the description
matches the work at hand. The body can reference bundled supporting
files (scripts, references, templates) that load only when needed. This
is what lets a skill act as an on-demand capability rather than a
snippet the user has to remember to paste.

Concretely, the gap today:

| Capability | Claude Code | stado today |
|---|---|---|
| Model can invoke a skill by matching its description | yes (core) | **no** |
| Skill body is a directory with bundled scripts/refs | `<name>/SKILL.md` + files | flat `.md`, no bundle |
| Personal / global skills | `~/.claude/skills/` | no (deliberate, EP-8 D2/skills.go) |
| Plugin-bundled skills (namespaced) | `<plugin>/skills/` | no |
| Arguments / substitution | `$ARGUMENTS`, `$N`, `$name`, `${...}` | no (EP-8 non-goal) |
| Dynamic context injection | `` !`cmd` `` runs before model sees it | no (EP-8 non-goal) |
| Per-skill tool pre-approval | `allowed-tools` | no |
| Invocation control | `disable-model-invocation`, `user-invocable` | partial (`slash:` only) |
| Forked/subagent execution | `context: fork` + `agent:` | no |
| Per-persona skill scoping | via subagent `skills:` | **yes — stado is ahead here** |

The headline is the first row. Everything else is supporting surface
that only matters once the model can choose a skill. The rest of this
EP is mostly about closing the first row **without** inheriting Claude
Code's full security-relevant surface area uncritically — stado is a
sandboxed, trust-boundary-conscious tool (EP-5, EP-39, EP-44), and a
project-checked-in skill that the model auto-loads, that grants tools,
or that runs shell on open is exactly the repo-config-as-attacker
problem EP-44 already fenced.

## Goals

- Let the model discover and load a skill on its own, by description,
  with the body costing context only when loaded (progressive
  disclosure).
- Adopt the `<name>/SKILL.md` directory format so a skill can bundle
  supporting files (scripts, references, examples) the model loads on
  demand — while keeping existing flat `.stado/skills/<name>.md` files
  working.
- Give skill authors the invocation-control and tool-scoping knobs that
  make model invocation safe and predictable (`disable-model-invocation`,
  `user-invocable`, `allowed-tools`).
- Map every newly model-reachable or shell-touching skill surface onto
  stado's existing trust boundary (EP-44 / EP-39) rather than bolting on
  a parallel trust model.
- Reuse the deferred-tool search/activate machinery (EP-37) as the
  delivery vehicle instead of inventing a second progressive-disclosure
  mechanism.

## Non-goals

- **Not** removing the existing user-invoked surface. `/skill:<name>`,
  `--skill`, and `slash:` keep working unchanged; they become *one* way
  to invoke a skill, not the only way.
- **Not** shipping the entire Claude Code frontmatter surface in one
  release. `context: fork`, `model`/`effort` overrides, `paths` glob
  activation, `skillOverrides` settings, live file-watch reload, and
  `--add-dir` skill loading are explicitly deferred to Phase 4 / future
  EPs (see Open questions). They are parity-nice, not gap-defining.
- **Not** a general macro/templating language for its own sake.
  Argument substitution and dynamic injection (Phase 2) are scoped to
  the Agent Skills standard's syntax, gated on trust, and justified only
  because the model-invocation use cases (`/fix-issue 123`, grounding a
  skill in `git diff`) need them — this is the deliberate reversal of
  EP-8's "no templating" non-goal, recorded in D5.

## Design

### Phasing

The EP lands in phases so the gap-defining capability ships first and
the trust-sensitive surface area lands deliberately.

- **Phase 1 — Model invocation + directory format.** The core gap.
- **Phase 2 — Arguments + dynamic context injection.** Reverses EP-8
  non-goals; trust-gated.
- **Phase 3 — Distribution scopes.** Personal/global + plugin-bundled
  skills.
- **Phase 4 — Parity tail (deferred).** Fork execution, model/effort,
  `paths`, settings overrides, live reload. Tracked as open questions,
  not specified here.

### Phase 1 — Model-invocable skills

**Surface to the model via the deferred-tool mechanism.** Stado already
solves "let the model discover and pull in a capability on demand" for
tools: `tools__search` / `tools__describe` / `tools__activate` (EP-37).
Skills get the same treatment rather than a new mechanism:

- At session start, each loaded skill contributes a one-line
  `name` + `description` entry to the model's context (the same budgeted
  listing the deferred-tool surface uses). This is the cheap half of
  progressive disclosure.
- A `skills__load` host tool (or a `kind=skill` row in the existing
  tool-search index — see D2) returns the rendered skill body. When the
  model calls it, the body enters context exactly as a user `/skill:`
  injection does today, except the *model* initiated it.
- User-facing `/skill:<name>`, `--skill`, and `slash:` shortcuts now
  resolve to the same load path. One body, two initiators.

**Directory format with flat-file back-compat.** A skill becomes a
directory whose entrypoint is `SKILL.md`:

```
.stado/skills/
  summarize-changes/
    SKILL.md          # required: frontmatter + body
    scripts/
      check.sh        # referenced from the body, executed not inlined
    reference.md      # loaded on demand when the body points to it
```

Loader changes in `internal/skills`:

- `Load` keeps walking up from cwd collecting `.stado/skills/`. For each
  entry it now recognizes **both** `name/SKILL.md` (directory skill) and
  the legacy `name.md` (flat skill). Flat files keep working verbatim —
  they are skills with no bundle.
- A new `Dir string` field on `Skill` records the skill's own directory
  (empty for flat skills), exposed to the body as `${STADO_SKILL_DIR}`
  so a bundled script is referenced by a stable path regardless of cwd
  (mirrors Claude Code's `${CLAUDE_SKILL_DIR}`). Supporting files are
  **not** auto-loaded; the body names them and the model reads/executes
  them through normal FS/exec tools, which keeps them under the same
  capability grants as any other file (EP-5).
- The existing `os.Root` confinement, 1 MiB file cap, 4096-entry cap,
  and symlink rejection (skills.go) extend to the directory walk:
  `SKILL.md` and supporting files are read through the confined root;
  symlinks out of the skill dir are still rejected.

**Frontmatter additions (Phase 1).** The parser grows from `{name,
description, slash}` to add:

| Field | Meaning |
|---|---|
| `when_to_use` | Extra trigger context appended to `description` in the listing. |
| `disable-model-invocation` | `true` ⇒ skill is omitted from the model-facing listing; only the user can invoke it. The safe default for side-effecting skills (`deploy`, `commit`). |
| `user-invocable` | `false` ⇒ hidden from `/skill` menu and `slash:`; model-only background knowledge. |
| `allowed-tools` | Tools pre-approved while this skill is active (see Trust below). |

`description` stays optional but becomes load-bearing: it is what the
model matches on, so the docs guidance shifts from "optional label" to
"write it for the model." Listing text is budget-capped the way the
deferred-tool listing already is (EP-37); no new budget knob in Phase 1.

**Trust boundary (the part stado must get right).** Model-invocable
skills and `allowed-tools` are privilege surface, and project skills are
attacker-controlled input under EP-44's threat model. Rules:

1. `allowed-tools` from a **project** skill (`.stado/skills/` inside the
   repo) takes effect only after the project clears the EP-44 trust gate
   (TOFU). Untrusted project skills load as model-invocable prompt text
   but grant **no** tools — identical to how EP-44 strips powerful
   project config keys. Personal/global skills (Phase 3) are operator-
   authored and trusted by location.
2. `allowed-tools` never escalates past the session's own sandbox
   policy. It pre-approves a *prompt* for an already-available tool; it
   cannot widen the broker ceiling or the sandbox (consistent with the
   wasm-tool-confinement model in memory). A skill cannot grant `bash`
   network it doesn't otherwise have.
3. Model invocation respects a session-level off switch: denying the
   skill-load tool (analogous to Claude's `Skill` permission) disables
   model invocation wholesale, leaving user invocation intact.

### Phase 2 — Arguments and dynamic context injection

This phase reverses EP-8's "no templating / no parameter substitution"
non-goal (D5), scoped to the Agent Skills standard:

- **Argument substitution** in the body: `$ARGUMENTS`, `$ARGUMENTS[N]`,
  `$N`, and named `$name` (declared via an `arguments:` frontmatter
  list), plus `${STADO_SESSION_ID}` and `${STADO_SKILL_DIR}`. If a skill
  is invoked with arguments but contains no `$ARGUMENTS`, the input is
  appended as `ARGUMENTS: <value>` (matching Claude Code). An
  `argument-hint` field feeds autocomplete.
- **Dynamic context injection**: `` !`cmd` `` inline and ```` ```! ````
  fenced blocks run **before** the body is shown, output replacing the
  placeholder. This is preprocessing, not a tool call.

**Trust boundary.** `` !`cmd` `` is arbitrary command execution at skill
load. For a project skill this is RCE-on-open. Therefore:

- Dynamic injection from a project skill runs **only** when the project
  is trusted (EP-44 gate) **and** the injected command executes under
  the session sandbox policy — never the raw operator shell on a `run`/
  `tui` session by default. When untrusted, the placeholder is replaced
  with `[shell execution disabled: untrusted project skill]` (mirrors
  Claude's `disableSkillShellExecution`), and a `disable-skill-shell`
  config key provides a hard global off switch for managed setups.
- Argument substitution is pure string templating and carries no
  execution risk, so it is not trust-gated — but a substituted value is
  never re-scanned for `` !`cmd` `` (single-pass, matching Claude Code),
  so an argument can't smuggle in an injection.

### Phase 3 — Distribution scopes

- **Personal/global skills** under `~/.config/stado/skills/` (XDG),
  loaded after the cwd walk. This reverses the skills.go
  "no user-global dir" stance (D2 of EP-8); the "why did this skill
  run?" concern it raised is now answered by the model-facing listing
  itself making provenance visible, and by precedence rules: project
  (nearest) > personal > bundled, with name collisions resolved
  nearest-wins as today.
- **Plugin-bundled skills** under `<plugin>/skills/<name>/SKILL.md`,
  namespaced `plugin:skill` so they can't collide with project/personal
  skills, and inheriting the plugin's existing trust/signing posture
  (EP-39) rather than the project TOFU gate.
- Per-persona scoping (already shipped) composes on top unchanged.

## Migration / rollout

Pre-1.0: this is a **clean break**, not a compatibility-preserving
change. No shims, no opt-in flags to retain old behavior. The breaking
changes are enumerated in `CHANGELOG.md` at implementation time (the
project's no-kid-gloves-pre-1.0 stance).

- **Behavioral break (Phase 1):** every existing
  `.stado/skills/<name>.md` becomes model-invocable by default — its
  `description` enters the model-facing listing and the model may load
  it on its own initiative (D7). A skill that should stay user-only adds
  `disable-model-invocation: true`. This changes behavior for every repo
  with a `.stado/skills/` on upgrade; acceptable pre-1.0, and it gets a
  `CHANGELOG.md` entry.
- Flat `.md` and directory `SKILL.md` both load (D3): flat is the
  single-file convenience form, the directory form is for bundles. A
  format choice, not a back-compat shim.
- Phases 2–3 are additive frontmatter/locations.
- Docs to update on implementation: `docs/features/skills.md`,
  `docs/commands/run.md`, `docs/commands/tui.md` (add a model-invocation
  section), plus the `CHANGELOG.md` entry enumerating the break. EP-8's
  frontmatter already carries `extended-by: [18, 45]`; its accepted body
  stays append-only.

## Failure modes

- **Skill triggers when unwanted.** Author tightens `description` or sets
  `disable-model-invocation: true`. Surfaced via `/skill` listing showing
  what the model can see.
- **Skill never triggers.** Description lacks the words the user says.
  Same remedy as Claude Code; `/skill` shows the live listing for
  debugging.
- **Listing budget overflow** with many skills: least-used descriptions
  drop first (reuse EP-37's budgeting); names always retained.
- **Malicious project skill** grants tools / runs shell on load:
  defeated by the EP-44 trust gate (Phase 1 rule 1, Phase 2 trust rule).
  An untrusted repo's skills are inert prompt text with no tools and no
  shell.
- **Broken skill file** (bad frontmatter, oversized, symlink): stays
  non-fatal per the existing loader contract — warning emitted, other
  skills still load.

## Test strategy

- **Loader** (`internal/skills`): directory `SKILL.md` discovery,
  flat-file back-compat, `${STADO_SKILL_DIR}` resolution, supporting-file
  confinement (symlink/`..` escape still rejected), precedence across
  cwd/personal/plugin scopes.
- **Model surface**: skill listing appears in the deferred-tool index;
  `skills__load` returns the rendered body; `disable-model-invocation`
  omits the entry; denying the load tool disables model invocation while
  `/skill:` still works.
- **Trust**: untrusted project skill grants no `allowed-tools` and its
  `` !`cmd` `` is replaced with the disabled marker; trusted project +
  personal skill behave fully. Pin against EP-44's existing trust tests.
- **Substitution** (Phase 2): `$ARGUMENTS`/`$N`/`$name` expansion,
  single-pass guarantee (substituted value not re-scanned), `ARGUMENTS:`
  append fallback.
- **TUI E2E** (pty-bridge, per CLAUDE.md): `/skill` listing reflects
  model-visibility state; a model-initiated load renders the same block
  a user `/skill:` does. Run inside `distrobox enter kali`.

## Open questions

- **Q1.** Is the model surface a dedicated `skills__load` tool or a
  `kind=skill` extension of the existing `tools__*` search index?
  (See D2 — leaning dedicated tool for a clean permission name, but the
  shared index is less surface.)
- **Q2.** Phase 4 scope: `context: fork` + `agent:` execution maps onto
  stado's background-agent/subagent machinery (EP-38) and is attractive,
  but pulls in model/effort overrides and agent-type resolution. Worth
  its own EP once Phase 1–3 land.
- **Q3.** `paths` glob activation (load a skill only when touching
  matching files) needs a file-event hook stado doesn't have for skills
  yet; defer with live-reload to Phase 4.
- **Q4.** Settings-side visibility (`skillOverrides`-equivalent) vs.
  frontmatter-only control. Frontmatter covers the common case; a
  settings override matters mainly for skills you don't own (plugin/
  shared-repo). Defer until plugin skills (Phase 3) exist.

## Decision log

### D1. Define a skill by model-invocability, not by file shape

- **Decided:** the gap this EP closes is the model's ability to discover
  and load a skill by description (progressive disclosure); the directory
  format and frontmatter additions are in service of that, not the point.
- **Alternatives:** ship the `SKILL.md` directory format and supporting
  files without model invocation (cosmetic parity), or ship only
  invocation without the bundle (no on-demand reference files).
- **Why:** model invocation is the one property that makes a "skill" a
  skill in the Agent Skills standard; without it stado has custom
  commands wearing the name.

### D2. Reuse the deferred-tool surface (EP-37) for disclosure

- **Decided:** skills surface to the model through the existing
  search/activate/deferred-tool machinery rather than a bespoke
  progressive-disclosure path.
- **Alternatives:** a separate skill-listing channel in the system
  prompt; always-inline all skill bodies (defeats the cost win).
- **Why:** stado already budgets and gates "capabilities the model pulls
  in on demand" for tools; a second mechanism would duplicate the budget
  logic and the permission story.

### D3. Support both flat `.md` and directory `SKILL.md`

- **Decided:** the loader recognizes both `name/SKILL.md` and `name.md`;
  a one-line skill needs no directory, a bundling skill gets one.
- **Alternatives:** require directories for every skill (ceremony for the
  common one-file case), or refuse the directory form (no bundles).
- **Why:** flat files are the right ergonomics for the single-prompt
  common case; the directory form earns its ceremony only where
  supporting files exist. This is a format choice, not a back-compat
  obligation — the *behavioral* break (flat skills become
  model-invocable) is clean and intentional, see D7.

### D4. Bind new skill surface to the existing trust boundary, don't fork it

- **Decided:** project-skill `allowed-tools` and `` !`cmd` `` injection
  are gated on the EP-44 project-trust (TOFU) decision; plugin skills
  inherit EP-39 plugin trust; `allowed-tools` never widens the sandbox.
- **Alternatives:** a skill-specific trust prompt; trusting project
  skills implicitly (matches some of Claude Code's defaults but violates
  EP-44).
- **Why:** a project skill is attacker-controlled input under stado's
  threat model; reusing EP-44/EP-39 keeps one trust story instead of
  two, and keeps untrusted-repo skills inert.

### D5. Reverse EP-8's "no templating" non-goal — scoped, in Phase 2

- **Decided:** adopt the Agent Skills substitution + dynamic-injection
  syntax (`$ARGUMENTS`, `` !`cmd` ``, …), trust-gated, single-pass.
- **Alternatives:** keep EP-8's hard line (no args ever) — but then
  `/fix-issue 123`-style skills and diff-grounded skills are impossible,
  which are exactly the model-invocation use cases.
- **Why:** the non-goal made sense when skills were static paste-ins;
  once the model drives them, parameterization and grounding are the
  point, not gold-plating. Scoped to the standard and gated on trust so
  it doesn't become an unbounded macro language or an RCE vector.

### D6. Phase the distribution scopes; ship the core gap first

- **Decided:** personal/global and plugin-bundled skills land in Phase 3,
  after model invocation and the directory format.
- **Alternatives:** ship all locations at once.
- **Why:** the core gap is invocation, not where files live; global/
  plugin skills add trust and precedence surface that benefits from the
  Phase 1 trust rules already being in place.

### D7. All skills model-invocable by default — clean break, no opt-in to old behavior

- **Decided:** on upgrade, every existing skill (flat or directory)
  becomes model-invocable; opting *out* is one frontmatter line
  (`disable-model-invocation: true`). No flag, default, or shim preserves
  the old user-only behavior.
- **Alternatives:** default flat skills to user-only and treat the
  `SKILL.md` directory as the opt-in signal (the conservative,
  upgrade-safe path the first draft leaned toward).
- **Why:** pre-1.0 stado takes clean breaks and documents them in
  `CHANGELOG.md` rather than carrying compatibility shims. A
  per-format opt-in default would be exactly the kid-gloves the project
  avoids, and it would split skill behavior on an irrelevant axis (file
  shape) instead of on the author's intent (`disable-model-invocation`).

## Related

- [EP-8: Repo-Local Instructions and Skills](./0008-repo-local-instructions-and-skills.md) — the current skill contract this extends.
- [EP-37: Tool dispatch and operator surface](./0037-tool-dispatch-and-operator-surface.md) — the deferred-tool search/activate machinery reused for disclosure.
- [EP-44: Repo-config trust boundary](./0044-repo-config-trust-boundary.md) — the trust gate project-skill tool grants and shell injection bind to.
- [EP-39: Plugin distribution and trust](./0039-plugin-distribution-and-trust.md) — trust posture inherited by plugin-bundled skills.
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md) — the ceiling `allowed-tools` can never widen.
- [Agent Skills open standard](https://agentskills.io) and [Claude Code skills docs](https://code.claude.com/docs/en/skills) — the parity target.
- [docs/features/skills.md](../features/skills.md) — user-facing doc to update on implementation.
