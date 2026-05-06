# Stado architectural reset — final summary

> **Editorial note (2026-05-06):** This file was previously a 2018-line
> verbatim chat transcript between the operator and the Claude assistant
> during the 2026-05-05 architectural-reset planning session. The
> conversational back-and-forth has been replaced with the curated
> "final summary" the assistant produced at the close of that session
> (lines 1-152 of the original file). The deleted material was
> brainstorm-style and superseded by:
>
> - The locked decisions block below
> - `docs/eps/0037-*.md` and `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md`
>   which contain the EP-final designs, including superseding decisions
>   the chat preceded
> - `docs/superpowers/specs/` where the per-feature specs live
>
> If you need the original transcript for archaeological purposes, it's
> in git history before the security/PII audit commit; refer to
> `git log --diff-filter=D --follow -- docs/eps/notes/2026-05-05-architectural-reset.md`.

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

**Third-party (out-of-tree, e.g. `~/<projects>/htb-toolkit/`)**:
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
