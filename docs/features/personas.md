# Personas

A persona is the agent's operating manual — what it pays attention to, how aggressive it is, what it writes down, when to delegate. Selecting one switches the system-prompt body without changing what the project knows about itself (your `AGENTS.md` / `CLAUDE.md` still applies on top).

Personas are markdown files with optional YAML frontmatter. stado ships eight; you can add more under `~/.stado/personas/` (global) or `{project-root}/.stado/personas/` (per-project, shadows global).

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

Drop a markdown file with frontmatter under `~/.stado/personas/` or `{project}/.stado/personas/`:

```markdown
---
name: my-style
title: My Style
description: One-line summary
inherits: software-engineer        # optional — load named base, then this body appends
collaborators: [qa-tester]         # optional — listed as delegation targets
recommended_tools: [read, edit]    # optional — hint, not enforced
version: 1
---
# My Style

(markdown body — the operating manual)
```

Fields:

| Field | Purpose |
|---|---|
| `name` | Canonical id; must match the filename without `.md` |
| `title` | Human-readable name shown in `/persona` and `stado plugin list --personas` |
| `description` | One-line summary; appears in pickers |
| `inherits` | Optional — name of a base persona; its body loads first, then this body appends |
| `collaborators` | Optional — names of personas you'd typically delegate to via `agent.spawn` |
| `recommended_tools` | Optional — hint to the operator about what this persona expects |
| `version` | Optional — bumped when the body changes meaningfully (for your own tracking) |

The body is plain markdown. Lean into the shape:

- What the agent IS (one paragraph).
- Modes it should recognise and switch between.
- Operating posture — bias toward action, when to ask, when to push back, what to do when stuck, self-critique loop.
- Decomposition unit (specs, hypotheses, paragraphs — whatever fits).
- Validation discipline (what's "done" for this kind of work).
- Delegation rules — when to spawn another persona.

Read the bundled personas under `internal/personas/library/` for reference. They average 6–10 KB each.

## Inheritance

`inherits: <name>` loads the named base persona's body first, then appends yours. One level deep — no chained inheritance — to keep the merge behaviour predictable.

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

`stado plugin list --personas` shows every persona visible to the resolver, with its source path. Useful when a project-level override isn't taking effect.

```
NAME              SOURCE
default           (bundled)
software-engineer (bundled)
my-project-engineer  /home/me/code/proj/.stado/personas/my-project-engineer.md
...
```

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
- [`docs/commands/plugin.md`](../commands/plugin.md) — `stado plugin list --personas`
