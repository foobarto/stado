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
inside a sandboxed runtime; one runtime per plugin invocation, torn
down on return.

### 1.1 Required exports

Every module **must** export:

| Export | Signature | Purpose |
|---|---|---|
| `stado_alloc` | `(size i32) → i32` | Allocate a buffer in linear memory; return its base pointer (or 0 on failure). The host calls this to hand args + result buffers to the plugin. |
| `stado_free` | `(ptr i32, size i32) → ()` | Free a previously-allocated buffer. The host calls this after a tool invocation. |
| `stado_tool_<name>` | `(args_ptr i32, args_len i32, result_ptr i32, result_max i32) → i32` | One per tool the manifest declares. Returns bytes written to result (positive), or `-N` to signal an error envelope was written instead (see §5). |

Wasm names use snake_case; the host strips `stado_tool_` to get the
tool's bare name, then maps it to wire form (`<plugin>__<tool>`) for
the registry. See §13 for full naming rules.

### 1.2 Optional exports

| Export | Purpose |
|---|---|
| `stado_plugin_init` | One-shot init called once after instantiation, before any tool call. Use for bootstrapping caches, opening long-lived handles. |
| `stado_plugin_close` | Called before the runtime is torn down. Last chance to flush state. |
| `stado_plugin_tick` | Background-plugin tick. Called periodically when the plugin is registered as a background plugin. |

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

Wasm imports are plain functions; they take `i32`/`i64` parameters
and return at most one value. There is no host-side multithreading
or callbacks (despite a few imports being named `_observe` etc. —
those are polling shims, see §5.4).

### 3.1 Synchronous, no callbacks

Every host import is synchronous. The wasm caller blocks until the
host returns. There is no callback ABI — when an event-style API
is needed (session events, agent fleet output) the host exposes a
**polling** import that returns 0 when no event is available right
now.

This is a deliberate constraint: no closures cross the boundary;
no host-side timer fires into wasm; no goroutines call back. It
keeps the security model simple — every cross-boundary effect is on
the wasm side's stack.

### 3.2 No host-managed wasm threads

The host runs one wasm module per plugin call. Concurrent plugin
calls each get their own runtime. Inside the module, you can use
goroutines (Go) or wasm threads (where supported) freely, but they
share linear memory — pin pointers (Go: `runtime.KeepAlive`) before
handing them to the host.

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
| `term` | PTY-backed terminal session | `stado_pty_create`, `stado_terminal_open` | pty_read/write/resize/signal/destroy/attach/detach/list |
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
| Open HTTP clients | 32 |

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
| stado_log message | per-call | `maxPluginRuntimeLogMessageBytes = ~4 KiB` |
| stado_progress payload | per-call | 4 KiB |
| stado_json_get / _format input | per-call | 256 KiB |
| Session field (e.g. `history`) | per-call | bounded by session size |

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
allowlist. **Lacking the cap → import returns -1, never crashes.**

### 8.1 Cap shapes

| Cap | Effect |
|---|---|
| `fs:read:<abs-or-rel-path>` | `stado_fs_read` of paths under that prefix. Relative paths resolve against the host's `Workdir`. |
| `fs:write:<abs-or-rel-path>` | `stado_fs_write` |
| `net:http_get` | `stado_http_get` |
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
| `net:<host>` | Generic `stado_http_get` host allowlist (deprecated; prefer `net:http_request:<host>`) |
| `exec:bash` / `exec:shallow_bash` | `stado_exec_bash` (refused on `plugin run` — needs an agent loop's sandbox runner) |
| `exec:proc[:<path-glob>]` | `stado_proc_*` and `stado_exec` (optional binary allowlist) |
| `exec:search` | bundled ripgrep via `stado_search_ripgrep` |
| `exec:ast_grep` | bundled ast-grep via `stado_search_ast_grep` |
| `exec:pty` | `stado_pty_*` PTY sessions |
| `terminal:open` | `stado_terminal_*` PTY sessions (alias for the EP-0038c terminal surface) |
| `lsp:query` | bundled LSP imports |
| `bundled-bin` | `stado_bundled_bin` access |
| `dns:resolve` | `stado_dns_resolve` |
| `dns:axfr` | `stado_dns_resolve_axfr` (RFC 5936 zone transfer; implies `dns:resolve`) |
| `dns:reverse` (reserved) | reverse DNS (deferred) |
| `crypto:hash` | `stado_hash`, `stado_hmac` |
| `compress` | `stado_compress`, `stado_decompress` |
| `session:read` | `stado_session_read` (history, counts, IDs) |
| `session:observe` | `stado_session_next_event` |
| `session:fork` | `stado_session_fork` |
| `llm:invoke[:<token-budget>]` | `stado_llm_invoke` (optional per-session token cap) |
| `memory:propose` / `read` / `write` | memory store imports |
| `agent:fleet` | `stado_agent_*` (bundled agent plugin only) |
| `cfg:state_dir` | `stado_cfg_state_dir` (read state-dir path) |
| `secrets:read[:<name-glob>]` | `stado_secrets_get` / `_list` |
| `secrets:write[:<name-glob>]` | `stado_secrets_put` / `_delete` |
| `state:read[:<key-glob>]` | `stado_instance_get` / `_list` |
| `state:write[:<key-glob>]` | `stado_instance_set` / `_delete` |
| `tool:invoke[:<name-glob>]` | `stado_tool_invoke` (call other registered tools) |
| `ui:approval` | `stado_ui_approve` (request operator approval) |

### 8.2 Glob semantics

Path/host globs use Go's `filepath.Match`:
- `*` matches any single path segment (does **not** cross `/`)
- `?` matches one character
- `[abc]` matches a character class

Host globs are case-insensitive. Paths are exact-segment.

### 8.3 Capability auditing

Every gated import call is auditable. The `stado plugin doctor`
subcommand parses a manifest's caps and emits a per-surface table
explaining what each cap unlocks and which `plugin run` flags are
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
| `background` | If true, the plugin's `stado_plugin_tick` is called periodically | — |
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

By default each `stado plugin run` invocation builds a fresh runtime,
calls one tool, and closes. Background plugins (declared via
`[plugins].background`) keep a long-lived runtime and accumulate
state across ticks. Bundled plugins behave like the per-call mode —
each tool dispatch instantiates a fresh runtime to keep blast radius
contained.

State that needs to survive across calls within a session uses
`stado_instance_*` (per-Runtime in-memory KV) or the operator
secret store (`stado_secrets_*`).

### 10.2 Optional `tool.Host` extensions

Long-lived hosts (TUI session, MCP server, headless agent loop)
share resources across the per-call runtimes by implementing
optional interfaces on the host they pass into `tool.Run`:

| Interface | Method | Purpose |
|---|---|---|
| `tool.AgentFleetProvider` | `AgentFleetBridge() any` | Bundled `agent.*` tools see a shared fleet |
| `tool.ProgressEmitter` | `EmitProgress(plugin, text)` | Bundled `stado_progress` emissions surface to the operator |
| `tool.PTYProvider` | `PTYManager() any` | Bundled `shell.*` / `pty.*` tools share a long-lived PTY registry — without this, `shell.spawn` returns an id that the next call's `shell.attach` / `read` / `write` can't see (each `bundledPluginTool.Run` would otherwise build a fresh `pty.NewManager`) |
| `pluginRuntime.ApprovalBridge` | (host-package interface) | Plugins requesting `ui:approval` get an interactive prompt |

When a host doesn't implement these, the bundled-plugin Run path
falls back gracefully (per-call manager, nil callback drop, deny
approval). Single-shot CLI invocations (`stado plugin run`,
`stado tool run`) are short-lived processes anyway, so the fallback
is appropriate for them.

---

## 11. Lazy-load and meta-tools

Per [EP-0037 §E](../eps/0037-tool-dispatch-and-namespacing.md), the
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

- **MINOR** bumps may add new host imports, new caps, new manifest
  fields. Existing plugins keep working.
- **MINOR** bumps may also add **new return-code sentinels** to
  existing imports (e.g. `-2` for accept timeout was added in
  v0.37). Plugins using a "negative = error" check still work; only
  plugins that switch on `==-1` exclusively need updates.
- **PATCH** bumps are bug fixes / docs / dependency bumps.
- Breaking ABI changes (renaming, removing imports) are still
  allowed pre-1.0. They appear in CHANGELOG with explicit migration
  notes.

`min_stado_version` in the manifest gates installation: a plugin
that uses `stado_progress` (added in v0.38) declares
`"min_stado_version": "0.38.0"`. Older stado refuses to install it.

---

## 14. SDK helpers (Go)

`internal/bundledplugins/sdk` provides a thin layer for Go-targeted
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
- **Memory**: memory_propose / query / update.
- **Tool-bridging imports**: native fs / shell / search / lsp tools
  exposed as `stado_<tool>_*` imports for plugins that wrap them.

---

## 16. Related documents

- [`docs/plugins/host-imports.md`](host-imports.md) — function-by-function reference
- [`docs/features/plugin-authoring.md`](../features/plugin-authoring.md) — first-time-author walkthrough
- [`docs/commands/plugin.md`](../commands/plugin.md) — operator CLI reference
- [EP-0002](../eps/0002-all-tools-as-plugins.md) — every-tool-is-a-plugin architecture
- [EP-0006](../eps/0006-signed-wasm-plugin-runtime.md) — signing + verification protocol
- [EP-0037](../eps/0037-tool-dispatch-and-namespacing.md) — wire form, dispatch, lazy-load
- [EP-0038](../eps/0038-abi-v2-bundled-wasm-and-runtime.md) — ABI v2 + tier system
