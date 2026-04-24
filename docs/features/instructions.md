# `AGENTS.md` / `CLAUDE.md` — project instructions

Drop a markdown file at the repo root and stado injects its contents
as project guidance in the system prompt on every turn. Standard cross-vendor shape:
Claude Code, Cursor, Aider, Opencode, and the `agents.md` proposal all
read the same file. One copy, every tool respects it.

## File format

Plain markdown. No schema, no frontmatter. Stado reads the body
verbatim into the `{{ .ProjectInstructions }}` field of the editable
system prompt template at `~/.config/stado/system-prompt.md`:

```markdown
# Project guidelines

- This repo is a Go module. Prefer standard-library first.
- All new packages need a doc comment on the package declaration.
- Tests live next to the code as `*_test.go`.
- Don't touch `vendor/` or generated code in `pkg/generated/`.
```

A single list. Three paragraphs. Ten sections with headings. It all
works — whatever you'd tell a new contributor during onboarding
belongs here.

## Resolution order

Stado walks from the current working directory **upward** to the
filesystem root, stopping at the first match. Within a directory, the
order is:

1. `AGENTS.md` — preferred, matches the [agents.md proposal][1].
2. `CLAUDE.md` — fallback, kept because so many repos already have
   one. Pick either; don't ship both.

[1]: https://agents.md/

So in a monorepo:

```
repo-root/
  AGENTS.md           # generic project-wide rules
  pkg/billing/
    AGENTS.md         # billing-specific notes override the root
    cmd/cli/main.go   # launched from here → billing AGENTS.md wins
  pkg/frontend/
    # no AGENTS.md here → launched from here → root AGENTS.md wins
```

Launch stado from `pkg/billing/cmd/cli/` and the billing-specific file
is the one injected. Same binary, different context per sub-project.

## Why the walk-upward

Three reasons:

1. **Monorepos.** Per-module instructions beat a single top-level file
   that has to call out every subsystem. With walk-upward semantics,
   a module owner can drop an AGENTS.md in their dir and it just works
   when anyone launches stado from there.
2. **Stay close to the code.** The instructions live in-repo, version
   with the code, and PR reviews can gate prompt changes. No shared
   "AI rules" doc that drifts out of sync.
3. **Zero-config onboarding.** No `--system-prompt` flag to remember.
   No `.stado/system.md`. Existing AGENTS.md / CLAUDE.md lights up
   stado automatically, the first time.

## What stado does with it

Every `TurnRequest` built by the runtime carries the rendered system
prompt template as `system`. The default template includes stado's
identity, runtime provider/model metadata, problem-solving defaults,
and the loaded project content. This applies to:

- The TUI (`stado`) on every turn.
- `stado run` one-shot prompts.
- `stado acp` / `stado headless` turns.

Sidebar in the TUI shows the basename (`AGENTS.md`, `CLAUDE.md`) so
you can tell at a glance which file informed the prompt. When no
file is found, the "Instructions" row is simply absent — rather than
rendering "Instructions: none" which looks broken.

## Size considerations

The content is sent every turn. Long instructions inflate every
request by their byte count:

| Instructions size | Per-turn overhead on Claude (approx.) |
|-------------------|--------------------------------------|
| 1KB (~200 words)  | ~250 tokens |
| 5KB (~1000 words) | ~1250 tokens |
| 20KB              | ~5000 tokens |

Under 2KB is comfortably cheap even across 100-turn sessions. Above
10KB and you're paying for it every turn on every request — consider
splitting into a concise top-level file plus module-local files that
only get loaded when you launch from the relevant subdir.

## Gotchas

- **Walks to filesystem root.** If you have an `AGENTS.md` in your
  home directory, stado will find it. That's intentional (personal
  cross-repo defaults), but if you were expecting repo-scoped only,
  move your personal file somewhere deeper.
- **A directory named `AGENTS.md/`** is skipped — some note-taking
  setups use directories with that name. Stado won't try to load
  them.
- **Broken file ≠ hard fail.** A permission error on AGENTS.md prints
  a stderr warning and boots the TUI with no system prompt. Better
  than refusing to start.
- **No include directive.** Stado reads the one file it finds. It
  does not expand `@import` / `{{ include }}` / similar. Keep each
  AGENTS.md self-contained.
- **No overrides from config.** There's deliberately no
  `config.toml → [instructions] path = "..."` knob — the walk-upward
  convention is the contract, so that same file works in every
  AGENTS.md-aware tool.

## See also

- [features/skills.md](./skills.md) — `.stado/skills/*.md` are the
  per-workflow sibling to the repo-global AGENTS.md.
- [commands/tui.md](../commands/tui.md#config) — where instructions
  sit in the TUI sidebar.
- [agents.md](https://agents.md/) — the cross-vendor proposal.
