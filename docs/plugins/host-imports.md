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

### stado_http_upload_create / stado_http_upload_write / stado_http_upload_finish

EP-0038i — chunked HTTP request body delivery for plugins uploading
large payloads (multi-GB files, dynamic content) without buffering
the body whole into wasm memory. Companion to the response-streaming
imports (the response from `_upload_finish` is a `httpresp:<id>`
the plugin drains via `_response_read` / `_response_close`).

| Import | Returns |
|---|---|
| `stado_http_upload_create(args_ptr, args_len, out_ptr, out_max) → i32` | bytes of result JSON written; -1 on error. Result: `{upload_handle: u32}` |
| `stado_http_upload_write(handle, data_ptr, data_len) → i32` | bytes written to request body; -1 on error |
| `stado_http_upload_finish(handle, out_ptr, out_max) → i32` | bytes of response JSON written; -1 on error. Result: `{status, headers, body_handle}` |

**Capability:** reuses `net:http_request[:<host>]`. No new cap.

**Args JSON:** `{method, url, headers?, timeout_ms?, content_length?}`.
Body is **not** in args — comes through `_upload_write`. Set
`content_length` if you know the size; otherwise the request uses
HTTP/1.1 chunked transfer encoding automatically.

**Flow:**

```
upload_handle = stado_http_upload_create(args)
loop:
    stado_http_upload_write(upload_handle, chunk)
result = stado_http_upload_finish(upload_handle)
# result.body_handle is a httpresp:<id>; drain via:
loop:
    n = stado_http_response_read(result.body_handle, buf, max, timeout)
    # n == 0 → EOF
stado_http_response_close(result.body_handle)
```

**Resource cap:** 8 concurrent in-flight uploads per Runtime. The
9th `_upload_create` returns -1. Reaped on Runtime shutdown.

**Out of scope:** HTTP/2 server-push body, multipart streaming,
request trailers, true bidirectional duplex (upload-while-
downloading concurrent reads). For "upload all then drain
response," compose `_upload_finish` + `_response_read`.

### stado_http_request_stream / stado_http_response_read / stado_http_response_close

EP-0038h — chunked HTTP response delivery for plugins fetching large
payloads (firmware, log archives) without OOMing the wasm instance.

| Import | Returns |
|---|---|
| `stado_http_request_stream(args_ptr, args_len, out_ptr, out_max) → i32` | bytes of result JSON written; -1 on error |
| `stado_http_response_read(handle, out_ptr, out_max, timeout_ms) → i32` | bytes written; 0 = EOF; -1 = error |
| `stado_http_response_close(handle) → i32` | 0 (idempotent); -1 on unknown handle |

**Capability:** reuses `net:http_request[:<host>]` from the
non-streaming variant. No new cap.

**Args JSON:** `{method, url, headers?, body_b64?, timeout_ms?}`
(narrow subset of `stado_http_request`'s args; proxy_url omitted in
v1).

**Result JSON:** `{status, headers, body_handle}` — `body_handle` is
a `httpresp:<id>` typed handle for the open response body. The
plugin drains it via `stado_http_response_read` until 0 (EOF) and
calls `_response_close` to release.

**Resource cap:** 8 concurrent open response streams per Runtime.
The 9th `_request_stream` returns -1. Open streams are reaped on
Runtime shutdown.

**Out of scope:** request body streaming (large uploads), HTTP/2
server-push, multipart streaming. `proxy_url` (SOCKS pivots) not
exposed in streaming v1 — use the non-streaming variant if you need
the proxy.

### stado_dns_resolve_axfr

EP-0038i — DNS zone transfer (AXFR, RFC 5936). Streams every record
in a zone over TCP. Most public servers refuse; useful for security
tooling against known-permissive or misconfigured infrastructure.

| Import | Capability |
|---|---|
| `stado_dns_resolve_axfr(req_ptr, req_len, result_ptr, result_cap) → i32` | `dns:axfr` (implies `dns:resolve`) |

Args JSON: `{"zone": "example.com", "server": "ns1.example.com[:53]", "timeout_ms"?: 30000}`.
Both `zone` and `server` are required — there's no recursion semantic
for AXFR, the plugin must name the authoritative server. Default
port `:53` is appended when the `server` value omits one.

Result JSON: `{"records": [{"name", "type", "class", "ttl", "rdata"}], "error"?: "..."}`.
`type` and `class` are the symbolic forms (`SOA`, `NS`, `A`, `AAAA`,
`MX`, `TXT`, `IN`, etc.). `rdata` is the type-specific text form
(e.g. `"192.0.2.1"` for an A record, `"ns1.example.com."` for NS).

A REFUSED rcode lands in `error` rather than crashing the plugin.

### stado_dns_resolve

| Field | Value |
|---|---|
| File | `host_dns.go` |
| Signature | `stado_dns_resolve(args_ptr, args_len, out_ptr, out_max) → i32` |
| Capability | `dns:resolve[:<glob>]` |
| Returns | bytes (JSON-encoded result) or -1 |

Args JSON: `{"name": "example.com", "qtype": "A"|"AAAA"|"TXT"|"MX"|"NS"|"PTR", "server"?: "8.8.8.8", "timeout_ms"?: 5000}`.
Result: `{"records": [{"name", "type", "value"}], "error"?: "..."}`.

### stado_net_*

Tier 1 raw socket primitives. EP-0038f shipped TCP dial; EP-0038g
adds UDP + Unix dial and TCP/Unix listen+accept. ICMP, AXFR, and
HTTP-streaming remain deferred. Tester #5: lets plugins talk to
non-HTTP services (SMTP, LDAP, NTP, banner grab, custom C2,
Docker daemon) without dropping to bash.

| Import | Returns | Capability |
|---|---|---|
| `stado_net_dial(transport_ptr, transport_len, host_ptr, host_len, port i32, timeout_ms i32) → i64` | typed handle `conn:<id>` (-1 on error) | `net:dial:<transport>:<host-glob>:<port-glob>` (or `:<path-glob>` for unix) |
| `stado_net_read(handle, out_ptr, out_max, timeout_ms) → i32` | bytes read; 0 = EOF | inherited from dial / accept |
| `stado_net_write(handle, data_ptr, data_len) → i32` | bytes written | inherited |
| `stado_net_close(handle) → i32` | 0 | inherited |
| `stado_net_listen(transport_ptr, transport_len, host_ptr, host_len, port i32) → i64` | typed handle `listen:<id>` (-1 on error) | `net:listen:<transport>:<host-glob>:<port-glob>` (or `:<path-glob>` for unix) |
| `stado_net_accept(lst_handle, timeout_ms i32) → i64` | typed handle `conn:<id>` (-1 on error, -2 on timeout) | inherited from listen |
| `stado_net_close_listener(lst_handle) → i32` | 0 | inherited |
| `stado_net_sendto(lst_udp, host_ptr, host_len, port, data_ptr, data_len) → i32` | bytes written; -1 on error | `net:listen:udp:<bind>` + `net:dial:udp:<peer-host>:<port>` |
| `stado_net_recvfrom(lst_udp, timeout_ms, body_ptr, body_max, addr_ptr, addr_max) → i64` | packed `(body_len << 32) \| addr_len`; -1 / -2 sentinels in body slot | inherited from UDP listen |

**Transports.** `stado_net_dial` accepts `"tcp"`, `"udp"`, `"unix"`.
For `"unix"`, the `host` parameter carries the socket path; `port` is
ignored. UDP dial is connect-mode (one peer per socket).
`stado_net_listen` accepts `"tcp"`, `"udp"`, `"unix"`. UDP listen
returns a stateless handle for `_sendto`/`_recvfrom` (any peer, gated
by `net:dial:udp:` globs).

**Stateless UDP — sendto / recvfrom.** A UDP listen handle can both
send packets to peers and receive from any sender:

```
lst = stado_net_listen("udp", "0.0.0.0", 0)        # bind ephemeral
stado_net_sendto(lst, "1.2.3.4", 53, query_bytes)  # peer cap-gated
n, addr_n = unpack(stado_net_recvfrom(lst, 1000, body, 1500, addr, 64))
# body[:n] = response payload, addr[:addr_n] = "host:port" of sender
```

The wasm caller un-packs the recvfrom return:
```
ret    : i64
body_n : int32 = int32(ret >> 32)        # signed cast preserves -1 / -2 sentinels
addr_n : int32 = int32(uint32(ret))      # unsigned low 32
```

Outbound peers in `stado_net_sendto` are gated by the **same
`net:dial:udp:<host>:<port>` glob set** as connect-mode UDP — a UDP
listener can't be a wildcard spray gun. Private peer addresses still
need `net:http_request_private`.

**Capability vocabulary.**

```
# outbound
net:dial:tcp:api.example.com:443
net:dial:tcp:*.example.com:*
net:dial:tcp:127.0.0.1:*           # loopback any port
net:dial:udp:*.ntp.org:123
net:dial:unix:/var/run/docker.sock
net:dial:unix:/tmp/*.sock          # path glob (filepath.Match)

# server-side
net:listen:tcp:127.0.0.1:8080      # loopback only
net:listen:tcp:0.0.0.0:9090        # any-interface — operator must opt in explicitly
net:listen:unix:/tmp/srv-*.sock
```

Listen capabilities match the host-port pair **verbatim** — there is
no implicit `127.0.0.1 ⊂ 0.0.0.0` widening. The operator spells out
which interface the plugin can bind.

**Private-address dial guard.** Dialing RFC1918 / loopback / link-local
addresses (TCP or UDP) requires `net:http_request_private`. Extends
to all `stado_net_dial` paths uniformly. Unix dial does not use this
guard — Unix sockets are inherently local; the path glob is the
control.

**Unix path constraints.** Both dial and listen refuse paths
containing `..` and paths longer than 104 bytes (BSD `sun_path` upper
bound; conservative across BSD/Linux).

**Resource caps.** 64 concurrent `conn` handles per plugin Runtime
(dial ∪ accept). 8 concurrent `listen` handles. The 65th dial /
accept and the 9th listen each return -1. On Runtime shutdown all
open conns and listeners are closed; Unix listeners also remove
their socket file.

**Accept timeout.** `stado_net_accept` requires a bounded timeout —
non-positive defaults to 5s, max 30s. Accept never blocks
indefinitely (DoS guard); plugins that need to wait longer re-loop.
Returns -2 on timeout (recoverable) vs -1 on error.

### stado_tool_invoke

Wasm plugins call other registered tools — the host-side composition
primitive. Resolves the tester's "exploit_tomcat_war_deploy can't use
exfil_listener_command to stand up a catch server before deploying
the webshell" friction. Avoids forcing inter-plugin coordination
through agent-loop turns.

| Field | Value |
|---|---|
| File | `host_tool_invoke.go` |
| Signature | `stado_tool_invoke(name_ptr, name_len, args_ptr, args_len, out_ptr, out_max) → i32` |
| Capability | `tool:invoke[:<name-glob>]` |
| Returns | bytes (result JSON) or -1 on cap denied / depth limit |

The plugin's manifest declares which tools it can invoke
(`tool:invoke:fs.read`, `tool:invoke:cve_lookup`, `tool:invoke:exploit_*`).
Empty glob = match-all. The TARGET tool's own capability requirements
are enforced against the SESSION's host (workdir, runner, etc.) — not
against the calling plugin. So `tool:invoke:fs.read` lets the plugin
call fs.read with the SESSION's `fs:read:.` cap, not the plugin's.

Recursion is bounded at depth 4. A plugin invoking another plugin
that invokes another plugin etc. counts depth at each step and
refuses with -1 beyond the limit. Threaded via context value.

Errors from the inner tool come back as a JSON envelope `{"error": "..."}`
so the plugin can distinguish failure from "tool returned an empty
result" (both write zero content bytes otherwise).

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
| `stado_secrets_delete(name_ptr, name_len) → i32` | 0 (idempotent) | `secrets:write[:<name-glob>]` |
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

### stado_progress

EP-0038h — operator-visible progress emission for long-running tools
(>2s). Tester #4: a multi-host probe should be able to print
`checking host 17/256` so the operator can tell it's making progress.

| Import | Returns |
|---|---|
| `stado_progress(text_ptr, text_len) → i32` | 0 on success / silent drop; -1 on overlong text |

**No capability required.** Bounded payload: 4 KB per call (longer
returns -1).

**Audience: operator only.** The agent / model sees only the final
tool result; this is a UX channel, not an information channel for
the model. Mid-tool partial output to the model would break tool-call
atomicity in current LLM contracts and is explicitly out of scope
for v1.

**Wiring.** The host caller (TUI, headless run, `stado plugin run`)
provides a callback `(plugin, text) → void`. When the callback isn't
set the import returns 0 and silently drops — the plugin shouldn't
fail because the operator surface isn't connected.

- `stado plugin run` prints `[plugin] text` to stderr.
- The TUI surfaces progress lines in the sidebar log tail tagged
  `PROGRESS [plugin] text`. Progress entries always show regardless
  of `--sidebar-debug` (the plugin author chose to emit them).

### stado_json_get, stado_json_format, stado_json_set

EP-0038h (`_get`, `_format`) + EP-0038i (`_set`) — host-side JSON
conveniences. Lets plugins extract a value, mutate a value, or
pretty-print a payload without bundling a 50 KB JSON parser into
every plugin binary.

| Import | Returns |
|---|---|
| `stado_json_get(json_ptr, json_len, path_ptr, path_len, out_ptr, out_max) → i32` | bytes written; -1 on malformed JSON / missing path / out_max too small |
| `stado_json_format(json_ptr, json_len, indent, out_ptr, out_max) → i32` | bytes written; -1 on malformed JSON / out_max too small |
| `stado_json_set(json_ptr, json_len, path_ptr, path_len, value_ptr, value_len, out_ptr, out_max) → i32` | bytes of modified document written; -1 on malformed JSON / malformed value / unwalkable path / out_max too small |

**No capability required** — pure compute. Input bounded to 256 KB
per call; larger payloads should be chunked via
`stado_http_response_read`.

**Path syntax (`_get`).** Dotted form, with non-negative integers
treated as array indices:

```
.            # whole document (also "")
user.name    # nested object key
items.0.id   # first array element's id field
```

No filters, globs, or recursive descent. Keys containing `.`
literally are unreachable; use `_format` and parse-by-walk if you
need that.

**Return form (`_get`).** Canonical JSON bytes of the value: numbers
are unquoted (`42`), strings keep their quotes (`"hello"`), objects
and arrays are valid JSON. The output is round-trippable into another
`_get` call.

**Indent (`_format`).** `0` = compact; `N>0` = N-space indent
(clamped to 16).

**Set semantics (`_set`).** The `value` payload must itself be valid
JSON — it gets parsed and embedded at the target location. New keys
on existing objects are added. Out-of-range or non-numeric array
indices return -1 (no implicit array growth). Walking through a
missing key creates intermediate empty objects so plugins can build
nested structure with successive sets:

```
{} + set("a.b.c", `"deep"`) → {"a":{"b":{"c":"deep"}}}
```

Empty path replaces the whole document (root-level set).

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

## Manifest extras

Beyond capabilities, the plugin manifest carries two extras worth
mentioning here for plugin authors:

### `requires`

Optional list of plugin dependencies. `stado plugin install`
verifies each entry is already installed at a satisfying version
before completing. Pre-1.0 supports only `>=` constraints.

```json
{
  "name": "exploit-lib",
  "requires": ["http-session >= 0.1.0", "secrets-store"]
}
```

### `tools[].categories`

The tool-level `categories` array enables operator-side
`[tools].autoload_categories = ["recon"]` config, which adds every
tool tagged with a matching category to the per-turn autoload set.
Lets HTB-tooling sessions run lean and pull, e.g., `recon` tools
always while `exploit` tools stay lazy-loaded behind tools.activate.

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
net:dial:tcp:<host>:<port>
dns:resolve                dns:resolve:<glob>
secrets:read[:<glob>]      secrets:write[:<glob>]
crypto:hash
tool:invoke[:<name-glob>]
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
