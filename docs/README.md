# stado — docs

Per-command + per-feature guides. Each file covers the **why** (design
choice, trade-off) alongside the **how** (invocation, config, flags).
Skim the TOC, jump to what you need.

Shorter forms live in:
- `stado --help` — one-line summaries at the CLI
- [README.md](../README.md) — top-level intro + install + a section on
  [configuring tools & sandboxing](../README.md#configuring-tools--sandboxing)
- [plugins/README.md](../plugins/README.md) — bundled/default vs
  example plugin catalog
- [DESIGN.md](../DESIGN.md) — as-built architecture
- [eps/README.md](eps/README.md) — retroactive design records and EP index
- [PLAN.md](../PLAN.md) — phased roadmap

## Command guides

| Command | Guide | One-liner |
|---------|-------|-----------|
| `stado` (TUI) | [commands/tui.md](commands/tui.md) | Interactive chat + tool loop |
| `stado run` | [commands/run.md](commands/run.md) | Non-interactive single-shot prompt |
| `stado session` | [commands/session.md](commands/session.md) | Create/list/fork/land agent sessions |
| `stado agents` | [commands/agents.md](commands/agents.md) | Parallel agent view + kill |
| `stado audit` | [commands/audit.md](commands/audit.md) | Verify signed tree/trace refs |
| `stado doctor` | [commands/doctor.md](commands/doctor.md) | Environment health-check |
| `stado config` | [commands/config.md](commands/config.md) | Edit / show effective config |
| `stado plugin` | [commands/plugin.md](commands/plugin.md) | Trust, verify, install, scaffold, and run WASM plugins |
| `stado memory` | [commands/memory.md](commands/memory.md) | Review plugin-proposed persistent memories |
| `stado learning` | [commands/learning.md](commands/learning.md) | Propose reviewable operational lessons |
| `stado stats` | [commands/stats.md](commands/stats.md) | Cost + usage dashboard |
| `stado headless` | [commands/headless.md](commands/headless.md) | JSON-RPC daemon |
| `stado acp` | [commands/acp.md](commands/acp.md) | Zed Agent-Client-Protocol server |
| `stado mcp-server` | [commands/mcp-server.md](commands/mcp-server.md) | Expose tools via MCP v1 |
| `stado verify` | [commands/verify.md](commands/verify.md) | Print build provenance |
| `stado self-update` | [commands/self-update.md](commands/self-update.md) | Download + install the latest release |
| `stado version` / `config-path` / `completion` | [commands/misc.md](commands/misc.md) | Small generated or informational commands |

## Features

| Feature | Guide | Why it exists |
|---------|-------|---------------|
| AGENTS.md / CLAUDE.md | [features/instructions.md](features/instructions.md) | Project-level system prompt, auto-loaded |
| `[budget]` cost gate | [features/budget.md](features/budget.md) | Warn + hard-cap on cumulative $ spend |
| `.stado/skills/*.md` | [features/skills.md](features/skills.md) | Reusable prompt fragments, TUI + CLI |
| `[hooks]` lifecycle | [features/hooks.md](features/hooks.md) | Shell hook on completed TUI, CLI, and headless turns |
| Slash commands | [features/slash-commands.md](features/slash-commands.md) | Every TUI `/` command, grouped |
| Shared tasks | [features/tasks.md](features/tasks.md) | User and agent task store, TUI browser, and model tool |
| Sandboxing | [features/sandboxing.md](features/sandboxing.md) | How Landlock + bwrap + seccomp interact |
| Context management | [features/context.md](features/context.md) | Token counting, soft/hard thresholds, compaction |
| Plugin authoring | [features/plugin-authoring.md](features/plugin-authoring.md) | First-time-author walkthrough — scaffold → sign → trust → install → run + `--workdir` / `--with-tool-host` / `[tools].overrides` patterns |
| Personas | [features/personas.md](features/personas.md) | Operating-manual personas (software-engineer, qa-tester, prose-writer, etc.) — bundled set, custom additions, resolution order, `agent.spawn` delegation |
| Plugin ABI | [plugins/abi-reference.md](plugins/abi-reference.md) | Systematic ABI reference — memory model, return-code conventions, typed handles, JSON envelope, capability vocabulary, manifest schema, lifecycle |
| Plugin host imports | [plugins/host-imports.md](plugins/host-imports.md) | Function-by-function reference for every wasm host import (~70 entries, grouped by tier) |
| Session refs | Covered in [commands/session.md](commands/session.md) and [DESIGN.md](../DESIGN.md) | Dual-ref (tree + trace) + turn tags |
| Enhancement Proposals | [eps/README.md](eps/README.md) | Durable design records for major architectural decisions |

## Reports

- [opencode TUI UAT report — 2026-04-24](reports/2026-04-24-opencode-tui-uat.md)
- [v0.26.0 release notes — 2026-05-04](reports/2026-05-04-v0.26.0-release-notes.md)
- [First-time-user feedback — Bazzite + LMStudio — 2026-05-04](reports/2026-05-04-first-time-user-bazzite.md)

## Status

`stado <cmd> --help` remains authoritative for exact flag spelling, but
every shipped top-level command now has a guide above. The shape each
guide follows:

1. **What it does** — one paragraph
2. **Why it exists** — design rationale, what it replaces or complements
3. **How to use it** — invocation, common flags, worked examples
4. **Config** — any `config.toml` sections that apply
5. **Gotchas** — known rough edges, workarounds, deferred work
