
---

## Assistant
*2026-05-05T06:40:42.901Z*

# Stado architectural reset — final summary

## The invariant

**Stado core has no built-in tools.** It exposes a host-import surface; every tool is a wasm plugin. A curated set is bundled by default, embedded into the binary; users add, replace, or disable any of them.

## What gets deleted

- `internal/runtime/bundled_plugin_tools.go` — every `r.Register(NativeTool{})` line.
- `newBundledPluginTool` / `buildBundledPluginRegistry` — the wasm-wrapper-over-native fakery.
- `internal/tools/{bash,rg,astgrep,lspfind,webfetch,httpreq,readctx,tasktool}` as registered tools.

The Go code in `internal/tools/*` doesn't disappear — it migrates to `internal/host/*` as host-import implementation only.

## Host-import surface (ABI v2)

```
Tier 1 — Capability primitives (host-only, lazy-init)
  stado_proc_{spawn,read,write,close,wait,kill}
  stado_exec(...)                                              # sugar over proc
  stado_terminal_{open,read,write,resize,close,wait}
  stado_net_dial(transport, addr, opts) → handle               # tcp | udp | unix
  stado_net_listen(transport, addr, opts) → listen_handle      # tcp | unix
  stado_net_accept(listen_handle, timeout)
  stado_net_{read,write,close}(handle, ...)
  stado_net_icmp_{open,send,recv,close}                        # full raw ICMP
  stado_fs_*                                                   # kept
  stado_bundled_bin(name) → path                               # lazy extract
  stado_session_*, stado_llm_invoke, stado_log,
  stado_approval_request                                       # existing

Tier 2 — Stateful conveniences (lazy-init)
  stado_http_client_{new,close}
  stado_http_request(client, ...)
  stado_http_request_streaming(client, ...) → response_handle
  stado_dns_resolve(name, type, opts?)
  stado_dns_resolve_axfr(name, server)
  stado_secrets_{get,set,delete,list}

Tier 3 — Stateless format conveniences
  stado_json_{parse,stringify}                                 # strict RFC 8259
  stado_hash(algo, bytes), stado_hmac(algo, key, bytes)        # md5,sha1,sha256,sha512,blake3
  stado_compress(algo, bytes), stado_decompress(algo, bytes)   # gzip,brotli,zstd
```

**Capability vocabulary** (manifest declarations):
`exec:proc`, `terminal:open`, `net:dial:<transport>:<host>:<port>`, `net:listen:<transport>:<port>`, `net:listen:privileged`, `net:icmp[:<host>]`, `bundled-bin:<name>`, `dns:resolve[:<glob>]`, `dns:axfr:<zone>`, `dns:reverse:<cidr>`, `secrets:read:<key>`, `secrets:write:<key>`, `crypto:hash`, plus existing `fs:read:<path>`, `fs:write:<path>`, `net:http_request[:<host>]`, `session:*`, `llm:invoke:<budget>`, `ui:approval`.

## Tool naming convention

- **Canonical form (docs, config, manifest, CLI)**: dotted. `fs.read`, `shell.exec`, `web.fetch`, `agent.spawn`, `tools.search`.
- **Wire form (LLM-facing)**: underscore. `fs_read`, `shell_exec`. Synthesized as `<plugin_name>_<tool_name>` at registration.
- **Plugin design idiom — family + default**: a plugin wrapping a family of similar implementations exposes both a default tool and per-implementation tools. E.g. `shell` plugin: `shell.exec` (uses `[plugins.shell] binary = ...`) + `shell.bash`, `shell.zsh`, `shell.fish`, `shell.sh`, `shell.pwsh` for explicit forcing. Same idiom for `agent.spawn` + `agent.opus`/`agent.haiku`. Host enforces nothing here; it's a convention.

## Tool dispatch — meta-tool, not always-loaded

Stado stops broadcasting every tool's schema in the system prompt. Always-loaded core is small; everything else lives behind `tools.search` / `tools.list` / `tools.describe`.

**Default always-loaded core**: `tools.search`, `fs.read`, `fs.write`, `fs.edit`, `fs.glob`, `fs.grep`, `shell.exec`.

**Configurable, four-layer precedence** (highest wins):
1. CLI flags
2. Project `.stado/config.toml`
3. User `~/.config/stado/config.toml`
4. Default core

**Wildcards**: `*` matches one segment within a namespace. `fs.*`, `tools.*`, `htb-lab.*`, plain `*` for everything.

**Disabled-wins-over-always-loaded** when both apply, to prevent silent override surprises.

```toml
# .stado/config.toml example (htb-writeups project)
[tools]
always_loaded = [
  "tools.*", "fs.*", "shell.*",
  "payload-generator.revshell",
  "netexec.command",
  "hash.identify",
  "htb-lab.spawn", "htb-lab.active",
]
disabled = ["browser.*"]

[plugins.shell]
binary = "/usr/bin/zsh"

[plugins.htb-lab]
default_token_path = ".secrets/htb_app_token"
```

**CLI flags** — three, semantically distinct:
- `--tools <list>` — whitelist (lockdown mode; *only* these available)
- `--tools-always <list>` — additive pin to always-loaded
- `--tools-disable <list>` — subtractive remove from availability

All accept comma-separated globs.

## Lazy-init / first-call-extract — universal rule

Applied to every stateful or expensive resource:
- Bundled native binaries (rg, ast-grep) — extracted to disk on first `stado_bundled_bin(name)` call.
- Bundled wasm plugins — wasm modules instantiated on first tool invocation, not at startup.
- HTTP clients, LSP processes, terminal handles, secret store backend.
- JSON / hash / compression engines — instantiated on first use.

Process startup does only registration, not initialisation.

## Tool inventory after migration

**Bundled (default-on, in `internal/bundledplugins/wasm/`)**:
- `fs` (read/write/edit/glob/grep), `shell` (exec/spawn + per-shell variants), `web` (fetch/search/browse), `http` (request, client_new), `lsp` (definition/references/symbols/hover), `rg` (search), `astgrep` (search), `readctx`, `task` (add/list/update/complete), `agent` (spawn), `mcp` (connect/list_tools/call), `image` (info), `tools` (search/list/describe), `dns` (resolve), plus existing `auto-compact` background plugin.
- Includes the five examples already written: `web-search`, `mcp-client`, `ls`, `image-info`, `browser`. Recompile-only.

**Third-party (out-of-tree, e.g. `~/Dokumenty/htb-writeups/htb-toolkit/`)**:
- 11 of 12 HTB plugins recompile-only (pure command emitters, fs-readers, output parsers).
- `htb-lab` optionally adopts `stado_http_client` + `stado_secrets`. No logic change required.

## Migration plan — two EPs

**EP-0037 — Tool-search dispatch.** No ABI changes. Lands first.
- Add `tools.search` / `tools.list` / `tools.describe` to the registry surface.
- Default always-loaded core; `.stado/config.toml` `[tools]` section; three CLI flags; wildcard globs.
- Removes prompt-budget pressure as a constraint on bundling.

**EP-0038 — ABI v2 + no-native-tools invariant.** Lands after EP-0037.
- Adds the full Tier 1/2/3 import surface above.
- Migrates `internal/tools/*` into `internal/host/*` (implementation moves; registrations delete).
- Writes wasm versions of every native default tool; embeds via `internal/bundledplugins/wasm/`.
- Versions ABI as `v2`; manifests declaring `v1` keep working against the existing import surface (aliased forward where semantics match).
- Documents the invariant in the EP itself: stado core ships no native tools, ever.

## What's locked

- Tool naming convention (dotted canonical / underscored wire).
- Always-loaded core, four-layer config precedence, wildcards, three CLI flags.
- Tier-1 primitives, Tier-2/3 conveniences as listed.
- ICMP fully raw (no `_ping` convenience).
- DNS via stub resolver convenience + raw via `net_dial("udp",...)`.
- `net.listen` for callbacks/webhooks/automation; not the workhorse for HTB reverse-shell ops.
- Lazy-init universal.
- Strict RFC 8259 JSON, md5/sha1/sha256/sha512/blake3, gzip/brotli/zstd both directions.
- `agent.spawn`, `shell.exec` + `shell.spawn` (plus per-shell variants at plugin-dev discretion).
- Plugin family idiom (default tool + dotted variants).

Ready to draft EP-0037 (small, no-ABI) first. Say go and I'll start writing.

---

## User
*2026-05-05T06:48:55.112Z*

I wonder... in order to help the llm model to find the tool it needs how about we could add somthing like tools.category_list() -> [string], tools.category_search(string) -> [string], tools.search_by_category(string) -> [string] (or [dict{}] ? - I think the former and model can get tools.describe then for more details)


---

## Assistant
*2026-05-05T06:50:23.457Z*

Good idea — and there's a refinement worth making.

## API shape — drop one, refine the others

Three tools is one too many. `category_search(string) → [category]` returns categories matching a query; given there'll be ~15-25 categories total, the model can just call `categories()` and pick. Dropping it loses nothing.

So the meta-tool surface becomes:

| Tool | Returns |
|---|---|
| `tools.search(query)` | `[{name, summary, categories}]` — text match across name/summary/categories |
| `tools.list()` | `[{name, summary, categories}]` — everything (mostly debug; could be capped) |
| `tools.describe(name)` | full JSONSchema + long-form docs + examples |
| `tools.categories()` | `[string]` — flat list of all category names |
| `tools.in_category(name)` | `[{name, summary}]` — tools in this category |

`in_category` reads better than `search_by_category` — there's no query, just a category name. And it parallels `tools.search` (which *does* take a query).

## Return shape — `[{name, summary, categories}]`, not `[string]`

You leaned `[string]` + force `tools.describe` for details. I'd push back. Reasons:

- **Round-trip economics.** Model calls `tools.in_category("crypto")` → gets `["hash.compute", "hash.identify", "hash.crack-command", "encrypt.aes", ...]`. To pick which one to use, it must call `tools.describe` on each candidate. With 5 candidates, that's 5 round trips. With `[{name, summary}]`, it picks in one call.
- **Consistency with `tools.search`.** That one *must* return summaries — the whole point is "find me a tool" without already knowing the name. If `search` returns `[{name, summary}]` and `in_category` returns `[string]`, the model has to learn two different result shapes for what's effectively the same task ("show me candidate tools").
- **Token cost is small.** Each summary is one sentence, ~25 tokens. 20 results = 500 tokens per call. Negligible vs. the cost of 20 round-trip `describe` calls.
- **Categories visible inline.** Including `categories` in the result lets the model cross-reference: "this is in `crypto` AND `ctf-offense`, so it's a CTF-flavoured crypto tool" — useful triage signal.

`tools.describe` stays the deep dive: full JSONSchema, longer docs, example invocations. Used when the model has narrowed to one tool and needs the schema before calling.

## Where categories come from — manifest-declared, curated taxonomy

Three sub-decisions:

**1. Plugin manifest declares categories per tool, not per plugin.** A plugin can have tools spanning categories — `payload-generator` has `payload.revshell` in `[ctf-offense, shell]`, `payload.listener` in `[ctf-offense, network]`, `payload.msfvenom` in `[ctf-offense, exploit-dev]`. Per-plugin would lose that resolution.

```json
{
  "name": "payload-generator",
  "tools": [
    {
      "name": "revshell",
      "categories": ["ctf-offense", "shell"],
      "schema": "...",
      "description": "..."
    }
  ]
}
```

**2. Curated canonical taxonomy, with explicit `extra_categories` escape hatch.** Plugin authors picking from a fixed list keeps the surface usable; freeform tagging produces "network" / "networking" / "net" fragmentation. But blocking plugin authors from coining new categories at all is too rigid. Compromise:

- `categories: [...]` — must be from the canonical list. Install fails if any value isn't.
- `extra_categories: [...]` — free-form. Indexed and searchable, but flagged in `tools.describe` output as `(plugin-defined)` so the model treats them with appropriate skepticism.

**3. First-pass canonical taxonomy** — ~20 names that cover today's bundled + HTB plugins:

```
filesystem, shell, network, web, dns, crypto, data, encoding,
code-search, code-edit, lsp, agent, task, mcp, image, secrets,
documentation, ctf-offense, ctf-recon, ctf-postex, meta
```

Lightly more granular than alphabetic alone — three CTF facets because HTB workflows really do split that way (recon → exploit → postex). `data` covers JSON/YAML/encoding. `documentation` is for knowledge-base plugins like `gtfobins`, `cve-index`. `meta` is `tools.*`. Consensus-list, not hyper-precise.

## Worth adding to EP-0037

Adds:
- Two meta tools (`tools.categories`, `tools.in_category`).
- One manifest field (`tools[].categories: []` + optional `extra_categories: []`).
- One install-time validator (canonical-list check).
- The canonical taxonomy list, frozen as part of the EP (changes only by amendment).

Doesn't add: any new ABI, any new caps, any new host imports. Pure registry mechanics. Fits the EP-0037 scope cleanly.

## Updated locked decisions

Add:
- Meta-tool surface is **five tools**: `tools.search`, `tools.list`, `tools.describe`, `tools.categories`, `tools.in_category`.
- All result lists return `[{name, summary, categories}]`, not bare strings.
- Categories: manifest-declared per-tool, drawn from a frozen canonical taxonomy of ~20 names, with `extra_categories` for plugin-defined-but-discouraged extensions.
- Initial canonical list: `filesystem, shell, network, web, dns, crypto, data, encoding, code-search, code-edit, lsp, agent, task, mcp, image, secrets, documentation, ctf-offense, ctf-recon, ctf-postex, meta`.

Now I'll write EP-0037. Will draft into `docs/eps/EP-0037-*.md` (or wherever your EP convention lives — let me check) and ping you for review before any code touches the registry.

---

## User
*2026-05-05T06:51:54.903Z*

agent.spawn, agent.list, agent.cancel, agent.read, agent.write (? can we?)... one correction tools.categories() - gets all categories but tools.categories(name) filters the result same with tools.list() vs tools.search()? or does it help to have two distinct?


---

## Assistant
*2026-05-05T06:53:15.062Z*

Both good questions. Working through them.

## `agent.*` surface — yes, including `write`

`agent.write` is technically possible if we commit to **agents running async by default** under the hood. Mechanism: each spawned agent has an inbox channel (parent → child) and outbox channel (child → parent); the parent's tool call doesn't block on the child's loop, it just enqueues/drains. Messages sent via `agent.write` are delivered at the child's next yield point (after the current assistant turn completes — never mid-tool-call).

Synchronous spawn is then sugar over async: `agent.spawn(prompt)` is shorthand for `spawn-async → read-until-done → return final output`. Same code path.

Proposed surface:

| Tool | Behaviour |
|---|---|
| `agent.spawn(prompt, model?, sandbox?, async?)` | Default `async: false` blocks until completion, returns final assistant output. `async: true` returns a handle immediately. |
| `agent.list()` | `[{id, status, model, started_at, last_turn_at}]` — running + recently-completed (configurable retention). |
| `agent.read(id, since?, timeout?)` | Drain outbox: returns new messages since the last offset, plus current status. Blocks up to `timeout` if nothing's available. |
| `agent.write(id, message)` | Enqueue a user-role message into the agent's inbox. Delivered at next yield point. |
| `agent.cancel(id)` | Send cancel signal; child's tool loop exits at next yield. |

What `write` *can't* do: interrupt a tool call in progress. The semantic is "queue a message; the agent picks it up after its current turn ends." If the parent needs a hard stop, that's `cancel`.

Two real complexities to flag:

1. **Race.** Parent reads partial output → decides → writes. Between read and write, the agent might have already finished its turn and pulled the inbox empty (no message), then concluded the conversation. So `write` doesn't guarantee the message is consumed; it might land after the agent decided it was done. Solution: agent's "conversation done" condition is "no inbox message AND no continuation triggered" — checked atomically. If `write` arrives first, agent picks it up. If agent has already finalized, `write` returns `{ok: false, reason: "agent_terminated"}`. Predictable.

2. **Resource lifecycle.** Async agents survive the parent's tool-call return. Need a reaper for orphaned/abandoned agents (parent never calls `read` again). Sensible: agents auto-terminate after N seconds of no `read` activity, configurable via `[agents] idle_timeout`.

Worth shipping all five in v1 — the cost over `spawn`+`cancel`-only is the inbox/outbox plumbing, which we need for `read` regardless. Adding `write` on top is ~20 lines and unlocks supervisor-style patterns (parent steers child mid-conversation) that today require restarting from scratch each time.

## Meta-tool consolidation — collapse `list` into `search`

You're right. `tools.list()` and `tools.search()` are the same operation with different defaults. A single tool with optional query is cleaner:

```
tools.search(query?)         # no arg → all tools; with arg → filtered
tools.describe(name)         # full schema + docs for one tool
tools.categories(query?)     # no arg → all categories; with arg → filtered (substring on category name)
tools.in_category(name)      # tools in a specific category
```

**Four tools, not five.** Drop `tools.list`.

`tools.in_category` stays distinct because it has different semantics: mandatory exact-name argument, returns tools (not category names), different result shape. Trying to collapse it into `tools.search` would require overloading the query syntax (`tools.search("category:crypto")` style), which makes the search interface lossier.

`tools.categories(query?)` mirrors `tools.search(query?)`:
- `tools.categories()` → `["filesystem", "shell", "network", ...]` (all 20)
- `tools.categories("ctf")` → `["ctf-offense", "ctf-recon", "ctf-postex"]` (substring-filtered)

Same optional-query convention, easy to remember.

## Updated locked decisions

- **Agent surface** = five tools: `agent.spawn`, `agent.list`, `agent.read`, `agent.write`, `agent.cancel`. Async-under-the-hood; sync default with `async: true` opt-in. Inbox/outbox handle table per agent. `idle_timeout` reaper.
- **Meta-tool surface** = four tools: `tools.search(query?)`, `tools.describe(name)`, `tools.categories(query?)`, `tools.in_category(name)`. `tools.list` deleted before it shipped.
- **Default always-loaded core updated**: `tools.search`, `tools.categories`, `fs.read`, `fs.write`, `fs.edit`, `fs.glob`, `fs.grep`, `shell.exec`. Adding `tools.categories` because it's the cheap entry point for "what kinds of tools exist?" — model can browse the taxonomy without spending tokens on schemas.

Two more things to confirm before I draft EP-0037:

1. `tools.describe` — return one tool, or accept a list of names? Current spec is one. If batched, a model browsing 5 candidates needs one call instead of 5. I'd lean **accept a list** (`tools.describe(names: [str])`) for the same round-trip economics argument that drove `[{name, summary}]` over `[string]`.
2. `agent.list` — show only this session's children, or all live agents in the stado runtime? Sandboxing argues for "this session only" (a plugin shouldn't see another plugin's agents). Confirm.

Sign off and I start writing.

---

## User
*2026-05-05T06:55:45.060Z*

so instead agent.write(id, msg) agent.send_message(id, msg).. also agents should get their own session, thoughts? not only for their own use/benefit but also for traceability...



---

## Assistant
*2026-05-05T06:57:01.436Z*

Both lock in cleanly.

## `agent.send_message` — yes, rename

`send_message` is more descriptive than `write` — and `write` was always going to clash visually with `fs.write` in the model's tool list ("does write a file or write a message?"). Cleaner.

For symmetry I'd rename `agent.read` to `agent.read_messages` — both ends of the channel have parallel structure (`send_message` ↔ `read_messages`, both pluralised, both explicit about the unit). The asymmetric pair `read`/`send_message` is fine but the parallel reads better.

```
agent.send_message(id, msg)
agent.read_messages(id, since?, timeout?) → {messages, offset, status}
```

## Agent owns a session — yes, this is load-bearing

Two reasons that compound:

**1. Architectural consistency.** A "session" in stado is already a first-class object — persistent record of a conversation, message history, fork lineage, capability scope, cost tracking. The `session:observe`, `session:fork`, `session:read` caps exist precisely for this. Agents *should* be sessions because that's the only object stado already has for "an entity having a conversation."

The current native `subagent` is the odd one out: it forks the parent's session implicitly but doesn't expose the resulting session as an addressable object. Replacing `subagent` with `agent.spawn` is also the moment to fix that — every spawned agent returns a session ID that the operator (or another tool) can later open, observe, or replay.

**2. Beautiful collapse of "agent" and "human session".** With agents-own-sessions, `agent.send_message(id, msg)` is *literally the same operation* as `stado run --resume <session_id>` with a user-role message. The agent's run loop reads from its session's pending-message queue; it doesn't know whether the message was typed by a human at a TTY or queued by a parent agent calling `send_message`. There's only one conversation primitive — the session — and "agent" is just "a session whose message queue is being driven by a parent process instead of a human."

This means:
- `agent.list` ≈ "list active sessions whose driver is a parent agent (not a human)"
- `agent.read_messages(id)` ≈ `session.read_recent_messages(session_id)`
- `agent.cancel(id)` ≈ `session.cancel(session_id)`
- A human can open an agent's session in their own terminal, type a message, and now *they're* the driver. Mid-flight handover is a free property of the architecture, not a feature.

## Implications worth surfacing

**Session metadata gains a "driver" field.** Values: `human` (started by `stado run`), `agent:<parent_session_id>` (spawned by a parent's `agent.spawn`), `cron:<routine_id>` (scheduled), `bridge:<plugin_name>` (started by something like auto-compact). Lets `session.list` filter by who's driving.

**Forking lineage stays intact.** The parent session's record gets a child-pointer (`spawned_agent: <agent_session_id>`); the child has a parent-pointer. Standard tree. No new model.

**Storage cost real but manageable.** A parent spawning 100 small agents in parallel = 100 persistent session records. Three knobs:
- `[agents] retention = "30d"` — auto-prune completed agent sessions older than N.
- `agent.spawn(..., ephemeral: true)` opt-in — session record is deleted on agent completion, no replay possible. Useful for one-shot disposable agents in tight loops.
- `[agents] retention_policy = "summary_only"` — keep the trace, drop full message contents after N days.

Sensible default: 30-day retention, full content. Operators tune from there.

**Cost attribution comes for free.** LLM token usage is already tracked per session, so per-agent cost is already separable. `agent.list` can include `cost_so_far_usd` per agent. Useful for budget-bounded agent fleets.

**Recursive spawning works.** An agent can call `agent.spawn` itself (subject to `session:fork` cap and `[agents] max_depth` config). Each level gets its own session; lineage tree records the chain.

**`agent.spawn` parameters.**

```
agent.spawn(
  prompt,                   # required
  model?,                   # default: parent's model
  sandbox_profile?,         # default: parent's profile (capability scope inherited)
  async?,                   # default: false (block until done)
  ephemeral?,               # default: false (persist session)
  parent_session?,          # default: caller's session (sibling spawn = pass null/'detached')
  allowed_tools?,           # default: parent's; override to narrow capability surface
)
→ { id, session_id, status, ... }
```

`session_id` returned alongside `id` even though they're the same thing logically — different keys signal "use `agent.*` to interact, use `session.*` to introspect." Same value, two affordances.

**`agent.list` scope** — confirm: this session's children (and their descendants?), not the global stado runtime. A plugin shouldn't see another plugin's agents. So `agent.list` is recursive over the caller session's spawn tree, capped at the session boundary.

For an operator who *does* want global view, that's `session.list --driver-prefix=agent:` from the CLI — separate concern, not a plugin tool.

## Updated locked decisions

- **Agent surface** = five tools: `agent.spawn`, `agent.list`, `agent.read_messages`, `agent.send_message`, `agent.cancel`.
- **Every agent owns a session.** Returned as `session_id` in spawn output. `agent.list` shows session IDs; operator can introspect via `session.*` CLI/tools.
- **`agent.list` scoped to caller's spawn tree** — not global runtime view.
- **Session metadata adds `driver` field.** `human`, `agent:<parent>`, `cron:<routine>`, `bridge:<plugin>`.
- **Retention defaults**: 30-day. `[agents] retention`, `ephemeral: true`, `retention_policy = "summary_only"` as escape hatches.
- **Recursive spawning** allowed; capped via `[agents] max_depth` (sane default: 5).
- **`tools.describe` accepts a list of names** (from prior message, want to confirm — round-trip economics argument).

Three more low-stakes confirmations before EP-0037 draft:

1. **`agent.read_messages` rename — go?** (or stay `agent.read`?)
2. **`tools.describe` batched — go?** (`tools.describe(names: [str])`)
3. **Agent default model**: inherits parent's? Or always the cheapest model unless specified? (Cheapest avoids accidental cost blowup when a Sonnet parent spawns 50 agents and each runs Sonnet too. But "smarter than parent" is sometimes wanted — e.g. Haiku parent dispatches Opus for hard sub-tasks.) I'd lean **inherits parent's, opt-out via `model: "haiku-4.5"`** — surprise-minimum default. Confirm.

Sign off on these three and EP-0037 starts.

---

## User
*2026-05-05T06:59:37.719Z*

aren't sessions used for 'stado usage/stats' ? they should always persist, until cleaned up explicitly I think.. also since model/agent can read other's session directly via session:observe session:read agent.read_messages would offer two different, but each useful in its own way, channels to communicate


---

## Assistant
*2026-05-05T07:00:25.166Z*

Right on both — corrections.

## Sessions persist forever by default

Walking back the 30-day retention. Sessions are part of the durability/audit model and feed `stado usage/stats` — auto-pruning them would silently drop usage history. The default is **never auto-prune**; cleanup is explicit:

- `stado session prune --before <date>` / `stado session delete <id>` — operator-driven CLI cleanup.
- `[sessions] auto_prune_after = "90d"` — opt-in time-based retention for operators who want it (off by default).
- `agent.spawn(..., ephemeral: true)` — opt-in per-spawn knob. Skips session persistence entirely; useful in tight loops where the parent doesn't care about replay/audit and is rate-spawning hundreds of one-shots. Trade: no `session.read`, no `stado usage/stats` line for that agent's tokens — costs roll up to the parent.

The runaway-agent concern was the wrong concern: disk space isn't the bottleneck; recursion depth + per-step rate are. Those are addressed by `[agents] max_depth` and per-call quotas, not by retention policy.

## Two channels for inter-agent communication — keep both, distinct

This is the right architectural read. They serve genuinely different purposes:

| Channel | What it sees | Who controls visibility | Use case |
|---|---|---|---|
| `agent.read_messages(id)` | **Only assistant-role messages** the child explicitly emits as outputs | Child curates (decides what to "say" back to parent) | Function-call-return shape — "what's the result?" |
| `session.read(session_id)` / `session.observe(session_id)` | Full transcript: assistant turns + tool calls + tool results + internal reasoning + system prompts | Bypassed — sees everything regardless of child intent | Audit trail, supervision, debugging, training data |

Critical refinement: **`agent.read_messages` filters to assistant-role messages only.** It's the child's *intended* output channel. If a parent wants the unfiltered execution trace — every `fs.read`, every reasoning token, every tool failure the child handled silently — it uses `session.read` against the agent's `session_id`.

This split has a nice consequence: a child can keep its tool-call noise out of the parent's context (parent calls `agent.read_messages`, gets a clean summary), while still being fully auditable when needed (operator or supervisor agent calls `session.read`). Different signal-to-noise ratios for different reader needs.

Caps stay clean:
- Calling `agent.read_messages(id)` requires owning the agent (parent of that spawn). No cap needed beyond ownership.
- Calling `session.read(id)` / `session.observe(id)` for someone else's session needs explicit `session:read:<id>` or `session:observe:<id>` cap. The agent's session_id is observable to its parent by default; sharing beyond that requires explicit cap grant.

## Updated locked decisions

- **Sessions persist forever by default.** No auto-prune. Explicit cleanup via `stado session prune` / `delete`. Opt-in `[sessions] auto_prune_after`. Per-spawn `ephemeral: true` opt-out for one-shots.
- **Two communication channels for agents, both first-class:**
  - `agent.read_messages(id, since?, timeout?)` — assistant-role messages only, child-curated, parent-owned by default.
  - `session.read(session_id)` / `session.observe(session_id)` — full transcript, bypasses child curation, requires `session:read|observe` cap if not the spawn parent.
- **`agent.spawn` returns `{ id, session_id, ... }`** — both keys exposed: `id` for `agent.*` ops, `session_id` for `session.*` introspection. Same value, different affordance.

Three confirmations still standing from last message:
1. `agent.read` → `agent.read_messages` rename.
2. `tools.describe(names: [str])` batched.
3. Agent default model: inherits parent's, opt-out via `model:` arg.

Sign off on those three and EP-0037 starts.

---

## User
*2026-05-05T07:05:43.274Z*

worth highlighting something that may be unacknowledged so far... even though security capabilities are at the heart of stado the default is 'yolo' approach. The whole reason I went with plugins as a core concept was that I wanted to leave up to plugin developers to decide what, when and how will be controlled... a more conservative plugin dev may add a lot more checks and balances before letting model do something and ask user via approvals, while other plugin dev will go with full speed / no permissions. Adding ability to additionally sandbox the plugins and stado's process capabilities is just the absolute wall that can be put in place if/when needed. In other words stado is more of a lean power tool with a lot of modules. Even though stado is not locked down at all out of the box it takes implementing security components/constructs seriously but leaving it to the end use to decide how they want their security implemented.


---

## Assistant
*2026-05-05T07:07:16.514Z*

Worth making explicit, agreed — this reframes a lot of what we've designed and needs to land in the EPs as a stated principle, not an emergent property.

## The philosophy, written out

**Stado is permissive-by-default, primitive-rich, layered.** Three positions:

1. **Plugin authors set policy.** Each plugin's manifest is a declaration of what it does, not a request stado evaluates against rules. A paranoid plugin author wraps every dangerous call in `approval.request` and refuses ambiguous inputs; a "go fast" author skips all that. Stado doesn't grade either. The plugin ecosystem is allowed to span "hardened CTF-grade tool with audit trail" through "one-shot script that yolo-shells anything." Same primitives, different policies, same first-class status.

2. **Operators set enforcement scope.** The capability vocabulary in manifests, the `--tools` whitelist, `[tools.disabled]`, `--tools-always`, the OS sandbox runner, the `[plugins.<name>] config` overrides — these are operator-side controls. The same plugin behaves identically wherever it's installed *unless the operator chooses to constrain it*. Stado gives operators the levers; stado doesn't pull them.

3. **Stado guarantees primitives, not policies.** What stado *does* commit to: capability declarations are honoured (a plugin without `net:dial:tcp:foo:443` cannot open that connection); the sandbox runner integrates correctly when present; approval prompts reach the operator through a known channel; sessions are durable and auditable; signature verification on plugin install isn't bypassable. What stado does *not* commit to: any particular default level of restriction, any prescribed minimum capabilities for tool categories, any opinion on whether a plugin's caps "seem reasonable."

The slogan: **lean power tool with modules.** Not "AI assistant with guardrails." The guardrails are modules too.

## Why this is non-obvious and worth stating

It's the inverse of how Claude Code, ChatGPT plugins, and most LLM-tool-runners orient. They lean *prescriptive defaults* — every tool call is gated by user approval out of the box, capability vocabularies are short and stado-defined, sandbox is mandatory. That's a defensible position, but it's not stado's. Stado is a power tool; the user is assumed to be the one with their hand on it; the security primitives are there for when they want to put walls up, not as the default scaffolding.

Operationally, this changes what "default behaviour" means in EP-0037 and EP-0038:

- **Always-loaded core defaults** = "what's convenient by default," not "what's safe by default."
- **Empty capability list in a manifest** = unrestricted (within OS limits), not "needs review."
- **Approval prompts** = a primitive plugin authors and operators choose to use, not a default checkpoint.
- **Sandbox runner missing** = a capability gap operators choose how to handle, not an automatic refusal.
- **Tools-search-by-default dispatch** = ergonomic prompt reduction, not "hide tools to make things safer."

## Implications across what we've designed

Some of these are clarifications of decisions already made; some are tensions we should resolve before locking the EP.

**Clarifications (no design change, just framing):**

- `--tools` whitelist is the *operator-tightening* mode, named for what it does, not "secure mode" or "safe mode."
- Plugin manifest's empty `capabilities: []` is *not* a security smell; it's the right shape for pure-templating plugins like `gtfobins` and `payload-generator` that don't touch state.
- `extra_categories` (free-form) is fine as a feature; the canonical taxonomy is *suggestion*, not enforcement.
- Default always-loaded core is `tools.search, tools.categories, fs.*, shell.exec` — chosen for ergonomics; if you want a stricter core, set it.

**Tensions worth resolving in the EP:**

- **EP-0028's hard refusal of `exec:bash` on no-sandbox hosts.** Today stado refuses outright when `sandbox.Detect()` returns NoneRunner. Per this philosophy, the right shape is probably *warn loudly, run anyway, document the override-to-silent flag*. The operator chose to install a plugin declaring `exec:bash` on a host without a syscall filter; stado's job is to make that visible, not to override their choice. Worth proposing as a behaviour change in the EP.

- **`net:listen:privileged` cap blocking ports < 1024.** Same philosophy applies. Don't invent a stado-side rule for what the OS already enforces. Drop the synthesised cap; let the kernel be the gate.

- **`CAP_NET_RAW` gating on ICMP.** Same. The kernel says yes/no; stado doesn't add a layer.

In each case the move is from *stado decides* to *stado describes; operator decides*. The cap declarations stay (so operators see what plugins want); stado-side rules over and above the OS go.

**A specific positive consequence:**

The two-channel `agent.read_messages` vs `session.read|observe` split — that division falls out *naturally* from this philosophy. `agent.read_messages` is the convenience channel (what the child intends to communicate); `session.read|observe` is the audit channel (what actually happened). A paranoid parent agent uses the audit channel; a trusting one uses the convenience channel. Same plugin, different operator/parent posture, identical stado behaviour. That's exactly the layering this philosophy implies.

## Where this lands

EP-0037 should open with a "Philosophy" section explicitly stating the three positions above. EP-0038 reiterates and applies it to the new capability vocabulary (every cap is a *declaration*, not a *gate stado adds beyond what the OS provides*).

Concretely added to locked decisions:

- **Permissive-by-default, primitive-rich, layered** is the stated security philosophy. Both EPs cite it as the rationale for default behaviours.
- **Plugin authors own policy**; **operators own enforcement scope**; **stado owns primitive correctness** (caps honoured, sandboxes integrated, approvals routed, sessions durable, signatures verified).
- **EP-0028 exec:bash hard-refusal becomes warn-loud-run-anyway** unless the operator opts back in. (Proposed in EP-0038, not silent change.)
- **Cap-vs-OS overlap rule**: where the OS is already the gate (port < 1024, CAP_NET_RAW), stado doesn't add a parallel refusal. The cap declaration stays for visibility; enforcement is the kernel's.
- **Default selections** (always-loaded core, retention defaults, etc.) are framed as "convenient" rather than "safe."

Three confirmations still standing:
1. `agent.read_messages` rename — confirm.
2. `tools.describe(names: [str])` batched — confirm.
3. Agent default model: inherits parent's — confirm.

And one new one this message produced:
4. **EP-0028 exec:bash refusal → warn+run** under the new philosophy — confirm or push back. (Real behaviour change for existing plugins; worth being explicit about whether this is in scope for EP-0038 or stays as-is.)

Sign off on those four and EP-0037 drafting starts.

---

## User
*2026-05-05T07:10:59.556Z*

yes, the operator/user should be able to use the kernel permissions to cap what the plugins can do - this provides extra layer of protection if the user so wishes from misbehaving plugins or model abusing a plugin... sandboxing fs, network (or forcing a use of a proxy) etc etc are those primitives which should be configurable (user/project level)... some old EPs or docs that contradict today's decisions and stado's shape going forward need to be acknowledged in new EP(s) and marked as superseeded..

---

## Assistant
*2026-05-05T07:12:44.770Z*

Both right. The third layer needs explicit treatment, and EP hygiene matters before we add two more.

## Operator-side sandboxing — third layer, explicit

The layered model written out:

| Layer | Mechanism | Decided by |
|---|---|---|
| 1. Plugin policy | manifest caps, `approval.request` calls, internal sanitisation | Plugin author |
| 2. Plugin admission | `--tools`, `[tools.disabled]`, accept/reject caps at install | Operator (per stado run / project) |
| 3. Process containment | OS namespaces, proxies, fs binds, capability drops, cgroups | Operator (per host / per project / external wrap) |

Layer 3 is what was missing from the locked decisions. Stado should make it first-class:

**`[sandbox]` section in `.stado/config.toml`** — operator-configurable, project- or user-scoped. Things stado-the-process can effect from the inside (env, proxies, DNS server) plus things stado can drive a wrapper to enforce on its own re-exec (mounts, namespaces, dropped capabilities):

```toml
# .stado/config.toml
[sandbox]
mode = "off" | "wrap" | "external"   # default "off" — yolo by design
                                      # "wrap" = stado re-execs itself under detected wrapper
                                      # "external" = stado refuses to start unless wrapped externally

# In-process settings (work in any mode, including "off")
http_proxy = "http://127.0.0.1:8080"  # forced into HTTP_PROXY/HTTPS_PROXY env for plugin runtime
dns_servers = ["1.1.1.1", "9.9.9.9"]  # override stado_dns_resolve to use only these
drop_env = ["AWS_*", "GITHUB_TOKEN"]  # stripped before any plugin runs
plugin_runtime_caps_drop = ["cap_net_raw", "cap_dac_override"]  # capng before wasm runtime starts

# Wrapper-mode-only settings (mode = "wrap")
[sandbox.wrap]
runner = "auto"  # auto | bwrap | firejail | sandbox-exec
bind_ro = ["/usr", "/etc/resolv.conf"]
bind_rw = ["~/Dokumenty/htb-writeups"]
network = "host" | "namespaced" | "off"
```

**`stado run --sandboxed` flag** — explicit per-invocation override, equivalent to `[sandbox] mode = "wrap"` for one run. Supersedes config when both present.

**External-wrap path documented as first-class.** `bwrap --bind ~/proj /work --unshare-net stado run` — stado doesn't try to be the only sandboxing layer; operators with strong opinions should be able to use the toolchain they trust without fighting stado's wrapper. The `[sandbox] mode = "external"` value gives them a way to force-fail stado at startup if it's not running inside a wrapper they expect (poor man's runtime assertion).

**What this is *not*:** stado *adding* gates on top of OS-level limits. If the operator runs in a network namespace with no internet, plugin caps declaring `net:dial:tcp:*:*` are accepted (stado doesn't refuse install), the wasm runtime tries the dial, the kernel says no, the plugin gets a connection error. Layered enforcement = caps describe intent, OS enforces. Stado doesn't double-gate.

**A pragmatic implication for HTB use.** Project-level `.stado/config.toml` for `htb-writeups/` could pin all outbound through tun0's HTB VPN, drop all env, force DNS to HTB's resolvers — without stado growing any HTB-aware code. Per-project sandboxing without per-project plugin awareness.

## Old EPs / docs — supersession discipline

You're right that this is overdue. Concretely:

**Process for the new EPs**:
- Each EP opens with a `## Supersedes` / `## Updates` block listing affected predecessors with the section that's affected.
- Affected EPs get their frontmatter updated: `status: superseded by EP-NNNN` (full) or `status: amended by EP-NNNN — see §X` (partial).
- The supersedence is reciprocal — old EP points forward, new EP points back.

**Pre-drafting sweep needed.** Before writing EP-0037 I should read every existing EP and `docs/` doc and identify what's contradicted by the new direction. From my partial view of the codebase I already know about: EP-0005 (capability gates / "approval ≠ policy" — partially preserved, framing updated), EP-0028 (`--with-tool-host` + bash D1 guard — significantly affected), EP-0035 (project-local `.stado/` — extended, not superseded), EP-0036 (/loop/monitor — orthogonal). There are likely others, and likely doc claims like "stado has these built-in tools…" in `README` / plugin-author docs that need updating.

**Tentative supersession map** (subject to the sweep):

| Predecessor | Status under EP-0037/0038 | Reason |
|---|---|---|
| EP-0005 | Updated by EP-0038 §philosophy | Capability principle preserved; framing shifts from "approval ≠ policy" to "stado describes, operator/OS enforces" |
| EP-0028 — wrapping bundled tools as wasm | Superseded by EP-0038 | "Wrap native tools as wasm" replaced by "no native tools, period" |
| EP-0028 — `--with-tool-host` flag | Superseded by EP-0038 | Becomes default; flag retired |
| EP-0028 D1 — exec:bash hard refusal on no-sandbox | Updated by EP-0038 | Becomes warn-and-run by default; hard refusal opt-in via `[sandbox]` config |
| EP-0035 — `.stado/` directory layout | Extended by EP-0037 (adds `[tools]`, `[sandbox]`, `[plugins.<name>]` sections) | Schema growth, not change |
| EP-0036 — /loop, /monitor, schedule | Unaffected | Orthogonal layer |

Plus likely-affected docs: `README.md`, `docs/plugins/authoring.md` (if exists), `CHANGELOG` references to "bundled tools", any `cmd/stado/help.go` text that lists native tools.

## Updated locked decisions

- **Three-layer security model**: plugin policy → operator admission → operator process containment. The third layer is **first-class**; stado provides config schema (`[sandbox]`) + a wrapper-mode runner + external-wrap doc-and-validation path. Default `mode = "off"` matches the yolo philosophy.
- **`[sandbox]` config keys**: `mode`, `http_proxy`, `dns_servers`, `drop_env`, `plugin_runtime_caps_drop`, plus a `[sandbox.wrap]` subtable for wrapper-driven binds/network mode.
- **`stado run --sandboxed`** = per-run wrapper-mode override. Supersedes config.
- **External wrapping is documented as supported**, with `[sandbox] mode = "external"` for runtime assertion.
- **Cap-vs-OS overlap rule reaffirmed**: stado declares, OS enforces. Stado doesn't double-gate where the kernel is the actual barrier.
- **EP supersession discipline**: every new EP lists affected predecessors with section granularity; predecessor frontmatter updated reciprocally; sweep happens before drafting.
- **EP scope split**: EP-0037 (philosophy + naming + categories + tool-search dispatch + config schema for `[tools]` and `[sandbox]` skeleton). EP-0038 (ABI v2 + bundled wasm + invariant + agent.* + EP-0028 supersession + sandbox implementation details).

## Concrete next step

Before drafting EP-0037, I should do the EP/docs sweep — read every existing EP and the `docs/` tree, build the actual supersession map (what I have above is a guess), then start writing with the map already in hand. Smaller chance of writing claims that contradict an EP I haven't read.

Two confirmations:
1. **EP scope split** as described (0037 = dispatch + philosophy + config skeleton; 0038 = ABI + bundled + invariant + agents + sandbox impl + EP-0028 update). Or do you want to call out sandbox impl as its own EP-0039?
2. **Sweep + supersession map first, then EP-0037 draft** — confirm sequence.

And the still-standing four:
- `agent.read_messages` rename
- `tools.describe(names: [str])` batched
- Agent default model: inherits parent's
- EP-0028 exec:bash hard-refusal → warn-and-run

Sign off and the sweep starts.

---

## User
*2026-05-05T07:22:21.029Z*

hmm drop_env is good but an allow list always beats a deny list so... incl_env, allow_env or pass_env? same with plugin_runtime_caps_drop, needs also plugin_runtime_caps_add network = host, namespaced, off and proxy? the last one would be either CONNECT or HTTP/HTTP/SOCKS? latter of course would need a custom/own root CA injected as well... former could be good v1 adding capability to do allow/deny list filtering eaily... a global arg --sandbox [name] where it could also have some default config if no name specified, user could define [sandbox.[name]] in config.toml then as you suggested... the whole mess with --*tool* arguments in CLI needs a cleanup and one clear direction... I think by default all are enabled, default auto load list is hardcoded in stado, overridable in config or cmd arg --tools-autoload... then if user specifies --tools=a,b,c only these tools are enabled + any autoloaded (if any).. additionally --tools-disable=x,y,z diables tools.. so the logic would flow in this sequence --tools (enable these, disable anything else), --tools-autoload (enable these and auto load, don't auto load any other, don't disable already enabled but not listed here) and finally --tools-disable (from all enabled/autoloaded disable those specified here)... ok give me a detailed summary what we aggreed on so far

---

## Assistant
*2026-05-05T07:24:13.928Z*

# Stado architectural reset — agreed-on summary

## Philosophy (load-bearing)

**Permissive-by-default, primitive-rich, layered.** Three positions, in order:

1. **Plugin authors set policy.** Each plugin chooses how heavily to gate its own behaviour (caps declared, approvals, validation). Stado doesn't grade; "go fast" and "hardened audit-trail" plugins are equal first-class citizens.
2. **Operators set admission.** What gets installed, what's enabled, what caps are accepted, what's surfaced to the model.
3. **Operators set process containment.** OS-level wrapping (namespaces, proxies, fs binds, capability changes) — first-class via `[sandbox]` config, not an afterthought.

**Stado guarantees primitives, not policies.** Caps are honoured (declared = enforced upper bound), sandbox runner integrates correctly when present, approval prompts route correctly, sessions durable, signatures verified. Stado does *not* prescribe minimum caps, default approval gates, or enforcement on top of OS-level limits.

**Cap-vs-OS overlap rule**: stado describes intent; the kernel enforces. Stado doesn't double-gate where the OS is already the gate (port < 1024 binding, CAP_NET_RAW for raw ICMP, network namespace blocking dial). Cap declarations remain for visibility and operator admission decisions.

## The invariant

**Stado core ships no native tools.** The host exposes a host-import surface; every tool is a wasm plugin. A curated bundle ships embedded by default; users add, replace, or disable any of them. Existing native tools (`fs`, `bash`/`shell`, `webfetch`, `httpreq`, `rg`, `astgrep`, `lspfind`, `readctx`, `tasktool`, `subagent`) become host-import implementations, not registered tools.

## Host-import surface — ABI v2

```
Tier 1 — Capability primitives (host-only, lazy-init)
  stado_proc_{spawn,read,write,close,wait,kill}
  stado_exec(...)                                              # sugar over proc
  stado_terminal_{open,read,write,resize,close,wait}
  stado_net_dial(transport, addr, opts) → handle               # tcp | udp | unix
  stado_net_listen(transport, addr, opts) → listen_handle      # tcp | unix
  stado_net_accept(listen_handle, timeout)
  stado_net_{read,write,close}(handle, ...)
  stado_net_icmp_{open,send,recv,close}                        # full raw ICMP, no _ping convenience
  stado_fs_*                                                   # kept
  stado_bundled_bin(name) → path                               # lazy extract on first call
  stado_session_*, stado_llm_invoke, stado_log,
  stado_approval_request                                       # existing

Tier 2 — Stateful conveniences (lazy-init)
  stado_http_client_{new,close}
  stado_http_request(client, ...)
  stado_http_request_streaming(client, ...) → response_handle
  stado_dns_resolve(name, type, opts?)
  stado_dns_resolve_axfr(name, server)
  stado_secrets_{get,set,delete,list}

Tier 3 — Stateless format conveniences
  stado_json_{parse,stringify}                                 # strict RFC 8259
  stado_hash(algo, bytes), stado_hmac(algo, key, bytes)        # md5,sha1,sha256,sha512,blake3
  stado_compress(algo, bytes), stado_decompress(algo, bytes)   # gzip,brotli,zstd
```

**Lazy-init / first-call-extract is universal.** Bundled binaries (rg, ast-grep) extracted on first `stado_bundled_bin` call. Bundled wasm plugins instantiated on first tool invocation. HTTP clients, LSP processes, terminal handles, secret backend, JSON/hash/compression engines — all lazy. Process startup does registration only.

**Capability vocabulary** (manifest declarations):
`exec:proc`, `terminal:open`, `net:dial:<transport>:<host>:<port>`, `net:listen:<transport>:<port>`, `net:icmp[:<host>]`, `bundled-bin:<name>`, `dns:resolve[:<glob>]`, `dns:axfr:<zone>`, `dns:reverse:<cidr>`, `secrets:read:<key>`, `secrets:write:<key>`, `crypto:hash`, plus existing `fs:read:<path>`, `fs:write:<path>`, `net:http_request[:<host>]`, `session:*`, `llm:invoke:<budget>`, `ui:approval`. **No synthesised privilege caps** (no `net:listen:privileged` — kernel handles it).

## Tool naming convention

- **Canonical form** (manifest, docs, config, CLI): dotted. `fs.read`, `shell.exec`, `web.fetch`, `agent.spawn`, `tools.search`.
- **Wire form** (LLM-facing): underscore. `fs_read`, `shell_exec`. Synthesised as `<plugin>_<tool>` at registration. (Anthropic API tool-name regex doesn't allow dots.)
- **Plugin family idiom**: a plugin wrapping a family of similar implementations exposes both a default tool and per-implementation tools, separated by dotted suffix. E.g. `shell.exec` (uses `[plugins.shell] binary = ...`) plus `shell.bash`, `shell.zsh`, `shell.fish`, `shell.sh`, `shell.pwsh`. Same idiom for `agent.spawn` + `agent.opus`/`agent.haiku`. Convention, not enforcement.

## Bundled tool inventory

`fs` (read/write/edit/glob/grep), `shell` (exec/spawn + variants), `web` (fetch/search/browse), `http` (request/client_new), `lsp` (definition/references/symbols/hover), `rg` (search), `astgrep` (search), `readctx`, `task` (add/list/update/complete), `agent` (spawn/list/read_messages/send_message/cancel), `mcp` (connect/list_tools/call), `image` (info), `tools` (search/describe/categories/in_category), `dns` (resolve), plus `auto-compact` background. Includes the five existing examples (`web-search`, `mcp-client`, `ls`, `image-info`, `browser`) — recompile-only.

## Meta-tool dispatch (replaces always-present registry)

Stado stops broadcasting every tool's schema in the system prompt. Always-loaded core is small; everything else lives behind tool-search.

**Four meta tools** (one collapsed from earlier draft):
- `tools.search(query?)` → `[{name, summary, categories}]` — no arg lists all, with arg filters.
- `tools.describe(names: [str])` → full JSONSchema + docs for each requested tool. Batched (round-trip economics).
- `tools.categories(query?)` → `[string]` — no arg lists all category names, with arg filters.
- `tools.in_category(name)` → `[{name, summary, categories}]`.

`tools.list` was redundant with `tools.search()` no-args — dropped.

**Categories**: per-tool in manifest (not per-plugin). Plugin authors pick from a frozen canonical taxonomy. `extra_categories: []` for free-form tags (allowed but flagged as plugin-defined in `describe` output).

**Initial canonical taxonomy** (~21 names, frozen as part of EP-0037, amended only by future EP):
`filesystem, shell, network, web, dns, crypto, data, encoding, code-search, code-edit, lsp, agent, task, mcp, image, secrets, documentation, ctf-offense, ctf-recon, ctf-postex, meta`

## Agent surface

Five tools, async-under-the-hood, sync default:

- `agent.spawn(prompt, model?, sandbox_profile?, async?, ephemeral?, parent_session?, allowed_tools?)` → `{id, session_id, status, ...}`. Default `async: false` blocks until completion; `async: true` returns handle immediately. Default model: **inherits parent's**.
- `agent.list()` → `[{id, session_id, status, model, started_at, last_turn_at, cost_so_far_usd}]`. Scoped to caller's spawn tree, not global runtime.
- `agent.read_messages(id, since?, timeout?)` → `{messages, offset, status}`. **Only assistant-role messages** the child explicitly emits — child-curated output channel.
- `agent.send_message(id, msg)` → enqueues user-role message into agent's inbox; delivered at next yield point. (Renamed from `agent.write` to disambiguate from `fs.write`.)
- `agent.cancel(id)` → cancel signal; child exits at next yield.

**Every agent owns a session.** `session_id` returned alongside `id`; same value, two affordances. Operator can `session.read|observe` for full transcript (audit channel) — bypasses child's curation.

**Session metadata gains a `driver` field**: `human`, `agent:<parent>`, `cron:<routine>`, `bridge:<plugin>`. Lets `session.list` filter by driver.

**Two communication channels both first-class**:
- `agent.read_messages` — convenience, child-curated, function-call-return shape.
- `session.read|observe` — audit trail, full transcript including tool calls. Requires `session:read|observe` cap if not the spawn parent.

**Recursive spawning** allowed; capped via `[agents] max_depth` (default 5).

## Sessions persist forever by default

No auto-prune. Sessions feed `stado usage/stats` and audit; auto-pruning would silently drop history.

- Explicit cleanup: `stado session prune --before <date>` / `stado session delete <id>`.
- Opt-in retention: `[sessions] auto_prune_after = "90d"` in config.
- Opt-in per-spawn: `agent.spawn(..., ephemeral: true)` skips persistence (no `session.read`, no `usage/stats` line).

## Configuration — `.stado/config.toml`

Schema commitments:

```toml
[tools]
autoload = ["tools.*", "fs.*", "shell.exec"]   # auto-load whitelist; supports * globs
                                                # default hardcoded in stado, overrideable here
disabled = ["browser.*"]                        # blacklist; final filter

[plugins.shell]
binary = "/usr/bin/zsh"
init = ["set -u", "set -o pipefail"]

[plugins.htb-lab]
default_token_path = ".secrets/htb_app_token"

[sessions]
auto_prune_after = ""                           # "" = never; "90d", "30d" otherwise

[agents]
max_depth = 5
idle_timeout = "10m"

[sandbox]
mode = "off"                                    # "off" | "wrap" | "external"
http_proxy = ""                                 # http_proxy + https_proxy in env
dns_servers = []                                # ["1.1.1.1", "9.9.9.9"]
allow_env = []                                  # allow-list; empty = pass-through everything
plugin_runtime_caps_add = []
plugin_runtime_caps_drop = ["cap_net_raw"]

[sandbox.wrap]
runner = "auto"                                 # auto | bwrap | firejail | sandbox-exec
bind_ro = ["/usr", "/etc/resolv.conf"]
bind_rw = ["~/Dokumenty/htb-writeups"]
network = "host"                                # host | namespaced | off

# Named sandbox profiles for --sandbox <name>
[sandbox.profiles.htb]
mode = "wrap"
http_proxy = "http://127.0.0.1:8080"
[sandbox.profiles.htb.wrap]
network = "namespaced"
bind_rw = ["~/Dokumenty/htb-writeups"]
```

**Env handling: allow-list, not deny-list.** `allow_env = []` (empty) = pass-through. `allow_env = ["PATH", "HOME", "TERM"]` = strict whitelist.

**Caps add+drop**: `plugin_runtime_caps_add` and `plugin_runtime_caps_drop`. Empty by default.

**Network mode values**: `host` (default), `namespaced` (own netns, no internet unless wrap binds tun/etc.), `off` (no network at all).

**Proxy semantics**: HTTP CONNECT-style proxy via `http_proxy` (passes traffic, allow/deny filter possible at v1 level). Future: full HTTP/HTTPS-intercept proxy with custom CA injection — flagged as "v2+, requires CA-injection plumbing."

## CLI flag flow — single clear sequence

Three flags, applied in this exact order (each filter sees the prior's output):

```
1. --tools=a,b,c             # ENABLE: only these are enabled (plus autoload entries below)
                             # If unset: all installed tools enabled.

2. --tools-autoload=x,y,z    # AUTOLOAD: these are loaded into the always-on set
                             # If unset: hardcoded default list (tools.*, fs.*, shell.exec)
                             #           with config override taking precedence over hardcoded
                             # All others reachable only via tools.search

3. --tools-disable=p,q,r     # DISABLE: removed from the resulting set
                             # Final filter — wins over enable AND autoload
```

**Worked example**:
```
--tools=fs.*,shell.*,htb-lab.*  --tools-autoload=fs.read,shell.exec  --tools-disable=shell.spawn

Step 1: enable fs.*, shell.*, htb-lab.*           → 12 tools available
Step 2: autoload {fs.read, shell.exec};
        rest reachable only via tools.search       → 2 in always-on, 10 deferred
Step 3: disable shell.spawn                       → 11 tools total, shell.spawn gone entirely
```

All flags accept comma-separated globs. `*` matches one segment. Disabled wins over enabled+autoload.

**Config-vs-CLI precedence**: CLI > project `.stado/config.toml` > user `~/.config/stado/config.toml` > stado hardcoded defaults.

**`--sandbox <name>`** — global flag, optional argument:
- `--sandbox` (no name) — applies `[sandbox]` defaults from config (mode = "wrap" if config says so, else fail with hint).
- `--sandbox htb` — applies `[sandbox.profiles.htb]` from config.
- Combines with `[sandbox.profiles.<name>]` config tables; named profile wins over root `[sandbox]`.

## EP supersession discipline

- Each new EP opens with explicit `## Supersedes` / `## Updates` listing affected predecessors at section granularity.
- Affected EPs get `status: superseded by EP-NNNN` or `status: amended by EP-NNNN — see §X` in frontmatter.
- Reciprocal pointers: old → new and new → old.
- **Pre-drafting sweep required**: read all existing EPs and `docs/` before EP-0037 draft, build actual supersession map.

**Tentative map** (subject to sweep):

| Predecessor | Effect | Reason |
|---|---|---|
| EP-0005 | Updated by EP-0038 §philosophy | Capability principle preserved; framing shifts to "stado describes, OS enforces" |
| EP-0028 — bundled-tools-as-wasm-wrappers | Superseded by EP-0038 | Wrapping-native-as-wasm replaced by no-native-tools invariant |
| EP-0028 — `--with-tool-host` flag | Superseded by EP-0038 | Becomes default; flag retired |
| EP-0028 D1 — exec:bash hard refusal on no-sandbox | Updated by EP-0038 | Becomes warn-loud-run-anyway by default; hard refusal opt-in via `[sandbox]` |
| EP-0035 — `.stado/` directory | Extended by EP-0037 | Schema growth (`[tools]`, `[sandbox]`, `[plugins.<name>]`); not change |
| EP-0036 — /loop, /monitor, schedule | Unaffected | Orthogonal layer |

## EP scope split

**EP-0037 — Tool dispatch + naming + categories + config skeleton + philosophy.**
- Philosophy section (load-bearing).
- Tool naming convention (canonical / wire).
- Categories + frozen taxonomy + manifest field.
- Four meta-tool surface.
- Three-flag CLI.
- `.stado/config.toml` schema for `[tools]`, `[plugins.<name>]`, `[sessions]`, `[agents]`.
- `[sandbox]` schema commitment (skeleton; impl deferred to EP-0038).
- No ABI changes. Lands first.

**EP-0038 — ABI v2 + bundled wasm + invariant + agents + sandbox impl + EP-0028 supersession.**
- Tier 1/2/3 import surface.
- No-native-tools invariant.
- `internal/host/*` migration (deletes `internal/runtime/bundled_plugin_tools.go`'s registrations, moves `internal/tools/*` to host imports).
- Bundled wasm tool inventory.
- Agent surface (5 tools + sessions-per-agent).
- Sandbox implementation (`mode = "wrap"` re-exec under bwrap/firejail/sandbox-exec; env allow-list; caps add/drop; proxy enforcement).
- EP-0028 D1 behaviour change.
- ABI version bump to v2; v1 plugins continue working against existing import surface where compatible.

## Confirmations still standing

1. EP scope split as above (vs splitting sandbox into EP-0039)?
2. Sweep + supersession map first, then EP-0037 — confirm sequence?
3. `agent.read_messages` rename (was `agent.read`) — confirm?
4. `tools.describe(names: [str])` batched — confirm?
5. Agent default model: inherits parent's — confirm?
6. EP-0028 exec:bash hard-refusal → warn-and-run by default — confirm?

## What's locked

Everything above except the six confirmations. Sign-off on those six and the EP/docs sweep starts.

---

## User
*2026-05-05T07:29:31.965Z*

one more addition... stado subcommands... plus each will need equivalent /slash command to do:  1- stado tool {enable,disable,reload,ls,add,remove,autoload???}  [name] --global  (this would update config.toml accordingly either in project/cwd or globally in all project at user home level); 2- stado plugin {???} (we already have that but it needs a sanity check/quality pass perhaps?) 3- inside stado TUI shell commands allowing on top of previously mentioned access to stats, ps, top-like info about wasm plugins.. ability to kill wasm plugin instance that may be misbehaving...  thoughts, opinions, ideas and recommendations?


---

## Assistant
*2026-05-05T07:31:36.334Z*

Yes — this is the layer that makes the architecture *useable* day-to-day. Without these, every config change is a TOML hand-edit and runtime debugging means re-launching. Working through each.

## `stado tool` — verbs + persistence model

Drop `add` and `remove` — they're confusing overlap with `plugin install/remove` (since every tool comes from a plugin). The real surface is two-axis: *visibility* (`enable`/`disable`/`autoload`) and *introspection* (`ls`/`info`/`cats`/`reload`).

Final verbs:

| Verb | Action | Mutates config |
|---|---|---|
| `tool ls [glob]` | List tools with status: enabled/disabled/autoloaded, plugin source, categories | no |
| `tool info <name>` | Full schema + docs + caps + manifest reference (CLI mirror of `tools.describe`) | no |
| `tool cats [glob]` | List categories, optionally filtered (CLI mirror of `tools.categories`) | no |
| `tool enable <glob>` | Add to `[tools]` whitelist (or remove from `disabled`) | yes |
| `tool disable <glob>` | Add to `[tools.disabled]` | yes |
| `tool autoload <glob>` | Add to `[tools.autoload]` | yes |
| `tool unautoload <glob>` | Remove from `[tools.autoload]` | yes |
| `tool reload <glob>` | Drop cached wasm instance(s); next call re-inits | no (runtime only) |

Flags on the mutating verbs:

- `--global` (writes `~/.config/stado/config.toml` instead of project's `.stado/config.toml`)
- `--config <path>` (explicit target file — for shared/team configs)
- `--dry-run` (show what would change, write nothing)

`reload` deserves separate treatment: it doesn't touch config, it pokes the running stado instance to invalidate one or more cached plugin runtimes. Useful during plugin authoring (`./build.sh && stado tool reload fs.*` instead of restart). Lazy-init handles the rest.

`tool ls` output should make the three states visible at a glance:

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

Three states (autoloaded ⊂ enabled), one column. `--json` for machine-readable.

## `stado plugin` — quality pass + gap fixes

Existing surface as I read it: `install`, `list`, `run`, `trust`, `doctor`, `info`. Needs three kinds of work:

**Gaps from your NOTES feedback:**

- **sha256 drift detection on install.** Suggestion C from the dogfood notes — when reinstalling a manifest with the same `name`+`version` but a different `wasm_sha256`, current behaviour is `skipped: already installed`. Should detect drift and either auto-replace (`reinstalling (sha256 changed)`) or require `--force`. I'd argue auto-replace on drift, because identical name+version+different sha is unambiguous evidence the operator rebuilt.
- **`plugin trust --pubkey-file <path>`** — your NOTES point. `author.pubkey` already lives in the build dir; let `trust` accept the file path directly instead of `$(cat author.pubkey)`.
- **`plugin install --autoload`** — pin to autoload at install time, persisted in config. Saves the operator the second `tool autoload` call.

**New verbs needed:**

- `plugin update <name>` — fetch newer version (semantics depend on install source: dir = re-install, URL = re-download). Distinct from `install --force` because it's *aware* of the existing version.
- `plugin remove <name>` — uninstall. Currently spelled? Check the sweep.
- `plugin dev <dir>` — your NOTES suggestion. Loop: `gen-key` → `build.sh` → `trust` → `install --force` → `tool reload`. One command for the inner-loop authoring cycle. Uses a TOFU-pinned dev key so it doesn't pollute the trust store with throwaway authors.
- `plugin sign <dir> --key <path>` — explicit signing step (today probably bundled into `install`?). Useful for CI pipelines.

**Consistency / quality pass:**

- `plugin ls` output should match `tool ls` columns where they overlap (use the same renderer).
- `--json` everywhere, same shape conventions.
- `plugin info <name>` and `tool info <name>` overlap — `plugin info` is plugin-level (manifest, signature, caps, list of tools); `tool info` is tool-level (schema, docs). Both useful, different levels. Cross-link in their output.
- `plugin doctor <name>` should now also check: declared caps vs. operator's `[tools.disabled]`, declared caps vs. `[sandbox]` constraints (e.g. plugin wants `net:dial:tcp:*:*` but operator's sandbox is `network = "namespaced"` — flag with severity).

## TUI slash commands

The pattern: **every `stado <subcommand>` has a `/<subcommand>` mirror**, with two semantic differences:
1. Slash commands default to **per-session, non-persistent** — the inverse of CLI which persists. Add `--save` to write to config.
2. Slash commands have access to runtime state CLI doesn't (live agents, live wasm instances, current session token count).

**The runtime introspection commands you asked for:**

| Slash | Mirror of | Output |
|---|---|---|
| `/stats` | (none) | Tokens used (this session, today, this week), cost, session count, active agent count, uptime |
| `/ps` | (none) | All live wasm/proc/term/net handles; plugin source; memory; cpu; runtime; status |
| `/top` | (none) | Live-updating `/ps` (Ctrl-C exits) |
| `/kill <id\|name>` | (none) | Terminate handle or plugin instance |
| `/sessions [filter]` | `stado session ls` | Sessions including agent-driven ones |
| `/agents` | (none, but is `/sessions --driver=agent`) | Active agents in this session's tree |
| `/sandbox` | (none) | Current sandbox state: mode, proxy, env allow-list, namespace status |
| `/config [section]` | `stado config show` | Effective config (merged project+user+CLI), with source attribution |
| `/tool *` | `stado tool *` | All tool subcommands inside the session |
| `/plugin *` | `stado plugin *` | All plugin subcommands inside the session |
| `/tools <query>` | (calls `tools.search`) | Quick search without going through the model |

**Handle ID convention** — the missing piece for `/ps` and `/kill` to be usable. Every spawned thing gets a typed, dotted ID:

```
plugin:fs                 # the fs plugin's wasm instance
proc:fs.7a2b              # a proc handle owned by fs, instance ID 7a2b
term:shell.9c1d           # terminal handle owned by shell
agent:bf3e                # agent (also addressable as session:bf3e000…)
session:abc12345
conn:web.4f5a             # net handle
listen:browser.8a91       # listen handle
```

Type-prefix is mandatory (avoids namespace collisions); dotted plugin context makes `/ps` output legible without extra columns; numeric tail is the per-instance ID.

`/kill proc:fs.7a2b` kills that single handle. `/kill plugin:fs` drops the entire wasm instance (cascades to all handles owned by it). `/kill agent:bf3e` cancels an agent.

`/ps` output:

```
ID                      STATE    OWNER          CPU%  MEM     AGE
plugin:fs               idle     -              0.0   2.1MB   12m
plugin:web              busy     -              0.4   8.4MB   6m
  conn:web.4f5a         active   plugin:web     -     -       3s
plugin:shell            idle     -              0.0   1.8MB   12m
  proc:shell.7a2b       waiting  plugin:shell   0.0   12MB    45s
  term:shell.9c1d       active   plugin:shell   0.1   8MB     2m
agent:bf3e              busy     plugin:agent   0.0   -       1m
  session:bf3e0001      driver   agent:bf3e     -     -       1m
```

Tree view (handles indented under their owner plugin) keeps causality visible. `--flat` for non-tree.

## What I'd recommend deferring vs. shipping

**Ship in EP-0037 (with the dispatch + naming + categories work):**
- `stado tool` subcommand surface — directly exercises the new `[tools]` config schema, natural fit.
- `/tool *` slash mirrors — same.
- `/sandbox`, `/config` — read-only views over the new config, no new state.

**Ship in EP-0038 (with the wasm migration):**
- `/ps`, `/top`, `/kill` — these need the new wasm runtime state model (handle table, plugin instance lifecycle). Cleaner to land alongside the migration.
- `/stats` — needs the per-agent / per-session cost attribution that comes with agent-owns-session. Land with EP-0038.
- Handle ID convention — formalised in EP-0038 as part of the runtime model.

**Defer / separate small PR (no EP):**
- `stado plugin` quality pass — sha drift, `--pubkey-file`, `update`, `dev`, output renderer consistency. Each is a small, isolated fix; bundling them as one "plugin CLI quality" PR is cheaper than embedding in EP-0037 or 0038.
- `plugin install --autoload` — small.
- `plugin doctor` cap-vs-sandbox checks — depends on EP-0038 for sandbox impl, so land in EP-0038.

## Two judgment calls worth your input

1. **Slash command persistence default.** I argued slash = per-session, CLI = persistent, with `--save` flipping slash to write config. Alternative: both persist by default, `--cli-only` flag flips to ephemeral. The "slash is ephemeral" position is more honest about what a slash command typically does (try something, see if it works, decide); it also matches how Claude Code's `/permissions` works (session-only by default).
2. **`tool reload`** — should it also accept `tool reload <plugin-name>` (reload all tools from one plugin)? Today the addressing is per-tool. Plugin-level reload is more natural during dev work ("reload my whole plugin, not just one of its 5 tools"). Could spell it `plugin reload <name>` instead and keep `tool reload` tool-level only. Either works; I'd nudge to `plugin reload` for whole-plugin and `tool reload` for individual tools.

## Updated locked decisions (delta)

Add:

- **`stado tool` subcommand**: 8 verbs (`ls`, `info`, `cats`, `enable`, `disable`, `autoload`, `unautoload`, `reload`), `--global`/`--config`/`--dry-run` flags. Mutating verbs persist by default.
- **`stado plugin` quality pass** as a separate scoped PR (not an EP): sha-drift auto-detect, `--pubkey-file`, `update`, `remove`, `dev`, `sign`, `--autoload` install flag, output renderer consistency.
- **TUI slash commands mirror CLI**, plus runtime-only commands (`/stats`, `/ps`, `/top`, `/kill`, `/agents`, `/sandbox`, `/config`).
- **Handle ID convention**: type-prefixed dotted IDs (`proc:fs.7a2b`, `agent:bf3e`, `plugin:web`). Formalised in EP-0038.
- **Slash commands default to per-session non-persistent**, `--save` to persist.
- **Scope split**: `stado tool` + `/tool` mirrors land in EP-0037. `/ps` `/top` `/kill` `/stats` + handle convention land in EP-0038. `stado plugin` quality pass is a separate small PR.

Six earlier confirmations still standing, plus three new:

7. Slash persistence default = per-session (vs. mirror CLI persist-by-default)?
8. `plugin reload <name>` (whole-plugin) + `tool reload <name>` (individual tool) — both, distinct?
9. Drop `tool add`/`remove` from the verb list as redundant with `plugin install`/`remove` — confirm?

Sign-off and the EP/docs sweep starts.

---

## User
*2026-05-05T07:42:11.124Z*

slash per session, yes, unless some argument stating otherwis to persist/make default? tool reload [name] optionally yes, , tool add/remove not needed correct, EP 37+38 ok, this is pre-1.0 I'm the only user so no kid gloves no need to worry about temporary instability, agent.read_messages yes, tools.describe batched yes, agent inherits by default, EP-028 superseeded - new design decisions only carry forward masking old ones... one last concern... plugin signing and distribution... currently in scenario where operator == plugin dev it's all nice and easy, but what if a plugin dev wants to disrtibute their plugins and other people want to use them? we need a simple mechanism to accomodate this without loosing the benefit of signed/versioned plugins.... I would like to see something integrating github as the main source of repository but not only limited to it (similar to Go packages initially)... So perhaps the pub key has to be in a known location of a github repo and user just installs it by stado plugin install github.com/foobarto/superduperplugin@v1.0.0 ?


---

## Assistant
*2026-05-05T07:44:05.682Z*

Right call to surface this now — the rest of the design assumes signing works, but distribution makes signing actually meaningful for non-dev operators. Working through it.

## Identity = URL + version, Go-style

```
stado plugin install github.com/foobarto/superduperplugin@v1.0.0
stado plugin install github.com/foo/bar@v1                    # major-version pin (latest v1.x.y)
stado plugin install github.com/foo/bar@latest                # latest semver tag
stado plugin install github.com/foo/bar@main                  # branch HEAD (warns: unpinned)
stado plugin install github.com/foo/bar@a1b2c3d               # pinned commit SHA
stado plugin install github.com/foo/bar                       # = @latest
```

Identity rules:
- Path is the canonical name. `github.com/foobarto/superduperplugin` is *the* name; the manifest's local `name: "superduperplugin"` is a display alias.
- Version uses semver tags (`v1.0.0`) or commit SHAs. No floating "latest" without a lock file (it'd be a footgun).
- Other VCS hosts work with no extra config: `gitlab.com/...`, `codeberg.org/...`, `git.sr.ht/~user/...`. Generic prefix → `https://<host>/<owner>/<repo>.git`.

Vanity/private-host support deferred to v2 (Go-style `<meta name="stado-import">` discovery). Ship straight prefix-matching first; covers ~95% of cases.

## Pubkey discovery — `.stado/author.pubkey` in the plugin repo

Single load-bearing convention: the plugin's own repo carries `.stado/author.pubkey` (hex-encoded ed25519 pubkey, one line). On install, stado:

1. Fetches the artifact (release/tag).
2. Reads `.stado/author.pubkey` from the tagged tree (or release asset).
3. Verifies `plugin.manifest.sig` against it.
4. **First time seeing this fingerprint**: shows operator a TOFU prompt:

```
First-time install of github.com/foobarto/superduperplugin@v1.0.0
Author key fingerprint: 49cb:eaa8:289e:6623
Trust this key for: github.com/foobarto/superduperplugin    [Y/n/scope]
```

5. Subsequent installs of *anything signed by that fingerprint* match the trust scope and skip the prompt.

`[Y/n/scope]` opens a third option: change the trust scope before saving. Default scope is the exact repo path; operator can broaden:

```
Scope options:
  1. github.com/foobarto/superduperplugin   (exact repo, default)
  2. github.com/foobarto/*                  (all of this author's plugins)
  3. *                                      (any repo signed with this key — strongest trust)
```

Trust state lives in `~/.config/stado/trust.toml`:

```toml
[[trusted_authors]]
fingerprint = "49cbeaa8289e6623"
scope = "github.com/foobarto/*"
trusted_at = "2026-05-05T10:00:00Z"
note = "Bartosz Ptaszynski — htb-toolkit, stado examples"
```

Operator can pre-trust without installing anything: `stado plugin trust github.com/foobarto --scope=author`. Or untrust: `stado plugin untrust 49cbeaa8289e6623`.

## Artifact resolution — three-tier fallback

When fetching `github.com/foo/bar@v1.0.0`, stado tries in order:

1. **GitHub Release attached to tag**: download `plugin.wasm`, `plugin.manifest.json`, `plugin.manifest.sig` as release assets. Preferred — no source build, no toolchain needed.
2. **Files at `dist/` in the tagged tree**: if no release, fetch from `dist/plugin.wasm` etc. via `git archive` or raw download.
3. **Source build**: clone tag, run `build.sh` if present, expect `plugin.wasm` artefact afterwards. Last resort — requires Go/Rust/Zig toolchain present, plenty of failure modes. Probably opt-in via `--build` flag rather than silent fallback.

(1) is what casual operators want; (2) is for authors who don't bother with releases; (3) is for development.

## Lock file — `.stado/plugin-lock.toml`

Per-project, pinned by checksum:

```toml
[plugins."github.com/foobarto/superduperplugin"]
version = "v1.0.0"
wasm_sha256 = "abc..."
manifest_sha256 = "def..."
author_fingerprint = "49cbeaa8289e6623"
source = "release"                              # release | tree | build
installed_at = "2026-05-05T10:00:00Z"
```

`stado plugin install --from-lock` reproduces exactly. Mirrors `go.sum` / `Cargo.lock` / `package-lock.json` semantics. Lock file commits to git alongside `.stado/config.toml`; teammates / CI get bit-identical plugins.

## Updates and version drift

```
stado plugin update github.com/foo/bar              # bump to latest matching tag, write lock
stado plugin update --all                           # update every locked plugin
stado plugin update --check                         # show available updates without installing
stado plugin install github.com/foo/bar@v1.0.0      # explicit version, lock file updated
```

`update` warns when a plugin's `wasm_sha256` changes but the author fingerprint stays the same (expected, just informs the diff); errors when the author fingerprint changes (treat as a key rotation event — operator decides whether to re-trust).

## Sandbox interaction

`stado plugin install <remote>` makes outbound HTTPS calls. The sandbox layer must be aware:

- `[sandbox] http_proxy` — used for all GitHub/registry HTTPS.
- `[sandbox] mode = "wrap"` with `network = "off"` — install fails fast with a clear message ("install requires network egress; current sandbox mode is 'off'"). Operator either drops sandbox temporarily or arranges out-of-band installation.
- A `[plugins] mirror = "https://my-mirror.local"` config key reserved (not implemented v1) for self-hosted teams. Prefixes all `github.com/...` lookups.
- A local cache (`~/.cache/stado/plugin-tarballs/`) reused across installs. Enables offline reinstalls after first fetch.

## CLI surface delta

Adds to existing `stado plugin`:

```
plugin install <local-path-or-archive>           # existing
plugin install <repo-url>[@version]              # NEW: remote
plugin install --from-lock                       # NEW: reproducible install from .stado/plugin-lock.toml
plugin install ... --autoload                    # NEW: pin to autoload at install time
plugin install ... --build                       # NEW: opt-in source build fallback

plugin update <name|all> [--check]               # NEW
plugin trust <repo-or-fpr> [--scope=...]         # extended: scope arg
plugin untrust <fpr-or-repo>                     # NEW (today only `trust`)
plugin verify <name>                             # NEW: re-verify signature against trust store
```

In TUI: `/plugin install github.com/foo/bar@v1.0.0` works the same; per-session by default, `--save` to persist to lock file.

## Two design calls worth flagging

**1. Where exactly does `.stado/author.pubkey` live in the plugin repo?**

Three positions:

- α) Repo root: `<repo>/.stado/author.pubkey`. One key per repo. Simple.
- β) Plugin-dir-relative: `<repo>/<plugin-subdir>/.stado/author.pubkey`. Multiple plugins per repo (mono-repo style — like `htb-toolkit/`!) each have their own key.
- γ) Both: prefer plugin-dir, fall back to repo root.

The HTB-toolkit case is the deciding example. Today its 12 plugins each have their own author key. That's good practice (compromise of one plugin's key doesn't compromise others), and the install path needs to support it: `stado plugin install github.com/foobarto/htb-writeups/htb-toolkit/gtfobins@v1.0.0`.

So: **γ — both**. Plugin-dir wins; repo-root is the fallback for single-plugin repos. Also enables `stado plugin install github.com/foobarto/htb-writeups/htb-toolkit/*` to install the whole toolkit (each plugin found has its own pubkey).

**2. Author identity vs. plugin identity in the trust prompt.**

The TOFU prompt should show *which key signs which plugins under which scope*. Default trust scope = "this exact repo" is conservative; "this author's `*`" is friendly but optimistic. I'd lean: show the prompt with the conservative default selected, but make broadening one keystroke. The HTB use case (12 plugins, same author, same repo) screams for `github.com/foobarto/htb-writeups/*` to be a one-time approval — but that's still narrower than "trust this key everywhere."

## What this means for EP scope

This isn't EP-0037 (dispatch + naming + categories) territory. It's its own concern: plugin lifecycle. Two options:

**A) Fold into EP-0038** (already large — has ABI v2, agents, sandbox impl, EP-0028 supersession). Adds: remote install, TOFU trust, lock file, three-tier resolution.

**B) Separate EP-0039 — Plugin distribution.** Smaller, more focused, easier review. EP-0038 stays runtime-internal (host imports + bundled wasm + agents); EP-0039 handles the operator-facing distribution layer.

I'd argue B. EP-0038 is internal-architecture (host invariant, ABI surface). EP-0039 is operator-UX (how do you actually get plugins onto your machine). Different audiences review them, different timelines for landing — distribution can come slightly later without blocking the architecture work.

## Updated locked decisions (delta)

Add to the locked set:

- **Remote install identity**: `<host>/<owner>/<repo>[/<plugin-subdir>]@<version>`. v1 supports prefix-matched VCS (GitHub, GitLab, Codeberg, generic git hosts). Vanity-import discovery deferred to v2.
- **Pubkey discovery**: `.stado/author.pubkey` at plugin-dir or repo root (plugin-dir wins). One ed25519 key per scope.
- **Trust model**: TOFU with scope choice (exact repo / `<host>/<owner>/*` / `*`). Persisted in `~/.config/stado/trust.toml` keyed by fingerprint.
- **Artifact resolution order**: GitHub release → `dist/` in tree → source build (`--build` opt-in).
- **Lock file**: `.stado/plugin-lock.toml` per project. `--from-lock` for reproducible installs. Mirrors `go.sum` semantics.
- **Update flow**: `plugin update [--check]`. Warns on sha drift with same author; errors on author-fingerprint change (forces re-trust decision).
- **Sandbox interaction**: install requires network egress; respects `[sandbox] http_proxy`; fails fast under `network = "off"`. Local tarball cache enables offline reinstalls.
- **EP-0039 — Plugin distribution** as a separate EP. Lands after EP-0038 (or in parallel; doesn't block it).

## Outstanding confirmations going into the sweep

All earlier confirmations are sign-off-locked per your last message. The two remaining open from this round:

1. **`.stado/author.pubkey` location** — plugin-dir-with-repo-root-fallback, confirm?
2. **EP-0039 split for distribution** — separate EP, confirm? (vs. fold into EP-0038)

Answer those two and I do the EP/docs sweep, then start drafting EP-0037.

---

## User
*2026-05-05T07:53:39.971Z*

also it should be possible to have multiple versions of same plugin downloaded - not sure if allowing them to be enabled by the same time is a good idea however...re versioning.. no loose/overwriteable tags like 'latest' only semver or commit hash ID, author pub key - plugin's repo at .stado/author.pub - no support for repos with plugins with multiple authors - one gatekeeper if that's the case signs the plugin wasm binary... trust is to the author (single person, organization - technically the signatory). Accepting author's cert means installing different versions from that repo will succeed without issues as long as other versions are also signed by the already approved author's key... if not, ie if user approved installation of a foobarto/superduperpluginV1.0.0 and foobarto/superduperplugin@v2.0.0 was signed using different key stado should complain loudly and refuse to download that same package at different version... hmm other option I think you're hinting at is what if foobarto/ is the anchor of trust, right? so what if we'd require author to have an pub key at well-known repo-name... something like foobarto/stado-plugins and in it the key would have to be located in .stado/author.pub ... homebrew has similar requirement/setup... would make it easier to manage different plugins in different folders,,,yeah I like that approach better unless you have strong opinion otherwise? this deserver new EP plugin distribution and trust management...

---


## Assistant
*2026-05-05T07:55:25.081Z*

Both calls are right — and the anchor-repo pattern is genuinely cleaner. Walking through the resulting model.

## Anchor-of-trust — Homebrew-style, confirmed

**Convention**: each author maintains a well-known anchor repo `<host>/<owner>/stado-plugins/` containing `.stado/author.pub` (single ed25519 hex pubkey). Discovery is mechanical:

```
Plugin install:   github.com/foobarto/superduperplugin@v1.0.0
Anchor lookup:    github.com/foobarto/stado-plugins/.stado/author.pub
```

Verification chain on every install:
1. Fetch artefact for `<repo>@<version>`.
2. Resolve owner segment (`foobarto`) → fetch anchor repo's `.stado/author.pub` (cached after first fetch).
3. Verify `plugin.manifest.sig` against that pubkey.
4. Refuse install on signature mismatch.

**Why this wins over per-repo pubkeys:**

- Single source of truth scales — author maintains one key in one place; key rotation is one commit, not N.
- Operator's mental model matches reality: trust is to *foobarto*, not to *foobarto's specific repo*. TOFU prompt is "trust this signing entity?" once, applies forever to anything that owner signs.
- Author can use the same anchor repo as a published plugin index — list their tools in `stado-plugins/index.toml`, give operators a discoverable catalogue (Homebrew tap analogue).
- Misuse-resistance: a typo on a plugin repo's pubkey is silently broken; a typo on the anchor breaks *every* install attempt loudly.

**Concerns mitigated:**

- *Anchor repo unavailability*: stado caches the pubkey locally after first fetch. The anchor only needs to be available for the first-ever install from that owner. Cached at `~/.cache/stado/anchors/<host>/<owner>/author.pub`. Re-fetched on `plugin update` to catch rotation; warning emitted if cached and current differ (key rotation event — operator confirms).
- *Multi-org trust*: a Foundation pattern like `cncf/stado-plugins` works identically. Foundation owns the anchor; project repos can be split anywhere under `cncf/*` and all sign with the foundation's key.
- *Author publishing on behalf of someone else*: manifest `author:` field is display-only; the signing fingerprint is the authority. If foobarto wants to publish work under a pseudonym or for a contributor, they sign with their key; operator sees it under `foobarto/`'s namespace, trusts foobarto's vetting.

**Single signing entity per repo** confirmed: no multi-author plugin repos. The signature establishes one accountable party. If a project has multiple human contributors, one of them (or the org's release infrastructure) is the gatekeeper.

## Versioning — strict, no floating

- **Accepted**: `vX.Y.Z` semver tags + full commit SHAs (>= 7 chars).
- **Rejected with helpful error**: `@latest`, `@main`, `@HEAD`, branch names. CLI message: `"@latest is not allowed; pin a semver tag (vX.Y.Z) or commit SHA. To find the newest published version, run: stado plugin update --check github.com/foo/bar"`.
- **Rationale**: floating tags break the entire trust chain. A signed `v1.0.0` is signed forever; a signed `latest` is whatever the author last pushed there, including a key-rotated post-compromise version. Refusing them entirely keeps the security property simple.
- `stado plugin update --check <repo>` queries the anchor's index (or git tags if no index) for newest semver tag matching constraints. Operator decides explicitly whether to update. No silent "always pull newest."

## Multi-version coexistence on disk; single-active per name

Install layout (already supported by current `<name>-<version>/` directory structure):

```
$XDG_DATA_HOME/stado/plugins/
  superduperplugin-v1.0.0/
  superduperplugin-v1.1.0/
  superduperplugin-v2.0.0/
```

All three present. Active one tracked separately (e.g., a `.active-version` symlink, or a `current` field in the per-plugin metadata).

CLI:

```
stado plugin install github.com/foo/bar@v1.0.0     # installs; becomes active if no other version is
stado plugin install github.com/foo/bar@v2.0.0     # installs; ASKS which to make active (default: this one)
                                                   # --keep-active to install but leave v1.0.0 active
stado plugin use github.com/foo/bar v1.0.0         # switch active version (rollback)
stado plugin ls --all-versions                     # shows all installed, active marked
stado plugin remove github.com/foo/bar@v1.0.0      # removes a specific version
stado plugin remove github.com/foo/bar             # removes ALL versions; confirms first
```

Runtime rule: **at most one version of a given plugin name active per session**. The model never sees two `fs.read` tools; the active version's tool surface wins. Project-level config can pin:

```toml
[plugins.fs]
version = "v2.0.0"      # pinned; install-time defaults respected only if absent
```

Why allow side-by-side at all? Three real cases:
- **Rollback**: `v2.1.0` regressed something; `plugin use ... v2.0.0` reverts in one command without re-fetching.
- **A/B during dev**: author iterates; operator switches between candidate versions to compare.
- **Project pinning**: project A uses `v1.x`, project B uses `v2.x`; operator's machine has both.

## Trust + version mismatch handling

Locked behaviour for the "different key signs new version" case:

1. Operator: `stado plugin install github.com/foobarto/superduperplugin@v2.0.0`
2. Stado fetches anchor → fingerprint `49cb...` (already trusted).
3. Stado fetches v2.0.0 artefact → manifest sig fingerprint `aa77...` ≠ `49cb...`.
4. **Loud refusal**:

```
SIGNATURE MISMATCH
  Repo:                    github.com/foobarto/superduperplugin
  Anchor pubkey (trusted): 49cbeaa8289e6623
  This version signed by:  aa77...

This is either a key rotation event or a compromise. Stado refuses to install.

To resolve:
  - If foobarto rotated keys legitimately, the new key should be in the anchor.
    Run: stado plugin update-anchor github.com/foobarto
    (Re-fetches and re-prompts trust on the new fingerprint.)
  - If you don't trust the new key, do nothing. Existing versions stay installed.
```

Three escape hatches: trust the new key (`update-anchor`), pin to a known-good older version (already installed if you have it), or walk away. No silent acceptance.

The same protection covers the "anchor pubkey changed" case: stado fetches the anchor on every install attempt, compares to cached fingerprint, prompts if different. Operator either accepts the rotation or refuses and pins to the cached-key-signed versions only.

## Anchor-repo as published plugin index (optional, future)

The anchor repo is also a natural place for the author to publish a plugin manifest:

```toml
# foobarto/stado-plugins/index.toml — optional, for discoverability
[author]
name = "Bartosz Ptaszynski"
contact = "..."

[plugins.superduperplugin]
description = "..."
repo = "github.com/foobarto/superduperplugin"
versions_supported = ["v1.x", "v2.x"]

[plugins.htb-toolkit]
description = "..."
repo = "github.com/foobarto/htb-writeups"
plugin_subdir_pattern = "htb-toolkit/*"
```

This unblocks `stado plugin search foobarto`, which fetches the anchor, lists plugins, lets the operator discover what's available without already knowing the names. Doesn't have to ship in v1; the convention should reserve the file path now (`index.toml` in the anchor) so it can be added later.

## Updated locked decisions (delta)

- **Anchor-of-trust pattern**: `<host>/<owner>/stado-plugins/.stado/author.pub` is the canonical pubkey location. One key per owner. Cached locally after first fetch. Plugin repos do *not* carry their own pubkey.
- **Single signing entity per repo**: no multi-author repos. The signature is the authority; the manifest's `author:` field is display-only.
- **Strict versioning**: `vX.Y.Z` tags or full commit SHAs only. Floating tags rejected. `plugin update --check` for newest-version queries.
- **Multi-version installs allowed**; **single active per plugin name** at runtime. `plugin use <repo> <version>` switches active. Pin via `[plugins.<name>] version = ...` in config.
- **Trust = author fingerprint**, applies across all versions and all plugins by that owner. Mismatched fingerprint refuses install with three explicit resolution paths (`update-anchor`, pin older version, walk away). Same logic on anchor pubkey rotation.
- **Anchor-as-index**: reserve `index.toml` in the anchor repo for future plugin discovery. Not required v1; convention reserved.
- **EP-0039 — Plugin distribution and trust**: separate EP, lands after EP-0038 or in parallel.

## State of the world

All confirmations through this round are sign-off-locked. The EP/docs sweep is the next step — read every existing EP and `docs/` doc, build the actual supersession map, then start drafting EP-0037.

Want me to kick off the sweep now (read-only, will report the supersession map back before any EP draft starts)?

---

## User
*2026-05-05T07:58:40.115Z*

beautiful! one last time, detailed summary please

---


## Assistant
*2026-05-05T08:01:29.061Z*

# Stado architectural reset — final detailed summary

## 1. Philosophy (load-bearing)

**Permissive-by-default, primitive-rich, layered.** Three positions:

1. **Plugin authors set policy.** Each plugin chooses how heavily to gate its own behaviour (caps declared, approvals, validation). "Go fast" and "hardened audit-trail" plugins are equal first-class citizens.
2. **Operators set admission.** What gets installed, what's enabled, what caps are accepted, what's surfaced to the model.
3. **Operators set process containment.** OS-level wrapping (namespaces, proxies, fs binds, capability changes) — first-class via `[sandbox]` config, not an afterthought.

**Stado guarantees primitives, not policies.** Caps are honoured (declared = enforced upper bound), sandbox runner integrates correctly when present, approval prompts route correctly, sessions durable, signatures verified. Stado does *not* prescribe minimum caps, default approval gates, or enforcement on top of OS-level limits.

**Cap-vs-OS overlap rule**: stado describes intent; the kernel enforces. Stado doesn't double-gate where the OS is already the gate (port < 1024 binding, CAP_NET_RAW for raw ICMP, network namespace blocking dial). Cap declarations remain for visibility and operator admission decisions.

**Slogan**: *lean power tool with modules*. Not "AI assistant with guardrails." The guardrails are modules too.

---

## 2. The invariant

**Stado core ships no native tools.** The host exposes a host-import surface; every tool is a wasm plugin. A curated bundle ships embedded by default; users add, replace, or disable any of them. Existing native tools (`fs`, `bash`/`shell`, `webfetch`, `httpreq`, `rg`, `astgrep`, `lspfind`, `readctx`, `tasktool`, `subagent`) become host-import implementations, not registered tools. `internal/runtime/bundled_plugin_tools.go` registrations all delete; `newBundledPluginTool`/`buildBundledPluginRegistry` wrapper layer deletes; `internal/tools/*` migrates to `internal/host/*` as host-import implementation only.

---

## 3. Host-import surface — ABI v2

```
Tier 1 — Capability primitives (host-only, lazy-init)
  stado_proc_{spawn,read,write,close,wait,kill}
  stado_exec(...)                                              # sugar over proc
  stado_terminal_{open,read,write,resize,close,wait}
  stado_net_dial(transport, addr, opts) → handle               # tcp | udp | unix
  stado_net_listen(transport, addr, opts) → listen_handle      # tcp | unix
  stado_net_accept(listen_handle, timeout)
  stado_net_{read,write,close}(handle, ...)
  stado_net_icmp_{open,send,recv,close}                        # full raw ICMP, no _ping convenience
  stado_fs_*                                                   # kept
  stado_bundled_bin(name) → path                               # lazy extract on first call
  stado_session_*, stado_llm_invoke, stado_log,
  stado_approval_request                                       # existing

Tier 2 — Stateful conveniences (lazy-init)
  stado_http_client_{new,close}
  stado_http_request(client, ...)
  stado_http_request_streaming(client, ...) → response_handle
  stado_dns_resolve(name, type, opts?)
  stado_dns_resolve_axfr(name, server)
  stado_secrets_{get,set,delete,list}

Tier 3 — Stateless format conveniences
  stado_json_{parse,stringify}                                 # strict RFC 8259
  stado_hash(algo, bytes), stado_hmac(algo, key, bytes)        # md5,sha1,sha256,sha512,blake3
  stado_compress(algo, bytes), stado_decompress(algo, bytes)   # gzip,brotli,zstd
```

**Lazy-init / first-call-extract is universal.** Bundled binaries (rg, ast-grep) extracted on first `stado_bundled_bin` call. Bundled wasm plugins instantiated on first tool invocation. HTTP clients, LSP processes, terminal handles, secret backend, JSON/hash/compression engines — all lazy. Process startup does registration only.

**Capability vocabulary** (manifest declarations):
`exec:proc`, `terminal:open`, `net:dial:<transport>:<host>:<port>`, `net:listen:<transport>:<port>`, `net:icmp[:<host>]`, `bundled-bin:<name>`, `dns:resolve[:<glob>]`, `dns:axfr:<zone>`, `dns:reverse:<cidr>`, `secrets:read:<key>`, `secrets:write:<key>`, `crypto:hash`, plus existing `fs:read:<path>`, `fs:write:<path>`, `net:http_request[:<host>]`, `session:*`, `llm:invoke:<budget>`, `ui:approval`. **No synthesised privilege caps** (no `net:listen:privileged` — kernel handles it).

---

## 4. Tool naming convention

- **Canonical form** (manifest, docs, config, CLI): dotted. `fs.read`, `shell.exec`, `web.fetch`, `agent.spawn`, `tools.search`.
- **Wire form** (LLM-facing): underscore. `fs_read`, `shell_exec`. Synthesised as `<plugin>_<tool>` at registration. (Anthropic API tool-name regex doesn't allow dots.)
- **Plugin family idiom**: a plugin wrapping a family of similar implementations exposes both a default tool and per-implementation tools, separated by dotted suffix. E.g. `shell.exec` (uses `[plugins.shell] binary = ...`) plus `shell.bash`, `shell.zsh`, `shell.fish`, `shell.sh`, `shell.pwsh`. Same idiom for `agent.spawn` + `agent.opus`/`agent.haiku`. Convention, not enforcement.

---

## 5. Bundled tool inventory

`fs` (read/write/edit/glob/grep), `shell` (exec/spawn + per-shell variants), `web` (fetch/search/browse), `http` (request/client_new), `lsp` (definition/references/symbols/hover), `rg` (search), `astgrep` (search), `readctx`, `task` (add/list/update/complete), `agent` (spawn/list/read_messages/send_message/cancel), `mcp` (connect/list_tools/call), `image` (info), `tools` (search/describe/categories/in_category), `dns` (resolve), plus `auto-compact` background. Includes the five existing examples (`web-search`, `mcp-client`, `ls`, `image-info`, `browser`) — recompile-only.

---

## 6. Meta-tool dispatch

Stado stops broadcasting every tool's schema in the system prompt. Always-loaded core is small; everything else lives behind tool-search.

**Four meta tools**:
- `tools.search(query?)` → `[{name, summary, categories}]` — no arg lists all, with arg filters.
- `tools.describe(names: [str])` → full JSONSchema + docs for each requested tool. **Batched** (round-trip economics).
- `tools.categories(query?)` → `[string]` — no arg lists all category names, with arg filters.
- `tools.in_category(name)` → `[{name, summary, categories}]`.

(`tools.list` was redundant with `tools.search()` no-args — dropped.)

**Categories**: per-tool in manifest (not per-plugin). Plugin authors pick from a frozen canonical taxonomy. `extra_categories: []` for free-form tags (allowed, flagged as plugin-defined in `describe` output).

**Initial canonical taxonomy** (~21 names, frozen as part of EP-0037, amended only by future EP):
`filesystem, shell, network, web, dns, crypto, data, encoding, code-search, code-edit, lsp, agent, task, mcp, image, secrets, documentation, ctf-offense, ctf-recon, ctf-postex, meta`

---

## 7. Agent surface

Five tools, async-under-the-hood, sync default:

- `agent.spawn(prompt, model?, sandbox_profile?, async?, ephemeral?, parent_session?, allowed_tools?)` → `{id, session_id, status, ...}`. Default `async: false` blocks until completion; `async: true` returns handle immediately. Default model: **inherits parent's**.
- `agent.list()` → `[{id, session_id, status, model, started_at, last_turn_at, cost_so_far_usd}]`. Scoped to caller's spawn tree, not global runtime.
- `agent.read_messages(id, since?, timeout?)` → `{messages, offset, status}`. **Only assistant-role messages** the child explicitly emits — child-curated output channel.
- `agent.send_message(id, msg)` → enqueues user-role message into agent's inbox; delivered at next yield point.
- `agent.cancel(id)` → cancel signal; child exits at next yield.

**Every agent owns a session.** `session_id` returned alongside `id`; same value, two affordances. Operator can `session.read|observe` for full transcript (audit channel) — bypasses child's curation.

**Two communication channels both first-class**:
- `agent.read_messages` — convenience, child-curated, function-call-return shape.
- `session.read|observe` — audit trail, full transcript including tool calls. Requires `session:read|observe` cap if not the spawn parent.

**Session metadata gains a `driver` field**: `human`, `agent:<parent>`, `cron:<routine>`, `bridge:<plugin>`. Lets `session.list` filter by driver.

**Recursive spawning** allowed; capped via `[agents] max_depth` (default 5).

---

## 8. Sessions persist forever by default

Sessions feed `stado usage/stats` and audit; auto-pruning would silently drop history.

- Explicit cleanup: `stado session prune --before <date>` / `stado session delete <id>`.
- Opt-in retention: `[sessions] auto_prune_after = "90d"` in config (off by default).
- Opt-in per-spawn: `agent.spawn(..., ephemeral: true)` skips persistence (no `session.read`, no `usage/stats` line).

---

## 9. Configuration — `.stado/config.toml`

```toml
[tools]
autoload = ["tools.*", "fs.*", "shell.exec"]   # auto-load whitelist; supports * globs
                                                # default hardcoded in stado, overrideable here
disabled = ["browser.*"]                        # blacklist; final filter

[plugins.shell]
binary = "/usr/bin/zsh"
init = ["set -u", "set -o pipefail"]

[plugins.htb-lab]
default_token_path = ".secrets/htb_app_token"

[plugins.fs]
version = "v2.0.0"                              # pin specific installed version

[sessions]
auto_prune_after = ""                           # "" = never; "90d", "30d" otherwise

[agents]
max_depth = 5
idle_timeout = "10m"

[sandbox]
mode = "off"                                    # "off" | "wrap" | "external"
http_proxy = ""                                 # http_proxy + https_proxy in env
dns_servers = []
allow_env = []                                  # allow-list; empty = pass-through everything
plugin_runtime_caps_add = []
plugin_runtime_caps_drop = ["cap_net_raw"]

[sandbox.wrap]
runner = "auto"                                 # auto | bwrap | firejail | sandbox-exec
bind_ro = ["/usr", "/etc/resolv.conf"]
bind_rw = ["~/Dokumenty/htb-writeups"]
network = "host"                                # host | namespaced | off

[sandbox.profiles.htb]
mode = "wrap"
http_proxy = "http://127.0.0.1:8080"
[sandbox.profiles.htb.wrap]
network = "namespaced"
bind_rw = ["~/Dokumenty/htb-writeups"]
```

**Env handling: allow-list, not deny-list.** `allow_env = []` (empty) = pass-through. `allow_env = ["PATH", "HOME", "TERM"]` = strict whitelist.

**Network mode values**: `host` (default), `namespaced` (own netns, no internet unless wrap binds tun/etc.), `off` (no network at all).

**Proxy semantics**: HTTP CONNECT-style proxy via `http_proxy` v1 (allow/deny filtering possible). HTTPS-intercept proxy with custom CA injection: v2+, flagged.

---

## 10. CLI flag flow — single clear sequence

Three flags, applied in this exact order (each filter sees the prior's output):

```
1. --tools=a,b,c             # ENABLE: only these are enabled (plus autoload entries below)
                             # If unset: all installed tools enabled.

2. --tools-autoload=x,y,z    # AUTOLOAD: these are loaded into the always-on set
                             # If unset: hardcoded default list (tools.*, fs.*, shell.exec)
                             #           with config override taking precedence over hardcoded
                             # All others reachable only via tools.search

3. --tools-disable=p,q,r     # DISABLE: removed from the resulting set
                             # Final filter — wins over enable AND autoload
```

All flags accept comma-separated globs. `*` matches one segment. Disabled wins over enabled+autoload.

**Config-vs-CLI precedence**: CLI > project `.stado/config.toml` > user `~/.config/stado/config.toml` > stado hardcoded defaults.

**`--sandbox <name>`** — global flag, optional argument:
- `--sandbox` (no name) — applies `[sandbox]` defaults from config (`mode = "wrap"` if config says so).
- `--sandbox htb` — applies `[sandbox.profiles.htb]` from config.
- Combines with `[sandbox.profiles.<name>]`; named profile wins over root `[sandbox]`.

---

## 11. `stado tool` subcommand

| Verb | Action | Mutates config |
|---|---|---|
| `tool ls [glob]` | List tools with state (autoloaded/enabled/disabled), plugin source, categories | no |
| `tool info <name>` | Full schema + docs + caps + manifest reference | no |
| `tool cats [glob]` | List categories, optionally filtered | no |
| `tool enable <glob>` | Add to `[tools]` whitelist (or remove from `disabled`) | yes |
| `tool disable <glob>` | Add to `[tools.disabled]` | yes |
| `tool autoload <glob>` | Add to `[tools.autoload]` | yes |
| `tool unautoload <glob>` | Remove from `[tools.autoload]` | yes |
| `tool reload <glob>` | Drop cached wasm instance(s); next call re-inits | runtime only |

Flags on mutating verbs: `--global` (writes user-level config), `--config <path>` (explicit target), `--dry-run`. (`tool add`/`remove` rejected as redundant with `plugin install`/`remove`.)

---

## 12. `stado plugin` quality pass

Adds + fixes (separate scoped PR, not an EP):
- sha256 drift auto-detect on `install` (was silent skip).
- `plugin trust --pubkey-file <path>` (avoid `$(cat author.pubkey)` workaround).
- `plugin update [--check] [--all]`.
- `plugin remove <name>[@<version>]`.
- `plugin dev <dir>` — gen-key + build + trust + install --force + reload, one command.
- `plugin sign <dir> --key <path>`.
- `plugin install --autoload` (pin to autoload at install time).
- `plugin doctor` extends with cap-vs-sandbox checks.
- `plugin ls` output renderer matches `tool ls`.
- Plus `plugin reload <name>` (whole-plugin reload), distinct from `tool reload <tool-name>`.

---

## 13. TUI slash commands

Pattern: every CLI subcommand has a `/<subcommand>` mirror. **Slash commands default per-session non-persistent**; `--save` flips to persist.

Mirrored: `/tool *`, `/plugin *`, `/sessions`, `/config [section]`.

Runtime-only:
- `/stats` — tokens, cost, session/agent counts, uptime.
- `/ps` — live wasm/proc/term/net handles, tree by plugin.
- `/top` — live-updating `/ps`.
- `/kill <id|name>` — terminate handle or plugin instance.
- `/agents` — `/sessions --driver=agent` shorthand.
- `/sandbox` — current sandbox state (mode, proxy, env, namespace).
- `/tools <query>` — quick search without going through model.

**Handle ID convention** (formalised in EP-0038): type-prefixed dotted IDs.
```
plugin:fs                 # the fs plugin's wasm instance
proc:fs.7a2b              # a proc handle owned by fs, instance 7a2b
term:shell.9c1d
agent:bf3e
session:abc12345
conn:web.4f5a             # net handle
listen:browser.8a91
```
`/kill plugin:fs` cascades to all child handles. `/kill proc:fs.7a2b` kills one handle.

---

## 14. Plugin distribution and trust (EP-0039)

**Identity**: `<host>/<owner>/<repo>[/<plugin-subdir>]@<version>`.
- v1 supports prefix-matched VCS: `github.com/`, `gitlab.com/`, `codeberg.org/`, generic git hosts.
- Vanity-import discovery (Go-style `<meta>` tag) deferred to v2.

**Versioning — strict, no floating**:
- Accepted: semver tags `vX.Y.Z` and full commit SHAs (>= 7 chars).
- Rejected with helpful error: `@latest`, `@main`, `@HEAD`, branch names.
- Newest-version queries: `stado plugin update --check <repo>`.

**Anchor-of-trust pattern (Homebrew-style)**:
- Convention: `<host>/<owner>/stado-plugins/.stado/author.pub` is the canonical pubkey location.
- One key per owner; one signing entity per repo (no multi-author repos).
- Manifest's `author:` field is display-only; the signing fingerprint is the authority.
- Cached locally at `~/.cache/stado/anchors/<host>/<owner>/author.pub` after first fetch.
- Anchor repo doubles as future plugin index (`index.toml` reserved, not required v1).

**Trust model**:
- TOFU on first install per owner. Prompt offers three scopes: exact repo / `<host>/<owner>/*` (default for that owner) / `*`.
- Trust persisted in `~/.config/stado/trust.toml` keyed by fingerprint.
- Operator can pre-trust without installing: `stado plugin trust github.com/foobarto`.
- Loud refusal on signature mismatch (different key signed a new version): three explicit resolution paths offered (`update-anchor`, pin older known-good version, walk away). Same protection on anchor pubkey rotation.

**Artefact resolution order**:
1. GitHub release attached to tag (preferred).
2. Files at `dist/` in the tagged tree.
3. Source build (last resort, opt-in via `--build`, requires toolchain).

**Lock file** — `.stado/plugin-lock.toml` per project:
```toml
[plugins."github.com/foobarto/superduperplugin"]
version = "v1.0.0"
wasm_sha256 = "abc..."
manifest_sha256 = "def..."
author_fingerprint = "49cbeaa8289e6623"
source = "release"
installed_at = "2026-05-05T10:00:00Z"
```
`stado plugin install --from-lock` reproduces exactly. Mirrors `go.sum` semantics. Commit alongside `.stado/config.toml`.

**Multi-version coexistence**:
- Multiple versions installed side-by-side at `$XDG_DATA_HOME/stado/plugins/<name>-<version>/`.
- At most one version of a given plugin name **active per session**.
- `stado plugin use <repo> <version>` switches active.
- `[plugins.<name>] version = "v2.0.0"` in config pins.
- `plugin remove <repo>@<version>` removes specific; `plugin remove <repo>` (no version) removes all and confirms first.

**Sandbox interaction**:
- Install requires network egress; respects `[sandbox] http_proxy`.
- Fails fast under `network = "off"` with clear message.
- Local tarball cache (`~/.cache/stado/plugin-tarballs/`) enables offline reinstalls after first fetch.
- `[plugins] mirror = "https://my-mirror.local"` config key reserved (not v1) for self-hosted teams.

---

## 15. EP supersession discipline

- Each new EP opens with explicit `## Supersedes` / `## Updates` listing affected predecessors at section granularity.
- Affected EPs get `status: superseded by EP-NNNN` or `status: amended by EP-NNNN — see §X` in frontmatter.
- Reciprocal pointers: old → new and new → old.
- **Pre-drafting sweep required**: read all existing EPs and `docs/` before EP-0037 draft, build actual supersession map.

**Tentative map** (subject to sweep):

| Predecessor | Effect | Reason |
|---|---|---|
| EP-0005 | Updated by EP-0038 §philosophy | Capability principle preserved; framing shifts to "stado describes, OS enforces" |
| EP-0028 — bundled-tools-as-wasm-wrappers | Superseded by EP-0038 | Wrapping-native-as-wasm replaced by no-native-tools invariant |
| EP-0028 — `--with-tool-host` flag | Superseded by EP-0038 | Becomes default; flag retired |
| EP-0028 D1 — exec:bash hard refusal on no-sandbox | Updated by EP-0038 | Becomes warn-loud-run-anyway; hard refusal opt-in via `[sandbox]` |
| EP-0035 — `.stado/` directory | Extended by EP-0037 | Schema growth (`[tools]`, `[sandbox]`, `[plugins.<name>]`); not change |
| EP-0036 — /loop, /monitor, schedule | Unaffected | Orthogonal layer |

**EP-0028 supersession is full** — new design decisions carry forward, masking old ones. No grandfathered behaviour.

---

## 16. EP scope split

**EP-0037 — Tool dispatch + naming + categories + config skeleton + philosophy.**
- Philosophy section (load-bearing).
- Tool naming convention (canonical / wire).
- Categories + frozen taxonomy + manifest field.
- Four meta-tool surface.
- Three-flag CLI.
- `.stado/config.toml` schema for `[tools]`, `[plugins.<name>]`, `[sessions]`, `[agents]`.
- `[sandbox]` schema commitment (skeleton; impl deferred to EP-0038).
- `stado tool` subcommand + `/tool` slash mirrors.
- No ABI changes. Lands first.

**EP-0038 — ABI v2 + bundled wasm + invariant + agents + sandbox impl + EP-0028 supersession.**
- Tier 1/2/3 import surface.
- No-native-tools invariant.
- `internal/host/*` migration (deletes `internal/runtime/bundled_plugin_tools.go`'s registrations, moves `internal/tools/*` to host imports).
- Bundled wasm tool inventory.
- Agent surface (5 tools + sessions-per-agent).
- Handle ID convention.
- Sandbox implementation (`mode = "wrap"` re-exec under bwrap/firejail/sandbox-exec; env allow-list; caps add/drop; proxy enforcement).
- Runtime introspection (`/ps`, `/top`, `/kill`, `/stats`).
- EP-0028 D1 behaviour change.
- ABI version bump to v2; v1 plugins continue working against existing import surface where compatible.

**EP-0039 — Plugin distribution and trust.**
- Anchor-of-trust convention (`<host>/<owner>/stado-plugins/.stado/author.pub`).
- Strict versioning rules.
- Three-tier artefact resolution (release → tree → build).
- Lock file format.
- Multi-version install + active-version switching.
- TOFU trust prompt with scope choice.
- Anchor pubkey caching, rotation handling, mismatch refusal.
- `stado plugin install <remote>`, `update`, `trust`, `untrust`, `use`, `update-anchor`.
- Lands after EP-0038 or in parallel.

**Separate scoped PR (no EP)** — `stado plugin` quality pass: sha drift, `--pubkey-file`, `update`, `dev`, `sign`, `--autoload`, output renderer consistency.

---

## 17. Pre-1.0 stance

- This is pre-1.0; you are the only user.
- Temporary instability during migration is acceptable.
- No backwards-compat shims for retired flags or behaviour.
- Old EPs marked superseded; no grandfathered code paths kept around for "old" users.

---

## 18. Sequence

1. **EP/docs sweep** (read-only). Read every existing EP and `docs/`. Build the actual supersession map. Report back before any EP draft starts.
2. **EP-0037** drafted into `docs/eps/` (or wherever the EP convention lives — confirmed via sweep).
3. Operator review.
4. **EP-0038** drafted.
5. Operator review.
6. **EP-0039** drafted.
7. Operator review.
8. Implementation begins, in EP order. Each EP's PR set tagged with the EP number for traceability.

---



## User
*2026-05-05T08:03:29.178Z*

since every agent gets a session how about if user could switch to that sessions (same shortcut/slash command as right now)... read-only at minimum but what do you think about user being able to communicate with subagent directly then?



## Assistant
*2026-05-05T08:05:14.447Z*

Yes — and it's not really a "feature," it's the architecture's symmetry becoming visible. If agents are sessions, and sessions can be opened and typed into, then the operator switching into an agent's session and chatting is just a natural consequence. Worth designing carefully though.

## What falls out automatically

Recall the locked claim: *"`agent.send_message(id, msg)` is literally the same operation as `stado run --resume <session_id>` with a user-role message."* That's the whole architectural payoff. The agent's run loop pulls user-role messages from its session's inbox; it doesn't care who put them there.

So "let the operator switch in and type" is just **adding the operator as a second producer of user-role messages into the same queue**. No new mechanism — the queue is multi-producer by virtue of being a queue.

## Read-only mode (free, ship in EP-0037)

```
/session list [--driver=agent] [--tree]
/session show <id>
/session attach <id> --read-only
```

`show` = one-shot render of full transcript. `attach --read-only` = scroll-back-and-follow live updates as the agent works. Either way, no input. This is essentially `session.read|observe` exposed in the TUI.

## Read-write attach (real design work, ship in EP-0038)

```
/session attach <id>      # default: read-write
/session detach           # return to original session (Ctrl-D also works)
/session inject <id> <msg>  # one-shot inject without switching focus
```

When the operator is attached, their typed input becomes a user-role message in the child's session, with metadata `source: human:<operator>`. The child's loop reads from the inbox; doesn't care if the user-role message came from a parent agent's `agent.send_message` or the human's TTY.

**Three concrete behaviours that need to be locked:**

### 1. Producer separation in metadata, not queue

Multi-producer is fine; the session's message log records each message's `source` field:
- `source: human:operator` — typed via TUI
- `source: agent:<parent_session_id>` — sent via `agent.send_message`
- `source: cron:<routine_id>` — scheduled injection (future)
- `source: api:<caller>` — programmatic injection (future)

The renderer uses these to mark the transcript:

```
[parent:bf3e] What's the SHA of /tmp/x?
[child]       SHA-256: abc123...
[parent:bf3e] OK, verify it matches the manifest.
[child]       Checking...
[YOU]         Wait — verify the GPG signature first.
[child]       Verifying GPG signature... matched.
```

`[YOU]` is the operator-typed message; visually distinct. Subsequent automated runs/replays preserve the trace; readers see exactly where human intervention happened.

### 2. The parent agent doesn't see human interventions through `agent.read_messages`

The convenience channel is documented as **only assistant-role messages**. Human inputs are user-role; they don't leak. So a parent that pings `agent.read_messages` periodically just sees the child's responses, which now happen to incorporate the human's redirection. Parent has no idea something exceptional happened — the work continues.

A *paranoid* parent (or supervisor agent) that uses `session.observe(child_id)` sees everything including the human's `[YOU]` line. Detection is opt-in by capability, not free; matches the philosophy ("stado describes; consumer chooses what to read").

This is the right shape: the human supervisor can intervene without the parent panicking or restarting; the parent that *cares* about deviation can detect it explicitly.

### 3. Cost and replay attribution

- **Tokens consumed processing the human's message** roll up to the **child's session** (its `cost_so_far_usd` includes them). The child is the one who used the tokens; doesn't matter who sent the message.
- **Replay** of an agent's session by default *skips* messages with `source: human:*` (so re-running the agent doesn't reproduce a one-time human nudge). `--include-human-injections` flag for full fidelity replay. Same pattern for any future non-agent producer.

## Conflicts and corner cases

- **Parent calls `agent.send_message` while human is attached and typing**: messages serialize on the inbox queue in arrival order. Both go to the child. Child processes in order. Visible in trace.
- **Parent calls `agent.cancel` while human is attached**: cancel wins. Child terminates. Human's TUI view shows "session cancelled by parent agent." Operator can `/session detach` and continue elsewhere.
- **Human types `/session detach` mid-conversation**: their last message stays in the inbox if not yet consumed; child eventually picks it up and responds; parent's polling resumes seeing those responses. Detach is silent — no signalling, no abort.
- **Recursive attach** (operator attaches to grandchild while parent's also attached to child): allowed. Each attach is independent. The TUI shows a path breadcrumb (`session:root → agent:abc → agent:def → [YOU]`).
- **Operator attaches to *their own* live session by accident** (the one they're typing into): refused with `error: cannot attach to your own active session`.

## Caps and gating

In single-user stado (the only mode for now), the operator has implicit full access to all sessions in their own runtime. No per-attach cap.

When stado eventually grows multi-user (a daemon serving multiple humans), `session:attach:<id>` cap with read/write modes becomes the gate — same vocabulary as `session:read|observe`. Reserve the cap name now.

## Pause-on-attach question

Should attaching auto-pause the parent's polling, so the parent doesn't accidentally drown out the human's typing with its own `agent.send_message` calls?

I'd argue **no, don't pause by default**. Two reasons:
1. The parent might be doing something useful that pausing would stall (e.g., reading a sibling agent's output).
2. Polling and writing are independent — if the parent only polls, attaching doesn't conflict with it at all.

`/session attach <id> --pause-parent` as an opt-in flag for the cases where the operator wants exclusive control. Adds a `paused-by-attach` status to the parent's view of the child; resumes on detach.

## Summary of additions

**EP-0037 (read-only TUI):**
- `/session list [--driver=agent] [--tree]`
- `/session show <id>`
- `/session attach <id> --read-only` (live-follow viewer)

**EP-0038 (read-write TUI):**
- `/session attach <id>` (interactive, RW default)
- `/session detach`
- `/session inject <id> <message>` (one-shot, no focus switch)
- `/session attach <id> --pause-parent` (opt-in pause)
- Message-source metadata: `source: human:* | agent:* | cron:* | api:*`
- TUI renderer marks `[YOU]` visually for human-typed messages
- `agent.read_messages` continues to filter to assistant-role only (human intervention invisible to convenience channel by design); `session.observe` sees everything
- Replay skips `source: human:*` by default; `--include-human-injections` overrides
- Cap reserved: `session:attach:<id>` (single-user stado: implicit; multi-user future: explicit)

## Why this matters beyond "neat"

A few workflows it unblocks that are awkward today:

- **Mid-flight correction without restart**: agent goes down a wrong path; you barge in with three lines of guidance; agent absorbs and continues. No re-spawn cost, no context rebuild.
- **Hybrid agent + human work**: spawn an agent for prep ("read these 50 files and summarize what changed"), human takes over for the judgement call ("apply this fix to the three you flagged"), hands back to agent for follow-up ("now write tests"). One session, three drivers in sequence.
- **Plugin author debugging**: spawn an agent against a buggy plugin, watch it fumble in `/session attach --read-only`, intervene with manual tool calls in your own session to confirm a hypothesis, push a plugin fix, `tool reload`, attach and retry. Tight loop.
- **Live observability of long-running agents**: an agent kicked off an hour ago is still chugging; `/session attach --read-only` to peek without disturbing.

This isn't a feature you need to advertise — it's a property that should silently work because the architecture is right. EP-0038's job is to lock the producer-metadata model and the renderer behaviour so the symmetry holds in practice.

## Updated locked decisions (delta)

- **`/session attach <id>` is a first-class TUI operation.** Read-only viewer in EP-0037; interactive read-write in EP-0038.
- **Sessions are multi-producer** for user-role messages. Each message records `source: human:* | agent:* | cron:* | api:*`. TUI distinguishes `[YOU]` visually.
- **`agent.read_messages` continues to filter to assistant-role only**, by design — human interventions are invisible to the parent's convenience channel; visible to `session.observe`.
- **Replays skip `source: human:*` by default**; opt-in to include.
- **Cost rolls up to the session's owner**, regardless of who produced the message.
- **`session:attach:<id>` cap reserved** for future multi-user mode; single-user stado treats operator access as implicit.
- **Pause-on-attach is opt-in** via `--pause-parent` flag.

If the EP/docs sweep starts now, all of this lands cleanly distributed across EP-0037 (read-only viewer + slash skeleton) and EP-0038 (multi-producer model + interactive attach + renderer). Green light?
