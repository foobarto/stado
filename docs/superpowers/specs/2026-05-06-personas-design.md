# Personas — design

**Status:** drafted 2026-05-06; autonomous design call after operator brainstorm.
**Branch:** `feat/personas`.

## Problem

stado today has one operating posture — the implicit system prompt
plus whatever `AGENTS.md` / `CLAUDE.md` the project provides. The
agent reads the same way for every task: the way an operator phrases
"refactor this module" gets the same baseline disposition as "write
a blog post about it" or "audit the auth flow for misconfigurations."

The work has shapes. Software engineering, QA, technical writing,
prose writing, manuscript editing, offensive security, research —
each rewards different defaults around: how aggressive to be, what
to write down, when to ask, when to delegate, what counts as
"done."

A persona is the operating manual for one of these shapes.
Selecting it switches the agent's posture without changing what the
project knows about itself.

## Locked decisions

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | File format | YAML frontmatter + markdown body | Matches stado's skills + plugin manifests; metadata + body separate cleanly. |
| Q2 | Resolution order | `{cwd}/.stado/personas/<name>.md` → `~/.stado/personas/<name>.md` (or `$XDG_CONFIG_HOME/stado/personas/`) → bundled | Project beats user beats stado-shipped. Same semantic as skills. |
| Q3 | Layering with project AGENTS.md | Persona body REPLACES the implicit operating manual; project AGENTS.md still appends. | Persona = "how I think"; AGENTS.md = "what I know about THIS project." Both apply. |
| Q4 | Inheritance | One level deep via `inherits: <name>` frontmatter. Base body loads first, then this body appends. | Lets specialised personas extend `default` without copy-paste. Diamond inheritance not worth the complexity. |
| Q5 | Default persona | Ship `default.md` as a bundled persona. `/persona default` is a real switch, not a special case. | Codifies what's implicit today; gives users something to extend with `inherits: default`. |
| Q6 | Initial bundled set | `default`, `software-engineer`, `offsec`, `qa-tester`, `technical-writer`, `prose-writer`, `prose-editor`, `researcher` | Covers the spectrum operator named. Code-reviewer / architect / security-reviewer / data-scientist / devops are v2 candidates. |
| Q7 | Naming for the writing-domain personas | `prose-writer` (long-form: journalism, books, blogs) and `prose-editor` (publishing-style developmental + copy edit). | Pairs cleanly; "prose-" is unambiguous against `technical-writer` (docs / code-adjacent). |
| Q8 | Prompt size | Ship full prompts (~6–10 KB each), not condensed essentials. | Maximum behavioural conditioning; the user pays the prompt-size tax once per turn anyway. |
| Q9 | LLM-invoke ABI | **Break** the existing wire — single user, no compat tax. New shape: `stado_llm_invoke(args_json, out, out_max)` with `{prompt, persona?, model?, system?, max_tokens?, temperature?}`. | Cleaner long-term than `_v2` cohabiting. |
| Q10 | MCP exposure | New native `llm.invoke` tool registered in `BuildDefaultRegistry` so MCP clients hitting `stado mcp-server` see it. agent.spawn already in the registry; gains a `persona` arg. | Operators using stado as an LLM proxy from Claude Desktop / Zed / etc. can pick a persona per call. |
| Q11 | Default persona on entry | Read `[defaults].persona` from config; `[ ]` = `default`. Per-call surface flags override. | One source of truth + per-call escape hatch. |
| Q12 | Persona-aware delegation | The active persona's `collaborators:` frontmatter list is injected into the system prompt as "Personas you can spawn via agent.spawn." | Encourages writer→editor / engineer→qa-tester delegation without code changes. |
| Q13 | Reload | `/persona reload` re-reads from disk. Useful when iterating on a draft. | First-class authoring loop. |

## Frontmatter schema

```yaml
---
name: software-engineer
title: Software Engineer
description: Pragmatic builder; specs over tasks; small commits; iterate-don't-replace
inherits: default                 # optional — load named base first, then this body
collaborators: [code-reviewer, qa-tester]   # optional — surfaced for delegation
recommended_tools: [bash, edit, grep, glob, read, write]   # optional — hint, not enforced
recommended_plugins: []           # optional — hint, not enforced
version: 1                        # bumped when the body changes meaningfully
---
```

The body is plain markdown — the operating manual.

## Surfaces that take a persona

Universal rule: any code path that builds an `agent.TurnRequest`
honours this resolution order:

1. **Per-call override** — CLI flag, tool arg, MCP arg, ACP request
   field, JSON-RPC payload
2. **Session-active persona** — set by `/persona` in TUI; persists
   per session
3. **`[defaults].persona`** from config.toml
4. **Bundled `default`** as final fallback

| Surface | How |
|---|---|
| `stado` (TUI) | `/persona` slash command + `[defaults].persona`; status-line shows active |
| `stado run` | `--persona <name>` flag |
| `stado mcp-server` | `--persona` flag pins for the server's lifetime; clients can override per-call via tool arg |
| `stado headless` | `persona` field on the JSON-RPC turn-request payload |
| `stado acp` | `persona` in the ACP request envelope |
| `agent.spawn` | optional `persona` arg in the tool schema; default = inherit from parent |
| `stado_llm_invoke` host import | `persona` field in the JSON args |
| `llm.invoke` (new native tool, MCP-exposed) | `persona` arg in the tool schema |

## ABI changes

### `stado_llm_invoke` rewrite

Old:
```
stado_llm_invoke(prompt_ptr, prompt_len, out_ptr, out_max) → i32
```

New:
```
stado_llm_invoke(args_ptr, args_len, out_ptr, out_max) → i32

args: {
  "prompt": "...",
  "persona": "editor",          // optional — defaults to caller's active persona
  "model": "claude-sonnet-4-6", // optional — defaults to session model
  "system": "...",              // optional — additional system prompt content
  "max_tokens": 2048,           // optional
  "temperature": 0.7            // optional
}
```

Capability gate unchanged: `llm:invoke[:budget]`. Persona switch
doesn't open new attack surface.

### `agent.spawn` schema

Adds optional `persona` to the args schema. Default: inherit
parent's active persona. The bundled `agent.wasm` plugin gets a tool
schema update; the host-side `AgentSpawnRequest` struct gains the
field.

### New native tool: `llm.invoke`

Registered in `BuildDefaultRegistry`, exposed via the `mcp-server`
tool surface automatically. Schema:

```json
{
  "name": "llm.invoke",
  "description": "Run a single LLM completion against the active session's provider. Optionally select a persona for the system prompt.",
  "parameters": {
    "type": "object",
    "required": ["prompt"],
    "properties": {
      "prompt": {"type": "string"},
      "persona": {"type": "string", "description": "Persona name; defaults to session-active or [defaults].persona"},
      "model": {"type": "string"},
      "system": {"type": "string", "description": "Extra system content appended to the persona body"},
      "max_tokens": {"type": "integer"},
      "temperature": {"type": "number"}
    }
  }
}
```

Implementation calls into the existing `SessionBridge.InvokeLLM`
path with persona resolution applied.

## Architecture

### `internal/personas/` package

```go
package personas

type Persona struct {
    Name             string
    Title            string
    Description      string
    Inherits         string
    Collaborators    []string
    RecommendedTools []string
    Version          int
    Body             string  // the assembled markdown after inheritance merge
    SourcePath       string  // for debugging — empty for bundled
}

// Load resolves a persona by name with the standard order
// (cwd → user → bundled). Inheritance is resolved during Load
// so the returned Body is the assembled prompt.
func Load(name, cwd, configDir string) (*Persona, error)

// List returns all personas visible to the resolver, deduped
// (project shadows user shadows bundled). Each entry's
// SourcePath shows where it came from.
func List(cwd, configDir string) ([]Persona, error)

// Names returns just the resolvable names (sorted, deduped).
func Names(cwd, configDir string) []string
```

Bundled personas live under `internal/personas/library/*.md`
embedded via `embed.FS`. The package builds the master index at
init and serves from it.

### TurnRequest assembly

Every entry point that builds an `agent.TurnRequest` calls a single
helper:

```go
// AssembleSystem builds the system prompt for a turn. Order:
//   persona body
//   project AGENTS.md / CLAUDE.md
//   memory context (if any)
//   per-call extra (caller-supplied)
func AssembleSystem(p *personas.Persona, projectInstructions, memoryCtx, extra string) string
```

Centralised here so persona resolution + AGENTS.md layering happens
once and identically across TUI, headless, ACP, run, mcp-server,
agent.spawn, llm.invoke.

## Risk and self-critique

- **Prompt size** — 8 personas × ~6–10 KB. The user pays only the
  active one's tokens per turn, so this is fine. The disk weight is
  ~50 KB embedded; trivial.
- **Persona drift across versions** — bumping a bundled persona's
  body changes behaviour for users who didn't ask. Mitigation:
  `version` field in frontmatter; CHANGELOG calls it out; users
  who pinned behaviour can copy to `~/.stado/personas/` for
  stability.
- **Collaborator suggestions vs reality** — the frontmatter says a
  persona can spawn `prose-editor`, but the running stado may not
  have that persona installed (operator deleted it, etc.). The
  agent will discover at spawn time. We could pre-validate; not
  worth the friction for v1.
- **Inheritance loops** — `default` inherits `default` would loop
  forever. Resolver tracks visited names and errors on cycles.
- **Per-call override vs session-active** — if the user types
  `/persona writer`, then a tool spawns a sub-agent without an
  explicit persona, what does the sub-agent see? Answer: it
  inherits the session-active persona. Operator-explicit beats
  default; sub-agents inherit unless overridden.

## Done definition

- `internal/personas/` package with Load + List + Names
- 8 bundled persona files under `internal/personas/library/` (full
  prompts; default written last)
- `[defaults].persona` config field
- `--persona` flag on `stado run` and `stado mcp-server`
- `/persona` slash + `personapicker` package
- Status-line indicator
- `stado_llm_invoke` ABI rewrite with persona arg in JSON
- `agent.spawn` schema gains `persona` arg
- New `llm.invoke` native tool registered in `BuildDefaultRegistry`,
  visible via `mcp-server`
- Headless + ACP request envelopes carry `persona`
- Tests: persona load + resolution + inheritance + cycle detection;
  TurnRequest assembly; CLI flag round-trip; slash command
- Docs: `docs/features/personas.md` end-to-end; tui.md keybinds
  table updated; abi-reference.md mentions the new `stado_llm_invoke`
  shape
- CHANGELOG entry for v0.44.0
