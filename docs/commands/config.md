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
- What `[approvals]` mode applies — prompt, allowlist?
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
stado config show --raw        # the TOML as loaded, pre-resolution
```

Example output (human-readable):

```
[loaded]
  path         /home/user/.config/stado/config.toml

[defaults]
  provider     anthropic
  model        claude-sonnet-4-6

[approvals]
  mode         allowlist
  allowlist    read, grep, ripgrep, ast_grep, glob

[context]
  soft         0.70
  hard         0.90

[budget]
  warn         1.00
  hard         unset

[hooks]
  post_turn    notify-send stado 'turn done'

[plugins]
  background   (none)
  crl          https://…/crl.json

[agent]
  thinking             auto
  thinking_budget      16384
```

A missing file is not an error — the output says `path  (none)` and
the rest reflects env vars + defaults.

### Init

```sh
stado config init                          # write to XDG default
stado config init --path /path/to/cfg.toml # custom location
stado config init --force                  # overwrite an existing file
```

Default path: `$XDG_CONFIG_HOME/stado/config.toml`
(typically `~/.config/stado/config.toml`). The template is written
with every section commented; uncomment and edit what you need.

Refuses to overwrite an existing file without `--force` so you can
`config init` idempotently in dotfile-setup scripts without fearing
accidental clobber.

## Config resolution order

From highest-priority to lowest:

1. `STADO_*` env vars (dotted-key form: `STADO_DEFAULTS_MODEL`,
   `STADO_APPROVALS_MODE`, …)
2. `--config <path>` flag if the invoked subcommand supports it
3. `$XDG_CONFIG_HOME/stado/config.toml`
4. `~/.config/stado/config.toml` (XDG fallback on non-XDG systems)
5. Compiled-in defaults

Lower layers are partial — a disk file with just
`[defaults].provider = "openai"` leaves every other knob at the
default.

## Gotchas

- **`STADO_*` env vars override the file silently.** `config show`
  displays the merged values; use `--raw` to see the unmerged disk
  TOML.
- **Unknown keys are ignored** (koanf's default). A typo in a section
  name won't error — it just won't take effect. `config show` will
  display the ignored key in the raw output; double-check there if
  a setting isn't taking.
- **Init doesn't migrate.** If you already have a config.toml and you
  want to add a newly-supported section, `config init --force`
  overwrites the whole file. Manual migration is safer.

## See also

- [commands/doctor.md](./doctor.md) — "is my env set up" check.
- [features/budget.md](../features/budget.md) — `[budget]` details.
- [features/sandboxing.md](../features/sandboxing.md) — `[tools]` +
  sandbox backends wired via config.
