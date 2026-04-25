# `stado config`

Inspect and seed the config.toml that drives every other subcommand.

Two forms:

```sh
stado config show   # print the resolved effective config
stado config init   # write a commented template to the default path
```

## What it does

Stado's config is layered — disk file, then `STADO_*` env vars, then
the runtime defaults — using [koanf][1]. `stado config show` runs the
same resolver the TUI + headless + run-once subcommands use, then
dumps the merged result. `stado config init` seeds a first-time
install with a documented template.

[1]: https://github.com/knadh/koanf

The effective config answers:

- Which provider will stado dial (`[defaults].provider`)?
- Which model was pinned, if any (`[defaults].model`)?
- Which `[agent].system_prompt_path` is used for the editable system
  prompt template?
- Which display mode the TUI uses for provider-native thinking blocks
  (`[tui].thinking_display`)?
- Whether `[memory]` prompt context is enabled, and its item/token caps.
- Which `[tools]` filter applies?
- What `[context]` soft/hard thresholds are active?
- What `[budget]` caps, if any, are set?
- Where is the config file stado actually read?

Useful any time behavior surprises you — a `STADO_*` env override is
the usual suspect.

## Why it exists

Three reasons:

1. **Debugging override stacks.** `STADO_DEFAULTS_MODEL=foo stado`
   will beat a disk-configured model. `config show` surfaces the
   merged result so you don't have to guess.
2. **Onboarding.** `stado config init` writes a template with every
   knob commented. Copy-paste discovery beats "read the koanf struct
   tags" every time.
3. **Scripting.** `config show --json | jq .Defaults.Provider` feeds
   smoke-tests, CI assertions, and repo-setup scripts without having
   to shell out to `stado doctor`.

## Usage

### Show

```sh
stado config show              # human-readable
stado config show --json       # jq-able
```

Example output (human-readable):

```
config file    /home/user/.config/stado/config.toml
state dir      /home/user/.local/share/stado
worktree dir   /home/user/.local/state/stado/worktrees

[defaults]
  provider     anthropic
  model        claude-sonnet-4-6

[agent]
  thinking                 auto
  thinking_budget_tokens   16384
  system_prompt_path       /home/user/.config/stado/system-prompt.md

[tui]
  thinking_display   show

[memory]
  enabled        false
  max_items      8
  budget_tokens  800

[context]
  soft_threshold   0.70
  hard_threshold   0.90

[budget]
  warn_usd   (unset — no warn pill)
  hard_usd   (unset — no hard gate)
```

A missing config file is not an error — the output notes that values
come from defaults + env.

### Init

```sh
stado config init                          # write to XDG default
stado config init --force                  # overwrite an existing file
```

Default path: `$XDG_CONFIG_HOME/stado/config.toml`
(typically `~/.config/stado/config.toml`). The template is written
with every section commented; uncomment and edit what you need.

On first config load, stado also creates
`$XDG_CONFIG_HOME/stado/system-prompt.md`. That file is the editable
system-prompt template used for every provider request. It receives
`{{ .Provider }}`, `{{ .Model }}`, and `{{ .ProjectInstructions }}`.
The compiled default follows the cairn governing principles and
workflow discipline while preserving stado's runtime identity.
If the default-path template still exactly matches a known generated
template from an older release, stado updates it automatically; edited
templates are left untouched.

Refuses to overwrite an existing file without `--force` so you can
`config init` idempotently in dotfile-setup scripts without fearing
accidental clobber.

## Config resolution order

From highest-priority to lowest:

1. `STADO_*` env vars (dotted-key form: `STADO_DEFAULTS_MODEL`,
   `STADO_CONTEXT_SOFT_THRESHOLD`, …)
2. `$XDG_CONFIG_HOME/stado/config.toml`
3. `~/.config/stado/config.toml` (XDG fallback on non-XDG systems)
4. Compiled-in defaults

Lower layers are partial — a disk file with just
`[defaults].provider = "openai"` leaves every other knob at the
default.

## Gotchas

- **`STADO_*` env vars override the file silently.** `config show`
  displays the merged values. Inspect `~/.config/stado/config.toml`
  directly when you need the unmerged disk source.
- **Unknown keys are ignored** (koanf's default). A typo in a section
  name won't error — it just won't take effect. Double-check the
  effective output if a setting is not taking.
- **Init doesn't migrate.** If you already have a config.toml and you
  want to add a newly-supported section, `config init --force`
  overwrites the whole file. Manual migration is safer.

## See also

- [commands/doctor.md](./doctor.md) — "is my env set up" check.
- [features/budget.md](../features/budget.md) — `[budget]` details.
- [features/sandboxing.md](../features/sandboxing.md) — `[tools]` +
  sandbox backends wired via config.
