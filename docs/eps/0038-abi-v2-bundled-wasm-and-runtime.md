---
ep: 38
title: ABI v2, bundled wasm tools, and runtime surface
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Draft
type: Standards
created: 2026-05-05
requires: [37]
supersedes: [13, 28, 34]
see-also: [2, 5, 6, 14, 17, 29, 31, 35, 37]
history:
  - date: 2026-05-05
    status: Draft
    note: Initial draft. Restores EP-0002's "all tools as wasm plugins" invariant. Adds Tier 1/2/3 host-import surface, supersedes EP-0013 (subagent_spawn tool surface), supersedes EP-0034 (background agents fleet), amends EP-0028 (--with-tool-host becomes default; bash refusal becomes operator-controlled).
  - date: 2026-05-05
    status: Draft
    note: >
      Revision pass after codex + gemini independent review. Material edits: reframed agent
      spawn as async-runtime / sync-default-tool-shape (codex #5); split agent_id / session_id /
      typed-handle identity (codex #6); added privileged-built-in registry + agent imports as
      Tier 1+ (codex #8); added HTTP cap enforcement at redirect time (codex #9); added scoped
      `exec:proc:<glob>` cap variant (codex #10); secrets namespaced by canonical identity not
      alias (gemini #2); `agent.read_messages` returns content + external-input markers
      (codex #7); replay default flipped to full-fidelity (gemini #4 / codex #7); sandbox §I
      gained per-mode env defaults, default bind contract, signal propagation (codex #11,
      gemini #12), capability manipulation only in wrap mode (codex #12); migration gates per-
      tool parity tests (codex #13); 32-bit handle IDs with collision check (gemini #16);
      bundled_bin flock-serialised cache (gemini #10); agent.spawn default parent_session =
      caller's session (codex #14, gemini #14); agent + session handles decoupled from plugin
      instance lifetime (gemini #3). Decision log gained D13–D23 capturing the changes.
  - date: 2026-05-05
    status: Draft
    note: >
      Added stado_fs_read_partial to §B Tier 1 surface and D24. Motivated by image-info
      dogfood: all-or-nothing stado_fs_read forced 16 MiB buffer allocation per header
      inspection call. fs.read tool gains offset?/length? params at schema level.
---

# EP-0038: ABI v2, bundled wasm tools, and runtime surface

## Problem

Three problems concentrate at the runtime layer.

### 1. EP-0002's invariant has drifted

EP-0002 declared "all tools as wasm plugins" and was marked
`Implemented` at v0.1.0. The visible LLM-facing tool surface IS
plugin-shaped (`buildBundledPluginRegistry` in
`internal/runtime/bundled_plugin_tools.go` wraps every native tool
with `newBundledPluginTool`). But the wrapper is a façade. The actual
implementations are direct `r.Register(NativeTool{})` calls:

```go
// internal/runtime/bundled_plugin_tools.go:32-46
r.Register(fs.ReadTool{})
r.Register(fs.WriteTool{})
r.Register(fs.EditTool{})
r.Register(fs.GlobTool{})
r.Register(fs.GrepTool{})
r.Register(bash.BashTool{Timeout: 60 * time.Second})
r.Register(webfetch.WebFetchTool{})
r.Register(rg.Tool{})
r.Register(astgrep.Tool{})
r.Register(readctx.Tool{})
def := &lspfind.FindDefinition{}
r.Register(def)
r.Register(&lspfind.FindReferences{Definition: def})
r.Register(&lspfind.DocumentSymbols{Definition: def})
r.Register(&lspfind.Hover{Definition: def})
// + tasktool, subagent
```

The wasm wrapper layer wraps these AS THEY APPEAR to the model, but
the implementation is native Go. EP-0002's stated invariant —
"the implementation behind a tool name is a plugin module running in
the wazero host" — is not currently true. The user's instruction:
"this slipped through the cracks somewhere between versions /
iterations." That's exactly the drift this EP closes.

### 2. Program-specific host imports are wrong shape

Walking through what would be needed to ship every current native
tool as a plain wasm plugin against today's host-import surface
reveals gaps:

- `bash` needs process spawn — no `stado_exec` host import exists.
- `rg` and `astgrep` ship as embedded native binaries per OS — no
  way for a wasm plugin to reach them.
- `lspfind` runs long-lived `gopls` / `rust-analyzer` subprocesses
  with bidirectional JSON-RPC stdio — no proc-handle host import.
- DNS resolution today goes through `http_request`'s internal
  resolver as a black box — no `stado_dns_*` host import.
- Network primitives are HTTP-only — no `stado_net_dial` for raw
  TCP/UDP/Unix sockets.

The temptation is to add program-specific imports
(`stado_ripgrep`, `stado_lsp_definition`, `stado_lsp_references`).
That's wrong. It breaks the invariant in a different way — the host
becomes a catalogue of tool implementations rather than a
capability-primitive layer. The right shape is **a small set of
generic primitives** (process spawn, terminal, raw network sockets,
bundled-binary access) plus **stateful conveniences** (HTTP client
with cookie jar, DNS resolver, secret store) plus **stateless
format conveniences** (JSON, hash, compression). Application logic
— including program-specific argument shaping — moves to wasm.

### 3. Native subagent + ad-hoc agent fleet

EP-0013's `spawn_agent` is native ("It is native instead of WASM-
backed because it needs the live provider, config, and session fork
primitive rather than only the plugin host imports") and synchronous
("the parent's tool call doesn't return until the child finishes").
EP-0034 (Draft) wraps it in a goroutine + Fleet registry for `/spawn`
+ `/fleet` UX.

Both miss two architectural beats:

- **Sessions and agents should be unified.** Every spawned agent
  already forks a session. If `agent.spawn` returns the session
  id and the agent's run loop reads from its session's pending-
  message queue, then "send a message to a running agent" is the
  same primitive as "type into a stado session." One conversation
  primitive, two drivers (human or parent agent).
- **Async runtime foundation, sync-default tool surface.** Today's
  sync-only `spawn_agent` forces parents to block; the runtime
  needs to be async at its core so children can run while the
  parent's tool executor is free for other things. The model-
  facing default stays sync (`async: false`) because that's what
  most spawn calls want — the result is the value. `async: true`
  is the opt-in for parents that want to drive multiple children
  in parallel or check on long-running work. EP-0034's Fleet is
  exactly the runtime layer for the async foundation; it just
  needs the right tool-surface contract on top.

Plus: EP-0014's "active-session-only execution" rule, written when
sessions were strictly human-driven, is incompatible with async
agents owning live sessions.

### 4. Operator-side process containment is half-built

EP-0005 ships Linux/macOS sandbox-runner integration for `exec:bash`
plugins. That covers the in-plugin-call path. It doesn't cover
"operator wants to wrap stado-the-process" — fs binds, env allow-
list, network namespace, capability drops, proxy enforcement. Today
the answer is "operator wraps stado externally with bwrap";
documented but not first-class. Per EP-0037 §A (third position
in the philosophy), this needs to be a config-driven layer
operators can compose declaratively.

## Goals

- Restore EP-0002's invariant: every tool the model sees is a wasm
  plugin running in the wazero host. `internal/runtime/bundled_
  plugin_tools.go`'s `r.Register(NativeTool{})` calls all delete.
  `newBundledPluginTool` wrapper layer deletes.
- Define ABI v2: a generic primitive surface (Tier 1) + stateful
  conveniences (Tier 2) + stateless format conveniences (Tier 3).
  No program-specific host imports.
- Bundle wasm versions of every current native tool, embedded in
  the binary, lazy-loaded, individually disable-able.
- Replace native `spawn_agent` with a five-tool agent surface
  (`agent.spawn`, `agent.list`, `agent.read_messages`,
  `agent.send_message`, `agent.cancel`). **Async-foundation,
  sync-default**: the runtime always runs children async (the
  parent's tool call doesn't tie up the wasm executor); the model-
  facing default is `async: false`, which is sugar for
  spawn-async + read-until-completion + return final assistant
  output. `async: true` returns a handle immediately for parents
  that want to do other work while children run. Every agent owns
  a session.
- Land the `[sandbox]` config schema reserved by EP-0037 with
  full implementation: `mode = "off" | "wrap" | "external"`, env
  allow-list, capability drops, proxy enforcement, namespace
  control via wrapper re-exec.
- Land runtime introspection: `/ps`, `/top`, `/kill`, `/stats`,
  `/sessions`, `/session attach` (read-write). Establish the
  type-prefixed dotted handle ID convention.
- Supersede EP-0013 (tool surface) and EP-0034 (fleet runtime),
  amend EP-0028 (warn-and-run instead of hard refusal), amend
  EP-0014 (relax active-session-only when async agents are
  driving non-foreground sessions).

## Non-goals

- Tool dispatch / naming / categories / config schema for
  `[tools]` — all of that is EP-0037.
- Plugin distribution / remote install / trust anchor — EP-0039.
- Multi-process daemon mode for agents. Agents run as goroutines
  in the same stado process. Multi-process is a future EP.
- Cross-machine fleet. Single-host only.
- HTTPS-intercept proxy with custom CA injection. v2 of `[sandbox]`
  may add it; not in scope here.
- WebSocket as a host import. Plugins build it on `stado_net_dial` +
  `stado_http_client` + `stado_hash_sha1` (RFC 6455 framing is
  ~50 lines wasm).

## Design

### §A — The invariant, restored and codified

**Stado core ships no native tools.** The host exposes a host-import
surface; every tool the model sees is a wasm plugin running in
wazero. A curated bundle ships embedded by default; users add,
replace, or disable any of them.

What this means in code:

| Today                                                | After EP-0038                                      |
|------------------------------------------------------|----------------------------------------------------|
| `internal/runtime/bundled_plugin_tools.go` registers `fs.ReadTool{}`, `bash.BashTool{}`, etc. | File deletes. Bundled wasm plugins register through the same path as user-installed plugins. |
| `internal/tools/{fs,bash,webfetch,httpreq,rg,astgrep,lspfind,readctx,tasktool,subagent}` exists as `tool.Tool` types | These directories migrate to `internal/host/*` as host-import implementation only. The Go code survives; it's no longer a registered tool surface. |
| `newBundledPluginTool` wrapper presents native tools as plugin-shaped to the model | Wrapper deletes. Wasm plugins are plugin-shaped natively. |
| `exec:bash` capability-gated tool plugin via `--with-tool-host` flag (EP-0028) | `exec:proc` host import + `shell` plugin. `--with-tool-host` becomes default for any plugin run. |

Migration plan (from §F migration / rollout below) ships the bundled
wasm plugins alongside the host-import additions in one EP-0038
iteration.

### §B — Host-import surface (ABI v2)

Three tiers, organised by what makes each import necessary or
appropriate.

#### Tier 1 — Capability primitives (host-only, lazy-init)

These exist because the wasi sandbox denies them physically. Every
Tier 1 import is something a wasm plugin literally cannot do
without host help.

```
stado_proc_spawn(req: SpawnRequest) -> Handle
stado_proc_read(h: Handle, max: u32, timeout_ms: u32) -> bytes
stado_proc_write(h: Handle, b: bytes) -> u32
stado_proc_close(h: Handle)
stado_proc_wait(h: Handle) -> exit_code
stado_proc_kill(h: Handle, signal: i32)

stado_exec(req: ExecRequest) -> ExecResult     # one-shot sugar over proc

stado_terminal_open(req: TerminalRequest) -> Handle
stado_terminal_read(h: Handle, max: u32, timeout_ms: u32) -> bytes
stado_terminal_write(h: Handle, b: bytes) -> u32
stado_terminal_resize(h: Handle, cols: u32, rows: u32)
stado_terminal_close(h: Handle)
stado_terminal_wait(h: Handle) -> exit_code

stado_net_dial(transport: enum, addr: string, opts: DialOpts) -> Handle
stado_net_listen(transport: enum, addr: string, opts: ListenOpts) -> Handle
stado_net_accept(listen_h: Handle, timeout_ms: u32) -> Handle
stado_net_read(h: Handle, max: u32, timeout_ms: u32) -> bytes
stado_net_write(h: Handle, b: bytes) -> u32
stado_net_close(h: Handle)

stado_net_icmp_open(local_addr: string?) -> Handle
stado_net_icmp_send(h: Handle, dest: string, type: u8, code: u8, payload: bytes, ttl: u32?, id: u16?, seq: u16?)
stado_net_icmp_recv(h: Handle, timeout_ms: u32) -> Packet
stado_net_icmp_close(h: Handle)

stado_fs_*                                      # kept as-is from current ABI v1
stado_fs_read_partial(path, offset, length, buf, bufcap) -> bytes
                                               # partial read; same cap gates as stado_fs_read;
                                               # returns up to bufcap bytes starting at offset
stado_bundled_bin(name: string) -> string      # lazy-extract path; flock-serialised, sha-keyed cache

stado_session_*                                 # kept; observable per EP-0006
stado_llm_invoke                                # kept
stado_log                                       # kept
stado_approval_request                          # kept; per EP-0017
```

##### Privileged-built-in imports (Tier 1+)

The bundled `tools` and `agent` plugins need host capabilities
that don't fit the generic-primitive Tier 1 surface — registry
introspection (which tools exist, what their schemas are) and
agent fleet operations (spawn/list/read/write/cancel against the
runtime's Fleet registry). Codex review #8 surfaced that not
naming these as ABI imports leaves them as un-specified
privileged operations.

The ABI defines them as **Tier 1+**: real wasm imports with
explicit capability gates, but the only plugins that may declare
the gating caps are bundled signatures (the `tools` and `agent`
plugins shipped from `internal/bundledplugins/wasm/`). User-
installed plugins declaring these caps fail at install with
`reserved capability ... only available to bundled plugins
shipped with stado` — the gate is on capability *acceptance* at
install time, not on the import surface itself, so a future EP
can promote selected operations to general use without an ABI
change.

```
stado_registry_list() -> [{name, summary, categories, ...}]
                                                # what tools.search backs onto
stado_registry_describe(names: [string]) -> [{schema, docs, caps, ...}]
                                                # what tools.describe backs onto
                                                # ALSO activates the schema(s) into the model's
                                                # available tool surface for this session

stado_agent_spawn(req: AgentSpawnRequest) -> {id, session_id}
stado_agent_list(filter: AgentListFilter?) -> [AgentEntry]
stado_agent_read_messages(id, since?, timeout_ms?) -> AgentMessages
stado_agent_send_message(id, msg)
stado_agent_cancel(id)
```

Caps (bundled-only):

- `registry:list` — gates `stado_registry_list`. Bundled `tools`
  plugin declares.
- `registry:describe` — gates `stado_registry_describe`. Bundled
  `tools` plugin declares.
- `agent:fleet` — gates the five `stado_agent_*` imports together.
  Bundled `agent` plugin declares.

User-installed plugins that want similar functionality drive it
through `tools.search`/`tools.describe`/`agent.*` (the wasm
plugin surface) which is the indirection layer. The `tools` and
`agent` plugins enforce any policy a user-plugin's tool call
should be subject to — they're not bypass paths, they're
enforcement points.

##### Transport enum for `stado_net_dial` / `stado_net_listen`

```
tcp     - TCP stream socket
udp     - UDP datagram socket (connected, single-remote)
unix    - Unix domain socket
```

`udp` "connected" means: dial binds the remote address; subsequent
read/write operate against that bound peer. Unconnected UDP
(broadcast, multicast, sniffer) is not in scope; needs a separate
listen-style API or future `stado_net_udp_unconnected` import.

`net_listen` accepts only `tcp` and `unix`. UDP "listen" is achieved
by `stado_net_dial("udp", ":4444")` with no remote — the existing
shape covers it.

##### Handle lifecycle

Every handle returned by Tier 1 imports is **scoped to plugin
instance lifetime**. When the wasm module's instance terminates
(plugin reload, plugin uninstall, runtime shutdown), all handles
the instance owns are killed/closed by the host. No orphans.

**Agents and sessions are exceptions** (gemini review #3). They
are scoped to **runtime lifetime**, not plugin-instance lifetime.
Reloading the bundled `agent` plugin (e.g. via `/plugin reload
agent` during dev) does not cancel running agents — the agent's
session is a first-class durable object owned by the runtime's
session store, not by the wasm instance that spawned it. When
the bundled `agent` plugin restarts, it re-attaches to existing
agents in the Fleet registry by their `id` and continues serving
`agent.read_messages` etc. against them.

This decoupling matters because the bundled `agent` plugin is
expected to reload during stado dev iteration; killing every
running agent on reload would be a serious operational regression.

A future optimisation could let an LSP server (which IS
instance-scoped) stay warm across plugin restarts, but that's a
separate EP — not v1.

##### `stado_bundled_bin` concurrency

Gemini review #10 surfaced that lazy extract on first call is
race-prone: two plugins calling `stado_bundled_bin("ripgrep")`
concurrently could each try to extract and corrupt the result.
The implementation:

1. Resolve target path: `$XDG_CACHE_HOME/stado/bundled-bin/<name>-<sha256-prefix-12>/<binary>`.
   Cache directory is keyed by sha256 of the embedded blob (first
   12 hex chars of the digest), so a re-extract for the same
   bytes is a no-op.
2. Acquire `flock(LOCK_EX)` on the cache directory.
3. If target exists and matches embedded sha256, release lock,
   return path.
4. Else extract embedded bytes to a tmp file in the same
   directory, fsync, atomic-rename to target, release lock,
   return path.

Concurrent callers serialise on the lock; only one extracts.
Cache is durable across stado runs and process kills (atomic
rename + fsync makes the cache safe to re-use without
re-verification on next call).

##### Capability declarations

Each handle-creating import requires a corresponding capability in
the manifest. Both broad and scoped forms are accepted; scoped is
the recommended manifest discipline.

| Import                 | Broad cap                       | Scoped cap (recommended)                       |
|------------------------|---------------------------------|-------------------------------------------------|
| `stado_proc_*`         | `exec:proc`                     | `exec:proc:<absolute-path-glob>` (e.g. `exec:proc:/usr/bin/git`, `exec:proc:/usr/local/bin/*`) |
| `stado_exec` (sugar)   | (same as proc)                  | (same as proc)                                  |
| `stado_terminal_*`     | `terminal:open`                 | `terminal:open:<absolute-path-glob>`            |
| `stado_net_dial(tcp)`  | `net:dial:tcp:*:*`              | `net:dial:tcp:<host>:<port>`                    |
| `stado_net_dial(udp)`  | `net:dial:udp:*:*`              | `net:dial:udp:<host>:<port>`                    |
| `stado_net_dial(unix)` | `net:dial:unix:*`               | `net:dial:unix:<path-glob>`                     |
| `stado_net_listen(tcp)`| `net:listen:tcp:*`              | `net:listen:tcp:<port>`                         |
| `stado_net_listen(unix)`| `net:listen:unix:*`            | `net:listen:unix:<path-glob>`                   |
| `stado_net_icmp_*`     | `net:icmp`                      | `net:icmp:<host>`                               |
| `stado_bundled_bin(N)` | (no broad form)                 | `bundled-bin:<name>` (always per-name)          |

`exec:proc` was originally a single broad cap. Codex review #10
flagged this as too coarse — operator admission becomes
all-or-nothing for shell, rg, lsp, mcp-stdio, and source-build.
The scoped form `exec:proc:<absolute-path-glob>` lets a plugin
declare exactly which binaries it spawns; the host enforces the
glob match against `argv[0]` after PATH resolution. Plugins can
declare both: `exec:proc:/usr/bin/git` for git-specific spawns
plus broad `exec:proc` for fallback (operator reviews and decides
which to accept). Stado does not refuse a manifest that declares
only the broad form — the philosophy stays operator-decision —
but `plugin doctor` warns that scoped forms exist and should be
preferred for plugins that target known binaries.

Per EP-0037 D2, **no synthesised privilege caps**. ICMP raw socket
needs `CAP_NET_RAW`; the kernel decides. Privileged ports (< 1024)
need root or CAP_NET_BIND_SERVICE; the kernel decides. Stado does
not refuse install or invocation when these aren't available; the
import call returns a structured error and the plugin reports it
upward.

#### Tier 2 — Stateful conveniences (lazy-init)

These hold state worth sharing across calls — connection pools,
cookie jars, DNS resolvers, secret backends — and are awkward
enough in wasm that having one host implementation rather than
N plugin reimplementations is a real win.

```
stado_http_client_new(opts: HTTPClientOpts) -> Handle
stado_http_client_close(h: Handle)
stado_http_request(client_h: Handle, method, url, headers, body) -> Response
stado_http_request_streaming(client_h: Handle, ...) -> Handle  # for read-as-stream

stado_dns_resolve(name: string, type: string, opts: DNSOpts?) -> []Record
stado_dns_resolve_axfr(name: string, server: string) -> []Record

stado_secrets_get(key: string) -> bytes
stado_secrets_set(key: string, value: bytes, opts: SecretOpts?)
stado_secrets_delete(key: string)
stado_secrets_list(prefix: string?) -> []string
```

##### `stado_http_client_*` design

```
HTTPClientOpts {
    base_url: string?
    timeout_ms: u32?
    cookie_jar: bool        # default true
    follow_redirects: bool  # default true
    auto_decompress: bool   # default true; gzip/brotli/zstd negotiated
    tls_config: TLSConfig?
    default_headers: map<string, string>?
}
```

`stado_http_request` (single-arg form, no client handle) becomes
sugar: "make default client + one request + close." Both forms
ship; plugin authors pick by need.

The decompression policy as a client option fixes the rough edge
in the v0.32.0 shakedown notes (NOTES suggestion B): plugin
controls compression behaviour explicitly via the client config.

The cookie jar built-in retires the browser plugin's hand-rolled
jar.

`stado_http_request_streaming` returns a response-handle that
accepts further `stado_net_read`-shape calls, for plugins that
need to stream large bodies without buffering.

##### HTTP capability enforcement

Codex review #9 surfaced that gating only `net:dial:tcp:*` while
leaving HTTP imports un-capped lets plugins bypass dial-policy
through HTTP requests. **HTTP imports enforce equivalent net-dial
caps internally**: every `stado_http_request[_streaming]` call,
including across redirects, validates the resolved hostname:port
against `net:dial:tcp:<host>:<port>` (or `net:dial:tcp:*:*`).

The `net:http_request[:<host>]` cap from ABI v1 is preserved as
an alternative high-level grant. A plugin can declare either:

- `net:http_request:api.example.com` — broad HTTP grant for
  one host (covers all ports, scheme inferred from URL, redirects
  bound to same host or refused).
- `net:dial:tcp:api.example.com:443` (no `net:http_request:*`) —
  primitive grant; HTTP layer enforces this for matching hosts.

If both are declared, both are checked (intersection — host must
satisfy at least one). Redirects to an unmatched host fail with
a structured error; no silent follow-redirect across cap
boundaries.

`net:http_request_private` cap (kept from v1) gates loopback /
RFC1918 destinations explicitly; same precedence rule applies.

##### `stado_http_client_*` capability declaration

| Import                          | Cap                                         |
|---------------------------------|----------------------------------------------|
| `stado_http_client_new`         | (no cap on client creation; per-request)     |
| `stado_http_request`            | `net:http_request[:<host>]` OR `net:dial:tcp:<host>:<port>` |
| `stado_http_request_streaming`  | (same)                                       |

##### `stado_dns_resolve` design

System resolver (resolv.conf) is the default backend; `opts.server`
overrides for DoH/DoT/custom recursor cases. AXFR is its own
function with its own cap because it's a different threat shape
(zone enumeration, not normal lookup).

```
DNSOpts {
    server: string?         # override resolv.conf
    timeout_ms: u32?
    use_tcp: bool           # force TCP transport (needed for AXFR / large responses)
}
```

Caps:

- `dns:resolve` — broad, any name
- `dns:resolve:<glob>` — scoped (e.g., `dns:resolve:*.htb.local`)
- `dns:axfr:<zone>` — zone transfer
- `dns:reverse:<cidr>` — PTR lookups for a CIDR

Plugins that need pathological packet construction (DNS
rebinding research, fragmentation tricks) bypass with
`stado_net_dial("udp", "8.8.8.8:53")` directly.

##### `stado_secrets_*` design

Backend: OS keychain (libsecret on Linux, Keychain on macOS,
Credential Manager on Windows) with a fallback flat-file
encrypted-at-rest in `~/.local/share/stado/secrets/` (where
"encrypted at rest" means age-encrypted with a key derived from
the system credential store; details deferred to implementation).

##### Namespace by plugin canonical identity, not local alias

Gemini review #2 surfaced a real attack: if secrets are
namespaced by display alias, a malicious plugin installed under
the same alias inherits the secrets of the previous trusted
plugin. **Secrets are namespaced by the plugin's canonical
identity** (per EP-0039 §A — typically `<host>/<owner>/<repo>` or
`local:///<absolute-path>` for local-dir installs), not by the
operator-assigned local alias.

Concretely: a plugin installed from `github.com/foobarto/htb-lab`
calling `stado_secrets_get("token")` reads the key
`github.com/foobarto/htb-lab/token`. A different plugin installed
later under the same local alias `htb-lab` (e.g. from
`github.com/attacker/htb-lab`) reads `github.com/attacker/htb-lab/
token` and cannot access foobarto's stored secrets.

Cross-plugin secret access is not permitted at all; the namespace
is enforced at the host import boundary, not per-call. Operator
who wants to *share* a secret across two plugins copies it
explicitly via CLI:

```
stado secrets copy github.com/foobarto/htb-lab/token \
                   github.com/foobarto/htb-spawner/token
```

Caps:

- `secrets:read:<key-suffix>` — fine-grained per key (suffix is
  what the plugin sees; the canonical-identity prefix is added
  automatically by the host).
- `secrets:read:<prefix>*` — glob form (suffix prefix only;
  cross-namespace globs not allowed).
- `secrets:write:<key-suffix>` / `secrets:write:<prefix>*` —
  write equivalents.
- No `secrets:read:*` (no all-secrets read).
- `secrets:read:cross:<other-canonical-identity>:<key>` —
  reserved syntax for an explicit cross-plugin read grant
  declared at install time. Operator reviews and accepts the
  cross-grant. Not v1; reserved.

#### Tier 3 — Stateless format conveniences

Pure functions exposed because per-plugin reimplementation has
high binary-size cost (especially Rust/Zig wasm).

```
stado_json_parse(b: bytes) -> Value
stado_json_stringify(v: Value, opts: JSONOpts?) -> bytes

stado_hash(algo: enum, b: bytes) -> bytes
stado_hmac(algo: enum, key: bytes, b: bytes) -> bytes

stado_compress(algo: enum, b: bytes) -> bytes
stado_decompress(algo: enum, b: bytes) -> bytes
```

Algos:

- `stado_hash` / `stado_hmac`: `md5`, `sha1`, `sha256`, `sha512`, `blake3`.
- `stado_compress` / `stado_decompress`: `gzip`, `brotli`, `zstd`.

JSON commitment: **strict RFC 8259**. No comments, no trailing
commas, no NaN/Inf. One implementation, one behaviour, forever.

Caps:

- `crypto:hash` — gates `stado_hash` and `stado_hmac`. One cap
  covers all algos because they're functionally equivalent
  (committing to one implementation either way).
- No cap for `stado_json_*` or compression — these are pure
  functions with no privileged side effect; gating them is
  ceremony.

### §C — Bundled wasm tool inventory

Every tool the model sees ships as a wasm plugin embedded in
`internal/bundledplugins/wasm/*.wasm` and registered at startup
unless `[tools.disabled]` says otherwise. Lazy instantiation —
the wasm module is only loaded into wazero on first tool call.

Inventory shipping with EP-0038:

| Plugin           | Tools                                                                | Notes |
|------------------|-----------------------------------------------------------------------|-------|
| `tools`          | `tools.search`, `tools.describe`, `tools.categories`, `tools.in_category` | Meta-tools; built in EP-0037, ported to wasm here |
| `fs`             | `fs.read`, `fs.write`, `fs.edit`, `fs.glob`, `fs.grep`             | Uses `stado_fs_*` |
| `shell`          | `shell.exec`, `shell.spawn`, `shell.bash`, `shell.zsh`, `shell.fish`, `shell.sh`, `shell.pwsh` | Uses `stado_proc_*`, `stado_terminal_*` |
| `web`            | `web.fetch`, `web.search`, `web.browse`                            | Uses `stado_http_client_*` |
| `http`           | `http.request`, `http.client_new`                                  | Lower-level; uses `stado_http_client_*` |
| `lsp`            | `lsp.definition`, `lsp.references`, `lsp.symbols`, `lsp.hover`     | Uses `stado_proc_*`; spawns `gopls` etc., speaks JSON-RPC in wasm |
| `rg`             | `rg.search`                                                         | Uses `stado_bundled_bin("ripgrep")` + `stado_exec` |
| `astgrep`        | `astgrep.search`                                                    | Uses `stado_bundled_bin("astgrep")` + `stado_exec` |
| `readctx`        | `readctx.read`                                                      | Pure compute over `stado_fs_read` |
| `task`           | `task.add`, `task.list`, `task.update`, `task.complete`             | Uses `stado_fs_*` against `cfg:state_dir/tasks` |
| `agent`          | `agent.spawn`, `agent.list`, `agent.read_messages`, `agent.send_message`, `agent.cancel` | Uses `stado_session_*`, `stado_llm_invoke`, fleet host imports (§D) |
| `mcp`            | `mcp.connect`, `mcp.list_tools`, `mcp.call`                         | Uses `stado_http_client_*` (HTTP transport) and `stado_proc_*` (stdio transport) |
| `image`          | `image.info`                                                        | Pure compute over `stado_fs_read`; recompile-only from existing example |
| `dns`            | `dns.resolve`, `dns.reverse`                                        | Uses `stado_dns_*` |
| `secrets`        | `secrets.get`, `secrets.list` (no set tool — secrets set via CLI only) | Uses `stado_secrets_*` |

Plus existing `auto-compact` (background plugin, unchanged) and
the five recompile-only existing examples (`web-search`,
`mcp-client`, `ls`, `image-info`, `browser`) absorbed into
appropriate plugin namespaces above.

### §D — Agent surface

Five tools. The **runtime is always async** (children execute in
goroutines under a Fleet registry; the parent's tool executor is
not blocked). The **default tool-call shape is sync** (the most
common call pattern is "give me the result"); `async: true` opts
into handle-based polling for parents driving multiple children
or doing other work meanwhile.

```
agent.spawn(prompt, model?, sandbox_profile?, async?, ephemeral?,
            parent_session?, allowed_tools?) -> {id, session_id, status}
agent.list() -> [{id, session_id, status, model, started_at,
                  last_turn_at, cost_so_far_usd}]
agent.read_messages(id, since?, timeout?) -> {messages, offset, status}
agent.send_message(id, msg) -> {ok}
agent.cancel(id) -> {ok}
```

#### Identity model: id, session_id, handle

Three closely-related but distinct values:

- **`id`**: the bare agent identifier (e.g. `bf3e92a4`). Used as
  the `id` argument to `agent.list/read_messages/send_message/
  cancel`. Plain hex string with no prefix. Stable for the
  lifetime of the agent.
- **`session_id`**: the bare session identifier the agent runs in.
  When an agent is spawned with `parent_session = null` (the
  default — see §D below), `session_id == id` (same value, two
  affordances: `id` for `agent.*` ops, `session_id` for
  `session.*` ops). When an agent is spawned with explicit
  `parent_session = "<some-session>"`, the agent runs in a forked
  child of that session and `session_id` is the child session's
  bare ID, distinct from `id`.
- **Handles in `/ps`, `/kill`, etc.** (per §G): typed-prefix forms
  like `agent:bf3e92a4` and `session:bf3e92a4`. The prefix is
  display-side; the bare ID is the API value. `agent.*` tool
  calls accept either `bf3e92a4` or `agent:bf3e92a4` as the `id`
  argument (the `agent:` prefix is stripped if present). They do
  NOT accept `session:bf3e92a4` — that's the audit channel via
  `session.read|observe`, not `agent.*`.

#### `parent_session` default and audit lineage

```
agent.spawn(prompt, ...)
  # default: parent_session = caller's current session
  # forks parent's session via session:fork; child has lineage
  # pointer to parent.

agent.spawn(prompt, parent_session = null)
  # explicit detached spawn. Child has no parent lineage; appears
  # as a top-level session driven by an agent.

agent.spawn(prompt, parent_session = "<other_session_id>")
  # explicit re-parenting (rare; for cron-driven agents that
  # should be lineage-pointed at a specific human-driven session).
```

Default `parent_session` = caller's session ID (not `null`).
This preserves the audit trail and ensures `agent.list` for the
caller surfaces the spawn. Codex review #14 / gemini review #14
both flagged the orphan-by-default risk; fixed.

#### Sync-default tool surface; async opt-in

```
# Block until completion, return final assistant output. Default.
result = agent.spawn("investigate X")

# Return immediately with handle; parent polls + reads.
handle = agent.spawn("investigate X", async=true)
while True:
    update = agent.read_messages(handle.id, since=last_offset, timeout_ms=5000)
    last_offset = update.offset
    if update.status in ("completed", "cancelled", "error"):
        break
```

The runtime always runs the child async (in a goroutine under the
Fleet registry; the parent's wasm tool executor returns control
to the host as soon as the child is spawned). `async: false` is
the model-facing default and means "the runtime polls the child
for me and returns the final assistant message"; `async: true`
returns immediately with a handle.

#### Every agent owns a session

`agent.spawn` returns `{id, session_id}`. By default (when
`parent_session` is the caller's session), these are the same
bare value; explicit `parent_session = <other>` makes them
different. See "Identity model" above for the full semantics.

The agent's run loop reads from its session's pending-message
queue. `agent.send_message` enqueues a user-role message into that
queue; the agent consumes at the next yield point (after current
assistant turn ends, never mid-tool-call).

This is the architectural collapse: **`agent.send_message(id, msg)`
is literally the same operation as `stado run --resume <session_id>`
with a user-role message.** The agent's loop doesn't differentiate
the source. Multi-producer is a property of any session's message
queue.

#### Default model: inherits parent's

Conservative default — surprise-minimum. A Sonnet parent spawning
without an explicit `model:` arg gets Sonnet children. Operator
opts out via `agent.spawn(..., model="haiku-4.5")`.

#### `agent.list` scope: caller's spawn tree

A plugin that spawns child agents sees its own descendants in
`agent.list()`. It does not see siblings or unrelated agents in
the runtime. Operator who wants global view uses CLI:
`stado session ls --driver agent` (via §F's session metadata
extension).

#### Recursive spawning

Allowed; capped via `[agents] max_depth = 5` (default). Lineage
recorded in session metadata: parent_session pointer at child
creation.

#### `ephemeral: true` opt-out

Skips session persistence. Agent's session record is deleted on
agent completion. No replay, no `usage/stats` line for that agent's
tokens (rolled up to parent). Default `false` — sessions persist.

### §E — Multi-producer message metadata

Every message in any session carries source metadata:

```
source = "human:<operator_id>"
       | "agent:<parent_session_id>"
       | "cron:<routine_id>"
       | "bridge:<plugin_name>"
       | "api:<caller>"
```

Renderer marks human-typed messages distinctly:

```
[parent:bf3e]   What's the SHA of /tmp/x?
[child]         SHA-256: abc123...
[parent:bf3e]   OK, verify it matches the manifest.
[child]         Checking...
[YOU]           Wait — verify the GPG signature first.
[child]         Verifying GPG signature... matched.
```

`[YOU]` is the operator-typed message visually distinct.

#### `agent.read_messages` returns assistant content + external-input markers

The convenience channel is **not** "assistant-role messages only"
(an earlier draft of this EP said so; codex review #7 surfaced
that this loses critical signal — the parent might see a
behavioural change without seeing the human prompt that caused
it, breaking coordination, replay, and audit-driven reasoning).

What `agent.read_messages` actually returns:

- All **assistant-role** messages (the child's curated outputs),
  in full.
- All **external-input markers** for non-self user-role messages
  in the inbox: a small structured event recording `{source,
  offset, summary}` for each user-role message whose `source` is
  not `agent:<this_caller_id>`. The marker does NOT include the
  message body — that requires `session.read|observe` with the
  audit-channel cap. The marker tells the parent "something
  external came in here; you may want to look."
- Tool-call results NOT included (those are mid-step; only
  surfaced via `session.observe`).

Example output:

```json
{
  "messages": [
    {"role": "assistant", "content": "Checking..."},
    {"event": "external_input", "source": "human:operator",
     "offset": 5, "summary": "user message, 47 chars"},
    {"role": "assistant", "content": "Verifying GPG signature..."}
  ],
  "offset": 7,
  "status": "running"
}
```

This gives the parent a tractable audit-aware view: it sees the
child's outputs and knows when an external producer steered the
conversation, without leaking message bodies the parent might
not be entitled to read. A parent that wants the bodies declares
`session:read:<child_id>` (or `session:observe:<child_id>`) and
calls `session.read` directly.

#### Cost attribution

Tokens consumed processing any message — whether typed by human,
sent by parent, or injected by cron — accrue to the **session's
owner agent** (the agent that holds the session, or the human
session's user). The producer just put the message in the queue;
the consumer paid for handling it.

#### Replay

`stado session replay <id>` re-runs an agent's session.
**Default: full fidelity replay including all messages from every
source** (`human:*`, `agent:*`, `cron:*`, `bridge:*`, `api:*`).
The historical run is reproduced as it actually happened.

Codex review #7 / gemini review #4 both surfaced that the earlier
draft's "skip human messages by default" loses replay fidelity
when human input was load-bearing (corrected a hallucination,
provided a hint the agent depended on). The conservative default
is full reproduction.

`--omit-source human` (or `--omit-source bridge`, etc.) opts into
filtered replay where the operator wants to test "what if the
human hadn't intervened." This is the rare case and should be
explicit.

### §F — `/session attach` (read-write) + slash commands

EP-0037 ships read-only `/session show` and `/session attach
--read-only`. EP-0038 adds the read-write attach plus
the runtime-only slash commands.

```
/session attach <id>                 # interactive, RW default
/session attach <id> --read-only     # observation only (already in EP-0037)
/session attach <id> --pause-parent  # opt-in pause of parent agent's polling
/session detach                      # return to original session
/session inject <id> <message>       # one-shot inject without focus switch
```

While attached, the operator's typed input becomes a user-role
message with `source: human:<operator>`. Same producer-metadata
as everywhere else. Detach with `Ctrl-D` or `/session detach`
returns to the original session; the agent's session continues
running.

#### Conflict behaviour

- Parent agent calls `agent.send_message` while human attached:
  messages serialize on the inbox queue in arrival order. Both
  go to the child.
- Parent calls `agent.cancel` while human attached: cancel wins,
  child terminates, attached human view shows
  `session cancelled by parent agent`.
- Human types `/session detach` mid-conversation: their last
  message stays in the inbox if not yet consumed; child eventually
  picks it up; parent's polling resumes seeing those responses.
- Human attaches to their own active session: refused with
  `cannot attach to your own active session`.
- Recursive attach (operator attaches to grandchild while parent
  also attached to child): allowed. TUI shows breadcrumb
  (`session:root → agent:abc → agent:def → [YOU]`).

#### `--pause-parent` flag

When the operator wants exclusive control over the child while
attached, `--pause-parent` adds a `paused-by-attach` status to the
parent's view of the child. Parent's polling continues to return
status updates; just no new `agent.send_message` calls land. Resumes
on detach.

### §G — Handle ID convention

Every spawned thing (plugin instance, proc, terminal, agent,
session, network connection) gets a typed, dotted ID:

```
plugin:fs                   # the fs plugin's wasm instance
proc:fs.7a2b9c1d            # a proc handle owned by fs, instance 7a2b9c1d
term:shell.9c1da3e7         # terminal handle owned by shell
agent:bf3e92a4              # agent
session:bf3e92a4            # the agent's session — same bare ID as agent: when default-spawned
conn:web.4f5a8c1b           # net handle
listen:browser.8a91d2f3     # listen handle
```

Type-prefix mandatory (avoids namespace collisions across handle
families). Dotted plugin-context makes `/ps` output legible
without extra columns.

#### ID format and collision resistance

Tail is **8 hex chars (32 bits)** of cryptographic-random value,
not 16-bit. Gemini review #16 surfaced that 4-hex (16-bit) tails
collide on the order of 2^16 ≈ 65k handles in a long-running
process — feasible for any session that does meaningful agent
work. 8-hex (32-bit) collides at ~2^32 ≈ 4B which is well past
realistic working-set sizes. The host registry also runs a
**collision check at generation time**: if the fresh ID is
already in use (vanishingly rare but not impossible), the host
re-rolls.

Handles are stable for the lifetime of the underlying object;
operator-visible (in `/ps`, `/kill`, slash command results,
plugin log lines).

#### Agent and session ID relationship

Agents and the sessions they own share the bare ID portion when
spawned with the default `parent_session = caller's session`
(per §D Identity model). Same value, displayed under two
typed prefixes:

- `agent:bf3e92a4` — addressable via `agent.*` operations.
- `session:bf3e92a4` — addressable via `session.*` operations.

When `parent_session` is explicitly specified to a different
session, `agent_id` and `session_id` are different values; the
agent's session has its own bare ID forked from the requested
parent.

`/ps` shows the relationship via the tree structure, not by
duplicating the value:

```
agent:bf3e92a4                 busy
  session:bf3e92a4    driver   # same bare ID, different prefix
```

### §H — Runtime introspection

Slash commands that read or affect runtime state (no CLI mirror —
they need access to live state in the same process):

```
/stats                    # tokens used, cost, session/agent count, uptime
/ps                       # tree of live wasm/proc/term/net/agent/session handles
/top                      # live-updating /ps (Ctrl-C exits)
/kill <id>                # terminate handle or plugin instance
/agents                   # /sessions --driver=agent shorthand
/sandbox                  # current sandbox state (mode, proxy, env, namespace)
/config [section]         # effective config (merged) with source attribution
```

`/ps` example:

```
ID                       STATE    OWNER          CPU%  MEM     AGE
plugin:fs                idle     -              0.0   2.1MB   12m
plugin:web               busy     -              0.4   8.4MB   6m
  conn:web.4f5a8c1b      active   plugin:web     -     -       3s
plugin:shell             idle     -              0.0   1.8MB   12m
  proc:shell.7a2b9c1d    waiting  plugin:shell   0.0   12MB    45s
  term:shell.9c1da3e7    active   plugin:shell   0.1   8MB     2m
agent:bf3e92a4           busy     plugin:agent   0.0   -       1m
  session:bf3e92a4       driver   agent:bf3e92a4 -     -       1m
```

Tree view; child handles indented under their owner. `--flat`
flag for list view.

`/kill plugin:fs` cascades to all child handles owned by the
instance. `/kill proc:fs.7a2b9c1d` kills one specific handle.
`/kill agent:bf3e92a4` cancels an agent (same as `agent.cancel`).

### §I — Sandbox implementation

EP-0037 reserved the `[sandbox]` schema; EP-0038 implements it.

#### `mode = "off"` (default — yolo per philosophy)

Stado runs unwrapped. In-process settings still apply:
`http_proxy`, `dns_servers`, `allow_env`, `plugin_runtime_caps_drop`.
External wrapping (operator runs `bwrap stado run`) is supported
and recommended for any "actually contain this" use case but not
forced.

#### `mode = "wrap"`

On startup, stado detects wrapper availability (`bwrap` on Linux,
`firejail` on Linux as backup, `sandbox-exec` on macOS), constructs
a wrapper invocation from `[sandbox.wrap]` config, and re-execs
itself under that wrapper:

```bash
# What the operator sees
$ stado run

# What stado actually runs internally (mode=wrap, full default form)
$ bwrap \
    --die-with-parent \
    --new-session \
    --proc /proc --dev /dev \
    --bind /usr /usr --ro-bind /etc/resolv.conf /etc/resolv.conf \
    --bind ~/Dokumenty/htb-writeups /work \
    --bind ~/.local/share/stado ~/.local/share/stado \
    --bind ~/.config/stado ~/.config/stado \
    --bind ~/.cache/stado ~/.cache/stado \
    --tmpfs /tmp \
    --setenv PATH /usr/bin --setenv HOME /home/op --setenv TERM "$TERM" \
    --unshare-net \
    -- /usr/bin/stado run --no-rewrap
```

The `--no-rewrap` internal flag prevents infinite recursion; the
re-execed instance sees `mode = "off"` for the wrapper-detection
purpose but applies remaining settings.

`--die-with-parent` (gemini review #12) is mandatory — without it
sandbox processes orphan when stado is killed. `--new-session`
prevents signal propagation collisions; the re-execed stado
forwards signals it receives to its own children explicitly.

##### Default binds (per-mode contract)

Codex review #11 surfaced that the EP didn't specify what's bound
where; "auto-bound RW" was vague. The full contract for
`mode = "wrap"`:

| Path                           | Default bind | Why                                                        |
|--------------------------------|--------------|------------------------------------------------------------|
| `$XDG_DATA_HOME/stado`         | RW           | stado state — sessions, plugins, audit, secrets fallback   |
| `$XDG_CONFIG_HOME/stado`       | RW           | stado config — user-level + project-level via discovery    |
| `$XDG_CACHE_HOME/stado`        | RW           | plugin tarball cache, anchor cache, ripgrep extract dir    |
| `/usr`                         | RO           | system binaries the wasm runtime relies on (gopls etc.)    |
| `/etc/resolv.conf`             | RO           | DNS resolution (when `[sandbox] dns_servers` not set)      |
| `/etc/ssl/certs`               | RO           | TLS root CAs                                                |
| `/proc`, `/dev`                | (mounted)    | needed by wasm runtime + child processes                   |
| `/tmp`                         | tmpfs        | scratch — discarded on stado exit                          |
| operator's CWD                 | (NOT bound)  | operator must `bind_rw` explicitly for the workdir         |
| `$HOME/.ssh`, `$HOME/.gnupg`   | (NOT bound)  | credentials never auto-bound; explicit `bind_ro` required  |
| Provider credentials (`~/.config/anthropic`, `~/.aws`, etc.) | (NOT bound) | ditto |

The operator's CWD is **not auto-bound**. This is intentional —
the operator declares which workdirs the wrapped run can see via
`bind_rw`. A common project-level pattern:

```toml
# .stado/config.toml in a project repo
[sandbox]
mode = "wrap"
[sandbox.wrap]
bind_rw = [".", "~/Dokumenty/htb-writeups"]
```

Credentials (SSH keys, GPG keys, provider tokens) are NEVER
auto-bound. Operator who wants the wrapped run to use them
binds explicitly:

```toml
[sandbox.wrap]
bind_ro = ["~/.ssh"]
```

##### `[sandbox.wrap]` keys

- `runner = "auto" | "bwrap" | "firejail" | "sandbox-exec"`
- `bind_ro = []` — paths read-only-mounted into the sandbox
  (ADDITIVE on top of defaults; can't remove defaults via
  config — stado state/config/cache binds are mandatory).
- `bind_rw = []` — paths read-write-mounted (additive).
- `network = "host" | "namespaced" | "off"` — host network /
  isolated netns (requires explicit bind to a network interface
  or proxy) / no network at all.
- `signal_propagation = "auto"` — currently the only value.
  stado wraps its sandbox child with proper signal forwarding
  (SIGINT/SIGTERM/SIGHUP forwarded; SIGKILL handled by
  `--die-with-parent`).

##### Detection failure

If `mode = "wrap"` is set but no wrapper detected, stado refuses
to start with a clear error:

```
Sandbox mode 'wrap' configured but no wrapper detected.
Install bwrap (apt install bubblewrap / dnf install bubblewrap)
or set [sandbox] mode = "off" / "external".
```

#### `mode = "external"`

Stado refuses to start unless it detects it's already running
inside a wrapper (heuristics: presence of `BWRAP_*` env vars,
unshare namespaces, etc.). Used by operators who want to enforce
that stado is always wrapped externally.

#### In-process settings (apply to all modes)

```
[sandbox]
http_proxy = "http://127.0.0.1:8080"
```

Forces `HTTP_PROXY` / `HTTPS_PROXY` env vars into the plugin
runtime. `stado_http_client_*` honours these by default;
`stado_net_dial` doesn't (operator who routes raw TCP through
a proxy uses `[sandbox.wrap]` network controls).

```
[sandbox]
dns_servers = ["1.1.1.1", "9.9.9.9"]
```

`stado_dns_resolve` uses these instead of system resolv.conf.

```
[sandbox]
allow_env = ["PATH", "HOME", "TERM"]
```

Strict allow-list; only listed env vars pass to plugin runtime.

**`allow_env` default differs by sandbox mode**:

| Mode              | Default `allow_env` semantics                              |
|-------------------|-------------------------------------------------------------|
| `mode = "off"`    | Empty list = pass-through (current shell env).              |
| `mode = "wrap"`   | Empty list = strict minimal set (`PATH`, `HOME`, `TERM`, `LANG`, `LC_*`, `USER`). Operator widens by listing more keys. |
| `mode = "external"` | Empty list = no env injection beyond what the external wrapper provides; operator's wrapper is responsible. |

Codex review #11 surfaced that pass-through-by-default in wrap
mode is unsafe. The mode-specific semantics keep `mode = "off"`
operationally compatible (no env surprises) while making
`mode = "wrap"` actually opt-in for env exposure.

```
[sandbox]
plugin_runtime_caps_drop = ["cap_net_raw", "cap_dac_override"]
plugin_runtime_caps_add = []
```

Linux capabilities to add/drop **on the wrapper-spawned stado
re-exec** before wazero is initialised. Codex review #12
correctly noted that wazero runs in-process, so cap manipulation
applies process-wide; this works because `mode = "wrap"` does the
re-exec under bwrap, and the re-execed stado applies caps
*before* wazero starts. **`plugin_runtime_caps_*` are only
honoured in `mode = "wrap"` (or `"external"` if the wrapper
applies them externally).** In `mode = "off"`, these settings
are accepted but ignored with a warning at startup —
manipulating caps on an arbitrary unwrapped stado process from
the inside isn't reliable.

`add` is restricted to caps the parent process already has in
its permitted set (otherwise the operation fails per Linux
semantics); typically empty. `drop` is the common case
(remove caps the parent shell happens to have).

#### Named profiles via `--sandbox <name>`

```toml
[sandbox.profiles.htb]
mode = "wrap"
http_proxy = "http://127.0.0.1:8080"
[sandbox.profiles.htb.wrap]
network = "namespaced"
bind_rw = ["~/Dokumenty/htb-writeups"]
```

```bash
stado run --sandbox htb
```

CLI `--sandbox <name>` applies the named profile; CLI `--sandbox`
(no name) applies root `[sandbox]`. CLI presence wins over config-
default behaviour. Profile config wins over root `[sandbox]` when
both are referenced.

### §J — EP-0028 supersession (--with-tool-host + bash refusal)

`--with-tool-host` flag retired. The flag existed because EP-0028
needed an opt-in to bridge the bundled-tool host imports to plugin
invocations from the CLI. With EP-0038, every tool is a wasm
plugin; there is no "bundled tool" anymore as distinct from "plugin
tool." `stado plugin run <name> <tool>` always wires the tool host;
the flag becomes the default and the CLI accepts but ignores it
for compatibility (with deprecation warning). One release cycle
later: removed.

EP-0028 D1 (refuse `exec:bash` when no sandbox runner) becomes
**warn-loud-run-anyway** by default per the philosophy. The
operator chose to install a plugin declaring `exec:proc` (the
new spelling); on a host without a sandbox runner, the
default behaviour is:

```
WARNING: plugin 'shell' declares exec:proc but no syscall filter
is available on this host (NoneRunner). Plugin will run unsandboxed.

To enforce sandboxing: install bwrap (Linux) / sandbox-exec (macOS)
                      or set [sandbox] mode to "wrap" / "external"
To suppress this warning: set [sandbox] warn_no_runner = false
To refuse instead: set [sandbox] refuse_no_runner = true
```

Three knobs in `[sandbox]`:

- `warn_no_runner` — bool, default `true` (warn loudly).
- `refuse_no_runner` — bool, default `false` (run anyway).
- Setting `refuse_no_runner = true` restores EP-0028's hard refusal
  semantics for operators who want the EP-0005-strict behaviour.

This is the philosophy in action: stado describes the situation;
operator decides. The default favours running (the user can name
the tradeoff themselves) over refusing (the previous default).

### §K — `internal/host/*` migration

Moves `internal/tools/*` to `internal/host/*` with structure
organised by primitive rather than by tool:

```
internal/host/
  exec.go         # stado_proc_*, stado_exec; sandbox.Runner integration
  terminal.go     # stado_terminal_* (PTY internals)
  fs.go           # stado_fs_* (kept; existing implementation)
  net.go          # stado_net_dial / listen / read / write / close
  net_icmp.go     # stado_net_icmp_*
  bundled_bin.go  # stado_bundled_bin; owns embedded ripgrep/ast-grep
  http_client.go  # stado_http_client_*
  dns.go          # stado_dns_*
  secrets.go      # stado_secrets_*
  json.go         # stado_json_*
  hash.go         # stado_hash, stado_hmac
  compression.go  # stado_compress, stado_decompress
  session.go      # stado_session_* (existing)
  llm.go          # stado_llm_invoke (existing)
  log.go          # stado_log (existing)
  approval.go     # stado_approval_request (existing)
```

The Go code from `internal/tools/{fs,bash,webfetch,httpreq,
rg,astgrep,lspfind,readctx,tasktool,subagent}` survives — much of
it becomes the host-import implementation under the new `internal/
host/*` paths. Some becomes obsolete (the `tool.Tool` interface
implementations themselves, the schema metadata that's now in the
wasm plugin's manifest).

`internal/runtime/bundled_plugin_tools.go` is **deleted entirely**.
Its replacement is the bundled-plugin loader from
`internal/bundledplugins/`, which already exists for `auto-compact`
and gets extended to load every default tool plugin from
`internal/bundledplugins/wasm/*.wasm`.

## Migration / rollout

Pre-1.0; user is the sole operator; temporary instability accepted.
**Even so, the migration is gated by per-tool parity checks
rather than landed wholesale.** Codex review #13 surfaced that
"one large refactor PR set" risks losing native fallback before
parity is proven. The actual rollout is staged behind per-tool
feature flags:

- For each native tool currently registered in
  `internal/runtime/bundled_plugin_tools.go`, the EP-0038
  rollout adds a wasm equivalent AND a parity flag
  `[runtime.use_wasm.<tool>]` defaulting to `false`.
- A **golden parity test** for each tool replays a curated set of
  representative inputs against both implementations and asserts
  identical observable outputs (tool result JSON, stdout, error
  shapes). Test fixtures live in
  `internal/runtime/parity-fixtures/<tool>/`.
- A tool's parity flag flips to `true` *only* when its golden
  parity test passes. The native implementation stays in the
  binary as fallback for one full release after the flag flips.
- After all flags are flipped + one release of bake-in, the
  native implementations and the `bundled_plugin_tools.go`
  registrations get deleted (the deletion is the closing PR of
  the migration, not the opening one).

The phase order below is the **dependency order** for adding
capabilities; the parity-flag flip happens within each phase
once the wasm tool exists. A phase is "done" when:

1. Wasm plugin compiles and is embedded.
2. Golden parity test passes against the native equivalent.
3. The flag has been flipped to `true` and exercised in dev.

EP-0038 phase plan:

1. **Add Tier 1 host imports.** `stado_proc_*`, `stado_terminal_*`,
   `stado_net_*`, `stado_net_icmp_*`, `stado_bundled_bin`. Each
   implementation lives in `internal/host/*`. ABI manifest version
   bumps to v2; v1 plugins continue working against the v1-shaped
   imports (those are kept as aliases for one release).
2. **Add Tier 2 host imports.** `stado_http_client_*`, `stado_dns_*`,
   `stado_secrets_*`. Same shape.
3. **Add Tier 3 host imports.** `stado_json_*`, `stado_hash`,
   `stado_hmac`, `stado_compress`, `stado_decompress`.
4. **Write bundled wasm plugins.** Each native tool gets a wasm
   reimplementation that calls Tier 1/2/3 imports. The five
   existing example plugins (`web-search`, `mcp-client`, `ls`,
   `image-info`, `browser`) recompile against the new SDK.
5. **Embed wasm plugins** in `internal/bundledplugins/wasm/`. Extend
   `internal/bundledplugins/embed.go` to enumerate them; loader
   registers each on startup unless `[tools.disabled]` says
   otherwise.
6. **Delete `bundled_plugin_tools.go`'s registrations.** Delete
   `newBundledPluginTool`. Delete `internal/tools/*` directories
   that don't have host-import surviving content (or move them
   under `internal/host/*`).
7. **Wire agent surface.** `agent.spawn`/`list`/`read_messages`/
   `send_message`/`cancel` shipping as a wasm plugin. Backed by
   a `Fleet`-shaped runtime registry (per EP-0034's design,
   adapted) plus session-per-agent forking via existing
   `session:fork` machinery.
8. **Wire `[sandbox]` implementation.** `mode = "wrap"` re-exec
   logic. `allow_env` filtering. `plugin_runtime_caps_*`. Proxy
   environment injection.
9. **Wire `/ps`, `/top`, `/kill`, `/stats`, `/sandbox`,
   `/config`** — handle ID convention in place; runtime
   introspection slash commands.
10. **Wire `/session attach` (RW)** — multi-producer message
    metadata, renderer changes, `[YOU]` marker.
11. **EP-0028 deprecation.** `--with-tool-host` becomes default;
    `refuse_no_runner` flag added; D1's hard refusal opt-in.
12. **EP-0014 amendment.** "Active-session-only execution" relaxed
    for async agents owning their own sessions. The TUI's
    "active session" now means "session whose transcript is in the
    foreground viewport" — agent-driven sessions can stream and
    run tools in the background.
13. **Documentation pass.** README, DESIGN.md, PLAN.md, SECURITY.md,
    every `docs/commands/*.md` file referenced from EPs being
    superseded or amended.

### Backward compatibility

- ABI v1 plugins continue working against the v1-shaped imports
  for one release after EP-0038 lands; v1 imports are aliases for
  the v2 ones where semantics match. After one release, v1 imports
  are removed and v1 plugins must be recompiled.
- `--with-tool-host` accepted with deprecation warning for one
  release; removed after.
- All retired internal tool registrations (`fs.ReadTool{}` etc.)
  are gone; the model sees identically-named wasm-backed tools.
  No model-visible regression.
- Plugin manifests declaring `exec:bash` continue to work as
  before; the cap is aliased to `exec:proc` for one release.

## Failure modes

- **Wasm plugin crashes mid-call.** Wazero traps; host returns
  structured error to caller; plugin instance discarded; next
  call re-instantiates. Operator sees a `[ERROR] plugin:fs panic`
  log entry; tool result is the structured error, not a stado
  crash.
- **Plugin handle table fills up.** Each plugin instance has a
  per-instance handle limit (default 256 across all handle types).
  Exceeding it returns `EMFILE`-shape error to the plugin.
- **`stado_proc_spawn` against a missing binary.** Returns
  `errno=ENOENT` to the plugin. Plugin's responsibility to handle
  cleanly.
- **`stado_net_listen("tcp", ":443")` without root.** Kernel
  refuses bind; host returns `EACCES`. Operator sees no
  stado-side refusal — that's the OS-overlap rule.
- **`stado_net_icmp_open` without CAP_NET_RAW.** Returns
  `errno=EPERM`. Plugin reports upward. Stado does not silently
  fall back to shell-out `ping(1)`.
- **Agent's session inbox grows unbounded.** Per-session inbox
  caps at 256 unconsumed messages; further `send_message` calls
  return `{ok: false, reason: "inbox_full"}`. Backpressure to
  the producer.
- **Sandbox mode = "wrap" + bwrap missing.** Stado refuses to
  start (per §I); clear error pointing at install instructions.
  Same for "external" mode without detected wrapper context.
- **Recursive `agent.spawn` exceeds `max_depth`.** Innermost
  `spawn` call returns error `agent_max_depth_exceeded`;
  caller's responsibility to handle.
- **Two plugins both register a tool with colliding wire form.**
  Second registration fails at startup with a clear error
  (per EP-0037 failure modes); first registration wins; second
  plugin's other tools register normally.
- **Operator runs `/kill plugin:fs` while another plugin holds a
  handle owned by `plugin:fs`.** Holders get a structured error
  on next read/write attempt (`handle_owner_terminated`); they
  surface the error upward.

## Test strategy

- **Per-import unit tests** for every Tier 1/2/3 import: happy
  path, capability-not-declared, malformed args, lifetime cleanup
  on plugin instance termination.
- **Bundled-plugin smoke tests:** each shipped wasm plugin gets
  invoked through stado plugin run with a fixture scenario,
  asserting tool output matches the previous native
  implementation's expected output.
- **Agent surface tests:**
  - `agent.spawn` sync/async return shape.
  - `agent.read_messages` filtering to assistant-role only.
  - `agent.send_message` queue ordering with concurrent producers.
  - `agent.cancel` race with completion.
  - Session lineage in metadata after recursive spawn.
- **Multi-producer renderer tests:** typing into an attached
  session produces `[YOU]`-marked messages; parent agent's
  `read_messages` doesn't see them; `session.observe` does.
- **Sandbox mode tests:**
  - `mode = "off"` settings (env, proxy, dns) apply correctly.
  - `mode = "wrap"` re-exec under bwrap with bind / network
    options.
  - `mode = "external"` refuses to start without detected
    wrapper.
  - `--sandbox <profile>` overrides root `[sandbox]`.
  - `refuse_no_runner` restores EP-0028 D1 hard-refusal.
- **Handle ID convention tests:** unique IDs per type, lifetime
  scoped to plugin instance, `/kill` cascades correctly.
- **Runtime introspection tests:** `/ps` output format, `/top`
  refresh, `/stats` accuracy.
- **Migration tests:** ABI v1 plugin compiles + runs against v2
  host (alias period); same plugin recompiled against v2 SDK
  produces identical tool output.

## Open questions

- **PTY actually used?** No bundled plugin in EP-0038's inventory
  needs `stado_terminal_*` directly; `shell.spawn` uses it
  internally. Open question whether to ship Tier 1 PTY surface in
  v1 of ABI v2 or defer until concrete consumer lands. Position:
  ship now — `shell.spawn` and an eventual `ssh-session` plugin
  are real consumers; deferring forces a later ABI bump.
- **Process-level recovery for crashed plugins.** Today: instance
  discarded, next call re-instantiates. Open question: should the
  host preserve in-flight handles (e.g., the LSP server's stdio
  pipes) across plugin instance restart? Position: no for v1.
  Each plugin instance owns its handles fully. The optimisation
  case is a "warm LSP across plugin reload" scenario that can
  be its own follow-up EP.
- **Streaming HTTP read shape.** `stado_http_request_streaming`
  returns a handle the plugin reads from. Open question whether
  the read shape is `stado_net_read(handle, ...)` (reuse) or a
  dedicated `stado_http_response_read`. Position: reuse; the
  handle is opaque to the plugin and `stado_net_read` semantics
  fit (max bytes, timeout). Deferred to implementation if the
  read shape needs HTTP-specific behaviour.
- **`agent.read_messages` — buffering vs. live tail.** Today's
  design: drain inbox up to `since` offset. Open question:
  should the call optionally tail (block until something new)
  rather than poll? Position: yes via `timeout > 0` semantics
  (already specified). Confirmed.
- **`/session attach --pause-parent` lock duration.** While the
  operator is attached with --pause-parent, what if they Ctrl-Z
  the TUI? Position: pause persists; resume on detach (which is
  unblocked even if the TUI is suspended). Operator can detach
  via CLI: `stado session detach <id>` — future scope, not v1.
- **JSON dialect commitment.** Strict RFC 8259 — no comments, no
  trailing commas, no NaN/Inf. Confirmed in the philosophy
  decision; documented here for the manifest contract.

## Decision log

### D1. Restore EP-0002's invariant by deletion, not workaround

- **Decided:** `internal/runtime/bundled_plugin_tools.go`'s
  `r.Register(NativeTool{})` calls all delete; `newBundledPluginTool`
  wrapper deletes; the existing native implementations move to
  host-import implementation under `internal/host/*` and become
  invisible to the registered-tool surface.
- **Alternatives:** keep the wrapper layer + add a
  "bundled-tool-as-wasm" mode that progressively replaces
  individual tools; document the drift as the "current shape" and
  call EP-0002 aspirational; introduce a third invariant
  ("hybrid-bundled-tools").
- **Why:** deleting and rewriting is simpler than progressive
  migration. Pre-1.0; the user is the only operator; temporary
  instability is acceptable. Hybrid models calcify and the
  invariant doesn't hold. Single-cycle deletion + rewrite restores
  EP-0002 cleanly.

### D2. Generic primitive surface, not program-specific imports

- **Decided:** `stado_proc_*`, `stado_terminal_*`, `stado_net_*`,
  `stado_bundled_bin` — these are the Tier 1 capabilities. No
  `stado_ripgrep`, `stado_lsp_definition`, `stado_bash_exec`, or
  any other program-specific imports.
- **Alternatives:** add 8+ program-specific imports (one per
  current native tool); generic primitives PLUS convenience
  imports per major use case.
- **Why:** the host should expose what the wasi sandbox denies,
  not a catalogue of tool implementations. Application logic —
  including program-specific argument shaping, output parsing,
  protocol semantics — belongs in wasm. Generic primitives keep
  the host-side ABI flat (~20 functions vs. an open-ended growing
  list) and the wasm plugins owns the variation. EP-0037 D1
  (philosophy) makes the same argument from the operator side.

### D3. Tier 2 conveniences earn their place by state + size

- **Decided:** `stado_http_client_*`, `stado_dns_*`,
  `stado_secrets_*` ship as Tier 2 conveniences. Each holds
  state worth sharing (connection pool / cookie jar; resolver
  cache; OS keychain handle) AND has high enough wasm
  reimplementation cost (TLS+HTTP, DNS protocol, OS keychain
  bindings) that per-plugin duplication is real binary-size cost.
- **Alternatives:** force everything through Tier 1 primitives
  (every plugin ships its TLS stack); add more Tier 2
  conveniences (regex, time, URL parsing).
- **Why:** the convenience criterion is genuinely two-pronged.
  Pure functions with cheap wasm implementations stay in wasm;
  stateful + costly conveniences come to the host. JSON / hash /
  compression are Tier 3 (pure but costly); HTTP / DNS / secrets
  are Tier 2 (stateful + costly). YAML / TOML / regex / URL
  excluded — failing one prong each.

### D4. Strict RFC 8259 JSON

- **Decided:** `stado_json_*` accepts and emits strict RFC 8259
  only. No comments, no trailing commas, no NaN/Inf, no
  duplicate-key tolerance.
- **Alternatives:** JSON5 dialect; JSON-with-comments (JSONC);
  per-call `lenient` flag.
- **Why:** the host commits to one canonical implementation
  forever. Plugin authors who need a permissive parser ship
  their own (the cost of permissiveness is theirs to bear).
  RFC 8259 is unambiguous; alternatives are not.

### D5. Hash algos: md5, sha1, sha256, sha512, blake3

- **Decided:** five algos, including legacy md5 and sha1.
- **Alternatives:** sha256+ only (refuse legacy); operator
  config to enable legacy hashes.
- **Why:** the user's HTB workflows specifically need md5 and
  sha1 for legacy hash cracking, NTLM-related work, and CTF
  recipe replication. Refusing them on cryptographic-hygiene
  grounds doesn't fit the philosophy (stado describes; operator
  decides). The plugin author who only wants sha256+ doesn't
  declare md5/sha1 use; the cap (`crypto:hash`) covers all algos.

### D6. ICMP raw, no `_ping` convenience

- **Decided:** `stado_net_icmp_*` exposes raw ICMP (open/send/recv/
  close) keyed by ICMP type+code. No high-level `_ping` function.
- **Alternatives:** `stado_net_icmp_ping(host, count, timeout)` as
  a high-level convenience; per-type imports
  (`stado_net_icmp_echo`, `stado_net_icmp_ttl_exceeded`).
- **Why:** ICMP has many message types (echo 8/0, dest-unreachable
  3, ttl-exceeded 11, redirect 5, timestamp 13/14, etc.). Locking
  the host to `_ping` (echo only) means re-adding `_traceroute`
  (TTL manipulation), `_redirect`, etc. forever. Raw + plugin-side
  composition keeps the host surface flat and the application
  logic correct. Plugins compose: ping = echo+recv-with-id-match;
  traceroute = echo+ttl-incremented+recv-ttl-exceeded; etc.

### D7. Agent surface: 5 tools, async-by-default, sync sugar

- **Decided:** `agent.spawn`, `agent.list`, `agent.read_messages`,
  `agent.send_message`, `agent.cancel`. `agent.spawn(async=false)`
  default blocks; `async=true` returns handle.
- **Alternatives:** sync-only spawn (current EP-0013 behaviour);
  async-only with no sync sugar.
- **Why:** sync-only forces parents to block; async-only forces
  every parent to write a polling loop. Sync default is what
  most callers want; async opt-out is for the cases where the
  parent has work to do meanwhile. Sync is internally `spawn-async
  + read-until-done`; one underlying mechanism, two ergonomic
  shapes.

### D8. Every agent owns a session

- **Decided:** `agent.spawn` returns `{id, session_id}`. Same
  value, two affordances. Agent's session is observable via
  `session:read|observe` cap; addressable via session.* APIs.
- **Alternatives:** agents are ephemeral runtime objects only;
  agents store transcripts to a per-runtime fleet store
  separate from sessions.
- **Why:** sessions are already the conversation primitive
  (EP-0004 git-native; `session:fork` cap; `auto-compact`
  uses them). Unifying agents and sessions collapses the
  architecture: `agent.send_message` is just typing into a
  session; multi-producer is a property of the queue;
  observability falls out of `session:observe`. Audit trail
  for free.

### D9. Multi-producer message metadata; `[YOU]` renderer marker

- **Decided:** every message in any session carries a `source`
  field: `human:* | agent:* | cron:* | bridge:* | api:*`.
  Renderer marks human-typed messages distinctly. Replay skips
  human-source messages by default.
- **Alternatives:** message log is single-producer (all messages
  attribute to the session's owner); separate logs per producer.
- **Why:** the architecture allows multiple producers (parent
  agent + human supervisor + cron); ignoring that fact in the log
  loses the most useful audit data. `[YOU]` marker is the
  visualisation; replay-skip-human is the intuitive default
  (re-running an agent shouldn't reproduce one-off human
  redirections). `--include-human-injections` for fidelity.

### D10. EP-0028 D1 reversal: warn, don't refuse, by default

- **Decided:** when `exec:proc` is declared and no sandbox runner
  is available, default behaviour is warn-loud-run-anyway. Hard
  refusal opt-in via `[sandbox] refuse_no_runner = true`.
- **Alternatives:** keep EP-0028 D1 hard refusal; refuse silently;
  prompt operator at runtime.
- **Why:** EP-0037 §A philosophy makes the call. Stado describes;
  operator decides. The previous default (refuse with hint)
  presumed stado's job is to enforce on top of the operator's
  judgment; the new default surfaces the situation and lets the
  operator commit. `refuse_no_runner = true` restores the old
  behaviour for operators who want it.

### D11. Sandbox modes: off, wrap, external

- **Decided:** three modes. Default `off`. `wrap` re-execs under
  detected wrapper. `external` refuses to start unless wrapper
  context is detected.
- **Alternatives:** `wrap` only (force wrapper); `wrap` mandatory
  by default with `unsafe` opt-out; mode per-plugin rather than
  per-runtime.
- **Why:** `off` matches the philosophy default. `wrap` is the
  declarative path for operators who want stado to do the
  wrapping. `external` is the assertion-only mode for operators
  with their own wrapper toolchain. Per-plugin sandbox is more
  conceptually elegant but operationally complex (re-exec per
  plugin) — defer to a future EP if needed.

### D12. Handle ID convention: type-prefixed dotted

- **Decided:** `<type>:<plugin>.<id>` form for every spawned
  thing. Type prefix mandatory; plugin context dotted.
- **Alternatives:** UUIDs (no semantic structure); per-type
  numeric IDs without plugin context; flat-string IDs.
- **Why:** `/ps` output is human-readable without extra columns;
  `/kill <id>` operates on the right scope unambiguously;
  collisions across handle families are impossible.

### D13. Async runtime, sync-default tool surface

- **Decided:** the runtime always runs children async (Fleet
  registry; goroutine per child; parent's wasm executor not
  blocked). The model-facing `agent.spawn` defaults to `async:
  false`, which is sugar for spawn-async + read-until-completion.
  `async: true` returns a handle for parents that want to drive
  multiple children or do other work meanwhile.
- **Alternatives:** sync-only runtime (today's `spawn_agent` —
  parents block, no parallelism); async-only (every parent must
  write a polling loop).
- **Why:** codex review #5 surfaced that the original draft said
  "async by default" in some sections and `async: false` (sync)
  in others — a contradiction. The actual answer is "the
  runtime is async, the default tool shape is sync." Renaming
  the principle clarifies what's actually being committed to.

### D14. Default `parent_session` = caller's session

- **Decided:** `agent.spawn` with no `parent_session` argument
  forks the caller's current session. Explicit `parent_session
  = null` for detached spawns; explicit other-session-id for
  re-parenting.
- **Alternatives:** default `parent_session = null` (orphan by
  default); always require explicit `parent_session`.
- **Why:** codex review #14 / gemini review #14 both flagged
  the orphan-by-default risk — orphaned children break
  `agent.list` for the caller and remove audit-trail lineage.
  Defaulting to caller's session is the conservative choice;
  null and other-session remain available as explicit opt-ins.

### D15. `agent.read_messages` returns assistant content + external-input markers

- **Decided:** the convenience channel returns the child's
  assistant-role messages in full PLUS structured
  `external_input` markers (source + offset + summary) for any
  user-role messages from non-self producers. Bodies of external
  messages require `session:read|observe` cap to retrieve.
- **Alternatives:** assistant-role only (parent unaware of human
  intervention even when behaviour visibly changes); full
  message log (parent sees content the parent may not be
  entitled to read, leak risk in shared-trust contexts).
- **Why:** codex review #7 surfaced that "assistant-only" is too
  restrictive — parent may see child's behaviour change without
  context and reason incorrectly. The marker surfaces "external
  input occurred" as a structured event the parent can react to
  (e.g. by escalating to `session.observe` if it has the cap),
  while bodies stay behind the audit cap. Best of both worlds.

### D16. Replay default = full fidelity, including all sources

- **Decided:** `stado session replay <id>` reproduces every
  message regardless of source by default. `--omit-source human`
  (or other source-prefix) opts into filtered replay.
- **Alternatives:** default-skip human messages (the original
  draft); always-include with no opt-out.
- **Why:** codex review #7 / gemini review #4 both flagged that
  default-skip-human loses critical signal when human input was
  load-bearing (e.g. corrected a hallucination). Default-include
  is the conservative choice; explicit opt-out for the rare
  "what if the human hadn't intervened" replay use case.

### D17. Privileged-built-in imports for registry and agent fleet

- **Decided:** `stado_registry_*` and `stado_agent_*` imports
  are real wasm imports; capability gating prevents user-installed
  plugins from declaring the relevant caps (`registry:list`,
  `registry:describe`, `agent:fleet`). Bundled `tools` and
  `agent` plugins are the only signers permitted to declare
  these caps.
- **Alternatives:** put the operations on the generic Tier 1/2/3
  surface available to any plugin (over-broadens privileged
  operations); make these stado-internal not exposed via wasm
  ABI at all (loses the ability to swap or rewrite the meta-
  tool / agent plugins post-EP).
- **Why:** codex review #8 surfaced that meta-tool dispatch and
  agent-fleet operations need real host APIs that the original
  Tier 1/2/3 didn't list. Defining them as bundled-only caps
  keeps the privilege boundary clear (operator can't acquire a
  user plugin that exposes registry introspection or fleet
  manipulation), while keeping the door open to redefining the
  bundled plugins later without an ABI break.

### D18. HTTP imports enforce equivalent net-dial caps

- **Decided:** `stado_http_request[_streaming]` validates the
  resolved hostname:port against `net:dial:tcp:<host>:<port>`
  (or `net:http_request[:<host>]`) across redirects. No silent
  follow-redirect across cap boundaries.
- **Alternatives:** trust the high-level `net:http_request` cap
  alone (bypass risk if a plugin without `net:dial:tcp:foo:443`
  uses HTTP to reach foo:443); refuse all redirects at the host
  layer (breaks legitimate HTTP semantics).
- **Why:** codex review #9 surfaced the bypass — without
  redirect-time cap re-check, a plugin that declares only
  `net:http_request:trusted.example.com` could be redirected to
  `evil.example.com` and the host would dial without checking
  the cap. Re-validating per-resolved-host (including across
  redirects) closes the gap; the operator's installed cap set
  is still the source of truth.

### D19. Secrets namespace by canonical identity, not local alias

- **Decided:** `stado_secrets_*` keys are namespaced by the
  plugin's canonical install identity (`<host>/<owner>/<repo>`
  or `local:///<path>`), not by the operator-assigned local
  alias. Cross-plugin secret sharing requires explicit
  operator-driven copy via `stado secrets copy`.
- **Alternatives:** namespace by manifest name or local alias
  (the original draft); flat global namespace.
- **Why:** gemini review #2 surfaced an attack: a malicious
  plugin installed under the same alias as a previously
  trusted plugin would inherit secrets. Identity-based
  namespacing closes this; the alias is operator-display, the
  identity is what stored data is keyed against.

### D20. Sandbox cap manipulation only in wrap mode

- **Decided:** `[sandbox] plugin_runtime_caps_drop` /
  `_caps_add` only effective in `mode = "wrap"` (the wrapper-
  spawned re-exec applies caps before wazero starts). In `mode
  = "off"` the settings are accepted but ignored with a
  startup warning.
- **Alternatives:** apply in-process always (codex review #12
  correctly noted this is unreliable post-startup); refuse the
  config entirely outside `mode = "wrap"` (operator can't
  even draft progressive sandboxing as they tighten their
  config).
- **Why:** Linux capability semantics make post-startup
  manipulation of an already-wazero-running process unreliable;
  the wrap-mode re-exec is where caps can be applied before
  any plugin code runs. Documenting this prevents
  silent-no-op surprise.

### D21. Per-tool parity gate on migration

- **Decided:** native tools stay registered alongside wasm
  equivalents until each tool's golden parity test passes and
  one release of bake-in shows no regressions. Native cleanup
  is the closing PR of the migration, not the opening.
- **Alternatives:** "one large refactor PR set" cutover (the
  original draft); long-term native + wasm dual-tracking.
- **Why:** codex review #13 surfaced that wholesale cutover
  removes the fallback before parity is proven. Parity tests +
  staged flag flips give a controlled migration. Native
  cleanup at the end is when the EP completes.

### D22. 32-bit handle IDs with collision check

- **Decided:** handle ID tail is 8-hex (32-bit) cryptographic-
  random; host registry checks for collisions at generation
  time and re-rolls if (vanishingly rare) collision found.
- **Alternatives:** 16-bit (~65k handles before birthday
  collision); 64-bit (overkill for a single-process runtime);
  monotonic counter (predictable, easier exhaustion attack).
- **Why:** gemini review #16 surfaced 16-bit collision risk in
  long-running sessions. 32-bit pushes the birthday limit past
  any realistic working-set size while keeping IDs short
  enough to type / log. Collision check is cheap and removes
  the residual risk.

### D23. Scoped `exec:proc:<glob>` capability variant

- **Decided:** `exec:proc:<absolute-path-glob>` joins the broad
  `exec:proc` form. Plugins targeting known binaries are
  encouraged (via `plugin doctor` warnings) to use the scoped
  form. Operator review surface stays meaningful.
- **Alternatives:** broad `exec:proc` only (codex review #10:
  too coarse); refuse broad form entirely (breaks shell.exec
  use case).
- **Why:** codex review #10 surfaced that one-cap-allows-anything
  makes operator admission near-binary. Scoped variant lets
  authors declare exactly which binaries; broad form remains
  available for plugins that legitimately need it (shell.exec
  spawning whatever the user passed).

### D24. `stado_fs_read_partial` added to ABI v2

- **Decided:** add `stado_fs_read_partial(path, offset, length,
  buf, bufcap)` alongside the existing `stado_fs_*` surface.
  Same capability gates as `stado_fs_read`. The `fs.read` tool
  exposes `offset?` / `length?` params at the schema level.
- **Alternatives:** keep `stado_fs_*` fully unchanged; let the
  wasm plugin read the whole file and slice in linear memory.
- **Why:** dogfooding the `image-info` example plugin surfaced
  that `stado_fs_read`'s all-or-nothing contract forces plugins
  doing header inspection to allocate a 16 MiB buffer per call.
  The fix belongs at the host-import level so the host transfers
  only the requested bytes. ABI v2 is the right moment since the
  import surface is already being revised.

## Related

### Predecessors

- **EP-0002** (All Tools as WASM Plugins) — status changes from
  `Implemented` → `Partial`. Frontmatter gains
  `extended-by: [38]`. EP-0038 fulfils what EP-0002 stated; the
  invariant restoration is documented in §A.
- **EP-0005** (Capability-Based Sandboxing) — extended.
  EP-0038 adds Tier 1 capability families (`exec:proc`,
  `terminal:open`, `net:dial:<transport>:*`, `net:listen:*`,
  `net:icmp:*`, `bundled-bin:*`); Tier 2 (`dns:resolve:*`,
  `dns:axfr:*`, `dns:reverse:*`, `secrets:read:*`,
  `secrets:write:*`); Tier 3 (`crypto:hash`). EP-0005 D1, D2
  unchanged. New decision (D2 in this EP) extends EP-0005 §
  with the cap-vs-OS overlap rule.
- **EP-0006** (Signed WASM Plugin Runtime) — extended. The
  plugin runtime is now the only execution path (per the restored
  EP-0002 invariant). EP-0006's three plugin modes (tool /
  override / background-or-session-aware) become four when async
  agents are added; the "agent" mode IS session-aware-plugin with
  the new fleet runtime registry.
- **EP-0013** (Subagent Spawn Tool) — **superseded by EP-0038**
  for the tool surface. `spawn_agent` becomes `agent.spawn`;
  native → wasm; sync-only → async-by-default; ownership /
  write_scope / adoption mechanics survive as separate concerns
  layered on top of `agent.spawn` (the worker contract is now a
  plugin-author convention, not a runtime invariant). EP-0013's
  status becomes `Superseded`; frontmatter gains
  `superseded-by: [38]`. The complementary `stado session adopt`
  CLI flow is unchanged.
- **EP-0014** (Multi-Session TUI) — amended. "Active-session-only
  execution" relaxed: agent-driven sessions stream and run tools
  in the background. The TUI's "active" session now means "the
  one in the foreground viewport"; agent sessions can run in
  parallel without being foregrounded. Switching between
  sessions in the TUI continues to work as before; the new
  `/session attach` is for entering an agent session as a
  co-driver.
- **EP-0017** (Tool Surface Policy and Plugin Approval UI) —
  unchanged. EP-0017's `[tools].enabled/disabled` semantics
  retained (extended by EP-0037, not by this EP).
  `ui:approval` cap continues to be the plugin-only approval
  surface.
- **EP-0028** (`plugin run --with-tool-host` + HOME-rooted MkdirAll)
  — partially superseded. `--with-tool-host` becomes default
  behaviour; flag accepted with deprecation warning for one
  release then removed. D1's hard refusal of `exec:bash` becomes
  warn-and-run by default per EP-0038 D10; operator opt-in
  restores hard refusal. HOME-rooted MkdirAll work (D2-D6)
  unchanged. EP-0028 status becomes `Partial` (the bash
  refusal portion superseded; the MkdirAll portion shipped
  unchanged); frontmatter gains `superseded-by: [38]` (with
  the partial scope noted in the history entry).
- **EP-0029** (Config-introspection host imports) — aligned and
  extended. EP-0038 adds new `cfg:*` fields where bundled-as-wasm
  plugins need them: `cfg:config_dir`, `cfg:plugin_install_dir`,
  `cfg:worktree_dir`. Each its own additive EP-0029-shape change;
  tracked in §B.
- **EP-0031** (`fs:read:cfg:state_dir/...` path templates) — used.
  Bundled-as-wasm `task` plugin declares
  `fs:read:cfg:state_dir/tasks` + `fs:write:cfg:state_dir/tasks`;
  same pattern for any other plugin that operates against
  state-dir-relative paths.
- **EP-0034** (Background Agents Fleet) — **superseded by
  EP-0038**. EP-0034's `Fleet` registry concept survives as the
  runtime backing for `agent.list` and the `/agents` slash
  command, but the tool surface (slash `/spawn` + `/fleet` modal)
  is replaced by the model-facing `agent.spawn` family. EP-0034
  status changes from `Draft` to `Superseded`; frontmatter gains
  `superseded-by: [38]`.
- **EP-0035** (Project-local `.stado/`) — extended. EP-0038
  honours the discovery walk and load order unchanged. New
  schema sections (`[plugins.<name>]`, `[sessions]`, `[agents]`,
  `[sandbox]`) introduced by EP-0037 are populated and consumed
  by EP-0038 implementation.

### Companion EPs

- **EP-0037** (Tool dispatch + naming + categories + config + philosophy)
  — required prerequisite. EP-0038 builds on its dispatch model,
  naming convention, category taxonomy, and `[sandbox]` schema
  reservation.
- **EP-0039** (Plugin distribution and trust) — independent.
  Lands after or in parallel with EP-0038.

### External references

- WebAssembly System Interface (WASI) preview-1 — the wasi
  sandbox model that determines what Tier 1 has to provide.
- Wazero documentation — the host runtime stado uses for wasm.
  Embedded resource control + handle table integration described
  in §B map to wazero's existing memory/instance APIs.
- LSP specification — the JSON-RPC protocol the bundled `lsp`
  plugin speaks over `stado_proc_*`. Plugin owns framing,
  capability negotiation, server-specific quirks.
- RFC 6455 (WebSocket) — referenced as out-of-host: plugins build
  WebSocket on `stado_net_dial` + `stado_http_client_*` +
  `stado_hash_sha1`. ~50 lines of wasm.
- RFC 8259 (JSON) — committed dialect for `stado_json_*`.
