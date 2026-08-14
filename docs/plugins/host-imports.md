# Host imports — wasm plugin reference

This is the canonical reference for every host import a wasm plugin
can call. Generated from `internal/plugins/runtime/host_*.go` +
`tool_imports.go` (see [REGENERATION](#regeneration) below).

> **For context:** plugins compile to wasm and run inside `wazero`.
> Each call into `host_*` from inside a plugin is a host-import call.
> Capabilities declared in `plugin.manifest.json` gate which imports
> the plugin can reach; calling an ungated import fails using that import's
> documented negative-return convention and records a host-side audit entry.

## Table of contents

- [Surface map](#surface-map) — at-a-glance grouping
- [Tier 1 — capability primitives](#tier-1--capability-primitives)
  - [stado_log](#stado_log)
  - [stado_fs_*](#stado_fs_)
  - [stado_proc_* + stado_exec](#stado_proc_--stado_exec)
  - [stado_pty_*](#stado_pty_)
  - [stado_bundled_bin](#stado_bundled_bin)
  - [stado_session_*](#stado_session_)
  - [stado_llm_invoke](#stado_llm_invoke)
  - [stado_ui_approve](#stado_ui_approve)
  - [stado_ui_choose](#stado_ui_choose)
  - [stado_ui_print](#stado_ui_print)
  - [stado_ui_render](#stado_ui_render)
  - [stado_cfg_state_dir](#stado_cfg_state_dir)
- [Tier 2 — stateful conveniences](#tier-2--stateful-conveniences)
  - [stado_http_client_*](#stado_http_client_)
  - [stado_http_request](#stado_http_request)
  - [stado_dns_resolve](#stado_dns_resolve)
  - [stado_secrets_*](#stado_secrets_)
- [Tier 3 — stateless format conveniences](#tier-3--stateless-format-conveniences)
  - [stado_hash, stado_hmac](#stado_hash-stado_hmac)
  - [stado_compress, stado_decompress](#stado_compress-stado_decompress)
- [Agent surface](#agent-surface)
  - [stado_agent_*](#stado_agent_)
- [Artifact surface](#artifact-surface)
  - [stado_artifact_*](#stado_artifact_)
- [stado_lsp_*](#stado_lsp_)
- [SDK-side exports](#sdk-side-exports)
- [Patterns and anti-patterns](#patterns-and-anti-patterns)
- [Capability vocabulary](#capability-vocabulary)
- [Regeneration](#regeneration)

## Surface map

```
Tier 1 — capability primitives  (host_log, host_fs, host_proc,
                                  host_pty, host_bundled_bin,
                                  host_session, host_llm, host_ui,
                                  host_ui_render, host_cfg)

Tier 2 — stateful conveniences  (host_http_client, host_http_request,
                                  host_dns, host_lsp, host_secrets)

Tier 3 — stateless conveniences (host_crypto, host_compress)

Agent surface                   (host_agent)
Artifact surface                (host_artifact)
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
| `stado_fs_readdir(path_ptr, path_len, offset, out_ptr, out_max) → i32` | `fs:read:<glob>` | JSON array of `{name, type, mode}` entries from `offset`; ≤50000 per call (paginate via offset); 0 = no more |
| `stado_fs_stat(path_ptr, path_len, out_ptr, out_max) → i32` | `fs:read:<glob>` | JSON `{mode, size, mtime, type}` (type = file/dir/symlink/other); -1 on cap deny / not found |
| `stado_fs_last_error(out_ptr, out_max) → i32` | none (ungated) | The host's view of why the last fs primitive returned -1 (scope guard / cap deny / IO failure) — for surfacing a precise cause |

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

The narrow capability surface here is the replacement for the
broad `exec:bash` / `stado_exec_bash` tool import (both removed by
EP-no-internal-tools) — operators see exactly which binaries a
plugin runs.

### stado_pty_*

PTY-backed shell sessions.

| Import | Returns | Description |
|---|---|---|
| `stado_pty_create` | typed handle `term:<id>` | Open PTY session (args may carry `description`) |
| `stado_pty_list` | bytes (JSON list) | List active sessions (`id, cmd, description, alive, …`) |
| `stado_pty_read` | bytes | Raw incremental output read (no attach — EP-0043) |
| `stado_pty_write` | bytes | Stdin write (no attach) |
| `stado_pty_signal` | 0/-1 | Posix signal (out-of-band) |
| `stado_pty_resize` | 0/-1 | Cols + rows |
| `stado_pty_destroy` | 0 | Kill + free |
| `stado_pty_snapshot(args_ptr, args_len, res_ptr, res_cap) → i32` | bytes (JSON) | Rendered screen. With `mode:"auto"` returns `{"kind":"stream"}` (no render) when not on the alternate screen buffer, else `{"kind":"screen", text, cols, rows, cursor, title, svg?}`. Backs `shell.read mode:screen/auto`. |
| `stado_pty_expect(id_lo, id_hi, args_ptr, args_len, res_ptr, res_cap) → i32` | bytes (JSON) | Read until pattern match / timeout / EOF — see below |

Capability: `exec:pty` (broad) or `exec:pty:<binary-glob>` (scoped) — gates all PTY ops and constrains session creation.

**`stado_pty_expect` request shape:**

```jsonc
{
  "patterns":   ["password:", "$ "],   // 1..16 strings
  "regex":      false,                  // RE2 when true; substring otherwise
  "timeout_ms": 30000                   // total budget; 0 = check buffer only
}
```

**Response shapes** (discriminated on `matched` / `timeout` / `eof`):

```jsonc
// Match
{"matched": true, "pattern_index": 0, "before": "<base64>", "match": "<base64>"}
// Timeout
{"matched": false, "timeout": true, "before": "<base64>"}
// EOF (process exited)
{"matched": false, "eof": true, "before": "<base64>", "exit_code": 0}
```

`before` and `match` are base64 because PTY output routinely includes
non-UTF8 sequences (ANSI escapes, terminal control codes) JSON strings
can't carry losslessly. After-match bytes are pushed back into the
ring; subsequent `stado_pty_read` returns them.

Across patterns, the EARLIEST byte position wins; ties go to the
lower `patterns[i]` index. Concurrent `stado_pty_expect` on the
same session is rejected with a structured error. No attach required
(EP-0043: the session id is the handle; `stado_*_attach`/`_detach` were
removed).

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
| Signature | `stado_llm_invoke(args_ptr, args_len, out_ptr, out_cap) → i32` |
| Capability | `llm:invoke[:<budget-tokens>]` |
| Returns | bytes (model reply) or -1 if budget exceeded / no session bridge |

One-shot completion against the active provider. The first
ptr/len pair carries a JSON envelope so the call can be extended
without reshuffling the wasm signature; the second pair is the
caller-owned output buffer the host writes the reply into.

Args envelope (all fields except `prompt` are optional):

```json
{
  "prompt": "string (required)",
  "persona": "string — overrides the active session persona for this one call",
  "model": "string — overrides the active session model (e.g. claude-haiku-4-5)",
  "system": "string — appended after the persona's system prompt",
  "max_tokens": 0,
  "temperature": 0.0
}
```

Budget cap is per-session-cumulative across every `stado_llm_invoke`
the plugin instance makes; default 10000 tokens when no suffix is
declared on the cap (`llm:invoke` ≡ `llm:invoke:10000`). Once
exhausted, further calls return -1 immediately without consulting
the provider. Returns -1 with no token consumption when the host
has no session bridge attached (e.g., tool run outside a session).

### stado_ui_approve

| Field | Value |
|---|---|
| File | `host_ui.go` |
| Signature | `stado_ui_approve(title_ptr, title_len, body_ptr, body_len) → i32` |
| Capability | `ui:approval` |
| Returns | 1 (approved) / 0 (denied) / -1 (no approval bridge) |

Surfaces a yes/no workflow prompt via the TUI bridge. Returns -1 when running
outside a TUI (headless, plugin run); plugin should fail safely. The response is
useful for quality and workflow choices, but is not broker-authenticated proof
of operator intent and cannot authorize an artifact or capability transition.

### stado_ui_choose

| Field | Value |
|---|---|
| File | `host_ui.go` |
| Signature | `stado_ui_choose(req_ptr, req_len, resp_ptr, resp_cap) → i32` |
| Capability | `ui:choice` |
| Returns | bytes (JSON response) on success; `-n` = n bytes of error message at resp_ptr |

Presents an interactive choice prompt to the operator. Request JSON:
`{prompt, options: [{id, label, prefix?, input?}], multi?, default?}`.
Response JSON: `{selected: [...], input_value?, cancelled}`. Routes
to the host's choice bridge; when none is attached (headless, MCP
server) the call returns a structured "interactive UI unavailable"
error so the plugin can fall back to its `default` or fail.

### stado_ui_print

| Field | Value |
|---|---|
| File | `host_ui_print.go` |
| Signature | `stado_ui_print(req_ptr, req_len, err_ptr, err_cap) → i32` |
| Capability | `ui:print` |
| Returns | 0 on success / silent drop; `-n` where n bytes of error message are at err_ptr |

Fire-and-forget plain-text emit to the operator surface. Request
JSON carries the text plus optional streaming options. Routes to
the host's print bridge; a nil bridge drops on the floor with success
(same F9 contract as `stado_ui_render` — a print on a disconnected
channel is not an error). Text cap: 8 KiB.

### stado_ui_render

| Field | Value |
|---|---|
| File | `host_ui_render.go` |
| Signature | `stado_ui_render(req_ptr, req_len, err_ptr, err_cap) → i32` |
| Capability | `ui:render` |
| Returns | 0 on success; `-n` where n bytes of error message are at err_ptr |

Fire-and-forget structured-panel emit. The plugin marshals a Panel
JSON envelope and the runtime translates per-channel: TUI renders a
bordered system block; ACP emits `session/update kind=panel`; MCP
returns the panel struct in `CallToolResult.StructuredContent`
alongside an ASCII rendering in the text content; headless emits
`session.update kind=panel` on the JSON-RPC notification stream.

Wire envelope (size cap: 64 KiB total / 32 KiB per section):

```json
{
  "title":   "string (≤80 chars, required)",
  "sections": [
    {
      "kind":   "text | kv | list | code | table | diff",
      "heading": "string (≤80 chars, optional)",
      // exactly one body field per kind:
      "text":  "string",
      "kv":    [{"label": "...", "value": "..."}],
      "list":  {"marker?": "bullet|numbered|check", "items": ["..."]},
      "code":  {"language?": "...", "content": "..."},
      "table": {"columns": ["..."], "rows": [["..."]]},
      "diff":  {"before": "...", "after": "..."}
    }
  ],
  "variant": "info | ok | warn | error | recommendation",
  "id":      "string (≤64 bytes, optional, referenceable from a later choice)",
  "footer":  "string (≤200 chars, optional)"
}
```

Validation failures (cap denied / size exceeded / wrong body for
kind / unknown enum value / table row × col over caps) return `-n`
with a structured human-readable message at err_ptr. Same negative-
return convention every other host import in this codebase uses.

Drop-on-floor when no render bridge is attached (e.g., tool run
outside a session, MCP server with no per-call bridge): success
returns silently. Per F9 spec: "if channel disconnected, emit
succeeds silently."

Reference example: `render-demo-go` in [foobarto/stado-plugins](https://github.com/foobarto/stado-plugins).

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

### stado_http_request

| Field | Value |
|---|---|
| File | `host_http_request.go` |
| Signature | `stado_http_request(args_ptr, args_len, out_ptr, out_max) → i32` |
| Capability | `net:http_request` or `net:http_request:<host>` |
| Returns | bytes (JSON `{status, headers, body_b64, body_truncated}`) or -1 |

A true host primitive (no longer a `tool.Tool` delegate — the
`stado_http_get` bridging import and its backing webfetch tool were
removed by EP-no-internal-tools; use `stado_http_request` for all
plugin HTTP).

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

### stado_net_icmp_echo

EP-0038i — ICMP echo (ping) for plugins doing reachability checks
beyond what TCP probes can answer. Resolves the host, sends N
echoes, returns per-packet RTTs.

| Import | Capability |
|---|---|
| `stado_net_icmp_echo(args_ptr, args_len, out_ptr, out_max) → i32` | `net:icmp` |

Args JSON: `{"host": "...", "timeout_ms"?: 1000, "count"?: 1, "payload_size"?: 32}`.
Defaults: 1 echo, 1 s timeout, 32-byte payload. Caps: `count` ≤ 64,
`payload_size` ≤ 1500.

Result JSON: `{"rtts_ms": [...], "sent": N, "received": M, "error"?: "..."}`.
Each successfully-replied echo contributes one float to `rtts_ms`.
Lost echoes count toward `sent` but not `received`.

**Privilege.** Tries an unprivileged ICMP socket first (Linux
`net.ipv4.ping_group_range` covering the running uid). Falls back to raw on
`EPERM`. Raw needs `CAP_NET_RAW` or root. The error message names the fix:

```
icmp listen: ... (try `sysctl -w net.ipv4.ping_group_range='0 65535'` or run with CAP_NET_RAW)
```

**Private-IP guard.** Inherits `NetHTTPRequestPrivate`. Without that
cap, RFC1918 / loopback / link-local destinations are refused at
the resolve step.

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

**Resource caps (v0.75.2).** At most **50 000** records per transfer;
default wall-clock timeout **120 s** (request `timeout_ms` capped at
120 000). Hitting the record cap stops the transfer cleanly and returns
partial results with an `error` field rather than hanging the plugin.

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
| `stado_net_setopt(lst_udp, key_ptr, key_len, value_ptr, value_len) → i32` | 0 on success; -1 on cap-denied / unknown key / unknown handle / syscall failure | `net:multicast:udp` |

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

**Broadcast and multicast — `stado_net_setopt`.** A UDP listener
can be reconfigured for broadcast / multicast traffic via key-based
setopts:

| Key | Value form | Effect |
|---|---|---|
| `broadcast` | `"true"` / `"false"` | Toggle `SO_BROADCAST` — required for sendto to broadcast addrs (`255.255.255.255`, subnet broadcasts) |
| `multicast_join` | `"<group_ip>[,<iface_name>]"` | Join the multicast group on the named interface (default: any) |
| `multicast_leave` | `"<group_ip>[,<iface_name>]"` | Inverse of join |
| `multicast_loopback` | `"true"` / `"false"` | Whether multicast we send is looped back to us |
| `multicast_ttl` | `"<int 0..255>"` | TTL / hop limit on outgoing multicast |

All five keys require `net:multicast:udp` in the manifest. Useful
for discovery protocols (mDNS / SSDP / WS-Discovery / BACnet / NBNS).
Group IPs are validated as multicast (224.0.0.0/4 for IPv4, ff00::/8
for IPv6) — non-multicast inputs are rejected.

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
containing `..` and paths longer than 104 bytes (a conservative Linux
`sun_path` bound).

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

When the active session has a pinned `tools.Executor` (TUI, `stado run`,
headless `plugin.run`, `stado tool run --session`), nested invokes route
through `Executor.Run` — same audit trailers, lifecycle hooks, and
sandbox runner as top-level tool calls (v0.75.2). One-shot paths without
a session executor still invoke directly without that audit wiring.

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
| `stado_hash(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_max) → i32` | `crypto:hash` | md5, sha1, sha256, sha512 |
| `stado_hmac(algo_ptr, algo_len, key_ptr, key_len, data_ptr, data_len, out_ptr, out_max) → i32` | `crypto:hash` | same algorithms |

Output is a lowercase hex-encoded digest; do not re-encode it.

### stado_compress, stado_decompress

| Import | Capability | Algorithms |
|---|---|---|
| `stado_compress(algo_ptr, algo_len, data_ptr, data_len, out_ptr, out_max) → i32` | none — always available | gzip, zlib |
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

**Wiring — operator surface.** The host caller (TUI, headless run,
`stado tool run`) provides a callback `(plugin, text) → void`.
When the callback isn't set the import returns 0 and silently drops
— the plugin shouldn't fail because the operator surface isn't
connected.

- `stado tool run` prints `[plugin] text` to stderr.
- The TUI surfaces progress lines in the sidebar log tail tagged
  `PROGRESS [plugin] text`. Progress entries always show regardless
  of `--sidebar-debug` (the plugin author chose to emit them).

**Wiring — model surface (agent-loop integration).** When the tool
runs inside `Executor.Run` (agent loop, `stado run`, TUI tool
dispatch) progress emissions ALSO get collected per-call and
prepended to the tool's result envelope so the model sees the trail:

```
[progress] scanner: checking 1/256
[progress] scanner: checking 17/256: 22/tcp open
...
[progress] scanner: scan complete

discovered 3 hosts: 10.0.0.3, 10.0.0.7, 10.0.0.42
```

Bounded — at most 64 entries per call (FIFO drop on overflow). The
prepend is suppressed when the tool errored, so error messages
read cleanly. This is atomic from the model's POV (model still
sees the tool result exactly once, after completion) — true
mid-tool model streaming would need an LLM-API streaming-tool-call
contract that doesn't exist today.

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
`stado_http_response_read`. `_format` rejects nesting deeper than **64**
levels and caps output at **4 MiB** so compact JSON cannot amplify into
unbounded host allocations (v0.75.2).

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

## Lifecycle application state and scheduling

EP-0064 lifecycle applications use broker-owned durable primitives through the
uniform bounded signature `(req_ptr, req_len, resp_ptr, resp_cap) → i32`.
Authority comes from the native-held application binding; request JSON cannot
select a session, generation, plugin identity, or capability.

| Import | Capability | Operation |
|---|---|---|
| `stado_session_journal_append` | `session:journal:append` | Append namespaced application journal data |
| `stado_session_projection_read` | `session:projection:read` | Read the caller's bounded journal/hold/control/completion/worker-run/timer projection |
| `stado_session_hold_acquire`, `stado_session_hold_release` | `session:schedule` | Acquire or CAS-release a leased scheduling hold |
| `stado_session_request_pause`, `stado_session_request_stop` | `session:schedule` | Submit typed pause/stop proposals to host scheduling |
| `stado_session_complete` | `session:complete` | Durably transition one application run to successful completion |
| `stado_session_input_route` | `session:input:route` | Route one immutable broker-captured input to worker delivery or the broker-owned deferred-task projection |
| `stado_session_input_claim` | `session:input:route` | Durably claim one exact queued input for asynchronous review without settling it |
| `stado_session_worker_request` | `session:worker:request` | Request one bounded application-owned worker recurrence; the guest cannot activate it |
| `stado_session_worker_resume` | `session:worker:resume` | CAS-request continuation of the caller's exact interrupted worker run; activation remains native-only |
| `stado_session_worker_cancel` | `session:worker:cancel` | CAS-cancel the caller's requested, resume-requested, or active worker run |
| `stado_timer_schedule`, `stado_timer_cancel` | `timer:schedule` | Schedule or CAS-cancel a durable timer |

`stado_session_complete` is not a pause/stop alias and does not require a hold
to be left active. At a shared loop barrier, unconsumed scheduling facts use
the fixed precedence stop → successful completion → pause → active hold. A
successful completion ends the loop normally; it is not surfaced as an error.

`stado_session_worker_request` accepts
`{run_id, objective, prompt, conflict, idempotency_key?}`. `objective` and
`prompt` are required and bounded. `conflict` is either `reject` or
`replace_operator_loop`; an application can never replace another
application-owned run. The request is only a durable proposal. After a
successful signed command callback returns its `worker_run_id`, the native
command host uses a dedicated session-controller-authenticated broker RPC plus
the callback application's exact opaque binding to fetch and activate that
broker projection. The ordinary application bearer cannot select the native
lookup or activation operations. Replayed callbacks and activations are
idempotent and cannot start a second turn.

`stado_session_worker_resume` accepts
`{run_id, expected_version, idempotency_key?}`. Only the same interrupted run
in the same admitted session, generation, and canonical plugin namespace may
enter `resume_requested`; stopped, cancelled, and completed runs never do. A
successful signed command returns the distinct `resume_worker_run_id` field,
after which the native command host performs a separate controller-authenticated
lookup and resume activation CAS. Run identity, objective, prompt, journal,
captured input, and deferred-task ownership remain unchanged. A stop after the
interruption is terminal. A pause racing after the durable resume request makes
activation fail closed; a fresh exact resume request can acknowledge that
newer pause. Any unexpired hold in the exact session generation, including a
hold owned by another application, makes resume activation return a retryable
conflict while the run remains `resume_requested`. After the hold is settled,
the exact lookup/activation retry may proceed. Expired holds do not block.

`operator.input.queued` is a targeted mandatory-action event for the exact
application worker run that owned recurrence when the native controller
captured the operator's text. Capture accepts valid UTF-8 up to 48 KiB and
fails visibly on oversize input or bounded-queue backpressure; while the run
owns recurrence the TUI never falls through to an ordinary unsupervised steer
or prompt. The event data contains `{schema, input_id, run_id, version,
ordinal, text, digest}`. Outer session, generation, plugin targeting, WAL order,
and timestamps are broker-authored quality/audit provenance, not security
proof.

`stado_session_input_claim` accepts
`{input_id, run_id, expected_version, review_id, idempotency_key?}` and moves
only that exact queued record to `reviewing`. `review_id` is bounded,
application-authored correlation metadata; it identifies neither an agent nor
an authority. The durable claim permits acknowledgement of the original
mandatory event so that a later child-lifecycle event can reach the same
application cursor. It does not settle the input: reviewing remains part of
the pending-input bound, fences provider/tool scheduling for the whole session
generation, blocks completion, and is recovered in original order if its run
terminates. Rebind projects the same record and review ID for an idempotent
job/result replay. There is deliberately no native review timeout or
classification policy.

`stado_session_input_route` accepts `{input_id, run_id, expected_version,
disposition, review_id?, label?, rationale?, idempotency_key?}`. A queued
record rejects `review_id`; a reviewing record requires the exact stored
value. `disposition` is exactly `deliver` or `defer`; the application cannot
drop, replace, or retarget the original. Deferral is a projection of that same
broker record, exposed through `projection.read` as bounded `deferred_tasks`
pages. It is not a write to the legacy task store and uses no prose marker as a
second authority. Its status is derived as `open`, `pending_continuation`, or
`continued` from the input, completion, and receiver-delivery records. The
native controller receives a read-only bounded summary of open deferred tasks
as well, so a cancelled run or temporarily unavailable application cannot make
them invisible to the operator.

When completing a run, `stado_session_complete` may supply the exact ordered
`continuation_input_ids` set. The broker rejects duplicates, omissions,
foreign IDs, and completion while any input is still queued, reviewing, or
ready. Native delivery appends the immutable original messages to the exact
session using a broker-minted delivery ID and digest-checked structured receiver
record, then commits delivery. Replay with the same ID and bytes is idempotent;
a mismatch fails closed. Terminal runs recover queued/reviewing/ready originals
in capture order, while deferred records remain for successful ordered continuation.
Manual forks do not inherit them, and automatic context recovery currently
fails closed until the broker owns an atomic ancestry-authenticated transfer.
At TUI startup and after an exact logical-session switch, ordinary input stays
in the editor until the native active-worker projection resolves found or not
found; lookup, activation, and cancellation failures retain the same
fail-closed draft fence. These recovery facts do not grant security authority.

`stado_session_worker_cancel` accepts
`{run_id, expected_version, reason, idempotency_key?}`. Pause and stop use a
native controller-only consumption path instead: before returning the
scheduling result, the broker terminalizes the active run as `interrupted` or
`stopped` with the control WAL sequence. This prevents a rebind from silently
resuming recurrence. Operator `/loop stop` uses a separate
controller-authenticated broker cancellation, with the application binding
serving only as the exact namespace selector, so a plugin cannot make its
recurrence unstoppable by omitting its optional self-cancel capability.
Neither activation nor the aggregate active-run projection is exposed as a
WASM import.

## Agent surface

EP-0038c/EP-0064 — WASM plugins talk to the runtime fleet through
operation-scoped imports. New manifests declare only the operations they need:
`agent:spawn`, `agent:list`, `agent:read`, `agent:send`, and `agent:cancel`.
The removed aggregate `agent:fleet` capability grants no operation.
`agent:spawn:configure` is an additional, non-standalone attenuation: a plugin
must hold it as well as `agent:spawn` before it can select provider, model,
thinking mode/budget, or reasoning effort. It does not expose provider
credentials, endpoints, or an operation by itself.

### stado_agent_*

| Import | Capability | Description |
|---|---|---|
| `stado_agent_spawn(req_ptr, req_len, out_ptr, out_max) → i32` | `agent:spawn`; also `agent:spawn:configure` when any profile override is present | Spawn a child. Args include `{prompt, provider?, model?, thinking?, thinking_budget_tokens?, reasoning_effort?, async?, ephemeral?, role?, mode?, max_turns?, timeout_seconds?, tool_profile?, narrow_tools?[], token_budget?, execution?, source?, idempotency_key?}`. Plain spawn inherits the parent profile. `provider` is at most 128 bytes and requires `model` (at most 512 bytes); a model-only override stays on the parent provider. `thinking` is `auto\|on\|off`; its 0..2,000,000 token budget cannot exceed the child token budget; effort is `low\|medium\|high\|xhigh\|max`. The host resolves the exact requested provider/model, rejects silent model substitution and unsupported forced controls, and keeps credentials native. `source.at` may be the exact broker-stamped `turn_ref` from `session.turn_committed`; `source.session_id` can be omitted for that form because the host derives and ancestry-checks it from the ref. The source is resolved and pinned before asynchronous admission returns. Host policy fixes parent session, sandbox ceiling, and allowed role/mode combinations. Returns `{id, session_id, status, final_text?, terminal?}`. |
| `stado_agent_list(out_ptr, out_max) → i32` | `agent:list` | List agents in caller's spawn tree. |
| `stado_agent_read_messages(args_ptr, args_len, out_ptr, out_max) → i32` | `agent:read` | Args: `{id, since?, timeout_ms?}`. Returns `{messages[], offset, status, terminal?}`. Terminal metadata is `{usage:{input_tokens,output_tokens,cache_read_tokens?,cache_write_tokens?},usage_complete,cleanup?}`; cleanup is `{kind,fingerprint}` and never raw provider text. |
| `stado_agent_send_message(args_ptr, args_len) → i32` | `agent:send` | Args: `{id, message}`. Posted at the child's next yield point. |
| `stado_agent_cancel(args_ptr, args_len) → i32` | `agent:cancel` | Args: `{id}`. Child exits at the next cancellable boundary. |

For `execution: "retained"`, the host resolves the source synchronously into
an immutable session/generation/turn, conversation digest, tree commit, and
trace commit. Admission and every delayed launch or restart consume that same
fork point. A mutable selector such as `last_committed_turn` is never resolved
again after the spawn call returns.

The exact application selector is
`git:refs/sessions/<id>/tree@<commit>#turn-<N>-iteration-<M>`. It starts a
fresh child conversation over that immutable tree; applications provide their
own bounded review prompt rather than inheriting mutable worker chat. Terminal
token counters are collected by the host. If the provider omits or reports
invalid usage, `usage_complete` is false. A later provider-close failure is a
separate fingerprinted `cleanup` fact and cannot replace `final_text` or remove
the final assistant message.

Lifecycle applications should journal a bounded `idempotency_key` before an
asynchronous spawn and reuse it until the exact child acknowledgement is
durable. The host keys it by the authenticated canonical plugin, broker
session, and generation and stores the normalized request digest. Concurrent
or post-module-rebind replay in the same live Stado process returns the exact
same child; reuse with different input fails closed. The map is intentionally
process-local because Fleet children are in-process: after a process restart
the old child is terminal, so replay may admit a replacement rather than
falsely returning a dead child as live.

## Artifact surface

### stado_artifact_*

EP-0063 replaces the memory-specific ABI with a generic, broker-owned artifact
surface. All four imports use the same bounded convention:

```text
stado_artifact_*(req_ptr, req_len, resp_ptr, resp_cap) -> i32
```

| Import | Capability | Description |
|---|---|---|
| `stado_artifact_propose(...) → i32` | `artifact:propose:<local-kind>` | Propose a candidate artifact of a kind declared in this plugin's signed manifest |
| `stado_artifact_query(...) → i32` | `artifact:read:<qualified-kind-pattern>` | Query 1..32 explicit qualified kinds within broker scope and sensitivity policy; optional `refs:[{id,version}]` resolves exact immutable versions without recency pagination |
| `stado_artifact_edit(...) → i32` | `artifact:edit:<local-kind>` | Propose a candidate version of an existing artifact; never mutate an active version in place |
| `stado_artifact_observe(...) → i32` | `artifact:observe:<qualified-kind-pattern>` | Record a bounded observation for explicitly named qualified kinds |

A positive return is the JSON response byte count. A negative return is the
negated byte count of a bounded error written to the response buffer. Missing
broker wiring or canonical runtime identity fails closed.

`propose` and `edit` requests name a manifest-declared local `kind`; their
capabilities are exact local-name grants. `query` and `observe` requests carry
`kind` or `kinds` with fully qualified values such as
`github.com/acme/reviewer#review-contract`. Read/observe patterns are deliberately
limited to an exact qualified kind, `*`, or one trailing-`*` prefix; they are not
filesystem globs.

An exact configuration or evidence lookup supplies both explicit `kinds` and
`refs:[{"id":"art_...","version":3}]`. The broker applies the same scope,
kind, sensitivity, and expiry checks but does not let newer unrelated artifacts
push the selected version out of a bounded recency page. Missing, stale, or
invisible refs are omitted, so the application must require the exact expected
result and fail closed.

The host injects canonical plugin identity, principal, repository, session
generation, and ancestry immediately before broker dispatch. Those fields are
not guest authority input. Activation, rejection, retirement, deletion, and
operator grants remain outside this model-facing ABI. See
[EP-0063](../eps/0063-plugin-defined-harness-artifacts.md) for the envelope,
identity, schema archive, and single-writer contract.

## stado_lsp_*

LSP query primitives (`host_lsp.go`). These used to be tool-bridging
delegates; EP-no-internal-tools made them true primitives backed by
`internal/lspfind/`. The wider tool-bridging surface
(`stado_fs_tool_*`, `stado_exec_bash`, `stado_http_get`,
`stado_search_ripgrep`, `stado_search_ast_grep`) was **removed** —
use `stado_fs_*`, `stado_proc_*` + `exec:proc:<binary>`,
`stado_bundled_bin` (rg, ast-grep), and `stado_http_request` instead.

| Import | Capability |
|---|---|
| `stado_lsp_find_definition` | `lsp:query` + `fs:read` on the resolved path |
| `stado_lsp_find_references` | `lsp:query` + `fs:read` |
| `stado_lsp_document_symbols` | `lsp:query` + `fs:read` |
| `stado_lsp_hover` | `lsp:query` + `fs:read` |

These imports return **byte counts** (or -1) and write JSON
results into `(result_ptr, result_max)`. Each takes a JSON args
envelope `{path, line, column, ...}`; `fs:read` is enforced against
the plugin's scope on the resolved path, not just workdir
containment.

## SDK-side exports

The host calls into the plugin to:

- Allocate wasm memory: `stado_alloc(size i32) → i32` (returns ptr; null = OOM)
- Free wasm memory: `stado_free(ptr i32, size i32) → none`
- Run a tool: `stado_tool_<name>(args_ptr i32, args_len i32, result_ptr i32, result_max i32) → i32`

The in-tree Go SDK (`internal/plugins/bundled/sdk`) provides these
as `//go:wasmexport` declarations. Other languages implement the
same handful of helpers manually; example plugins for them live in
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins).

## Manifest extras

Beyond capabilities, the plugin manifest carries several application
declarations worth mentioning here for plugin authors:

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

### `artifact_kinds`

Declares plugin-owned artifact `data` shapes. Each entry has a bounded local
`name`, exact JSON Schema bytes in `schema`, and optional deterministic JSON
Pointer projections in `index`. The schema root must be an object; unknown or
external schema vocabulary is rejected at load rather than ignored. The signed
manifest digest covers these declarations. See EP-0063.

### `lifecycle`

Declares EP-0064 lifecycle application subscriptions: synchronous `points`,
durable broker `events`, `failure` (`open` or `closed`), and a bounded
`timeout_ms`. The manifest shape is validated and covered by the signed digest,
but validation alone does not make callback dispatch available. The persistent
WASM application dispatcher and its operation-scoped lifecycle capabilities are
the accepted, in-flight EP-0064 contract. A declaration never widens operator
policy and is not a compatibility alias for native application policy.

### `commands`

Declares operator-routed commands handled by the fixed
`stado_plugin_command` export. Each entry contains `name`, `description`, an
optional one-line `usage`, and optional `timeout_ms`. Zero inherits
`lifecycle.timeout_ms`; the signed command-specific value is capped at 15
minutes for interactive workflows that make several generic UI bridge calls.
It affects only wall-clock cancellation and grants no capability or authority.

## Patterns and anti-patterns

### Use `stado_proc_*` + `exec:proc:<binary>` for subprocesses

The broad bash escape hatch (`stado_exec_bash` / `exec:bash`) was
removed by EP-no-internal-tools. When you know the binary your
plugin needs, declare `exec:proc:/usr/bin/ldap-search` and call it
via `stado_proc_spawn`. The operator sees exactly which binaries you
run; the audit story stays clean.

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

Ordinary tool calls may receive a fresh wasm instance. The EP-0064 TUI
dispatcher gives lifecycle applications a serialized per-session instance,
but instance memory remains an optimization rather than authoritative durable
state. For short-lived state, see:

- **`stado_http_client_*`** — cookie jar persists for the
  client-handle's lifetime
- **`stado_secrets_*`** — operator-managed secrets persist on disk
- **`stado_instance_get/set`** — process-lifetime KV with per-plugin
  namespacing; it is cleared when the runtime closes.

Use broker-owned EP-0063 artifacts for versioned application records and the
EP-0057/0059 journal/event imports above for recoverable lifecycle progress.
`stado_cfg_state_dir` plus `stado_fs_write` may hold a plugin-private cache, but
it is not an authority, shared-WAL, or lifecycle-recovery bridge.

### Returning more bytes than `out_max` allows

The pattern: host imports return the *required* size when the
caller's buffer is too small, and the caller re-allocs and re-calls.
Plugin SDK helpers wrap this — see the Go SDK at
`internal/plugins/bundled/sdk`.

For imports without re-call semantics (some return -1 on overflow),
size your buffer based on the import's documented worst-case (e.g.
`stado_dns_resolve` worst case is ~64KB for a TXT record blob).

## Capability vocabulary

Every cap-gated import lists its capability above. The full
vocabulary (manifest declarations):

```
fs:read:<glob>             fs:write:<glob>
exec:proc:<binary-glob>    exec:pty
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
session:journal:append      session:projection:read
session:schedule            session:complete
session:input:route
session:worker:request      session:worker:resume
session:worker:cancel
timer:schedule
agent:spawn                 agent:spawn:configure
agent:list
agent:read                  agent:send
agent:cancel
artifact:propose:<local-kind>
artifact:read:<qualified-kind-pattern>
artifact:edit:<local-kind>
artifact:observe:<qualified-kind-pattern>
ui:approval                ui:choice
ui:print                   ui:render
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
