# `.stado/skills/` — reusable prompts

Skills are Markdown prompts stored with a project or selected by a persona.
The operator can invoke them directly with `/skill:<name>`, a declared slash
shortcut, or `stado run --skill <name>`. Model-driven discovery is provided by
the explicitly installed official `skills` WASM plugin; native stado does not
append a skill listing to the system prompt or rewrite a plugin result into a
user message.

## File format

A flat skill is `.stado/skills/<name>.md`:

```markdown
---
name: refactor
description: Extract a function while preserving call sites
when_to_use: the user asks to simplify or separate repeated code
---
Find repeated code near the target and factor it into the narrowest helper.
```

A directory skill uses `.stado/skills/<name>/SKILL.md` and may carry scripts,
references, or templates beside it:

```text
.stado/skills/
  summarize/
    SKILL.md
    scripts/check.sh
```

The body may refer to `${STADO_SKILL_DIR}/scripts/check.sh`. Stado expands that
token inside the opened Markdown body. Supporting files are not automatically
read or executed; normal filesystem and process tools remain separately
capability- and sandbox-bound.

Supported frontmatter:

| Key | Meaning |
|---|---|
| `name` | Skill name; defaults to the flat filename or directory name |
| `description` | Primary model-search and operator-listing summary |
| `when_to_use` | Additional search/trigger context |
| `slash` | Bare TUI shortcut registered as `/<name>` |
| `disable-model-invocation: true` | Omit from the model-visible host resource catalog; operator gestures still work |
| `user-invocable: false` | Hide from `/skill` and slash shortcuts; model discovery may still use it |
| `allowed-tools` | Persona-only candidates for session surface activation; project declarations are inert |

Skills are plain text, not a macro language. Arguments, command substitution,
automatic includes, and dynamic shell preprocessing are not implemented.

## Resolution

Stado walks from the current directory toward the filesystem root and loads
each `.stado/skills/` directory. The nearest skill with a given name wins.
Persona `skills:` entries are then resolved relative to the persona file and
merged additively; a project-discovered name wins a collision.

Files are size-bounded, directory scans are entry-bounded, and symlink/path
escapes are rejected. One invalid entry produces a warning without discarding
valid siblings.

## Operator invocation

TUI:

```text
/skill                 # choose an operator-visible skill
/skill:refactor        # append its body as an operator/user message
/review                # example `slash: review` shortcut
```

CLI:

```sh
stado run --skill refactor
stado run --skill refactor --prompt "apply this to the billing package"
```

These are explicit operator gestures and remain native. Their user-role
provenance is truthful because the operator initiated them.

## Model invocation

Install and trust the official package after its signed release:

```sh
stado plugin install github.com/foobarto/stado-plugins/skills@v0.1.0
```

It is optional, not bundled, and not enabled or autoloaded by installation.
Expose `skills__search` and `skills__load` through the normal `[tools]` policy;
for example, adding the `meta` autoload category exposes both official skill
and registry discovery tools:

```toml
[tools]
autoload_categories = ["meta"]
```

If `[tools].enabled` is an allowlist, it must also admit both exact names.

- `skills__search {"query":"review"}` searches the current admitted skill
  facts and returns opaque IDs, summaries, scopes, and provenance.
- `skills__load {"id":"sha256:…"}` opens the exact opaque ID returned by
  search under a fresh digest-fenced catalog and
  returns JSON containing the Markdown `body`, `scope`, and `provenance` as an
  ordinary tool result.

Skill names are display/search labels, not selectors. Duplicate names remain
independently loadable by ID; a fabricated or stale ID fails closed against
the current catalog.

There is no native prompt listing and no skill-specific handler in AgentLoop,
TUI, ACP, headless, or subagents. Those surfaces bind the same effective skill
set as generic host facts when they dispatch the plugin. Disabling or omitting
`skills__load` prevents model-driven body invocation; it does not require the
host to hide a separate system-prompt listing because none exists.

## Trust and allowed tools

Native stado owns admission facts; the plugin owns search, matching, output
formatting, and the decision to request a session-surface edit.

- `disable-model-invocation` resources are mechanically omitted before WASM
  can catalog or open them.
- Rendered bodies over 128 KiB are omitted from the model catalog. Native
  operator `/skill` and `--skill` keep the loader's separate 1 MiB file limit;
  the narrower model bound guarantees the complete labeled JSON result fits
  the ordinary 1 MiB WASM tool-result channel even under worst-case escaping.
- Project `allowed-tools` is always inert. EP-44 did not create a project
  authority transition for this field.
- Persona `allowed-tools` is operator-authored relative to the persona. The
  host limits it to 64 unique valid names and intersects it with the exact
  filtered registry and session ceiling. Unknown, globally disabled, and
  session-disabled names are omitted.
- The plugin activates the resulting exact names in one digest-fenced atomic
  edit. It cannot widen the registry, sandbox, broker, or session ceiling.

`skills__search` is NonMutating. `skills__load` is StateMutating because a
valid persona skill may edit the session tool surface. Neither capability is a
security boundary by itself; normal signed-plugin admission and host ceilings
remain authoritative.

## See also

- [EP-45](../eps/0045-model-invocable-skills.md)
- [Plugin installation](../commands/plugin.md)
- [Host context-resource imports](../plugins/host-imports.md#stado_context_resource_catalog-and-stado_context_resource_open)
- [Repository instructions](./instructions.md)
