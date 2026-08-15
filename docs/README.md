# stado — docs

Concise command and feature guides live alongside longer articles about design
choices and failure modes. Use the guides when you need an invocation, option,
or configuration detail; use the articles when you want the reasoning behind a
feature.

Shorter forms live in:
- `stado --help` — one-line summaries at the CLI
- [README.md](../README.md) — top-level intro + install + a section on
  [configuring tools & sandboxing](../README.md#configuring-tools--sandboxing)
- [plugins/README.md](../plugins/README.md) — bundled/default vs
  example plugin catalog
- [DESIGN.md](../DESIGN.md) — as-built architecture
- [eps/README.md](eps/README.md) — retroactive design records and EP index
- [PLAN.md](../PLAN.md) — phased roadmap

## Articles

| Article | Focus |
|---------|-------|
| [The Loop Needs a Witness](articles/supervise-in-practice.md) | Why supervised work separates the worker, watchdog, verifier, application policy, and broker-enforced authority |
| [What Survives the Window](articles/adaptive-context.md) | Why useful history needs governed persistence, retrieval, and learning rather than a larger prompt |

## Command guides

| Command | Guide | One-liner |
|---------|-------|-----------|
| `stado` (TUI) | [commands/tui.md](commands/tui.md) | Interactive chat + tool loop |
| `stado run` | [commands/run.md](commands/run.md) | Non-interactive single-shot prompt |
| `stado session` | [commands/session.md](commands/session.md) | Create/list/fork/land/kill agent sessions |
| `stado audit` | [commands/audit.md](commands/audit.md) | Verify signed tree/trace refs |
| `stado doctor` | [commands/doctor.md](commands/doctor.md) | Environment health-check |
| `stado config` | [commands/config.md](commands/config.md) | Edit / show effective config |
| `stado plugin` | [commands/plugin.md](commands/plugin.md) | Trust, verify, install, sign, and scaffold WASM plugins |
| `/memory` application | [commands/memory.md](commands/memory.md) | Future signed TUI lifecycle command; staged source is unsigned/unpublished |
| `/learn` application | [commands/learning.md](commands/learning.md) | Future candidate-only TUI review; no native CLI fallback |
| `stado stats` | [commands/stats.md](commands/stats.md) | Cost + usage dashboard |
| `stado run --headless` | [commands/headless.md](commands/headless.md) | JSON-RPC daemon |
| `stado acp` | [commands/acp.md](commands/acp.md) | Zed Agent-Client-Protocol server |
| `stado mcp-server` | [commands/mcp-server.md](commands/mcp-server.md) | Expose tools via MCP v1 |
| `stado verify` | [commands/verify.md](commands/verify.md) | Print build provenance |
| `stado self-update` | [commands/self-update.md](commands/self-update.md) | Download + install the latest release |
| `stado version` / `stado config-path` / `stado completion` | [commands/misc.md](commands/misc.md) | Small generated or informational commands |
| `stado auth` / `stado secrets` | [commands/operations.md](commands/operations.md) | Provider credential references and operator plugin secrets |
| `stado daemon` / `stado tool` | [commands/operations.md](commands/operations.md) | Stateful tool host, inspection, policy, and direct invocation |
| `stado harness` / `stado integrations` | [commands/operations.md](commands/operations.md) | Security harness setup and external-agent discovery |
| `stado schedule` / `stado usage` | [commands/operations.md](commands/operations.md) | Persistent scheduled runs and audit-derived usage reports |
| `stado install` / `stado uninstall` | [commands/operations.md](commands/operations.md) | User-local binary installation lifecycle |

## Features

| Feature | Guide | Why it exists |
|---------|-------|---------------|
| All tools as WASM | [features/no-internal-tools.md](features/no-internal-tools.md) | Why every model-facing tool is a plugin |
| AGENTS.md / CLAUDE.md | [features/instructions.md](features/instructions.md) | Project-level system prompt, auto-loaded |
| `[budget]` token gate | [features/budget.md](features/budget.md) | Warn + hard caps on cumulative token usage |
| `.stado/skills/*.md` | [features/skills.md](features/skills.md) | Reusable prompt fragments, TUI + CLI |
| `[hooks]` shell hook | [features/hooks.md](features/hooks.md) | Fire-and-forget shell hook on completed TUI, CLI, and headless turns |
| `[[hooks.lifecycle]]` | [features/lifecycle-hooks.md](features/lifecycle-hooks.md) | Scriptable Lua deny/mutate hooks at pre/post-tool + pre/post-llm + post-turn |
| Slash commands | [features/slash-commands.md](features/slash-commands.md) | Every TUI `/` command, grouped |
| Tasks application (release pending) | [features/tasks.md](features/tasks.md) | Explicit TUI lifecycle application with global broker artifacts and no native fallback |
| Supervised work (signed plugin) | [features/supervise.md](features/supervise.md) | Explicitly installed and enabled official WASM quality gate; signed `supervise/v0.1.1` is published for stado 0.80.0 and newer |
| Sandboxing | [features/sandboxing.md](features/sandboxing.md) | How Landlock + bwrap + seccomp interact |
| Context management | [features/context.md](features/context.md) | Token counting, soft/hard thresholds, compaction |
| Plugin authoring | [features/plugin-authoring.md](features/plugin-authoring.md) | First-time-author walkthrough — scaffold → sign → trust → install → run + `--workdir` / `[tools].overrides` patterns |
| Personas | [features/personas.md](features/personas.md) | Operating-manual personas — bundled set, user additions, opt-in project personas (`allow_project_persona`), resolution order, `agent.spawn` delegation |
| Plugin ABI | [plugins/abi-reference.md](plugins/abi-reference.md) | Systematic ABI reference — memory model, return-code conventions, typed handles, JSON envelope, capability vocabulary, manifest schema, lifecycle |
| Plugin host imports | [plugins/host-imports.md](plugins/host-imports.md) | Function-by-function reference for every wasm host import (~70 entries, grouped by tier) |
| Session refs | Covered in [commands/session.md](commands/session.md) and [DESIGN.md](../DESIGN.md) | Dual-ref (tree + trace) + turn tags |
| Enhancement Proposals | [eps/README.md](eps/README.md) | Durable design records for major architectural decisions |

## Status

`stado <cmd> --help` remains authoritative for exact flag spelling, but
every shipped top-level command now has a guide above. Guides stay practical:
what the command or feature does, how to invoke and configure it, and the
important gotchas. Design arguments that need room to breathe belong in
[articles](articles/).
