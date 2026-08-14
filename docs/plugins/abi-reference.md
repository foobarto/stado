# stado WASM ABI reference

This is the systematic reference for the host ↔ wasm interface that
plugins implement. It complements [`host-imports.md`](host-imports.md)
(the per-import function reference) by documenting the **conventions**
that apply across every import and export — memory model, return-code
sentinels, typed handles, JSON envelope, buffer sizing, lifecycle.

If you're writing a plugin from scratch, read this end-to-end once,
then keep `host-imports.md` open as you work.

For the lifecycle from "I want a custom tool" to "the LLM can call it,"
see [`docs/features/plugin-authoring.md`](../features/plugin-authoring.md).

---

## 1. Module shape

A stado plugin is a WebAssembly module compiled for `wasip1` (Go,
Rust, Zig, AssemblyScript — whatever produces a WASI-compatible
`.wasm`). The host loads the module via [wazero](https://wazero.io/)
inside a sandboxed runtime. Ordinary tools use a fresh module per invocation.
A manifest-declared lifecycle application instead receives one persistent,
serialized module for an exact canonical plugin/session/generation tuple; see
[§10.2](#102-lifecycle-application-contract).

### 1.1 Required exports

Every loaded module **must** export:

| Export | Signature | Purpose |
|---|---|---|
| `stado_alloc` | `(size i32) → i32` | Allocate a buffer in linear memory; return its base pointer (or 0 on failure). The host calls this to hand args + result buffers to the plugin. |
| `stado_free` | `(ptr i32, size i32) → ()` | Free a previously-allocated buffer. The host calls this after a tool invocation. |
| `stado_tool_<name>` | `(args_ptr i32, args_len i32, result_ptr i32, result_max i32) → i32` | Required once for each tool the manifest declares. A lifecycle-only application may declare no tools. Returns bytes written to result (positive), or `-N` to signal an error envelope was written instead (see §5). A lifecycle application uses the same persistent instance for these exports. |

Wasm names use snake_case; the host strips `stado_tool_` to get the
tool's bare name, then maps it to wire form (`<plugin>__<tool>`) for
the registry. See §13 for full naming rules.

### 1.2 Optional exports

| Export | Purpose |
|---|---|
| `stado_plugin_init` | One-shot init called once after instantiation, before any tool call. Use for bootstrapping caches, opening long-lived handles. |
| `stado_plugin_close` | Called before the runtime is torn down. Last chance to flush state. |
| `stado_plugin_tick` | Periodic tick for a legacy background plugin or persistent lifecycle application. |
| `stado_plugin_lifecycle` | Required when `lifecycle.points` is non-empty. Receives one bounded `stado.dev/lifecycle/v1` envelope and returns a strict lifecycle decision. |
| `stado_plugin_event` | Required when `lifecycle.events` is non-empty. Receives one durable broker event and returns exactly `{"status":"ack"}` or `{"status":"unregister"}`. |
| `stado_plugin_command` | Required when `commands` is non-empty. Receives one host-selected `stado.dev/application-command/v1` envelope and returns a strict bounded command result. Each declaration may sign an independent `timeout_ms` up to 15 minutes for interactive UI workflows; zero inherits the lifecycle timeout. |

### 1.3 Required imports

**None are mandatory.** A pure-compute plugin (e.g. JSON canonicaliser)
can import zero host functions. Imports are opt-in and capability-gated
— the manifest declares which capabilities the plugin needs, and the
runtime denies any host-import call lacking the corresponding cap.

---

## 2. Memory model

WebAssembly linear memory is a single byte array shared between host
and module. There are no pointers — only `i32` offsets into that array.
Strings and byte slices cross the boundary as **(ptr, len) pairs**.

### 2.1 Allocation contract

Buffers used to pass data into the host (args JSON, paths, payloads)
are allocated by the **module** via `stado_alloc`. The host writes
into them, the module reads them, and the module is responsible for
calling `stado_free` when it's done.

Buffers used to receive data from the host (tool results, file reads,
HTTP responses) are also allocated by the module — the module passes
in `(out_ptr, out_max)` and the host writes up to `out_max` bytes,
returning the actual count written.

The host **never** holds onto a pointer past the call that received it.

### 2.2 Pointer/length encoding

Every `stado_*` import that takes a string or byte slice spells out
both halves explicitly:

```
stado_log(level_ptr, level_len, msg_ptr, msg_len)
stado_fs_read(path_ptr, path_len, buf_ptr, buf_cap) → bytes_written
```

There is no null terminator. A `len` of 0 means an empty string;
calling sites that need a sentinel "no value" use a separate flag in
the args JSON or omit the field.

### 2.3 Lifetime rules

- Memory passed to a host import is valid only for the duration of
  that single call. Host code does not retain pointers across calls.
- The module is free to reuse / free a buffer immediately after the
  call returns.
- For typed handles (§6), the *handle* (an `i32`) outlives any
  particular call — but the underlying memory backing the handle is
  managed entirely host-side.

---

## 3. Calling convention

WASM imports are plain synchronous functions; they take `i32`/`i64`
parameters and return at most one value. Persistent applications additionally
expose the serialized host-entry functions below.

### 3.1 Synchronous serialized entry

Every host import is synchronous. The WASM caller blocks until the host
returns. Ordinary tools do not receive callbacks while an import is on their
stack.

A lifecycle application has additional host-initiated entry points:
`stado_plugin_lifecycle`, `stado_plugin_event`, `stado_plugin_command`,
`stado_plugin_tick`, and its declared `stado_tool_*` exports. The host admits
all of them through one context-aware serialization gate; the module is never
re-entered concurrently. Durable events are delivered and acknowledged in
broker WAL order. A trap, timeout, cancellation, or malformed result leaves an
event pending for replay instead of advancing the cursor.

No closure or native pointer crosses the boundary. Timers and agent completion
become broker events and enter the same serialized dispatcher; they do not
invoke arbitrary guest code from a host goroutine.

### 3.2 No host-managed wasm threads

Ordinary tool calls each get their own runtime. A lifecycle application keeps
one module but the host never calls it concurrently. Inside a module, plugin
code remains responsible for its own goroutines and shared linear memory; pin
pointers (Go: `runtime.KeepAlive`) before handing them to the host.

---

## 4. Return value conventions

Most imports return an `i32`. The sign and value carry both result
size and error sentinels:

| Return | Meaning |
|---|---|
| **`> 0`** | Success — bytes written to the output buffer |
| **`0`** | Success with no payload (e.g. `_close` on a clean handle) **or** EOF (for streaming reads — see context) |
| **`-1`** | Generic error — capability denied, malformed input, unknown handle, host-side failure. The plugin treats this as "the call did not succeed and no useful output was produced." |
| **`-2`** | Operation-specific timeout sentinel. Currently used by `stado_net_accept` and `stado_net_recvfrom`. Signals "no event in window" — the plugin can re-loop. |

### 4.1 Packed `i64` returns

Two imports return an `i64` to deliver two scalar outputs in one
call. The convention: **high 32 bits = primary value, low 32 bits =
secondary**. Sentinels live in the high half.

```
stado_net_dial(...) → i64        # handle as uint32 promoted (high bits 0); -1 on error.
stado_net_recvfrom(...) → i64    # high32 = body_len_or_sentinel, low32 = addr_len.
                                 # Extract: body = int32(ret >> 32); addr = int32(uint32(ret))
```

### 4.2 Tool-result negation convention

`stado_tool_<name>` exports use a slightly different convention:

| Return | Meaning |
|---|---|
| **`> 0`** | Bytes of normal tool result written to `result_ptr` |
| **`< 0`** | Bytes of error envelope written, value is the **negated** byte count |
| **`0`** | Empty result, no error |

This lets the host distinguish "successful call returning nothing
visible" from "error payload written" without a separate flag.
Plugin authors writing in Go can use the SDK helper `sdk.Write` and
return its result; for errors, return `-sdk.Write(...)`.

---

## 5. Typed handles

Resources whose lifetime spans multiple wasm calls (subprocesses,
PTYs, sockets, HTTP response bodies) are referenced by **typed
handles**: a 32-bit ID prefixed with a stable type tag.

### 5.1 Handle types

| Tag | Resource | Allocator | Imports that produce it | Imports that consume it |
|---|---|---|---|---|
| `proc` | One-shot subprocess | `stado_proc_spawn`, `stado_exec` | proc_read/write/wait/kill/close |
| `term` | PTY-backed terminal session | `stado_pty_create` | pty_read/write/resize/signal/destroy/list/snapshot/expect (no attach — EP-0043) |
| `agent` | Sub-agent in the fleet | `stado_agent_spawn` | agent_list/read_messages/send_message/cancel |
| `session` | Forked stado session | `stado_session_fork` | (operator-facing — referenced by session ID, not handle) |
| `plugin` | Reference to another loaded plugin | (internal — used by `tool:invoke`) | — |
| `conn` | TCP / UDP / Unix socket (dialed or accepted) | `stado_net_dial`, `stado_net_accept` | net_read/write/close |
| `listen` | TCP / UDP / Unix listener | `stado_net_listen` | net_accept, net_sendto, net_recvfrom, net_close_listener |
| `httpresp` | Open HTTP response body | `stado_http_request_stream`, `stado_http_upload_finish` | http_response_read/close |
| `httpup` | In-flight HTTP request body writer | `stado_http_upload_create` | http_upload_write/finish |
| `http` | Stateful HTTP client (cookie jar) | `stado_http_client_create` | http_client_request, http_client_close |

### 5.2 Handle lifecycle

1. **Allocation.** The producing import returns a 32-bit ID inside
   the `i32` (or i64) return slot. The host has stored an internal
   record keyed by the ID.
2. **Use.** Subsequent imports take the ID as a parameter. Wrong-
   type IDs are rejected with `-1` (the host validates the type tag).
3. **Free.** Either the plugin calls the resource's `_close` /
   `_destroy` import, or the host's reaper runs at runtime shutdown
   (`Runtime.Close`) and reaps any open handles.

### 5.3 Operator-facing rendering

Internally a handle is `(type_tag, uint32)`; for operator-facing
display (logs, audit, error messages), the host renders it as
`<type>:<plugin>.<hex>` — e.g. `proc:fs.7a2b`, `conn:abc123`.
Plugin authors don't need to construct these strings; the host
formats them. The wasm side just passes the `i32` ID around.

### 5.4 Per-Runtime resource caps

To prevent a misbehaving plugin from exhausting host resources,
each handle type has a per-Runtime cap:

| Resource | Cap |
|---|---|
| Open subprocess + PTY handles | (configurable) |
| Open net connections (dial ∪ accept) | 64 |
| Open net listeners | 8 |
| Open HTTP response streams | 8 |
| In-flight HTTP uploads | 8 |
| Open HTTP clients | 64 |

Calls that would exceed the cap return `-1`. The plugin should
close handles it no longer needs.

---

## 6. JSON envelope conventions

All structured args / results cross the boundary as **JSON bytes**.
Tool inputs come in as a JSON object; outputs go out as a JSON
object. The `application/json` is implicit — no MIME header.

### 6.1 Args parsing

The plugin's `stado_tool_<name>` export reads `(args_ptr, args_len)`,
treating the bytes as JSON. Defensive plugins handle empty input
(0-length means "no args supplied"):

```go
var args MyArgs
if raw := sdk.Bytes(argsPtr, argsLen); len(raw) > 0 {
    if err := json.Unmarshal(raw, &args); err != nil {
        return -sdk.Write(resultPtr, []byte(`{"error":"invalid args"}`))
    }
}
```

Bound: `maxPluginRuntimeToolArgsBytes = 1 MiB`. Larger args are
refused at the boundary.

### 6.2 Result writing

Successful tool output: write JSON bytes to `result_ptr`, return the
byte count. Bound: 1 MiB result buffer. If the result wouldn't fit,
truncate gracefully (e.g. session.search trims results until they
fit) rather than returning -1.

### 6.3 Error envelope

When `stado_tool_<name>` wants to surface an error to the agent,
write a JSON object with an `"error"` field and return the
**negated** byte count:

```json
{"error": "session:read denied — declare session:read in manifest"}
```

The agent runtime renders this as a tool error rather than a normal
result. Plugin authors should keep error messages actionable —
mention the missing capability, the failed precondition, the
alternative call to make.

### 6.4 Host imports that return JSON

Several host imports return JSON payloads instead of plain bytes:

- `stado_session_read("history")` → JSON array of `{role, text}`
- `stado_dns_resolve` → JSON `{records: [...], error?: ...}`
- `stado_http_request` → JSON `{status, headers, body_b64, body_truncated}`
- `stado_http_request_stream` → JSON `{status, headers, body_handle}`
- `stado_secrets_list` → JSON array of names

These follow the same return-value convention as binary imports
(positive = bytes, -1 = error).

---

## 7. Buffer sizing and truncation

The plugin chooses how much memory to allocate for receiving data;
the host writes up to that bound. There is **no protocol for the
host to say "needs more"** — if your buffer was too small, you
either get a partial result (binary streaming reads) or `-1`
(structured outputs that don't tolerate truncation).

### 7.1 Defaults and limits

| Surface | Plugin-side default | Host-side cap |
|---|---|---|
| Tool args buffer | 1 MiB | `maxPluginRuntimeToolArgsBytes = 1 MiB` |
| Tool result buffer | 1 MiB | `maxPluginRuntimeImportBytes = 16 MiB` |
| File read buffer | per-call | `maxPluginRuntimeFSFileBytes = 16 MiB` |
| stado_log message | per-call | `maxPluginRuntimeLogMessageBytes = 64 KiB` |
| stado_progress payload | per-call | 4 KiB |
| stado_json_get / _format input | per-call | 256 KiB |
| Session field (e.g. `history`) | per-call | bounded by session size |
| Artifact request | per-call | 1 MiB |

### 7.2 Truncation strategies

Different imports handle "buffer too small" differently:

- **Read-style imports** (`stado_fs_read`, `stado_net_read`,
  `stado_http_response_read`): return up to `cap` bytes; caller
  loops to drain.
- **Structured-payload imports** (`stado_session_read`,
  `stado_dns_resolve`, `stado_http_request`): return -1 — partial
  JSON would be nonsense. Plugin re-calls with a bigger buffer.
- **Tool result writing**: graceful trim is recommended; see
  `session.search` for an example that drops match entries until
  the result fits.

---

## 8. Capability vocabulary

Capabilities are declared in the manifest's `capabilities` array.
Each cap is a colon-separated string. The host parses these at
plugin-load time and gates every import call against the resulting
allowlist. **Lacking the cap fails closed and never crashes:** most imports
return `-1`; the generic artifact imports use their documented `-n` buffered
error form.

### 8.1 Cap shapes

| Cap | Effect |
|---|---|
| `fs:read:<abs-or-rel-path>` | `stado_fs_read` of paths under that prefix. Relative paths resolve against the host's `Workdir`. |
| `fs:write:<abs-or-rel-path>` | `stado_fs_write` |
| `net:http_request[:<host>]` | `stado_http_request` and `_stream` (optional host allowlist) |
| `net:http_request_private` | Loosens dial guard to RFC1918 / loopback / link-local |
| `net:http_client` | `stado_http_client_*` (cookie-jar HTTP) |
| `net:dial:tcp:<host-glob>:<port-glob>` | `stado_net_dial` outbound TCP |
| `net:dial:udp:<host-glob>:<port-glob>` | UDP dial + UDP listener `_sendto` peer cap |
| `net:dial:unix:<path-glob>` | Unix socket dial |
| `net:listen:tcp:<host-glob>:<port-glob>` | `stado_net_listen` TCP bind (verbatim match — no implicit `127.0.0.1 ⊂ 0.0.0.0`) |
| `net:listen:udp:<host-glob>:<port-glob>` | UDP bind for stateless send/recv |
| `net:listen:unix:<path-glob>` | Unix socket bind |
| `net:multicast:udp` | `stado_net_setopt` keys: broadcast, multicast_join/leave/loopback/ttl |
| `net:icmp` | `stado_net_icmp_echo` (ping; unprivileged ICMP if available, raw fallback needs `CAP_NET_RAW`) |
| `exec:proc[:<path-glob>]` | `stado_proc_*` and `stado_exec` (optional binary allowlist). Replaces the dropped `exec:bash` / `exec:search` / `exec:ast_grep` caps — declare each binary your plugin runs (`exec:proc:/usr/bin/rg`) plus `bundled-bin:<name>` for stado-bundled tools (rg, ast-grep) |
| `exec:pty[:<path-glob>]` | all `stado_pty_*` imports; the optional glob scopes `stado_pty_create` to matching binaries |
| `lsp:query` | bundled LSP imports |
| `bundled-bin` | `stado_bundled_bin` access |
| `dns:resolve` | `stado_dns_resolve` |
| `dns:axfr` | `stado_dns_resolve_axfr` (RFC 5936 zone transfer; implies `dns:resolve`) |
| `dns:axfr_private` | AXFR against private / RFC1918 servers (implies `dns:axfr`) |
| `dns:reverse` | reverse / PTR lookups via `stado_dns_resolve` (`qtype: "PTR"`) |
| `crypto:hash` | `stado_hash`, `stado_hmac` |
| `compress` | `stado_compress`, `stado_decompress` |
| `session:read` | `stado_session_read` (history, counts, IDs) |
| `session:observe` | `stado_session_next_event` |
| `session:fork` | `stado_session_fork` |
| `llm:invoke[:<token-budget>]` | `stado_llm_invoke` (optional per-session token cap) |
| `artifact:propose:<local-kind>` | `stado_artifact_propose`; exact local kind declared in this plugin's signed manifest |
| `artifact:read:<qualified-kind-pattern>` | `stado_artifact_query`; exact qualified kind, `*`, or one trailing-`*` prefix |
| `artifact:edit:<local-kind>` | `stado_artifact_edit`; exact local kind declared in this plugin's signed manifest |
| `artifact:observe:<qualified-kind-pattern>` | `stado_artifact_observe`; exact qualified kind, `*`, or one trailing-`*` prefix |
| `session:journal:append`, `session:projection:read` | Broker-owned lifecycle journal append and caller-scoped projection read |
| `session:schedule` | Leased hold acquire/release and typed pause/stop proposals |
| `session:complete` | Typed durable successful-completion transition for one application run |
| `session:input:route` | Claim an exact immutable input for asynchronous review, then classify it as `deliver` or `defer`; no discard or text replacement |
| `session:worker:request` | Request a bounded durable application-owned worker run; activation remains native-only |
| `session:worker:resume` | CAS-request resume of the same interrupted worker run; native activation rechecks pause/stop/completion, unexpired holds, and recurrence ownership |
| `session:worker:cancel` | CAS-cancel the application's own requested, resume-requested, or active worker run |
| `timer:schedule` | Durable timer schedule/cancel |
| `agent:spawn`, `agent:list`, `agent:read`, `agent:send`, `agent:cancel` | The matching `stado_agent_*` operation. The removed aggregate `agent:fleet` capability grants no operation. |
| `agent:spawn:configure` | Additional attenuation required with `agent:spawn` to override provider, model, thinking mode/token budget, or reasoning effort. Grants no operation or credential access by itself. |
| `cfg:state_dir` | `stado_cfg_state_dir` (read state-dir path) |
| `secrets:read[:<name-glob>]` | `stado_secrets_get` / `_list` |
| `secrets:write[:<name-glob>]` | `stado_secrets_put` / `_delete` |
| `state:read[:<key-glob>]` | `stado_instance_get` / `_list` |
| `state:write[:<key-glob>]` | `stado_instance_set` / `_delete` |
| `tool:invoke[:<name-glob>]` | `stado_tool_invoke` (call other registered tools). When a session executor is pinned (TUI, `stado run`, headless), nested invokes route through `Executor.Run` for audit + lifecycle hooks (v0.75.2). See [host-imports.md](host-imports.md). |
| `ui:approval` | `stado_ui_approve` (request a yes/no workflow decision; not durable authority) |
| `ui:choice` | `stado_ui_choose` (interactive operator choice) |
| `ui:print` | `stado_ui_print` (bounded plain-text operator output) |
| `ui:render` | `stado_ui_render` (bounded structured operator panel) |

### 8.2 Glob semantics

Path/host globs use Go's `filepath.Match`:
- `*` matches any single path segment (does **not** cross `/`)
- `?` matches one character
- `[abc]` matches a character class

Host globs are case-insensitive. Paths are exact-segment.

Artifact read/observe patterns do **not** use `filepath.Match`: EP-0063 limits
them to an exact qualified kind, `*`, or a single trailing `*` prefix. Propose
and edit grants are exact manifest-declared local kind names.

### 8.3 Capability auditing

Every gated import call is auditable. The `stado plugin doctor`
subcommand parses a manifest's caps and emits a per-surface table
explaining what each cap unlocks and which `tool run` flags are
needed to exercise it.

---

## 9. Manifest schema

A plugin manifest is JSON. Required fields:

```json
{
  "name": "my-plugin",
  "version": "0.1.0",
  "author": "Display Name",
  "author_pubkey_fpr": "ed25519:<hex-fingerprint>",
  "wasm_sha256": "<sha256 of plugin.wasm>",
  "capabilities": ["fs:read:.", "net:http_request:api.example.com"],
  "tools": [
    {
      "name": "search",
      "description": "Search the repository for…",
      "schema": "<JSON schema of the args>"
    }
  ],
  "min_stado_version": "0.36.0",
  "timestamp_utc": "2026-05-06T00:00:00Z",
  "nonce": "<random string>"
}
```

Optional fields:

| Field | Purpose | Since |
|---|---|---|
| `requires` | Array of `"<plugin-name>"` or `"<name> >= <semver>"` entries; install fails if a dep isn't present | v0.36.0 |
| `tools[].categories` | Array of category tags (`file`, `code-search`, `network`, …); used by `[tools].autoload_categories` to surface tools without explicit names | v0.36.0 |
| `artifact_kinds` | Signed EP-0063 local kind declarations: bounded object-root JSON Schema plus deterministic index projections | pre-1.0 |
| `lifecycle` | Signed EP-0064 lifecycle point/event subscriptions, failure policy, and timeout | pre-1.0 |
| `commands` | Signed operator command names/descriptions/usages and optional per-command `timeout_ms` (maximum 15 minutes) routed to the fixed `stado_plugin_command` export; a command grants no authority by itself | pre-1.0 |
| `description` | One-line description | — |
| `homepage`, `license` | Operator-facing metadata | — |

### 9.1 Signature

Manifests are Ed25519-signed. The signature lives in
`plugin.manifest.sig` adjacent to the manifest. The host:

1. Verifies the signature against the pinned signer pubkey.
2. Verifies the wasm digest matches `wasm_sha256`.
3. Checks `min_stado_version` against the running build.
4. Checks rollback protection (this signer hasn't shipped a higher
   version of this plugin already).
5. Checks `requires` deps resolve.

---

## 10. Lifecycle

A single plugin invocation goes through:

```
NewHost(manifest, workdir, logger)
  ↓ parse caps; build Host struct with allowed bits set
InstallHostImports(ctx, runtime, host)
  ↓ wire the ~70 stado_* imports against the runtime
Runtime.Instantiate(ctx, wasmBytes, manifest)
  ↓ instantiate the module; calls stado_plugin_init if exported
PluginTool.Run(ctx, args, toolHost)
  ↓ dispatch to stado_tool_<name>
Runtime.Close(ctx)
  ↓ stado_plugin_close if exported; reap all typed handles
```

For background plugins, `Runtime.Close` is deferred until the
session ends; tools dispatch through the same long-lived runtime.

### 10.1 Per-call vs long-lived runtime

By default each `stado tool run` invocation builds a fresh runtime,
calls one tool, and closes. Background plugins (declared via
`[plugins].background` in **user config only** — stripped from project
`.stado/config.toml`, EP-0044) keep a long-lived runtime and accumulate
state across ticks. Bundled plugins behave like the per-call mode —
each tool dispatch instantiates a fresh runtime to keep blast radius
contained.

State that needs to survive across calls within a session uses
`stado_instance_*` (per-Runtime in-memory KV) or the operator
secret store (`stado_secrets_*`). Versioned durable application records belong
in broker-owned EP-0063 artifacts; instance memory and
`stado_cfg_state_dir` are not authority or shared-WAL bridges.

### 10.2 Lifecycle application contract

[EP-0064](../eps/0064-wasm-lifecycle-applications.md) defines the corrective
application mode for larger signed workflows: one serialized instance per
effective plugin/session/generation, signed `lifecycle` subscriptions, bounded
callbacks, durable broker events, and operation-scoped authority. Plugin
documentation must not present native application policy or ambient filesystem
state as a substitute.

The persistent dispatcher, capability gates, durable event cursor, command
router, and shared tool instance are implemented. Each callback receives the
broker-admitted application identity and session/generation anchor; guest JSON
cannot select either. The generic UI bridge used by these applications is
named
`stado_ui_choose` (`ui:choice`), `stado_ui_print` (`ui:print`), and
`stado_ui_render` (`ui:render`). Operator interaction does not itself create
artifact authority or scheduling authority.

For v0.80/v1, the interactive TUI is the only supported application host. It
routes commands, tools, hooks, events, ticks, and generic bridges through the
same persistent instance. `stado run`, headless JSON-RPC, and ACP reject
configured lifecycle applications before provider/session work; legacy
background loading skips lifecycle manifests and ephemeral `plugin.run`
rejects them. See [EP-0064](../eps/0064-wasm-lifecycle-applications.md#supported-host-surface-for-v1).

Conditional exports use this common signature:

```text
stado_plugin_lifecycle(input_ptr, input_len, result_ptr, result_cap) -> i32
stado_plugin_event(input_ptr, input_len, result_ptr, result_cap) -> i32
stado_plugin_command(input_ptr, input_len, result_ptr, result_cap) -> i32
```

`stado_plugin_command` returns
`{"status":"ok|error","message"?:string,"worker_run_id"?:string,"resume_worker_run_id"?:string}`.
The two worker handoff fields are mutually exclusive and valid only with
`status:"ok"`. `worker_run_id` names a new requested run;
`resume_worker_run_id` names an exact durable `resume_requested` transition.
Neither field carries authority
or activate recurrence: the native command host fetches that exact run from
the callback application's broker namespace through a dedicated
session-controller-authenticated RPC, applies the generic recurrence conflict
rule, and performs a versioned native-only activation. The ordinary
application bearer cannot select native lookup, activation, or operator
cancellation. A command may update the application's own capability-bounded
quality workflow, but it cannot request an artifact authority transition.
Operator-origin artifact activation remains a separate broker operation under
EP-59.

Commands inherit `lifecycle.timeout_ms` unless their signed manifest entry sets
its own `timeout_ms`. The independent command ceiling is 15 minutes so a TUI
workflow can make several bounded `stado_ui_choose` calls without granting the
same delay to hooks, durable events, or ticks. Cancellation still closes the
call, and a longer timeout never enables a missing UI bridge or capability.

### 10.3 Optional `tool.Host` extensions

Long-lived hosts (TUI session, MCP server, headless agent loop)
share resources across the per-call runtimes by implementing
optional interfaces on the host they pass into `tool.Run`:

| Interface | Method | Purpose |
|---|---|---|
| `tool.AgentFleetProvider` | `AgentFleetBridge() any` | Bundled `agent.*` tools see a shared fleet |
| `tool.ProgressEmitter` | `EmitProgress(plugin, text)` | Bundled `stado_progress` emissions surface to the operator |
| `tool.PTYProvider` | `PTYManager() any` | Bundled `shell.*` / `pty.*` tools share a long-lived PTY registry — without this, `shell.spawn` returns an id that the next call's `shell.read` / `write` can't see (each `bundledPluginTool.Run` would otherwise build a fresh `pty.NewManager`) |
| `pluginRuntime.ApprovalBridge` | (host-package interface) | Plugins requesting `ui:approval` get an interactive prompt |

When a host doesn't implement these, the bundled-plugin Run path
falls back gracefully (per-call manager, nil callback drop, deny
approval). Single-shot CLI invocations (`stado tool run`) are
short-lived processes anyway, so the fallback is appropriate for them.

---

## 11. Lazy-load and meta-tools

Per [EP-0037 §E](../eps/0037-tool-dispatch-and-operator-surface.md), the
**per-turn tool surface** sent to the model is a subset of the full
registry. Tools are autoloaded if they're in `defaultAutoloadNames`,
match a `[tools].autoload` glob, or have a category in
`[tools].autoload_categories`. Other tools are loaded **on demand**
via the model calling `tools.describe`.

### 11.1 Meta-tools

| Tool | Purpose |
|---|---|
| `tools.search` | List tool names matching a query |
| `tools.describe` | Return full schemas for named tools — and **activate them** for the rest of the turn |
| `tools.categories` | List all category tags |
| `tools.in_category` | List tool names tagged with a category |
| `tools.activate` | Manually surface a tool without describing it |
| `tools.deactivate` | Remove a tool from this session's per-turn surface |
| `plugin.load` | Activate every tool a named plugin provides |
| `plugin.unload` | Deactivate the same |

### 11.2 Plugin authoring impact

Plugin tools default to **not autoloaded** unless the operator opts
in via `[tools].autoload` or `[tools].autoload_categories`. To make
your tool discoverable:

- Tag the tool with a useful category (`tools[].categories`)
- Add a clear, terse description — `tools.search` matches it

Plugin authors don't need to do anything else for lazy-load; the
TUI / agent loop handles activation transparently.

---

## 12. Naming conventions

Tool names cross three forms:

| Form | Where used | Example |
|---|---|---|
| **Wasm export** | `//go:wasmexport` in plugin code, host registers under this | `stado_tool_search` |
| **Bare** | Strip `stado_tool_` prefix; used as the tool's "name" within the plugin | `search` |
| **Wire** | Plugin-prefixed registry name (`<plugin>__<bare>`); what `tools.search` returns | `session__search` |
| **Dotted** | Operator-friendly form for config / display (`<plugin>.<bare>`) | `session.search` |

The host builds the wire form automatically from the manifest's
`name` and the tool's bare name. Plugins don't construct these
themselves.

`[tools].enabled`, `[tools].disabled`, `[tools].autoload`, etc. all
accept any of bare, wire, dotted, or globbed (`fs.*`) — see
`runtime.ToolMatchesGlob`.

---

## 13. Versioning and compatibility

stado tags run `vMAJOR.MINOR.PATCH`. Pre-1.0 (current state):

- Any pre-1.0 release may add, rename, or remove host imports, capabilities,
  manifest fields, or return-code sentinels when required to restore the
  accepted architecture. The current ABI reference is the contract; source
  aliases are not a promise that an older plugin will keep working.
- **PATCH** bumps are bug fixes / docs / dependency bumps.
- Breaking changes appear in CHANGELOG with explicit migration notes, but do
  not require a deprecation period before v1.

`min_stado_version` in the manifest gates installation: a plugin
that uses `stado_progress` (added in v0.38) declares
`"min_stado_version": "0.38.0"`. Older stado refuses to install it.

---

## 14. SDK helpers (Go)

`internal/plugins/bundled/sdk` provides a thin layer for Go-targeted
plugins:

```go
sdk.Alloc(size int32) int32              // implements stado_alloc
sdk.Free(ptr, size int32)                // implements stado_free
sdk.Bytes(ptr, len int32) []byte         // read N bytes from memory
sdk.Write(ptr int32, data []byte) int32  // write to memory + return len
```

Other languages don't have an SDK; they implement the same handful
of helpers manually. See [`docs/features/plugin-authoring.md`](../features/plugin-authoring.md)
for Zig and Rust examples.

---

## 15. Index of imports

For the per-import reference (signatures, capability gates, behavior
notes, examples) see [`host-imports.md`](host-imports.md). It's
organized by tier:

- **Tier 1 — capability primitives**: log, fs_read/write, proc,
  pty/terminal, bundled_bin, session_*, llm_invoke, ui_approve,
  cfg_state_dir.
- **Tier 2 — stateful conveniences**: http_client_*, http_request,
  http_request_stream, dns_resolve, net_*, tool_invoke, instance_*,
  secrets_*.
- **Tier 3 — stateless format conveniences**: hash, hmac, compress,
  decompress, progress, json_get, json_format.
- **Agent surface**: agent_spawn / list / read_messages /
  send_message / cancel.
- **Artifact surface**: artifact_propose / query / edit / observe (EP-0063).
- **LSP primitives**: lsp_find_definition / find_references /
  document_symbols / hover (the wider tool-bridging surface —
  `stado_fs_tool_*`, `stado_exec_bash`, `stado_http_get`,
  `stado_search_*` — was removed by EP-no-internal-tools).

---

## 16. Related documents

- [`docs/plugins/host-imports.md`](host-imports.md) — function-by-function reference
- [`docs/features/plugin-authoring.md`](../features/plugin-authoring.md) — first-time-author walkthrough
- [`docs/commands/plugin.md`](../commands/plugin.md) — operator CLI reference
- [EP-0002](../eps/0002-all-tools-as-plugins.md) — every-tool-is-a-plugin architecture
- [EP-0006](../eps/0006-signed-wasm-plugin-runtime.md) — signing + verification protocol
- [EP-0037](../eps/0037-tool-dispatch-and-operator-surface.md) — wire form, dispatch, lazy-load
- [EP-0038](../eps/0038-abi-v2-bundled-wasm-and-runtime.md) — ABI v2 + tier system
