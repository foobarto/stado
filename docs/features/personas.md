# Personas

A persona is the agent's operating manual — what it pays attention to, how aggressive it is, what it writes down, when to delegate. Selecting one switches the system-prompt body without changing what the project knows about itself (your `AGENTS.md` / `CLAUDE.md` still applies on top).

Personas are markdown files with optional YAML frontmatter. stado ships eight; you can add more under `~/.config/stado/personas/` (global) or `{project-root}/.stado/personas/` (per-project, shadows global).

## Bundled personas

| Name | Use for |
|---|---|
| `default` | Generalist baseline; falls back here when no specialised persona fits |
| `software-engineer` | Building, fixing, refactoring code |
| `qa-tester` | Testing, edge cases, regression suites, validating fixes |
| `technical-writer` | Documentation, API references, how-tos |
| `prose-writer` | Long-form — journalism, books, blogs |
| `prose-editor` | Manuscript editing, copy editing, line editing |
| `researcher` | Literature reviews, hypothesis-driven inquiry, fact-checking |
| `offsec` | Bug bounty, CTF, engagement work |

## Switching personas in a chat: `/persona`

**In an interactive `stado run` session, switch persona at any time — mid-conversation — with the `/persona` slash command.** This is the everyday way to change personas; you don't need to restart or edit config.

- **`/persona`** (no argument) — opens the **persona picker**: every resolvable persona (project → user → bundled), each labelled with its source. Select and press Enter.
- **`/persona <name>`** — switches straight to that persona, skipping the picker. E.g. `/persona prose-editor`.

What happens on a switch:

- It takes effect on your **next turn** — the new operating-manual body replaces the previous one (your `AGENTS.md` / `CLAUDE.md` still applies on top).
- The **status line** updates to show the active persona.
- The system prints a confirmation block: `persona: software-engineer → prose-editor`.
- The choice is **saved to `[defaults].persona`**, so it also becomes your default for future sessions. `/persona` is both an in-session switch *and* the no-hand-editing way to set your default.

## Selecting a persona

Resolution order, highest first:

1. **Per-call override** — `--persona` CLI flag (at launch), the `persona` arg on `agent.spawn`, or the `persona` arg on a server's `session.new` / `stado_llm_invoke` call.
2. **`[defaults].persona`** in `config.toml` — your saved default. The interactive `/persona` command (above) writes here, so an in-chat switch sticks across restarts.
3. **Bundled `default`** — the fallback when nothing else resolves.

### CLI

```sh
stado run --persona prose-writer "Draft a 600-word post about ..."
stado mcp-server --persona software-engineer
```

`--persona` on `mcp-server` pins the default for the server's lifetime. Clients can override per-call via the `persona` arg on `llm.invoke` / `agent.spawn`. (To switch persona *inside* an interactive session, use `/persona` — see above.)

### Config

```toml
[defaults]
persona = "researcher"
```

### Inside an agent loop

`agent.spawn` accepts `persona` as an arg. Empty = inherit parent's. Use this for the writer→editor delegation pattern:

```json
{"tool": "agent.spawn", "args": {
  "prompt": "Edit the draft I just produced for clarity and pacing.",
  "persona": "prose-editor"
}}
```

### Inside wasm plugins

The `stado_llm_invoke` host import takes a JSON envelope:

```json
{"prompt": "...", "persona": "researcher", "model": "claude-sonnet-4-6"}
```

When `persona` is empty the call inherits the active session's persona.

## Writing your own

Drop a markdown file with frontmatter under `~/.config/stado/personas/` or `{project}/.stado/personas/`:

```markdown
---
name: my-style
title: My Style
description: One-line summary
inherits: software-engineer        # optional — load named base, then this body appends
collaborators: [qa-tester]         # optional — listed as delegation targets
recommended_tools: [read, edit]    # optional — promoted into the autoload surface (see Scope)
tools: [bash, "fs.*"]              # optional — extra tools promoted when active (see Scope)
skills: [skills/recon.md]          # optional — extra skill files, relative to THIS persona's dir
plugins: [my-recorder]             # optional — extra background plugins (LAUNCH-only)
version: 1
---
# My Style

(markdown body — the operating manual)
```

Fields:

| Field | Purpose |
|---|---|
| `name` | Canonical id; must match the filename without `.md` |
| `title` | Human-readable name shown in the `/persona` picker |
| `description` | One-line summary; appears in pickers |
| `inherits` | Optional — name of a base persona; its body loads first, then this body appends. Scope keys (`tools`/`skills`/`plugins`/`recommended_tools`) accumulate up the chain (union) |
| `collaborators` | Optional — names of personas you'd typically delegate to via `agent.spawn` |
| `recommended_tools` | Optional — tool names/globs promoted into the per-turn autoload surface when active (union with `tools`). See [Per-persona scope](#per-persona-scope-skills-tools-plugins) |
| `tools` | Optional — tool names/globs promoted into the per-turn autoload surface when active. See [Per-persona scope](#per-persona-scope-skills-tools-plugins) |
| `skills` | Optional — skill-file paths loaded additively when active; resolved relative to this persona file's own directory. See [Per-persona scope](#per-persona-scope-skills-tools-plugins) |
| `plugins` | Optional — background-plugin ids added when the persona is active **at launch** (launch-only). See [Per-persona scope](#per-persona-scope-skills-tools-plugins) |
| `version` | Optional — bumped when the body changes meaningfully (for your own tracking) |

The body is plain markdown. Lean into the shape:

- What the agent IS (one paragraph).
- Modes it should recognise and switch between.
- Operating posture — bias toward action, when to ask, when to push back, what to do when stuck, self-critique loop.
- Decomposition unit (specs, hypotheses, paragraphs — whatever fits).
- Validation discipline (what's "done" for this kind of work).
- Delegation rules — when to spawn another persona.

Read the bundled personas under `internal/personas/library/` for reference. They average 6–10 KB each.

## Per-persona scope: skills, tools, plugins

A persona can declare extra **skills**, **tools**, and **background plugins** in its frontmatter. These are **additive**: when the persona is active they layer ON TOP of the global defaults. A persona EXTENDS the surface — it never hides or restricts a globally-available tool or skill.

```yaml
---
name: pentester
title: Pentester
tools: [bash, "exploit.*"]          # promoted into the model-facing autoload set
recommended_tools: [nmap]           # also a tools source (union with `tools`)
skills: [skills/recon.md, skills/report.md]   # extra /skill: commands, relative to this file's dir
plugins: [session-recorder]         # extra background plugin (only when active at launch)
---
```

- **`tools` / `recommended_tools`** — names or globs (e.g. `fs.*`) of registered tools promoted into the per-turn autoload surface (what the model sees each turn). The two keys are unioned. Promotion is **live**: switching persona in a chat re-scopes the tool surface on the next turn. Unknown names are a non-fatal warning; the tool must already be registered (a persona can promote a tool from the registry, but can't conjure one that isn't installed).

- **`skills`** — paths to skill `.md` files, resolved **relative to the persona file's own directory** (e.g. `{project}/.stado/personas/` for a project persona). They are loaded additively on top of the cwd-discovered `.stado/skills/` set and registered as `/skill:` commands. Re-scoped **live** on a `/persona` switch: switching away removes the prior persona's skills; the global/project skills are never removed. Paths are confined to the persona directory (no symlink-escape, no `..` traversal) and capped at 1 MiB, same as the cwd skills loader. A bundled (embedded) persona has no on-disk directory, so its `skills:` resolve to nothing.

- **`plugins`** — background-plugin ids added to the background set, **launch-only**. They are loaded when the persona is active at launch (`--persona` or `[defaults].persona`). A later live `/persona` switch does **not** start or stop background plugins — that lifecycle is session-start-only. Unknown ids surface the usual per-plugin load advisory.

Unknown skill path / plugin id / tool name → a non-fatal warning (stderr at launch, an in-chat system block on a live switch); valid entries still apply.

## Inheritance

`inherits: <name>` loads the named base persona's body first, then appends yours. The scope keys (`tools`, `recommended_tools`, `skills`, `plugins`) accumulate up the chain — a child's effective scope is the union of the parent's and its own (additive, parent entries first, deduped).

Use it when you want to specialise a bundled persona slightly without copying its full body. Example: a project-specific software-engineer that adds three project conventions on top of the bundled posture.

```yaml
---
name: my-project-engineer
inherits: software-engineer
---
# My Project Engineer

In addition to the standard software-engineer posture, this project requires:

- All HTTP handlers must use the project's `recoverPanic` middleware.
- Database queries go through `db.Query` only — no raw `*sql.DB` access.
- Logging via `slog` with the structured-log helpers in `internal/logger`.
```

## Resolution debug

There's no CLI command to list personas yet. To see what the resolver
found and which source won, open the **`/persona` picker** (no argument)
in an interactive `stado run` session: it lists every resolvable persona
and labels each with its source (project → user → bundled). Useful when a
project-level override isn't taking effect — the picker shows whether your
`{project}/.stado/personas/` file is being seen and whether it shadows the
user or bundled one.

## Where the persona lands in the prompt

```
[persona body]
                         ← blank line
[project AGENTS.md / CLAUDE.md]
                         ← blank line
[memory context, if any]
```

The persona body REPLACES stado's default operating-manual prompt. The project's `AGENTS.md` / `CLAUDE.md` and any memory-context still append. This way the persona controls *how* the agent thinks; the project controls *what* the agent knows about THIS project.

## Related

- [`docs/features/instructions.md`](instructions.md) — `AGENTS.md` / `CLAUDE.md` mechanics
- [`docs/plugins/abi-reference.md`](../plugins/abi-reference.md) — `stado_llm_invoke` JSON-args shape including `persona`
