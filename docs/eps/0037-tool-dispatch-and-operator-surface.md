---
ep: 37
title: Tool dispatch, naming, and operator surface
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Draft
type: Standards
created: 2026-05-05
extended-by: []
see-also: [2, 5, 6, 17, 28, 29, 31, 35]
history:
  - date: 2026-05-05
    status: Draft
    note: Initial draft. Companion to EP-0038 (ABI v2 + bundled wasm) and EP-0039 (plugin distribution and trust). Captures the philosophy + dispatch model decided across the EP-0037/0038/0039 design conversation.
  - date: 2026-05-05
    status: Draft
    note: >
      Revision pass after codex + gemini independent review. Material edits: §B replaced
      single-underscore wire form with `__` separator and split plugin identity / local alias /
      tool name (codex #4, gemini #1, codex #3 / gemini #6); §E reframed always-loaded as
      non-disableable kernel of all four meta-tools with `tools.describe` activating schemas
      onto the model surface (codex #1); §J dropped manifest top-level `name:` field;
      decision-log D3 / D7 rewritten to record the new shapes.
---

# EP-0037: Tool dispatch, naming, and operator surface

## Problem

Three problems converge on the same surface — how the model sees tools,
how operators control which tools the model sees, and how the project's
load-bearing security philosophy is stated explicitly so future EPs
don't have to re-derive it.

### 1. Prompt budget pressure on the always-loaded tool registry

Today every registered tool's schema lives in the model's system
prompt every turn. Bundled tools (~14 today: `fs.read/write/edit/glob/grep`,
`shell.exec`, `webfetch`, `httpreq`, `rg`, `astgrep`, `lspfind.*`,
`subagent`, plus growing utility plugins) plus user-installed plugins
(the htb-toolkit alone is 12 plugins) means 5–10K tokens of schemas
shipped on every turn. Adding more bundled tools — which EP-0038 will
do, en route to fulfilling EP-0002's "all tools as wasm plugins"
invariant — pushes this past sustainable.

The model only ever uses a small fraction of available tools per
conversation. The architecture should reflect that.

### 2. Inconsistent tool naming, no namespace discipline

Existing tools mix conventions: `read_file` / `write_file` / `edit`
(snake_case verbs), `webfetch` / `subagent` (compound nouns),
`spawn_agent` (verb_noun). Plugin authors invent their own. The model
has no way to discover related tools — `fs.*` would be obvious
grouping, today there is none.

Anthropic's tool-name regex is `^[a-zA-Z0-9_-]{1,64}$` — dots aren't
allowed on the wire. So a "use dots everywhere" answer doesn't work
naively; needs a dual-form convention.

### 3. The security philosophy is implicit

EP-0005 D1 ("enforce capabilities in the runtime, not in the prompt")
and EP-0017 ("tool visibility, not approval prompts, is the policy
surface") together imply a coherent stance, but it has never been
written down as a load-bearing principle. New EPs (this one,
EP-0038, EP-0039) are about to make decisions that depend on that
stance — particularly around defaults (permissive vs locked-down),
operator controls, and where stado guarantees vs. where it just
describes. Stating the philosophy explicitly prevents future drift.

## Goals

- State the project's security philosophy explicitly, as a Process-level
  framing both EP-0037 and EP-0038 cite as the rationale for default
  behaviours.
- Move the tool registry from "every tool always visible to the model"
  to a meta-tool dispatch model — small always-loaded core plus
  searchable catalogue.
- Establish a single canonical naming convention (dotted in
  manifest/docs/config; underscore on wire) and a frozen category
  taxonomy.
- Make the operator's tool-surface controls (`--tools`, `--tools-autoload`,
  `--tools-disable`, `[tools.*]` in `.stado/config.toml`) clear,
  composable, and consistent between CLI and TUI.
- Reserve `[sandbox]` config schema for the layer-3 process-containment
  controls that EP-0038 will implement.
- Land `stado tool` subcommand + `/tool` slash mirrors so the operator
  can drive tool config from inside or outside the TUI.

## Non-goals

- ABI changes. The host-import surface is unchanged by EP-0037; that
  work lives in EP-0038.
- New host-side wasm runtime mechanics. Meta-tool dispatch is purely
  registry-side policy.
- Plugin distribution / remote install. That's EP-0039.
- Replacing existing `[tools].enabled` / `[tools].disabled` semantics
  from EP-0017. EP-0037 *extends* with `[tools].autoload` plus
  wildcard glob support, keeping the EP-0017 contract.
- Replacing existing `.stado/` discovery semantics from EP-0035.
  EP-0037 extends the schema; the discovery walk and load order are
  unchanged.
- Renaming existing tool names retroactively in stable plugins. The
  naming convention applies to new plugins and the EP-0038 bundled
  rewrite. Existing user plugins keep working under the wire form
  they declared.

## Design

### §A — Stado security philosophy (load-bearing)

Stado is **permissive by default, primitive-rich, and layered.** Three
positions, in priority order:

1. **Plugin authors set policy.** A plugin's manifest is a *declaration*
   of capabilities, not a request stado evaluates against rules. A
   paranoid author wraps every dangerous call in `approval.request`
   and refuses ambiguous inputs; a "go fast" author skips that. Stado
   doesn't grade either. The plugin ecosystem is allowed to span
   "hardened CTF-grade tool with full audit trail" through "one-shot
   script that yolo-shells anything."

2. **Operators set admission.** Which plugins are installed, what
   capabilities are accepted at install time, what's enabled per
   project, what's surfaced to the model — these are operator
   decisions made through `[tools].*` config, `--tools*` CLI flags,
   trust-store decisions, and `stado plugin install/trust` choices.
   Stado provides the levers; stado does not pull them by default.

3. **Operators set process containment.** OS-level wrapping
   (namespaces, fs binds, env allow-lists, network mode, capability
   drops, proxies) is first-class via `[sandbox]` config (schema
   reserved here, implementation in EP-0038), not an afterthought.
   Layer 3 is where operators put the "actual hard wall" if they
   want one.

**What stado guarantees, full stop**:

- Capability declarations are honoured: a plugin declaring no
  `net:dial:tcp:foo:443` cannot open that connection. Capabilities
  bound an upper limit; OS-level constraints can shrink it further.
- Sandbox runner integrates correctly when present (Linux landlock,
  bubblewrap, seccomp; macOS sandbox-exec; degraded modes warned
  per EP-0005 D4).
- Approval prompts route through a known channel and reach the
  operator unambiguously (per EP-0017).
- Sessions are durable and auditable (per EP-0004).
- Plugin signatures are verified against the operator's pinned trust
  store (per EP-0006).

**What stado does NOT do, by design**:

- Prescribe minimum capabilities for tool categories ("a `shell.*`
  tool MUST declare exec:proc"). Plugin authors decide.
- Add default approval gates beyond what the plugin author or
  operator configured. UI prompts are not containment.
- Enforce policies on top of OS-level limits. **Cap-vs-OS overlap
  rule**: where the OS is already the gate (port < 1024 binding,
  CAP_NET_RAW for raw ICMP, network namespace blocking dial), stado
  doesn't add a parallel refusal. The cap declaration stays for
  visibility and operator admission; enforcement is the kernel's.

**The slogan**: *lean power tool with modules.* Not "AI assistant
with guardrails." The guardrails are modules too.

This is the inverse of how Claude Code, ChatGPT plugins, and most
LLM tool runners orient. Those lean prescriptive-defaults — every
tool call gated, capabilities short and stado-defined, sandbox
mandatory. That's a defensible position; it's not stado's. Stado is
a power tool; the user is assumed to be the one with their hand on
it; the security primitives are there for when they want to put walls
up, not as default scaffolding.

### §B — Tool naming convention (canonical / wire)

Two forms, deterministically synthesised at registration:

- **Canonical form** (docs, config, CLI, slash commands): dotted.
  `fs.read`, `shell.exec`, `web.fetch`, `agent.spawn`,
  `tools.search`, `htb-lab.spawn`.
- **Wire form** (LLM-facing tool name): `<local_alias>__<tool_name>`
  (double underscore separator), with `.` and `-` in either segment
  replaced by `_`. Anthropic's `^[a-zA-Z0-9_-]{1,64}$` regex doesn't
  allow dots. The double-underscore separator is mandatory and
  reserved — plugin local aliases and tool names cannot contain `__`
  themselves. Examples: `fs__read`, `shell__exec`, `web__fetch`,
  `htb_lab__spawn` (the dash in `htb-lab` becomes `_`; the namespace
  separator is unambiguous because it is `__`).
- Synthesis is deterministic and round-trippable: any wire name
  reverses to one canonical name (split on the first `__`, dashes
  reverse where the original canonical form is recorded). Recording
  the canonical-to-wire mapping in the registry is mandatory because
  some canonical forms cannot be reversed by string substitution
  alone (e.g. `foo-bar.baz` and `foo.bar-baz` both contain a single
  dash in the local alias but at different positions). The registry
  is the source of truth; the wire form is a label.

#### Plugin identity vs. local alias vs. tool namespace

Three distinct concepts, kept separate to prevent the collisions
EP-0039 surfaces around remote install. The **local alias** is what
EP-0037 uses everywhere it says "plugin name."

- **Plugin identity** (canonical): the remote URL or a path-derived
  identifier — e.g. `github.com/foobarto/superduperplugin` or
  `local:///abs/path` for local-dir installs. Operator surfaces
  (lock file, trust, install dir) key on identity. EP-0039 §A is
  authoritative for the syntax.
- **Local alias** (display): a short string operators reference in
  config and slash commands. By default derived from the identity's
  last path segment (`github.com/foo/bar` → `bar`). Distinct from
  the identity precisely so two different remote plugins both
  named `fs` can coexist on disk and both appear in the registry,
  with operator-chosen aliases (`fs` vs `fs2` or operator-defined
  `myfs` vs `theirfs`) preventing collision.
- **Tool name** (within the plugin): from the manifest's
  `tools[].name` — `read`, `write`, etc. Combined with the local
  alias to form canonical (`fs.read`) and wire (`fs__read`) forms.

The plugin manifest model:

```json
{
  "tools": [
    { "name": "read", "description": "...", "schema": "..." },
    { "name": "write", "description": "...", "schema": "..." }
  ]
}
```

The manifest does NOT carry a top-level `name:` field anymore. Local
alias is operator-side metadata, recorded in `~/.local/share/stado/
plugins/<identity-hash>/local-alias` and surfaced via `stado plugin
ls`. Default alias = identity's last path segment with `-` and `.`
allowed; on collision at install time, operator is prompted:

```
github.com/foo/fs collides with already-installed
github.com/bar/fs (local alias: fs).
Choose a local alias for the new install:
  [1] fs2 (default — auto-incremented)
  [2] foo-fs (host+owner-prefix derived)
  [3] <type your own>
```

Wire-form synthesis takes the local alias + tool name. Two plugins
both with local alias `fs` and tool `read` can never coexist (the
operator already disambiguated at install time). Cross-version-of-
same-plugin is handled by §F's per-project active-version pinning,
not by aliasing.

#### Registration validation

At plugin registration / load time:

1. Local alias parsed; rejected if it contains `__` or characters
   outside `[a-zA-Z0-9._-]`.
2. Each tool's name parsed; same constraint.
3. Wire form synthesised: `<alias_with_._and_-_to_underscore>__<tool_with_._and_-_to_underscore>`.
4. Wire form length verified ≤ 64 characters (Anthropic's regex
   bound). Rejected at registration with a clear error if exceeded;
   plugin install can also surface this earlier as a doctor warning.
5. Wire form uniqueness checked against the registry. Collisions
   (cross-plugin) refuse the registration of the second plugin's
   colliding tool with a clear error citing both plugins and the
   conflicting wire form. The first plugin's tool wins; the
   operator resolves by aliasing one of the two at install time
   (`stado plugin install ... --alias=...`).

#### Plugin family idiom (convention, not enforcement)

A plugin wrapping a family of similar implementations exposes both a
default tool and per-implementation tools, separated by dotted
suffix. Example for a `shell` plugin:

- `shell.exec` — uses `[plugins.shell] binary = ...` config
- `shell.spawn` — stateful session, same default binary
- `shell.bash`, `shell.zsh`, `shell.fish`, `shell.sh`, `shell.pwsh`
  — explicit-binary variants for when shell semantics matter

Same idiom for `agent.spawn` + `agent.opus` / `agent.haiku`, or
`http.request` + `http.client_new`. Host enforces nothing here; it's
plugin-dev style guidance documented as such.

### §C — Categories + canonical taxonomy

Tools are tagged with categories at the manifest tool-level (not the
plugin level — plugins like `payload-generator` straddle multiple
categories per tool):

```json
{
  "tools": [
    { "name": "revshell",   "categories": ["ctf-offense", "shell"], "description": "..." },
    { "name": "msfvenom",   "categories": ["ctf-offense", "exploit-dev"], "description": "..." }
  ]
}
```

(Plugin identity is determined by install source per EP-0039 §A;
the manifest no longer carries a top-level `name:` field. Local
alias is operator-chosen at install time per §B.)

#### Frozen canonical taxonomy

Plugin authors pick from this fixed list. Adding a new category
requires a future EP that amends this taxonomy:

```
filesystem      shell           network         web
dns             crypto          data            encoding
code-search     code-edit       lsp             agent
task            mcp             image           secrets
documentation   ctf-offense     ctf-recon       ctf-postex
meta
```

Twenty-one names. Covers today's bundled inventory + the htb-toolkit's
12 plugins + likely near-future additions.

#### `extra_categories: []` escape hatch

Free-form tagging is permitted but flagged as plugin-defined:

```json
{
  "name": "experimental",
  "categories": ["network"],
  "extra_categories": ["protocol-fuzzing", "research"]
}
```

`tools.describe` output marks `extra_categories` distinctly so the
model and operator both see the tag is plugin-coined rather than from
the canonical list. Categories from `extra_categories` count for
filtering (`tools.in_category`) but don't appear in
`tools.categories()` output unless explicitly opted in (a plugin
declaring an extra category appearing as a category in the listing
would expand the surface arbitrarily — undesirable).

#### Validation at install time

`stado plugin install` verifies every declared `categories` entry is
in the canonical list. Manifest with unknown category → install
refused with an actionable error: `unknown category "foobar" in
tool "myname.foo"; canonical categories: filesystem, shell, ...`.

This is not a security gate (a malicious plugin could rename to
hit canonical names); it's a coherence enforcement. Operators
review categories at install time the same way they review
capabilities; canonical names keep the review surface small.

### §D — Meta-tool dispatch

Stado stops broadcasting every tool's schema in the system prompt.
The always-loaded core is small; everything else lives behind tool
search. **Four meta-tools** registered in the always-loaded core:

#### `tools.search(query?)`

- No-arg form: returns all enabled tools, light-shape:
  `[{name, summary, categories}]`. Names in canonical form
  (`fs.read`); summary is a single sentence; categories the
  declared list.
- With-query form: text search across name + summary + categories.
  Matches case-insensitive, substring. Empty result returns `[]`,
  not error.

`tools.search()` collapses what would have been a separate
`tools.list` — they're the same operation with default-empty query.

#### `tools.describe(names: [string])`

- Required `names` argument, list of canonical-form tool names.
- Returns full info per tool: schema (JSONSchema), long-form
  description, categories, declared capabilities, source plugin
  name and version. One-shot — no follow-up needed for the model
  to decide whether to call.
- **Batched** to avoid round-trip costs: model picks 5 candidates
  from `tools.search`, fetches all 5 schemas in one call.
- Unknown names in the list return `{name, error: "not found"}`
  entries; the call doesn't fail.

#### `tools.categories(query?)`

- No-arg form: returns the canonical-list category names that any
  enabled tool currently belongs to (so categories with zero
  active members don't show up).
- With-query form: substring filter on category name. Useful
  scaling-out if the canonical list grows.
- Return type is `[string]` — just names. Drilling into a
  category uses `tools.in_category`.

#### `tools.in_category(name)`

- Required `name` argument, exact canonical category name.
- Returns `[{name, summary, categories}]` for tools in that
  category. Same shape as `tools.search` so the model has one
  result format to learn.
- Plugin-defined `extra_categories` accepted as input — returns
  matching tools.

#### Why these four

- Two general-shaped tools (`search`, `describe`) for the
  "find specific tool" flow.
- Two category-shaped tools (`categories`, `in_category`) for the
  "browse the catalogue" flow.
- Returning summaries inline (rather than just names) saves the
  model 5–20 round-trips per discovery cycle, at ~25 tokens per
  summary. Negligible in vs. avoided describe-per-name calls out.

### §E — Default always-loaded core

Hardcoded in stado, overridable per-project and per-user.

#### Always-loaded kernel (cannot be disabled)

The four `tools.*` meta-tools form the **dispatch kernel**. The
model cannot invoke any other tool without them — `tools.search`
finds candidates, `tools.describe` loads schemas onto the wire so
the model can call them. They are loaded unconditionally at every
session start regardless of `[tools.disabled]`, `--tools`, or
`--tools-disable` settings:

```
tools.search
tools.describe
tools.categories
tools.in_category
```

Attempting to disable any of the four with `--tools-disable
'tools.*'` or `disabled = ["tools.*"]` is rejected with:

```
Refused to disable tools.* — the meta-tool dispatcher kernel
cannot be removed. tools.search and tools.describe are required
to make any other tool callable.

If you want a true tool-free run, omit autoload for everything:
  --tools '<empty>' --tools-autoload '<empty>'
The meta-tools will still be present.
```

This guarantees every `--tools` whitelist behaves predictably:
"only these tools" always means "these tools plus the dispatch
kernel", never an unrunnable shell.

#### Convenience defaults (overridable)

Beyond the kernel, the hardcoded autoload default adds the
filesystem + shell primitives that ~every conversation uses:

```
fs.read
fs.write
fs.edit
fs.glob
fs.grep
shell.exec
```

These are autoloaded by default but can be removed via
`[tools.autoload]` config or `--tools-autoload`. Six convenience
tools + four kernel meta-tools = ten always-loaded tools at
session start.

The kernel set is in stado's source as a literal; not configurable.
The convenience set is overridable. Both ship with the binary.

#### Schema availability for non-autoloaded tools

`tools.describe(names: [str])` is the only mechanism the model uses
to acquire schemas for non-autoloaded tools. Behaviour:

1. Model calls `tools.search` (or `tools.in_category`) — gets
   candidate tools with summaries.
2. Model calls `tools.describe(["fs.read", "shell.spawn"])` — gets
   full JSONSchema for each tool back as part of the call result.
3. **Result of `tools.describe` injects each described tool's
   schema into the assistant's available tool surface for the
   remainder of the session.** The model can now call `fs_read`,
   `shell_spawn` directly via the tool-call mechanism, schemas
   already known.

The dispatch is "describe-then-it's-callable", not
"describe-then-still-need-an-extra-step." This avoids the
round-trip-to-call gap codex's review #1 flagged. Subsequent
turns continue to see the schemas (no re-fetch needed unless the
plugin reloads).

A model that bypasses `tools.describe` and tries to call a
non-autoloaded tool directly (by guessing the wire name) gets a
structured error — the host's tool-call dispatcher refuses unless
the schema has been activated via `describe`. This is enforced
host-side, not via prompt instruction.

### §F — Configuration: `.stado/config.toml` schema

Building on EP-0035's `.stado/` discovery and load order. EP-0037
adds these sections to the project- and user-level config schema
(loaded against the same precedence: built-in defaults < user <
project < env):

```toml
# .stado/config.toml

[tools]
# Wildcard globs supported. * matches one segment within a namespace.
# fs.* matches every fs.X. Plain * matches everything.
autoload = ["tools.*", "fs.*", "shell.exec"]
disabled = ["browser.*"]
# enabled = [...]  — present means whitelist mode (lockdown)
                  — when unset, all installed tools are enabled

[plugins.shell]
binary = "/usr/bin/zsh"          # plugin-specific config keys
init = ["set -u", "set -o pipefail"]

[plugins.htb-lab]
default_token_path = ".secrets/htb_app_token"

[plugins.fs]
version = "v2.0.0"               # pin specific installed version (EP-0039)

[sessions]
auto_prune_after = ""            # "" = never (default); "90d", "30d" otherwise

[agents]
max_depth = 5
idle_timeout = "10m"

# [sandbox] schema reserved by EP-0037; implementation in EP-0038.
[sandbox]
mode = "off"                     # "off" | "wrap" | "external"
http_proxy = ""
dns_servers = []
allow_env = []                   # allow-list; empty = pass-through
plugin_runtime_caps_add = []
plugin_runtime_caps_drop = []

[sandbox.wrap]
runner = "auto"                  # auto | bwrap | firejail | sandbox-exec
bind_ro = []
bind_rw = []
network = "host"                 # host | namespaced | off

[sandbox.profiles.htb]
mode = "wrap"
http_proxy = "http://127.0.0.1:8080"
[sandbox.profiles.htb.wrap]
network = "namespaced"
bind_rw = ["~/Dokumenty/htb-writeups"]
```

#### Wildcard semantics

Single `*` matches one dotted segment:

- `fs.*` → `fs.read`, `fs.write`, `fs.edit`, `fs.glob`, `fs.grep`
- `tools.*` → all four meta-tools
- `*` (alone) → every registered tool
- `htb-lab.*` → all tools whose plugin name is `htb-lab`

There is no `**` (no nested namespaces today; revisit when needed).

#### `disabled` wins over `enabled` and `autoload`

When a tool name matches both lists, `disabled` wins. Prevents the
silent-override surprise where a user-config `enabled = ["*"]`
quietly counters a project-config `disabled = ["browser.*"]`.

#### Schema additions vs. EP-0017 / EP-0035

EP-0017 introduced `[tools.enabled]` and `[tools.disabled]` —
unchanged. EP-0037 adds `[tools.autoload]` and elevates wildcard
glob support to a documented schema feature (today's behaviour
treats entries as bare names). EP-0035's `[tools.overrides]` (plugin
override-by-name) — unchanged. EP-0035's `[plugins].search_path` —
unchanged.

EP-0037's new `[plugins.<name>]` per-plugin config tables, the
`[sessions]`, `[agents]`, and `[sandbox]` sections — all additive.
Existing config files load unchanged.

### §G — CLI flag flow

Three flags, one ordered pipeline. Each flag is a filter that sees
the previous filter's output:

```
1.  --tools <list>             [WHITELIST mode]
                                If set: ONLY these are enabled
                                If unset: all installed tools enabled

2.  --tools-autoload <list>    [AUTOLOAD selection]
                                If set: these are pinned to always-on
                                If unset: hardcoded default core
                                          (overridable in [tools.autoload])
                                Tools not in autoload are still
                                reachable via tools.search

3.  --tools-disable <list>     [BLACKLIST]
                                Removed from the result of (1)+(2)
                                Wins over enable AND autoload
```

All three accept comma-separated globs. Order of application is
fixed; flags can be combined.

#### Worked example

```
stado run \
  --tools 'fs.*,shell.*,htb-lab.*' \
  --tools-autoload 'fs.read,shell.exec' \
  --tools-disable 'shell.spawn'

Step 1: enable fs.*, shell.*, htb-lab.*           → 12 tools available
Step 2: autoload {fs.read, shell.exec}            → 2 in always-loaded core
        rest reachable only via tools.search        10 deferred
Step 3: disable shell.spawn                       → 11 tools total
                                                    shell.spawn gone entirely
```

#### CLI vs. config precedence

```
CLI flags  >  project .stado/config.toml  >  user ~/.config/stado/config.toml
                                          >  hardcoded defaults
```

Lists union additively for `autoload` (each layer adds entries),
and union for `disabled` (each layer adds entries). The whitelist
form `enabled` (or `--tools`) replaces — if any layer specifies it,
the most-specific layer wins fully.

#### `--sandbox <name>` — global flag

Not a `tools-*` flag, but lives in the same family:

- `--sandbox` (no name) → applies `[sandbox]` defaults from config
  (effective if `mode = "wrap"` is set; otherwise no-op with hint
  pointing at `[sandbox]` or `--sandbox <name>`).
- `--sandbox htb` → applies `[sandbox.profiles.htb]`.
- Profile config wins over root `[sandbox]` config; CLI flag
  presence wins over both.

Implementation in EP-0038; flag declaration here.

### §H — `stado tool` subcommand

Operator surface for inspection + tool config mutation, mirrored as
slash commands in the TUI:

| Verb | Action | Mutates config |
|------|--------|----------------|
| `tool ls [glob]` | List tools with state (autoloaded / enabled / disabled), plugin source, categories | no |
| `tool info <name>` | Full schema + docs + caps + manifest reference (CLI mirror of `tools.describe`) | no |
| `tool cats [glob]` | List categories, optionally substring-filtered | no |
| `tool enable <glob>` | Add to `[tools.enabled]` (or remove from `[tools.disabled]`) | yes |
| `tool disable <glob>` | Add to `[tools.disabled]` | yes |
| `tool autoload <glob>` | Add to `[tools.autoload]` | yes |
| `tool unautoload <glob>` | Remove from `[tools.autoload]` | yes |
| `tool reload <glob>` | Drop cached wasm instance(s); next call re-inits | runtime only |

Flags on mutating verbs:

- `--global` — write user-level config (`~/.config/stado/config.toml`)
  rather than project-level.
- `--config <path>` — explicit target file (for shared/team configs
  outside the discovery walk).
- `--dry-run` — print the diff that would be applied; write nothing.

`tool ls` example output:

```
NAME              STATE       PLUGIN          CATEGORIES
fs.read           autoloaded  fs              filesystem
fs.write          autoloaded  fs              filesystem,code-edit
shell.exec        autoloaded  shell           shell
shell.spawn       enabled     shell           shell
shell.bash        enabled     shell           shell
browser.fetch     disabled    browser         web
htb-lab.spawn     enabled     htb-lab         ctf-recon,documentation
```

`--json` everywhere for machine-readable output, same shape across
all read verbs.

`tool reload <glob>` is runtime-only — doesn't touch config. Useful
during plugin authoring (`./build.sh && stado tool reload fs.*`
instead of restarting stado entirely). Lazy-init handles the
re-instantiation on next call.

#### Why no `tool add` / `tool remove`

Both would overlap with `plugin install` / `plugin remove` (since
tools come from plugins). Two-axis split kept clean: `plugin *`
manages installation; `tool *` manages visibility. Operator who
wants a tool that doesn't exist runs `plugin install`, then
`tool enable` if the new plugin's tools should be exposed.

### §I — TUI slash command mirrors

Pattern: every CLI subcommand has a `/<subcommand>` slash mirror.
Two semantic differences from CLI:

1. **Slash commands default to per-session, non-persistent** — they
   apply to the current TUI session and don't rewrite config files.
   Add `--save` to persist to project (or `--save --global` for
   user-level).
2. Slash commands have access to runtime state CLI doesn't have:
   live agents, currently-loaded plugin instances, current session's
   token count, etc. Some slash commands have no CLI mirror as a
   result — see EP-0038 for `/ps`, `/top`, `/kill`, `/stats`.

Mirrored slash commands landed by this EP:

```
/tool ls [glob]                    /tool info <name>
/tool cats [glob]                  /tool enable <glob>
/tool disable <glob>               /tool autoload <glob>
/tool unautoload <glob>            /tool reload <glob>
/tool * --save [--global]          (persist mode)

/session list [--driver=agent]     /session show <id>
/session attach <id> --read-only   (live-follow viewer; EP-0038
                                    extends with read-write attach)
```

`/session list` and `/session show` are read-only here; the
read-write `/session attach` (with the multi-producer message
metadata work, the renderer changes, the cap reservation) ships in
EP-0038 alongside the agent surface that makes it useful.

### §J — Plugin manifest field additions

Schema additions to the plugin manifest, validated at install time:

```json
{
  "version": "v0.2.0",
  "tools": [
    {
      "name": "read",
      "description": "Read a file from the workdir",
      "schema": "...",
      "categories": ["filesystem"],
      "extra_categories": []
    }
  ],
  "capabilities": ["fs:read:."]
}
```

The manifest does not carry a top-level `name:` field — plugin
identity comes from install source (EP-0039 §A); operator-side
local alias from `~/.local/share/stado/plugins/<identity-hash>/local-alias`
(EP-0037 §B).

- `tools[].categories` — required; list of strings from the canonical
  taxonomy. Empty list permitted but discouraged (tool won't appear
  in any `tools.in_category` lookup).
- `tools[].extra_categories` — optional; list of strings, free-form.
  Validated only for non-empty entries (no character restrictions
  beyond standard JSON string).

Validation runs in `plugin install` after signature verification.
Unknown canonical category → install fails with the
"unknown category 'X' in tool 'plugin.toolname'; canonical
categories: ..." error. Pre-EP-0037 manifests with no `categories`
field are accepted for backwards compatibility but the resulting
tools don't appear in any category-based listing — the operator can
update the manifest, or the tool stays text-search-only.

## Migration / rollout

### What ships in EP-0037

- Philosophy section, written into `docs/eps/0037-...` and referenced
  from EP-0038, EP-0039 as the rationale anchor.
- Tool naming convention enforced for new plugins via wire-form
  synthesis at registration time.
- Canonical category taxonomy; manifest validation at install time.
- Four meta-tools (`tools.search`, `tools.describe`, `tools.categories`,
  `tools.in_category`) implemented as native registry mechanics in
  EP-0037 (will move to wasm in EP-0038; not blocking here).
- Default always-loaded core hardcoded; `[tools.autoload]` schema
  + per-config / CLI overrides.
- `--tools`, `--tools-autoload`, `--tools-disable` CLI flags.
- `[sandbox]` schema reserved (no implementation; EP-0038 adds
  the wrap-mode and env/cap/proxy enforcement).
- `stado tool` subcommand with the eight verbs.
- `/tool *` slash mirrors; `/session list`, `/session show`,
  `/session attach --read-only` slash commands.

### What does NOT ship in EP-0037

- ABI changes, host-import additions: deferred to EP-0038.
- Read-write `/session attach`, `/session inject`, multi-producer
  message metadata: EP-0038.
- `/ps`, `/top`, `/kill`, `/stats`, handle ID convention: EP-0038.
- Sandbox wrap-mode implementation: EP-0038.
- Plugin distribution / remote install: EP-0039.

### Backward compatibility

- Existing tool registrations continue to work; their tools get
  wire-form names synthesised from their existing names (no rename
  visible to the model unless a plugin opts in to the new manifest
  shape).
- Manifests without `categories` are accepted; affected tools don't
  show up in `tools.in_category` results.
- `[tools.enabled]` / `[tools.disabled]` semantics unchanged.
- `[tools.autoload]` is new; absence is the documented default
  (use the hardcoded core).
- Wildcard glob support for tool lists is new behaviour; today's
  bare-name entries continue to work because a non-wildcard
  string is treated as an exact match.

### Pre-1.0 stance

Per the conversation that produced this EP: stado is pre-1.0 and the
author is the only operator. Temporary instability during the EP-0037
+ EP-0038 + EP-0039 implementation window is acceptable. No
backwards-compat shims for retired flags or behaviours unless they
serve user-facing manifest contracts (which the canonical-tool-name
synthesis preserves automatically).

## Failure modes

- **Operator config sets `[tools.enabled] = ["fs.*"]` but the
  hardcoded core includes `shell.exec`.** Conflict resolved by the
  whitelist semantics: `--tools` / `[tools.enabled]` REPLACES the
  convenience default. The dispatch kernel (§E) is unaffected —
  the four `tools.*` meta-tools are always loaded. So
  `--tools fs.*` results in `tools.* + fs.*` available; `shell.exec`
  removed; convenience defaults dropped. If operator wanted shell
  back: `--tools 'fs.*,shell.exec'`.

- **Operator attempts to disable the kernel.** `[tools.disabled] =
  ["tools.*"]` or `--tools-disable tools.*` is refused at config
  parse time with a clear error pointing at §E's "kernel cannot be
  disabled" rule. Stado does not silently keep the kernel after
  the operator declared they want it gone — it refuses the
  declaration outright.

- **Plugin declares `extra_categories: ["network"]`** (overlapping a
  canonical name). Permitted; the entry duplicates the canonical
  category visually in `tools.describe` output (`extra_categories`
  marked separately) but doesn't bypass install validation. The
  declared canonical list is what counts for `tools.in_category`.

- **Wildcard expands to zero matches.** `--tools-disable 'browser.*'`
  on a system without the browser plugin: silent no-op. Documented as
  expected behaviour.

- **Tool name with no canonical synthesis** — wire-form already
  matches a different tool. Detected at registration with a clear
  error: `tool fs.read (wire form: fs_read) collides with already-
  registered tool from plugin myfs (wire form: fs_read)`. Operator
  resolves by `--tools-disable` for one or the other, or via plugin
  uninstall. No silent shadowing.

- **`tools.search("")` returns hundreds of tools.** Output gets capped
  at 200 by default with a `truncated: true` flag. Caller passes
  `tools.search(query, limit?)` for explicit control.

## Test strategy

- **Naming convention.** Unit tests for wire-form synthesis: dotted
  → underscore, dashes preserved as underscores, name collision
  detection.
- **Category validation.** Manifest install tests: canonical list
  accepted, unknown category refused with actionable error,
  `extra_categories` accepted without canonical-list check.
- **Meta-tool dispatch.** Unit + integration tests for `tools.search`
  with/without query, `tools.describe` batching, `tools.categories`
  filtering, `tools.in_category` returning correct subsets.
- **Wildcard expansion.** `*`, `fs.*`, `htb-lab.*`, plain-name match
  precedence; empty match no-op.
- **Three-flag pipeline.** Combinations of `--tools` / `--tools-
  autoload` / `--tools-disable` with the worked-example assertion;
  precedence between CLI / project / user / hardcoded.
- **Config schema.** Round-trip TOML serialization for every section.
- **`stado tool` subcommand.** Each verb against a fixture project,
  asserting both the runtime effect and the file mutation.
- **Slash mirrors.** TUI scenario tests for `/tool ls`,
  `/tool enable`, `/tool reload` (runtime effect without config
  mutation), `/session show`, `/session attach --read-only`.

## Open questions

- **Always-loaded core size.** Eight tools is the proposed default.
  Open question whether `tools.in_category` and `tools.describe`
  belong there too, since they're the natural follow-ups from
  `tools.search` / `tools.categories`. Position: leave them
  reachable-via-search rather than always-loaded; the model gets
  them in the first search result. Revisit if usage shows
  consistent extra round-trip.
- **`tools.search` ranking.** No-arg form returns tools in some
  order. Today: alphabetical by canonical name. Open question
  whether to bias by frequently-used or autoloaded tools first.
  Position: keep alphabetical until usage data suggests
  otherwise.
- **Case-insensitive search.** Currently substring-match,
  case-insensitive. Open question whether to support fuzzy /
  prefix matching. Position: substring is enough for now;
  add prefix or fuzzy when concrete miss-cases surface.
- **Scope of `tool reload`.** Plugin-level reload (drop everything
  the plugin owns) is naturally addressed by `plugin reload <name>`
  in EP-0039; tool-level `tool reload <tool>` is per-tool only.
  Confirmed. Both verbs ship; different scopes.

## Decision log

### D1. Permissive-by-default security philosophy as load-bearing

- **Decided:** stado guarantees primitives, not policies. Plugin
  authors set policy; operators set admission and process
  containment. Default behaviour is unrestricted within OS limits.
- **Alternatives:** prescriptive defaults like Claude Code; mandatory
  approval gates; minimum-cap requirements per tool category.
- **Why:** stado is a power tool, not a guardrails-first AI
  assistant. The user is the operator with their hand on it. The
  primitives (cap declarations, sandbox runner, approval routing,
  signed plugins) are all there; they're modules to be composed,
  not defaults that hide the mechanism. Stating this explicitly
  prevents future drift toward the prescriptive-default norm in
  the rest of the LLM-tool-runner ecosystem.

### D2. Cap-vs-OS overlap rule

- **Decided:** stado does not add parallel refusals where the OS is
  already the gate (port < 1024, CAP_NET_RAW, network namespace).
  Cap declarations stay for visibility and operator admission; the
  kernel is the actual barrier.
- **Alternatives:** synthesise stado-side caps that mirror OS
  privilege boundaries (`net:listen:privileged`); refuse install or
  invocation when stado-detected privilege is missing.
- **Why:** double-gating produces the wrong failure mode when stado
  and OS disagree (e.g., container with `cap_net_raw` granted, OS
  permits ICMP, stado refuses). Single-source-of-enforcement
  (the kernel) gives predictable behaviour and avoids the cap-string
  proliferation that tracking OS privileges as stado caps would
  require.

### D3. Tool naming: dotted canonical, double-underscore wire, identity ≠ alias

- **Decided:** canonical form is dotted (`fs.read`); wire form is
  `<local_alias>__<tool_name>` with the double underscore as the
  reserved namespace separator. Plugin identity (remote URL or
  local path) is distinct from local alias (operator-chosen
  display name) which is distinct from manifest tool name. The
  manifest does not carry a top-level `name:` field; alias is
  operator-side.
- **Alternatives:** single underscore separator (collision-prone:
  `foo-bar.baz` and `foo.bar-baz` both → `foo_bar_baz`); manifest
  carries `name:` and is canonical (collision risk: two repos both
  declare `name = "fs"`); double-underscore separator AND keep
  manifest `name:` as canonical (still leaves the duplicate-name
  collision for config tables and install dirs).
- **Why:** the codex+gemini review pass surfaced that single-
  underscore wire-form collapses dashes ambiguously, and that
  manifest `name:` is overloaded across config tables, install
  directories, and the per-tool prefix. Splitting identity (where
  the plugin came from) from alias (what the operator calls it
  here) makes both problems disappear: two `github.com/*/fs` plugins
  can coexist with operator-disambiguated aliases (`fs` vs `foo-fs`),
  and `__` as a reserved separator is unambiguously reversible.
  The double-underscore convention matches Claude Code's MCP tool
  naming (`mcp__server__tool`) — readers familiar with that
  recognise the pattern.

### D4. Frozen canonical category taxonomy

- **Decided:** 21 names, fixed in EP-0037 and amendable only by
  future EP. `extra_categories` for free-form tags.
- **Alternatives:** fully freeform tags (chaos: "network" /
  "networking" / "net"); operator-defined taxonomy in config;
  fully open + curated registry of common tags.
- **Why:** a model browsing the tool surface needs predictable
  category names. Free-form fragmentation is the most common
  failure mode of tag-based systems. Twenty-one names cover
  today's bundle + the user's htb-toolkit + likely near-future
  additions; keep it frozen until concrete pressure to amend.
  Free-form extras handle plugin-specific niches without
  polluting the discovery surface.

### D5. `tools.search` no-arg behaviour collapses `tools.list`

- **Decided:** four meta-tools (`search`, `describe`, `categories`,
  `in_category`); no separate `tools.list`.
- **Alternatives:** five tools with explicit `tools.list` returning
  everything; `tools.search` requiring non-empty query.
- **Why:** `tools.list()` and `tools.search("")` are the same
  operation. One tool with optional query is cleaner; the model
  learns one convention. `tools.in_category` stays distinct
  because it has different semantics (mandatory exact-name
  arg, different result shape, exact match not substring).

### D6. Three-flag CLI: --tools, --tools-autoload, --tools-disable

- **Decided:** three flags, semantically distinct, applied in
  fixed order (whitelist → autoload → blacklist).
- **Alternatives:** single `--tools` flag with prefix syntax
  (`+fs.*,-browser.*`); two flags merging autoload into enable.
- **Why:** three flags do three different things; conflating
  produces an awkward mini-DSL that documentation has to
  explain. Three flags one job each is the right tradeoff.
  Disabled-wins-over-enabled rule prevents silent override
  surprise across config layers.

### D7. Always-loaded kernel + convenience default

- **Decided:** four `tools.*` meta-tools (search/describe/
  categories/in_category) form a non-disableable dispatch kernel
  loaded at every session start. Six convenience tools (`fs.read`,
  `fs.write`, `fs.edit`, `fs.glob`, `fs.grep`, `shell.exec`) are
  autoloaded by default but configurable. `tools.describe`
  activates non-autoloaded tool schemas onto the model's available
  surface for the rest of the session — describe-then-it's-callable.
- **Alternatives:** kernel = `tools.search` only (forces an extra
  call before any non-autoloaded tool can run); no kernel at all
  (operator could lock themselves out of dispatch); allow kernel
  to be disabled (operator could ask for an unrunnable shell).
- **Why:** codex review item #1 surfaced that the original "search
  + categories autoloaded; describe and in_category reachable via
  search" leaves a gap — search returns names, but the model can't
  call a tool whose schema isn't on the wire. Making `tools.describe`
  the schema-activation mechanism, and putting all four meta-tools
  in a non-disableable kernel, closes that gap. The convenience
  defaults stay configurable. The kernel-non-disableable refusal
  prevents the operator from accidentally configuring an
  unrunnable shell; if the operator really wants no tools, they
  spell that explicitly via empty autoload + empty enable, and
  the kernel still exists but does nothing useful.

### D8. Slash commands default to per-session, not persistent

- **Decided:** `/tool *` slash commands apply to the current
  session and do not write config files; `--save` opts in to
  persistence (with `--global` for user-level).
- **Alternatives:** match CLI behaviour (persist by default);
  invert (CLI session-only by default, persist via `--save`).
- **Why:** slash commands are the experimentation surface;
  operators try things, decide, then optionally persist. CLI
  is the durable surface; operators run it intentionally to
  change config. Different defaults match the different
  intents. Aligns with how Claude Code's `/permissions` works.

### D9. Schema validation refuses unknown canonical categories at install

- **Decided:** `stado plugin install` rejects manifests declaring
  category names outside the canonical taxonomy.
- **Alternatives:** accept silently and warn; auto-classify to
  nearest canonical; allow without restriction (move to
  `extra_categories`).
- **Why:** install-time refusal is the right loud signal. A typo
  ("netork" instead of "network") gets caught immediately; a
  legitimate niche tag goes in `extra_categories` explicitly.
  The category surface stays clean for the operator and the
  model browsing it.

### D10. Sessions persist forever by default

- **Decided:** `[sessions] auto_prune_after = ""` (empty / never)
  is the default. Sessions feed `stado usage/stats`; auto-pruning
  drops history.
- **Alternatives:** 30-day or 90-day default; auto-prune on
  process startup.
- **Why:** any auto-prune default risks silently dropping data
  the operator may have wanted (especially audit trails for
  agent-driven work — see EP-0038). Explicit cleanup via
  `stado session prune` keeps the operator in charge.
  Operators on shared hosts can opt into time-based retention.

## Related

### Predecessors

- **EP-0001** (Process) — unaffected; this EP follows the EP
  process unchanged.
- **EP-0002** (All Tools as WASM Plugins) — the lean-core north
  star EP-0037 cites as motivation. EP-0038 will fulfil the
  invariant; EP-0037 prepares the dispatch model that makes
  fulfilling it tractable (no prompt-budget panic when the
  bundled-tool count grows).
- **EP-0005** (Capability-Based Sandboxing) — EP-0037 §A
  (philosophy) explicitly extends EP-0005 D1 ("enforce in the
  runtime, not the prompt") to cover the "stado describes;
  operator/OS enforces" framing for layer-3 process containment.
  No D1/D2 reversal; new cap families added in EP-0038.
- **EP-0017** (Tool Surface Policy and Plugin Approval UI) —
  EP-0037 extends with `[tools.autoload]`, dotted naming,
  wildcard glob support, and meta-tool dispatch. EP-0017's
  `[tools.enabled]` / `[tools.disabled]` semantics retained;
  D1 ("tool visibility as policy surface") and D2 ("approval
  plugin-scoped via `ui:approval`") unchanged.
- **EP-0029** (Config-introspection host imports) — aligned
  with the lean-core direction. EP-0038 will likely add more
  `cfg:*` fields (`cfg:config_dir`, `cfg:plugin_install_dir`)
  for bundled-as-wasm doctor / gc plugins.
- **EP-0031** (`fs:read:cfg:state_dir/...` path templates) —
  aligned and used. Bundled-as-wasm `plugin doctor` (EP-0038)
  will declare both `cfg:state_dir` and
  `fs:read:cfg:state_dir/plugins`.
- **EP-0035** (Project-local `.stado/` directory) — extended.
  Schema growth: `[tools.autoload]`, `[plugins.<name>]`,
  `[sessions]`, `[agents]`, `[sandbox]`. EP-0035's discovery
  walk and load order unchanged.

### Companion EPs (drafted in same conversation)

- **EP-0038** (ABI v2, no-native-tools invariant, agent surface,
  sandbox impl) — depends on EP-0037's dispatch model + config
  schema. Not blocked by EP-0037 acceptance, but lands after.
- **EP-0039** (Plugin distribution and trust) — independent of
  EP-0037 mechanics; can land in parallel.

### External references

- Anthropic Messages API tool schema:
  `name` matches `^[a-zA-Z0-9_-]{1,64}$`. Documented constraint
  driving the dotted-canonical / underscored-wire convention.
- Claude Code's `/permissions` slash command — referenced in D8
  as the precedent for per-session-by-default slash semantics.
- Homebrew tap convention — referenced in EP-0039; EP-0037's
  `[plugins.<name>]` config tables follow the same per-plugin
  configuration scoping idiom.
