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
| `stado doctor` | [commands/doctor.md](commands/doctor.md) | Environment health-check |
| `stado config` | [commands/config.md](commands/config.md) | Edit / show effective config |
| `stado plugin` | [commands/plugin.md](commands/plugin.md) | Trust, verify, install, scaffold, and run WASM plugins |

Other shipped commands do not have standalone guides yet. Until they do,
`stado <command> --help` is authoritative:

| Command | Guide | One-liner |
|---------|-------|-----------|
| `stado agents` | _(guide pending)_ | Parallel agent view + kill |
| `stado audit` | _(guide pending)_ | Verify signed tree/trace refs |
| `stado stats` | _(guide pending)_ | Cost + usage dashboard |
| `stado headless` | _(guide pending)_ | JSON-RPC daemon |
| `stado acp` | _(guide pending)_ | Zed Agent-Client-Protocol server |
| `stado mcp-server` | _(guide pending)_ | Expose tools via MCP v1 |
| `stado verify` | _(guide pending)_ | Print build provenance |
| `stado self-update` | _(guide pending)_ | Download + install the latest release |
| `stado config-path` | _(guide pending)_ | Print the path to the config file |
| `stado completion` | _(guide pending)_ | Generate the autocompletion script for the specified shell |
| `stado version` | _(guide pending)_ | Print stado version |

## Features

| Feature | Guide | Why it exists |
|---------|-------|---------------|
| AGENTS.md / CLAUDE.md | [features/instructions.md](features/instructions.md) | Project-level system prompt, auto-loaded |
| `[budget]` cost gate | [features/budget.md](features/budget.md) | Warn + hard-cap on cumulative $ spend |
| `.stado/skills/*.md` | [features/skills.md](features/skills.md) | Reusable prompt fragments, TUI + CLI |
| `[hooks]` lifecycle | [features/hooks.md](features/hooks.md) | Shell hook on completed TUI, CLI, and headless turns |
| Slash commands | [features/slash-commands.md](features/slash-commands.md) | Every TUI `/` command, grouped |
| Sandboxing | [features/sandboxing.md](features/sandboxing.md) | How Landlock + bwrap + seccomp interact |
| Context management | [features/context.md](features/context.md) | Token counting, soft/hard thresholds, compaction |
| Session refs | Covered in [commands/session.md](commands/session.md) and [DESIGN.md](../DESIGN.md) | Dual-ref (tree + trace) + turn tags |
| Enhancement Proposals | [eps/README.md](eps/README.md) | Durable design records for major architectural decisions |

## Status

Guide coverage is incremental. Linked rows above exist today; rows
marked `_(guide pending)_` do not. Until those guides land,
`stado <cmd> --help` is authoritative.

Contributions welcome — a doc PR that documents one command is a
great first patch. The shape each guide follows:

1. **What it does** — one paragraph
2. **Why it exists** — design rationale, what it replaces or complements
3. **How to use it** — invocation, common flags, worked examples
4. **Config** — any `config.toml` sections that apply
5. **Gotchas** — known rough edges, workarounds, deferred work
