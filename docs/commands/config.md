# `stado config`

Inspect and seed the config.toml that drives every other subcommand.

Two forms:

```sh
stado config show   # print the resolved effective config
stado config init   # write a commented template to the default path
```

## What it does

Stado's config is layered — user file, optional project overlay (with
security stripping), then `STADO_*` env vars, then compiled defaults —
using [koanf][1]. `stado config show` runs the same resolver the TUI +
headless + run-once subcommands use, then dumps the merged result. `stado config init` seeds a first-time
install with a documented template.

[1]: https://github.com/knadh/koanf

The effective config answers:

- Which provider will stado dial (`[defaults].provider`)?
- Which model was pinned, if any (`[defaults].model`)?
- Which `[agent].system_prompt_path` is used for the editable system
  prompt template?
- Which bundled theme and thinking display mode the TUI uses
  (`[tui].theme`, `[tui].thinking_display`)?
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

[approvals]
  mode         prompt

[agent]
  thinking                 auto
  thinking_budget_tokens   16384
  system_prompt_path       /home/user/.config/stado/system-prompt.md

[tui]
  theme                          (unset — uses theme.toml or bundled default)
  thinking_display               preview
  tool_display                   preview
  tool_output_collapsed_height   8

[memory]
  enabled        true
  max_items      8
  budget_tokens  800

[context]
  soft_threshold   0.70
  hard_threshold   0.90

[budget]
  warn_usd           (unset — no warn pill)
  hard_usd           (unset — no hard gate)
  warn_tokens        (unset)
  hard_tokens        (unset)
  warn_input_tokens  (unset)
  hard_input_tokens  (unset)
  warn_output_tokens (unset)
  hard_output_tokens (unset)

[verify]
  commands    (unset - disabled)
  max_rounds  3 when commands are configured
  strict      false
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

### Provider catalogue and setup

```sh
stado config providers
stado config providers setup <name>
stado config providers setup <name> --write
```

The catalogue groups native-SDK, OpenAI-compatible cloud, and local providers
and reports only whether their conventional credential environment variable is
present. `setup` prints provider-specific instructions. `--write` adds the
corresponding `[inference.presets.<name>]` block for compatible providers;
`--force` is required to replace an existing block. `--api-key` prints an
export command for the operator to copy but never writes the key into
`config.toml`.

## Config resolution order

From highest-priority to lowest:

1. `STADO_*` env vars (dotted-key form: `STADO_DEFAULTS_MODEL`,
   `STADO_CONTEXT_SOFT_THRESHOLD`, …)
2. `$XDG_CONFIG_HOME/stado/config.toml` (user config)
3. `{repo}/.stado/config.toml` (project overlay — see below)
4. Compiled-in defaults

Lower layers are partial — a disk file with just
`[defaults].provider = "openai"` leaves every other knob at the
default. For **allowed** keys, project values override user values;
env always wins over both.

## Project overlay (`.stado/config.toml`)

When the current working directory is inside a repo that has
`.stado/config.toml`, stado merges it after user config. Repository
contents are treated as **untrusted** (EP-0044): operator-domain keys
are **stripped** before merge. Stripping is case-insensitive; the
first hit per stripped prefix prints a stderr warning:

```
stado: ignoring "…" from project .stado/config.toml — not honored from a repo (security). Set it in your user/global config instead.
```

**Always stripped from project config:**

| Key / table | Why |
|-------------|-----|
| `[hooks]`, `[aliases]` | Arbitrary exec / slash-command expansion |
| `[verify]` | Automatic command execution when the model finishes |
| `[keymap]` | Could neutralize Esc/Ctrl+G interrupt or swap input model |
| `[defaults].persona`, `[defaults].allow_project_persona` | System-prompt injection; repo can't self-enable persona opt-in |
| `[agent].system_prompt_path` | Repo-controlled provider system prompt |
| `[plugins].background`, `[plugins].allow_project_plugins` | Wasm autostart; repo can't self-enable project-plugin autoload |
| `[acp]` (whole table) | MCP backdoor, `inherit_env`, turn-limit bypass |
| `[mcp.providers]`, `[mcp.servers]` | Secret passthrough + repo-declared subprocess servers |
| `[tui.sidebar]`, `[tui.footer]` | Hiding safety chrome |
| `[lsp].auto_diagnostics` | Repo can't re-enable unsandboxed LSP spawns |
| `[sandbox]`, `[runtime]`, `[inference]` | Weaken containment, native↔wasm swap, API-key exfil endpoints |

**Still honored from project config:** e.g. `[defaults].model` /
`.provider` (select among user-defined providers), `[tools].*`,
`[context].*`, `[memory].*`, `[budget].*`, `[approvals].*`.

`[verify]` is intentionally not project-overridable. Configure it in the user
file only; commands run automatically and therefore cross the repo-to-host
trust boundary even though execution remains inside the session sandbox.

### Operator opt-ins (user config only)

Set these in **user** config when you trust a specific repo:

```toml
[defaults]
allow_project_persona = true   # honor .stado/personas/*.md (default false)

[plugins]
allow_project_plugins = true   # autoload .stado/plugins/ (default false)

[lsp]
auto_diagnostics = true        # auto-spawn LSP after mutating edits (default false)
```

A repo cannot set these keys for itself — they are in the strip list.

See [EP-0044](../eps/0044-repo-config-trust-boundary.md) for the full design.

## Gotchas

- **`STADO_*` env vars override the file silently.** `config show`
  displays the merged values. Inspect `~/.config/stado/config.toml`
  directly when you need the unmerged disk source.
- **Project config is not a full schema copy.** If a knob is in the
  strip list, committing it to `.stado/config.toml` has no effect —
  move it to user config.
- **`stado config show` reflects the merged effective config** after
  stripping; keys that were stripped from the project file won't
  appear as if the repo set them.
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
