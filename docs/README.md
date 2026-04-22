# stado — docs

Per-command + per-feature guides. Each file covers the **why** (design
choice, trade-off) alongside the **how** (invocation, config, flags).
Skim the TOC, jump to what you need.

Shorter forms live in:
- `stado --help` — one-line summaries at the CLI
- [README.md](../README.md) — top-level intro + install + a section on
  [configuring tools & sandboxing](../README.md#configuring-tools--sandboxing)
- [DESIGN.md](../DESIGN.md) — as-built architecture
- [PLAN.md](../PLAN.md) — phased roadmap

## Commands

| Command | Guide | One-liner |
|---------|-------|-----------|
| `stado` (TUI) | [commands/tui.md](commands/tui.md) | Interactive chat + tool loop |
| `stado run` | [commands/run.md](commands/run.md) | Non-interactive single-shot prompt |
| `stado session` | [commands/session.md](commands/session.md) | Create/list/fork/land agent sessions |
| `stado agents` | [commands/agents.md](commands/agents.md) | Parallel agent view + kill |
| `stado audit` | [commands/audit.md](commands/audit.md) | Verify signed tree/trace refs |
| `stado stats` | [commands/stats.md](commands/stats.md) | Cost + usage dashboard |
| `stado doctor` | [commands/doctor.md](commands/doctor.md) | Environment health-check |
| `stado config` | [commands/config.md](commands/config.md) | Edit / show effective config |
| `stado plugin` | [commands/plugin.md](commands/plugin.md) | Install/verify/run wasm plugins |
| `stado headless` | [commands/headless.md](commands/headless.md) | JSON-RPC daemon |
| `stado acp` | [commands/acp.md](commands/acp.md) | Zed Agent-Client-Protocol server |
| `stado mcp-server` | [commands/mcp-server.md](commands/mcp-server.md) | Expose tools via MCP v1 |
| `stado verify` | [commands/verify.md](commands/verify.md) | Print build provenance |
| `stado self-update` | [commands/self-update.md](commands/self-update.md) | Download + verify latest release |

## Features

| Feature | Guide | Why it exists |
|---------|-------|---------------|
| AGENTS.md / CLAUDE.md | [features/instructions.md](features/instructions.md) | Project-level system prompt, auto-loaded |
| `[budget]` cost gate | [features/budget.md](features/budget.md) | Warn + hard-cap on cumulative $ spend |
| `.stado/skills/*.md` | [features/skills.md](features/skills.md) | Reusable prompt fragments, TUI + CLI |
| `[hooks]` lifecycle | [features/hooks.md](features/hooks.md) | Shell hook on turn boundaries |
| Slash commands | [features/slash-commands.md](features/slash-commands.md) | Every TUI `/` command, grouped |
| Sandboxing | [features/sandboxing.md](features/sandboxing.md) | How Landlock + bwrap + seccomp interact |
| Context management | [features/context.md](features/context.md) | Token counting, soft/hard thresholds, compaction |
| Session refs | [features/session-refs.md](features/session-refs.md) | Dual-ref (tree + trace) + turn tags |

## Status

Per-command guides are being filled in incrementally. Pages missing
from the tree above are TODO — until they land, `stado <cmd> --help`
is authoritative. A completed guide is linked; an unlinked row is a
stub to write.

Contributions welcome — a doc PR that documents one command is a
great first patch. The shape each guide follows:

1. **What it does** — one paragraph
2. **Why it exists** — design rationale, what it replaces or complements
3. **How to use it** — invocation, common flags, worked examples
4. **Config** — any `config.toml` sections that apply
5. **Gotchas** — known rough edges, workarounds, deferred work
