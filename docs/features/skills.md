# `.stado/skills/` — reusable prompts

Drop markdown files with YAML-style frontmatter in
`.stado/skills/<name>.md` and stado exposes them as reusable
prompts reachable via `/skill:<name>` in the TUI or `--skill <name>`
on the CLI.

## File format

```markdown
---
name: refactor
description: Extract a function
---
Find repeated code near the cursor and factor it out into a helper.
Prefer the narrowest shared scope; keep call sites unchanged.
```

Frontmatter is optional. When absent, the filename stem (without
`.md`) is the name and the whole file body is the content. Minimal
parser — one `key: value` per line, no quoting, no nested objects.

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
`.stado/skills/` directories. Every `*.md` inside gets registered.
Nearest wins — a module-local `.stado/skills/refactor.md` overrides
a repo-root one.

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

## Design notes

- **No argument injection.** Skills can't take `{name}` placeholders.
  If you need parameterisation, use `--prompt` to compose ad-hoc
  context on top of the skill body.
- **No include/reference expansion.** Skills are plain text; they
  can't `@import` other skills. Keep each one self-contained.
- **Scope is cwd-walk only.** No `~/.config/stado/skills/` user-
  global layer yet. The nearest-wins semantics wouldn't play nicely
  with a far-off global that could override every project — so
  global skills are deliberately not supported. Contributions
  welcome if you have a case where the tradeoff is wrong.

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
