---
ep: 35
title: Project-local .stado/ directory
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-05-05
see-also: [27, 28, 6, 9, 37]
extended-by: [37]
history:
  - date: 2026-05-05
    note: Implemented — config overlay + .stado/AGENTS.md + plugin search path
---

## Summary

A `.stado/` subdirectory at the project root (or any ancestor directory)
lets teams commit stado configuration alongside their code. Three
artefacts are read from it:

| Artefact | Purpose |
|----------|---------|
| `.stado/config.toml` | Per-project config overlay (same schema as user config) |
| `.stado/AGENTS.md` | Per-project agent instructions (stado-specific; doesn't conflict with cross-vendor AGENTS.md) |
| `.stado/plugins/` | Per-project plugin install directory (supplements the global state-dir plugins) |

## Motivation

Without project-local config, teams sharing a repo must either:

- Commit nothing and rediscover settings every clone (friction)
- Commit a wrapper script that exports `STADO_*` env vars (fragile, not idiomatic)
- Ask every contributor to edit `~/.config/stado/config.toml` (breaks shared
  defaults, makes onboarding hard)

The `.stado/` pattern follows `.editorconfig`, `.vscode/settings.json`, and
`.claude/settings.json`: a committed, human-readable file that travels with
the repository and requires no per-user setup beyond cloning.

**Security posture.** `.stado/config.toml` is treated the same as a
`Makefile` or `.envrc` in the repo — operator-controlled content the user
has already consented to run by cloning. It can't exec code (it's TOML);
it can change model/provider/tool defaults. No special sandbox is needed
beyond what the user's `~/.config/stado/config.toml` already allows.

## Spec

### Discovery

Walk from the current working directory upward to the filesystem root.
The **first** directory that contains a `.stado/` subdirectory wins.
This matches git's own discovery model — the nearest `.stado/` to cwd
takes effect, not all of them.

### 1. Config overlay (`.stado/config.toml`)

**Load order (ascending precedence):**

1. Built-in defaults
2. `~/.config/stado/config.toml` (user)
3. `.stado/config.toml` (project) ← new
4. `STADO_*` env vars

Same koanf merge semantics as the existing layers: later layers win
key-by-key within each TOML table. A project that sets `defaults.model`
overrides the user's model; a user who sets `[tui].thinking_display` is
not affected if the project doesn't mention it.

**Example** (what `htb-writeups/.stado/config.toml` would look like):

```toml
[defaults]
model = "claude-sonnet-4-6"  # faster for engagement loop

[tools]
overrides = { webfetch = "webfetch-cached-0.1.0" }

[plugins]
search_path = [".stado/plugins"]
```

### 2. AGENTS.md (`.stado/AGENTS.md`)

Inserted into the `instructions.Load` walk between `AGENTS.md` and
`CLAUDE.md` at each directory level:

```
For each dir walking upward:
  1. <dir>/AGENTS.md            (cross-vendor, wins over stado-specific)
  2. <dir>/.stado/AGENTS.md     (stado-specific, committed with repo)  ← new
  3. <dir>/CLAUDE.md            (Claude Code compat, lowest priority)
```

Rationale: `.stado/AGENTS.md` is preferred over `CLAUDE.md` (it's
explicitly stado-targeted) but defers to a top-level `AGENTS.md` (which
is the cross-vendor canonical, likely used by multiple tools). This avoids
surprising other tools that read `AGENTS.md` with stado-specific content.

### 3. Plugin search path (`.stado/plugins/`)

When `.stado/` is discovered, `<project-root>/.stado/plugins/` is
appended to the plugin search path. The TUI and headless surfaces search
this directory in addition to `~/.local/share/stado/plugins/`.

Plugin install: `stado plugin install --local .` installs into
`.stado/plugins/` rather than the global state dir. Without `--local`,
install goes to the global dir as before.

Trust: the `.stado/plugins/` directory does **not** auto-trust plugins
found there. All plugins still require `stado plugin trust <pubkey>` once.
The trust store remains user-local (`~/.local/share/stado/`), not
project-local, so a fresh clone never auto-executes untrusted wasm.

## Non-goals

- Recursive / cascading `.stado/` dirs (only the nearest is loaded).
- Per-directory config within a project (one `.stado/` per project root).
- Auto-trust for `.stado/plugins/` (trust remains user-local).
- `.stado/hooks/` — use `[hooks]` in `.stado/config.toml` instead.

## Implementation notes

Three touch points, all small:

1. `internal/config/config.go` — `Load()`: after reading user config,
   call `findProjectStadoDir(cwd)`, then `k.Load()` the project
   `config.toml` if present.

2. `internal/instructions/instructions.go` — `Names` slice and the
   walk: check `.stado/AGENTS.md` at each directory level as a
   second candidate between `AGENTS.md` and `CLAUDE.md`.

3. Plugin search: `internal/tui/model_plugins.go` + headless executor
   read the plugin root from `cfg.StateDir()/plugins`. Extend to also
   search the project `.stado/plugins/` dir when available. Expose via
   a new `Config.ProjectPluginsDir() string` helper.
