# Host imports — wasm plugin reference

This is the canonical reference for every host import a wasm plugin
can call. Generated from `internal/plugins/runtime/host_*.go` +
`tool_imports.go` (see [REGENERATION](#regeneration) below).

> **For context:** plugins compile to wasm and run inside `wazero`.
> Each call into `host_*` from inside a plugin is a host-import call.
> Capabilities declared in `plugin.manifest.json` gate which imports
> the plugin can reach; calling an ungated import returns -1 with
> a host-side audit-log entry.

## Table of contents

- [Surface map](#surface-map) — at-a-glance grouping
- [Tier 1 — capability primitives](#tier-1--capability-primitives)
  - [stado_log](#stado_log)
  - [stado_fs_*](#stado_fs_)
  - [stado_proc_* + stado_exec](#stado_proc_--stado_exec)
  - [stado_terminal_* / stado_pty_*](#stado_terminal_--stado_pty_)
  - [stado_bundled_bin](#stado_bundled_bin)
  - [stado_session_*](#stado_session_)
  - [stado_llm_invoke](#stado_llm_invoke)
  - [stado_ui_approve](#stado_ui_approve)
  - [stado_cfg_state_dir](#stado_cfg_state_dir)
- [Tier 2 — stateful conveniences](#tier-2--stateful-conveniences)
  - [stado_http_client_*](#stado_http_client_)
  - [stado_http_request / stado_http_get](#stado_http_request--stado_http_get)
  - [stado_dns_resolve](#stado_dns_resolve)
  - [stado_secrets_*](#stado_secrets_)
- [Tier 3 — stateless format conveniences](#tier-3--stateless-format-conveniences)
  - [stado_hash, stado_hmac](#stado_hash-stado_hmac)
  - [stado_compress, stado_decompress](#stado_compress-stado_decompress)
- [Agent surface](#agent-surface)
  - [stado_agent_*](#stado_agent_)
- [Memory surface](#memory-surface)
  - [stado_memory_*](#stado_memory_)
- [Tool-bridging imports](#tool-bridging-imports)
- [SDK-side exports](#sdk-side-exports)
- [Patterns and anti-patterns](#patterns-and-anti-patterns)
- [Capability vocabulary](#capability-vocabulary)
- [Regeneration](#regeneration)

## Surface map

```
Tier 1 — capability primitives  (host_log, host_fs, host_proc,
                                  host_pty, host_bundled_bin,
                                  host_session, host_llm, host_ui,
                                  host_cfg)

Tier 2 — stateful conveniences  (host_http_client, host_dns,
                                  host_secrets) + tool-bridging
                                  (stado_http_request via
                                  tool_imports.go)

Tier 3 — stateless conveniences (host_crypto, host_compress)

Agent surface                   (host_agent)
Memory surface                  (host_memory)
Tool-bridging                   (stado_fs_tool_*, stado_search_*,
                                 stado_lsp_*, stado_exec_bash,
                                 stado_http_get/_request)
```

ABI conventions:

- Pointer + length pairs: every variable-length argument passes as
  two i32 (pointer + byte length). The host reads from the wasm
  module's linear memory at `[ptr, ptr+len)`.
- Returns: `i32` for byte counts / status codes (-1 = error or
  capability denied; 0 = success or "no data"). `i64` for opaque
  handles (typed-prefix format `<type>:<id>` where surfaced; bare
  uint32-as-i64 internally).
- Output buffers: caller passes `(out_ptr, out_max)`; the host
  writes up to `out_max` bytes and returns the actual count. If
  the host needs more space than `out_max`, it returns the required
  count (caller can re-call with a larger buffer) OR `-1` with the
  per-import documented semantics.
- All allocation is plugin-side via `stado_alloc` (see
  [SDK-side exports](#sdk-side-exports)). The host never `malloc`s
  into wasm memory; it copies into a buffer the plugin pre-allocated.

## Tier 1 — capability primitives

Self-contained primitives the plugin uses directly. Lazy-init
universal: nothing here costs anything at registration time;
state is allocated on first call.

### stado_log

| Field | Value |
|---|---|
| File | `host_log.go` |
| Signature | `stado_log(level i32, msg_ptr i32, msg_len i32) → i32` |
| Capability | none — every plugin can log |
| Returns | always 0 |

Writes a structured log entry through stado's audit logger. Levels:
0=debug, 1=info, 2=warn, 3=error. The plugin's manifest name is
attached automatically.

### stado_fs_*

| Import | Capability | Notes |
|---|---|---|
| `stado_fs_read(path_ptr, path_len, out_ptr, out_max) → i32` | `fs:read:<glob>` | Reads up to `out_max` bytes; returns count or -1 |
| `stado_fs_read_partial(path_ptr, path_len, offset i64, out_ptr, out_max) → i32` | `fs:read:<glob>` | Ranged read; offset is byte-position from file start |
| `stado_fs_write(path_ptr, path_len, data_ptr, data_len) → i32` | `fs:write:<glob>` | Atomic write (tempfile + rename); returns 0 or -1 |

Capability paths are glob-matched against the resolved absolute
path. Workdir-rooted patterns (`fs:read:.`, `fs:read:./output/`)
are auto-rooted at the operator's CWD or the session worktree —
see EP-0027.

### stado_proc_* + stado_exec

| Import | Returns | Capability | Description |
|---|---|---|---|
| `stado_exec(args_ptr, args_len, result_ptr, result_max) → i32` | bytes written | `exec:proc:<binary>` | One-shot synchronous run. Argv passed as JSON array. Captures stdout+stderr. Result format: `{stdout, stderr, exit_code}` JSON |
| `stado_proc_spawn(...) → i64` | typed handle `proc:<id>` | `exec:proc:<binary>` | Async process; returns immediately. Drive via read/write/wait/kill |
| `stado_proc_read(handle, out_ptr, out_max) → i32` | bytes read | inherited from spawn | Non-blocking; -1 on EOF |
| `stado_proc_write(handle, data_ptr, data_len) → i32` | bytes written | inherited from spawn | Writes to stdin |
| `stado_proc_wait(handle) → i32` | exit code | inherited from spawn | Blocks until process exits |
| `stado_proc_kill(handle, signal i32) → i32` | 0/-1 | inherited from spawn | Posix signal number |
| `stado_proc_close(handle) → i32` | 0 | inherited from spawn | Idempotent; releases handle |

`exec:proc:<binary>` is a glob: `exec:proc:/usr/bin/ls`,
`exec:proc:/bin/*`, `exec:proc:*` (broad). Multiple `exec:proc:*`
caps stack — declare each binary your plugin needs.

The narrow capability surface here is the recommended replacement
for the broader `exec:bash` (`stado_exec_bash` tool import) —
operators see exactly which binaries a plugin runs.

### stado_terminal_* / stado_pty_*

PTY-backed shell sessions. Both name families register as aliases:
`stado_terminal_*` is the canonical (architectural-reset locked)
name; `stado_pty_*` is the legacy alias kept for backward compat.

| `stado_pty_*` (legacy) | `stado_terminal_*` (canonical) | Returns | Description |
|---|---|---|---|
| `stado_pty_create` | `stado_terminal_open` | typed handle `term:<id>` | Open PTY session |
| `stado_pty_list` | `stado_terminal_list` | bytes (JSON list) | List active sessions |
| `stado_pty_attach` | `stado_terminal_attach` | 0/-1 | Single-attach lock; force=true to steal |
| `stado_pty_detach` | `stado_terminal_detach` | 0/-1 | Release attach lock |
| `stado_pty_read` | `stado_terminal_read` | bytes | Buffered output read |
| `stado_pty_write` | `stado_terminal_write` | bytes | Stdin write |
| `stado_pty_signal` | `stado_terminal_signal` | 0/-1 | Posix signal (out-of-band) |
| `stado_pty_resize` | `stado_terminal_resize` | 0/-1 | Cols + rows |
| `stado_pty_destroy` | `stado_terminal_close` | 0 | Kill + free |

Capability: `terminal:open` (broad) — gates all PTY ops.

### stado_bundled_bin

| Field | Value |
|---|---|
| File | `host_bundled_bin.go` |
| Signature | `stado_bundled_bin(name_ptr, name_len, path_out_ptr, path_max) → i32` |
| Capability | `bundled-bin:<name>` |
| Returns | bytes (the on-disk path) or -1 |

Lazy-extracts a stado-bundled binary (rg, ast-grep) to disk on
first call; returns the absolute path. Plugin then calls it via
`stado_proc_spawn`. The cap is per-binary: `bundled-bin:rg`,
`bundled-bin:ast-grep`.

### stado_session_*

| Import | Returns | Capability |
|---|---|---|
| `stado_session_next_event(timeout_ms, out_ptr, out_max) → i32` | bytes | `session:observe` |
| `stado_session_read(field_ptr, field_len, out_ptr, out_max) → i32` | bytes | `session:read` |
| `stado_session_fork(at_ref_ptr, at_ref_len, seed_ptr, seed_len, out_ptr, out_max) → i32` | bytes (new session id) | `session:fork` |

`session:read` field names are spec-defined: `message_count`,
`token_count`, `session_id`, `last_turn_ref`, `history`. See
EP-0029.

### stado_llm_invoke

| Field | Value |
|---|---|
| File | `host_llm.go` |
| Signature | `stado_llm_invoke(prompt_ptr, prompt_len, out_ptr, out_max) → i32` |
| Capability | `llm:invoke[:<budget-tokens>]` |
| Returns | bytes (model reply) or -1 if budget exceeded |

One-shot completion against the active provider. Budget cap is
per-session-cumulative; default 10000 tokens when unspecified.

### stado_ui_approve

| Field | Value |
|---|---|
| File | `host_ui.go` |
| Signature | `stado_ui_approve(title_ptr, title_len, body_ptr, body_len) → i32` |
| Capability | `ui:approval` |
| Returns | 1 (approved) / 0 (denied) / -1 (no approval bridge) |

Surfaces an approval prompt to the operator via the TUI's approval
bridge. Returns -1 when running outside a TUI (headless, plugin
run); plugin should fail safely.

### stado_cfg_state_dir

| Field | Value |
|---|---|
| File | `host_cfg.go` |
| Signature | `stado_cfg_state_dir(out_ptr, out_max) → i32` |
| Capability | `cfg:state_dir` |
| Returns | bytes (absolute state-dir path) or -1 |

Returns `<XDG_DATA_HOME>/stado` so plugins can locate other
installed plugins, the trust store, etc. EP-0029.

## Tier 2 — stateful conveniences

### stado_http_client_*

EP-0038e — stateful HTTP client with cookie jar, redirect cap,
mux limits, dial guard.

| Import | Returns | Capability |
|---|---|---|
| `stado_http_client_create(opts_ptr, opts_len) → i64` | typed handle `http:<id>` | `net:http_client` (+ existing `net:http_request:<host>` allowlist applies) |
| `stado_http_client_close(handle) → i32` | 0 | inherited from create |
| `stado_http_client_request(handle, method_ptr/len, url_ptr/len, headers_ptr/len, body_ptr/len, resp_out_ptr, resp_max) → i32` | bytes | inherited |

`opts` JSON shape:
```json
{
  "max_redirects": 10,
  "follow_subdomain_only": false,
  "max_conns_per_host": 4,
  "max_total_conns": 32,
  "timeout_seconds": 30,
  "allowed_hosts": ["api.example.com"],
  "allow_private": false
}
```

Response JSON shape:
```json
{
  "status": 200,
  "headers": {"Content-Type": ["text/html"]},
  "final_url": "https://...",
  "body_b64": "<base64>"
}
```

`opts.allowed_hosts` are intersected with the host's
`net:http_request:<host>` allowlist — the manifest's host gates
are the upper bound. `opts.allow_private = true` requires
`net:http_request_private` cap on the manifest.

### stado_http_request / stado_http_get

These are tool-bridging imports — see [Tool-bridging imports](#tool-bridging-imports).

`stado_http_request` accepts an optional `proxy_url` field on its
request struct (added 2026-05-06). Schemes:

- `http://`, `https://` — HTTP CONNECT/forward proxy
- `socks5://`, `socks5h://` — SOCKS5 proxy (5h resolves at proxy)

Proxy use case: after a network pivot (e.g. ligolo-ng on
`127.0.0.1:1080`), every WASM tool wants to reach inner-subnet hosts
without dropping to bash. Set `proxy_url: "socks5h://127.0.0.1:1080"`
on the request; the dial guard still applies to the proxy address
itself, so set `net:http_request_private` if the proxy lives on
loopback / RFC1918 (the typical case for pivots).

### stado_dns_resolve

| Field | Value |
|---|---|
| File | `host_dns.go` |
| Signature | `stado_dns_resolve(args_ptr, args_len, out_ptr, out_max) → i32` |
| Capability | `dns:resolve[:<glob>]` |
| Returns | bytes (JSON-encoded result) or -1 |

Args JSON: `{"name": "example.com", "qtype": "A"|"AAAA"|"TXT"|"MX"|"NS"|"PTR", "server"?: "8.8.8.8", "timeout_ms"?: 5000}`.
Result: `{"records": [{"name", "type", "value"}], "error"?: "..."}`.

### stado_instance_*

Process-lifetime in-memory KV store with per-plugin namespacing.
For state that needs to span tool calls but doesn't need to persist
across stado restarts (auth cookies, session tokens, intermediate
data through a multi-step exploit chain). Use `stado_secrets_*` if
you need disk persistence.

| Import | Returns | Capability |
|---|---|---|
| `stado_instance_get(key_ptr, key_len, out_ptr, out_max) → i32` | bytes (value) | `state:read[:<glob>]` |
| `stado_instance_set(key_ptr, key_len, value_ptr, value_len) → i32` | 0 | `state:write[:<glob>]` |
| `stado_instance_delete(key_ptr, key_len) → i32` | 0 | `state:write[:<glob>]` (idempotent) |
| `stado_instance_list(prefix_ptr, prefix_len, out_ptr, out_max) → i32` | bytes (`\n`-joined) | `state:read` (broad — empty globs OR `*`) |

Bounds: 1 MB per value, 16 MB total per plugin. `_set` returns -1
when either limit would be exceeded. Glob shape matches secrets:
empty `state:read` = match-all; `state:read:cookies_*` narrows to a
key prefix.

Cleared on stado exit. Plugins can't read another plugin's keys
even with broad caps — namespacing is enforced per the host's
`State.PluginName` field.

### stado_secrets_*

EP-0038e — operator secret store. Files at
`<state-dir>/secrets/<name>` mode 0600; refusal on permissions
widening.

| Import | Returns | Capability |
|---|---|---|
| `stado_secrets_get(name_ptr, name_len, out_ptr, out_max) → i32` | bytes (raw value) | `secrets:read[:<name-glob>]` |
| `stado_secrets_put(name_ptr, name_len, value_ptr, value_len) → i32` | 0 | `secrets:write[:<name-glob>]` |
| `stado_secrets_list(out_ptr, out_max) → i32` | bytes (`\n`-joined) | `secrets:read` (broad) |

Every call (allowed AND denied) emits a structured audit event
via `Host.Secrets.AuditEmitter`. **Secret names go to logs;
secret VALUES never do.** Plugins are responsible for not echoing
values to their own stdout/error paths.

## Tier 3 — stateless format conveniences

### stado_hash, stado_hmac

| Import | Capability | Algorithms |
|---|---|---|
| `stado_hash(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_max) → i32` | `crypto:hash` | md5, sha1, sha256, sha512, blake3 |
| `stado_hmac(algo_ptr, algo_len, key_ptr, key_len, data_ptr, data_len, out_ptr, out_max) → i32` | `crypto:hash` | same algorithms |

Output is the raw digest (not hex/base64); the plugin formats it.

### stado_compress, stado_decompress

| Import | Capability | Algorithms |
|---|---|---|
| `stado_compress(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_max) → i32` | none — always available | gzip, zstd, brotli |
| `stado_decompress(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_max) → i32` | none | same |

If `out_max` is too small, returns the required size (re-call with
that buffer). On corrupt input returns -1.

## Agent surface

EP-0038c — wasm plugins talk to the in-process Fleet via these
imports. Manifest declares `agent:fleet`; bundled `agent` plugin
is the canonical user.

### stado_agent_*

| Import | Capability | Description |
|---|---|---|
| `stado_agent_spawn(req_ptr, req_len, out_ptr, out_max) → i32` | `agent:fleet` | Spawn child agent. Args: `{prompt, model?, async?, ephemeral?, parent_session?, sandbox_profile?, allowed_tools?[]}`. Returns `{id, session_id, status, final_text?}` |
| `stado_agent_list(out_ptr, out_max) → i32` | `agent:fleet` | List agents in caller's spawn tree |
| `stado_agent_read_messages(args_ptr, args_len, out_ptr, out_max) → i32` | `agent:fleet` | Args: `{id, since?, timeout_ms?}`. Returns `{messages[], offset, status}` |
| `stado_agent_send_message(args_ptr, args_len) → i32` | `agent:fleet` | Args: `{id, message}`. Posted at the child's next yield point |
| `stado_agent_cancel(args_ptr, args_len) → i32` | `agent:fleet` | Args: `{id}`. Child exits at next yield |

## Memory surface

EP-0015 memory-system plugin's host-side imports.

| Import | Capability | Description |
|---|---|---|
| `stado_memory_query(args_ptr, args_len, out_ptr, out_max) → i32` | `memory:read` | Query stored memories |
| `stado_memory_propose(args_ptr, args_len) → i32` | `memory:write` | Propose new memory entry |
| `stado_memory_update(args_ptr, args_len) → i32` | `memory:write` | Update existing entry |

## Tool-bridging imports

These imports let a wasm plugin invoke stado's bundled native
tools. Each is gated by the same capabilities the operator-facing
tool would require. Lives in `tool_imports.go`; the imports
re-marshal the wasm-side args into the tool's Go schema and
invoke it through the host's tool registry.

| Import | Backing tool | Capability |
|---|---|---|
| `stado_fs_tool_read` | fs.ReadTool | `fs:read:.` |
| `stado_fs_tool_write` | fs.WriteTool | `fs:write:.` |
| `stado_fs_tool_edit` | fs.EditTool | `fs:read:.` + `fs:write:.` |
| `stado_fs_tool_glob` | fs.GlobTool | `fs:read:.` (full scope) |
| `stado_fs_tool_grep` | fs.GrepTool | `fs:read:.` (full scope) |
| `stado_fs_tool_read_context` | readctx.Tool | `fs:read:.` (full scope) |
| `stado_exec_bash` | bash.BashTool | `exec:bash` (broad shell) |
| `stado_http_get` | webfetch.WebFetchTool | `net:http_get` or `net:<host>` |
| `stado_http_request` | httpreq.RequestTool | `net:http_request` or `net:http_request:<host>` |
| `stado_search_ripgrep` | rg.Tool | `fs:read:.` + `exec:rg` |
| `stado_search_ast_grep` | astgrep.Tool | `fs:read:.` + `fs:write:.` + `exec:ast-grep` |
| `stado_lsp_find_definition` | lspfind.Definition | `fs:read:.` + `lsp:query` |
| `stado_lsp_find_references` | lspfind.FindReferences | `fs:read:.` + `lsp:query` |
| `stado_lsp_document_symbols` | lspfind.DocumentSymbols | `fs:read:.` + `lsp:query` |
| `stado_lsp_hover` | lspfind.Hover | `fs:read:.` + `lsp:query` |

These imports return **byte counts** (or -1) and write JSON
results into `(result_ptr, result_max)`. Each tool's args are
documented in `internal/tools/<tool>/`'s package-level Go doc.

## SDK-side exports

The host calls into the plugin to:

- Allocate wasm memory: `stado_alloc(size i32) → i32` (returns ptr; null = OOM)
- Free wasm memory: `stado_free(ptr i32, size i32) → none`
- Run a tool: `stado_tool_<name>(args_ptr i32, args_len i32, result_ptr i32, result_max i32) → i32`

The plugin SDK (`pkg/plugin-sdk-zig/`, `pkg/plugin-sdk-go/`)
provides these as `pub fn` / `//export` declarations.

## Patterns and anti-patterns

### Use `stado_proc_*` + `exec:proc:<binary>`, not `stado_exec_bash`

The bash tool is a broad escape hatch. When you know the binary
your plugin needs, declare `exec:proc:/usr/bin/ldap-search` and
call it via `stado_proc_spawn`. The operator sees exactly which
binaries you run; the audit story stays clean.

### Don't conflate plugin execution with agent orchestration

A pattern that *seems* useful but is wrong:
"plugin reads stdout of an external shell.exec session."

That's mixing layers. The clean model:
- The agent calls `shell.exec` (bash/exec tool), gets stdout.
- The agent then passes that stdout as args to the wasm plugin.

If the plugin needs to spawn a subprocess on its own, use
`stado_proc_spawn` directly with a narrow `exec:proc:<binary>`
cap. Avoid building a "read someone else's session output" import
— it would entangle the audit graph.

### Long-lived state across tool calls

Tool calls today are stateless: each invocation gets a fresh wasm
instance. For state that needs to span calls (auth cookies, session
tokens, accumulated data), see:

- **`stado_http_client_*`** — cookie jar persists for the
  client-handle's lifetime
- **`stado_secrets_*`** — operator-managed secrets persist on disk
- **(Phase 2 follow-up)** `stado_instance_get/set` — KV store with
  per-plugin namespacing for arbitrary state. Not yet shipped.

If your plugin needs persistent state RIGHT NOW and these don't fit,
write to `<state-dir>/<plugin-name>/<key>` via `stado_fs_write`
with `fs:write:<that-path>` cap.

### Returning more bytes than `out_max` allows

The pattern: host imports return the *required* size when the
caller's buffer is too small, and the caller re-allocs and re-calls.
Plugin SDK helpers wrap this — see `stado.zig`'s `readToBuffer`
pattern.

For imports without re-call semantics (some return -1 on overflow),
size your buffer based on the import's documented worst-case (e.g.
`stado_dns_resolve` worst case is ~64KB for a TXT record blob).

## Capability vocabulary

Every cap-gated import lists its capability above. The full
vocabulary (manifest declarations):

```
fs:read:<glob>             fs:write:<glob>
exec:proc:<binary-glob>    exec:bash
exec:rg                    exec:ast-grep
net:http_get               net:<host>
net:http_request           net:http_request:<host>
net:http_request_private   net:http_client
dns:resolve                dns:resolve:<glob>
secrets:read[:<glob>]      secrets:write[:<glob>]
crypto:hash
state:read[:<glob>]        state:write[:<glob>]
session:observe            session:read
session:fork
llm:invoke[:<budget>]
agent:fleet
memory:read                memory:write
ui:approval
terminal:open
bundled-bin:<name>
cfg:state_dir
lsp:query
```

Globs use shell-glob semantics (`fs:read:./output/*.json`,
`exec:proc:/usr/bin/*`). Multiple caps of the same kind stack
(declare each path/binary/host you need).

## Regeneration

This doc is not auto-generated yet. To verify it stays in sync
after host_*.go edits:

```sh
# count imports declared in source
grep -rE 'Export\("stado_' internal/plugins/runtime/host_*.go \
  | grep -v _test.go | grep -v host_imports.go \
  | sort -u | wc -l

# additionally tool_imports.go's exportName entries
grep -E 'exportName: "stado_' internal/plugins/runtime/tool_imports.go | wc -l
```

If the totals diverge from this doc's counts, a host import has
been added/removed and this doc needs updating. Each new import
should add: (1) signature row, (2) capability row, (3) a paragraph
in the right Tier section.

A future improvement: a one-shot generator (`scripts/gen-host-imports-md.go`)
that reads the host_*.go files and produces this markdown; that's
out of scope for the initial doc but would be the right next step
once the surface stabilises.
