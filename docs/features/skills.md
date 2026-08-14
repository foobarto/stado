# `.stado/skills/` — reusable prompts

Drop markdown skill files under `.stado/skills/` and stado exposes them as
reusable prompts reachable via `/skill:<name>` in the TUI, `--skill <name>`
on the CLI, or **`skills__load`** when the model decides a skill matches
the task (EP-0045).

## File format

### Flat file (single prompt)

```markdown
---
name: refactor
description: Extract a function — shown to the model for matching
---
Find repeated code near the cursor and factor it out into a helper.
```

Path: `.stado/skills/<name>.md`

### Directory bundle (scripts / references)

```
.stado/skills/
  summarize/
    SKILL.md          # required entrypoint
    scripts/check.sh  # referenced via ${STADO_SKILL_DIR}/scripts/check.sh
```

The body may use `${STADO_SKILL_DIR}` for stable paths to bundled files.
Supporting files are not auto-loaded — the model reads or executes them
via normal FS/exec tools under the sandbox.

Frontmatter keys (one `key: value` per line):

| Key | Purpose |
|-----|---------|
| `name` | Skill id (defaults to filename stem or directory name) |
| `description` | Primary text the model matches on |
| `when_to_use` | Extra trigger context appended to the listing |
| `slash` | Bare TUI shortcut name (registers `/<name>`) |
| `disable-model-invocation: true` | User-only — omitted from model listing |
| `user-invocable: false` | Model-only — hidden from `/skill` and slash shortcuts |
| `allowed-tools` | Tools surfaced onto the slate on load (persona skills only; project skills fail closed) |

## Why skills exist

Three drivers:

1. **Repeated workflows deserve a name.** "Review this diff for
   security issues" isn't a one-off prompt — it's a recurring ask.
   A skill file turns it into a two-keystroke invocation.
2. **Prompts live with the code.** `.stado/skills/` is in-repo, so
   skills ship with the project, version with the code, and PR
   reviews can gate prompt changes.
3. **Works in CI + TUI.** The same skill file is invocable from
   `stado run --skill <name>` in a pipeline and from `/skill:<name>`
   in the interactive TUI, with no duplication.

Skills are NOT macros / templates / parameterised prompts — they're
deliberately single-shot text. Argument plumbing is out of scope;
just layer `--prompt` on top when you need an ad-hoc tweak.

## Resolution

Stado walks from cwd up to the filesystem root looking for
`.stado/skills/` directories. Each `*.md` file and each
`<name>/SKILL.md` directory entry is registered. Nearest wins.

```
repo-root/
  .stado/skills/
    review.md          # "Review for security + style"
    refactor.md        # root-level: generic extract-method prompt
  pkg/foo/
    .stado/skills/
      refactor.md      # foo-local: preserves the pkg's own style
```

Inside `pkg/foo/`, `/skill:refactor` uses `pkg/foo/.stado/skills/refactor.md`.
Anywhere else, it uses the root version.

## Using skills

### In the TUI

```
/skill                # list all loaded skills with descriptions
/skill:refactor       # inject refactor.md body as a user message
                      # (next Enter submits; /clear cancels)
```

The sidebar shows "Skills: N — /skill" when any are loaded, so
the feature is discoverable without prior knowledge.

### From the CLI

```sh
stado run --skill refactor
```

Resolves `.stado/skills/refactor.md` from cwd (same walk-up as TUI),
uses the body as the prompt. Combine with `--prompt`:

```sh
stado run --skill refactor --prompt "apply to the billing module"
```

Skill body first, then your prompt appended. Unknown skill →
actionable error listing what's available.

## Model invocation (EP-0045)

By default, every loaded skill's `name` + `description` appear in the
system prompt. The model can call **`skills__load`** with `{"name":"<skill>"}`
to inject the body as a user message — same effect as `/skill:<name>`.

- Set `disable-model-invocation: true` for side-effecting skills the
  model must not auto-load (`deploy`, `commit`, …).
- Deny `skills__load` via `[tools].disabled` to disable model invocation
  while keeping user `/skill:` working.
- Project skills never honor `allowed-tools`. EP-44's proposed per-project TOFU
  gate did not ship; the current fail-closed posture has no project authority
  transition.

### What `allowed-tools` does (and doesn't)

`allowed-tools` **surfaces** the named tools onto the per-turn slate when the
skill loads, so the model can call them without a `tools__describe`
round-trip. It does **not** widen the sandbox (a granted `bash` still runs
under the session sandbox policy).

There is no separate "skip the approval prompt" step, because stado's native
(bundled) tools have no approval prompt — they run as soon as they're surfaced
(see [commands/tui.md → Approvals](../commands/tui.md)). Surfacing a tool via
`allowed-tools` therefore already gives the Claude-Code "runs without asking"
behavior for native tools. The only interactive approval in stado is the
opt-in **plugin `ui:approval`** card, which `allowed-tools` does not currently
suppress.

## Design notes

- **Argument injection** lands in EP-0045 Phase 2 (`$ARGUMENTS`, `` !`cmd` ``).
- **No include/reference expansion.** Skills are plain text; keep each
  one self-contained or use directory bundles + `${STADO_SKILL_DIR}`.
- **Scope is cwd-walk + persona `skills:`** (no `~/.config/stado/skills/`
  until Phase 3).

## Sample

A minimal skill:

```markdown
---
name: audit-tests
description: Flag tests that look flaky or brittle
---
Scan the most recently-modified test files. For each, flag:
- Timing-dependent assertions (sleeps, race windows)
- Hard-coded ports / ephemeral-fd assumptions
- Tests that only assert "no error" without content

Output one bullet per finding with file:line.
```

With `tools enabled`, the model will `grep` / `read` through the
repo and produce a scoped review.

## See also

- [features/instructions.md](./instructions.md) — repo-global
  instructions file (AGENTS.md / CLAUDE.md). Skills are the
  per-workflow sibling.
- [commands/run.md](../commands/run.md) — `--skill` flag reference.
