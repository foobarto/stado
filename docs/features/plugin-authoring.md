# Plugin authoring

A walkthrough of the stado plugin lifecycle from "I want a custom
tool" to "the LLM can call it." Pulls together the surface area
documented across `stado plugin --help`, EP-0006, EP-0027, and
EP-0028 into one go-from-zero-to-shipping path.

## Design north star — lean core, plugin everything

stado's core stays small on purpose. **Most agent-facing functionality
belongs in WASM plugins** (EP-0002), with the core providing only:

- the wasm runtime + capability sandbox,
- a few foundational primitives (config / FS / sandbox / git
  sidecar),
- the plugin lifecycle CLI (`init`, `sign`, `trust`, `install`,
  `run`, `gc`, `doctor`),
- and signed-distribution machinery.

Hooks in the core are deliberately thin passthroughs: the runtime
calls into the plugin, not the other way round, and business logic
is on the plugin side so it can be swapped, upgraded, or replaced
without touching stado.

**When designing your plugin**, lean into this: capability-bound
swappable units beat monolithic feature flags. If your plugin grows
big, that's fine — sign it, ship it, and let the operator decide
whether to install. If a feature feels like it should live in
stado's core, double-check whether a plugin with the right
capabilities can do it equally well. The bar for "this must be in
core" is "no plugin capability can express this" — which is rare.

This document is for **plugin authors** (operators writing their
own plugins). For the trust model and signature security
properties, see [EP-0006](../eps/0006-signed-wasm-plugin-runtime.md)
and [SECURITY.md](../../SECURITY.md). For the per-command
reference, see [`docs/commands/plugin.md`](../commands/plugin.md).

## When to write a plugin (and when not to)

Write a plugin when you want to add a tool the LLM can call — a
project-specific lookup, a service wrapper, a domain-aware command.
The plugin runs in a wasm sandbox with the capabilities its
manifest declares; the LLM sees it the same way it sees the bundled
tools (`bash`, `webfetch`, `read`, etc.).

Don't write a plugin when:

- A bundled tool already does the job. `bash` + a small shell
  script is often the right answer.
- You want to override a bundled tool with a custom variant. Use
  `[tools].overrides = { webfetch = "webfetch-cached-0.1.0" }` in
  `config.toml` to point the bundled name at your installed plugin
  — no code change in the agent.
- You need to integrate an external service that already speaks
  the [Model Context Protocol](https://modelcontextprotocol.io).
  An MCP server is a much simpler integration than a wasm plugin
  for that case.

## The lifecycle in one block

```
┌─────────────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│ stado plugin    │ →  │ build.sh │ →  │ stado    │ →  │ stado    │
│   init <name>   │    │ (compile │    │ plugin   │    │ plugin   │
│ (Go scaffold)   │    │  + sign) │    │ install  │    │ run …    │
└─────────────────┘    └──────────┘    └──────────┘    └──────────┘
        │                    │              │              │
        ↓                    ↓              ↓              ↓
   plugin.go,          plugin.wasm,    state-dir       LLM-callable
   manifest            manifest.sig    plugin/<id>     tool
```

Each step has a verb you'll recognise from the analogue ecosystem
(npm/cargo/pip), but the artefact at every step is signed +
capability-bounded.

## Step 1 — Scaffold

```sh
stado plugin init my-plugin
cd my-plugin
ls
# build.sh  go.mod  main.go  plugin.manifest.template.json  README.md
```

`init` creates a Go `wasip1` project with the wasm ABI exports
(`stado_alloc`, `stado_free`, `stado_tool_<name>`) and host imports
(`//go:wasmimport stado stado_log` etc.) already wired. Replace the
`greet` demo tool with your real tool — the rest of the boilerplate
should work as-is.

The Go runtime overhead (~3 MB wasm output for a trivial plugin)
is real. If size matters, write the plugin in Zig or Rust against
the same ABI. Proven examples:

| Language | Example | Wasm size | Build |
|----------|---------|-----------|-------|
| Go | `http-session` | ~3.5 MB | `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared` |
| Zig | `hello` | ~800 B | `zig build-exe -target wasm32-freestanding -fno-entry -OReleaseSmall` |
| Zig | `encode-zig` | ~5 KB | same — full base64/hex/url/html encode+decode |
| Rust | (pending) | ~50–200 KB expected | `cargo build --target wasm32-unknown-unknown --release` |

Zig's `wasm32-freestanding` target needs no WASI or libc — the
stado ABI is the only interface. Rust requires declaring extern "C"
host imports and `#[no_mangle]` exports matching the same ABI surface.

**Key ABI constraint for Zig/Rust:** the host calls `stado_alloc`
twice per tool invocation — once for the args buffer and once for
the 1 MiB result buffer. Size your arena to at least 2 MiB to
accommodate both.

## Step 2 — Sign

Plugins are Ed25519-signed JSON manifests. The signing key never
needs to leave your machine.

```sh
stado plugin gen-key my-plugin.seed   # one-time; chmod 600 + back up
./build.sh
# → plugin.wasm + plugin.manifest.json + plugin.manifest.sig
```

`build.sh` is the scaffolded compile + sign script. It calls
`stado plugin sign` under the hood, which fills in the manifest's
`wasm_sha256` and `author_pubkey_fpr` fields and writes the
detached signature.

## Step 3 — Trust + install

Stado refuses to install plugins from un-pinned signers. First
time you install a plugin from your own key:

```sh
# Print the pubkey hex from your seed to use in the trust call.
# (gen-key already printed it; check the seed file's adjacent
# author.pubkey if you forgot.)
stado plugin trust <pubkey-hex> "Your Name"
stado plugin install .
```

Subsequent installs of newer versions from the same key just need
`stado plugin install .` — the trust pin survives.

## Step 4 — Run

```sh
stado plugin installed
# my-plugin-0.1.0  author=Your Name  tools=1  caps=1

stado plugin doctor my-plugin-0.1.0
# Prints: which surfaces this plugin runs on, with the exact flags
# to pass. Use this when `plugin run` returns errors and you want
# to know which knob to flip.

stado plugin run my-plugin-0.1.0 <tool> '<json-args>'
```

If `plugin run` produces a message like `stado_http_get returned
-1` or `stado_fs_read failed`, run `plugin doctor` against the
plugin id — it will tell you whether you need `--with-tool-host`,
`--workdir`, `--session`, or to use the TUI / `stado run` instead.

## Capabilities and the surface they require

The manifest declares capabilities that the host enforces at the
wasm-import boundary. The full vocabulary is catalogued in the
[ABI reference §8](../plugins/abi-reference.md#8-capability-vocabulary);
this table covers the most common groups and the plugin-run
surface each requires.

| Capability shape | What it gates | Required surface |
|------------------|---------------|------------------|
| `fs:read:/abs/path`, `fs:write:/abs/path` | `stado_fs_read` / `stado_fs_write` to that path | Any |
| `fs:read:.`, `fs:read:./sub` | Same, but resolved against `Workdir` | `plugin run --workdir=$PWD` (default workdir is the plugin's install dir, not the operator's CWD — EP-0027) |
| `net:http_get`, `net:<host>` | `stado_http_get` (markdown-converting URL fetch) | `plugin run --with-tool-host` (no flag → "plugin host has no tool runtime context" error — EP-0028) |
| `net:http_request[:<host>]` | `stado_http_request` and `_request_stream` | `plugin run --with-tool-host` |
| `net:http_request_private` | Loosens dial guard to RFC1918 / loopback / link-local / CGNAT. Off by default. | `plugin run --with-tool-host` |
| `net:http_client` | Stateful HTTP client with cookie jar (`stado_http_client_*`) | `plugin run --with-tool-host` |
| `net:dial:tcp:<host>:<port>`, `:udp:`, `:unix:<path>` | Outbound `stado_net_dial` (TCP / UDP / Unix). Private addresses still need `net:http_request_private`. | Any |
| `net:listen:tcp:<host>:<port>`, `:udp:`, `:unix:<path>` | Server-side `stado_net_listen` (verbatim host:port match — no implicit `127.0.0.1 ⊂ 0.0.0.0`) | Any |
| `exec:search`, `exec:ast_grep`, `lsp:query` | Bundled search / LSP imports | `plugin run --with-tool-host` |
| `exec:bash`, `exec:shallow_bash` | `stado_exec_bash` | TUI / `stado run` only — `plugin run` refuses (EP-0028) |
| `exec:proc[:<binary-glob>]` | `stado_proc_*` and `stado_exec` | TUI / `stado run` (sandbox runner needed) |
| `exec:pty`, `terminal:open` | PTY-backed shell sessions (`stado_pty_*` / `stado_terminal_*`) | TUI / `stado run` |
| `session:read`, `session:fork`, `session:observe` | Session reads + fork RPC | `plugin run --session <id>` |
| `llm:invoke[:<token-budget>]` | Outbound LLM calls | `plugin run --session <id>` (uses the session's provider) |
| `memory:propose`, `memory:read`, `memory:write` | Append-only memory store | `plugin run --session <id>` (or any agent loop) |
| `state:read[:<key-glob>]`, `state:write[:<key-glob>]` | Process-lifetime in-memory KV (`stado_instance_*`) | Any |
| `secrets:read[:<name-glob>]`, `secrets:write[:<name-glob>]` | Operator secret store (`stado_secrets_*`) | Any |
| `tool:invoke[:<name-glob>]` | Plugin calls other registered tools (`stado_tool_invoke`) | Any (depth-limited) |
| `agent:fleet` | Sub-agent fleet (`stado_agent_*`) — bundled agent plugin only | TUI / `stado run` |
| `dns:resolve` | `stado_dns_resolve` | Any |
| `crypto:hash`, `compress` | Stateless format helpers (hash, hmac, gzip, zlib) | Any |
| `cfg:state_dir` | Read state-dir path (`stado_cfg_state_dir`) | Any |
| `bundled-bin` | Read bundled binaries (`stado_bundled_bin`) | Any |
| `ui:approval` | Approval bridge (`stado_ui_approve`) | TUI / headless agent loop only |

`stado plugin doctor` automates this table — run it against any
installed plugin and the report will tell you exactly what flags
to pass.

### Manifest extras (v0.36+)

| Field | Purpose |
|---|---|
| `requires` | Array of `"<plugin-name>"` or `"<name> >= <ver>"` — install fails if a dep is missing. |
| `tools[].categories` | Array of category tags (`file`, `network`, `code-search`, …). Operators can add `[tools].autoload_categories = ["file"]` to surface tools by category instead of by name. |
| `min_stado_version` | Refuses install on older stado. Set to the version that introduced any host import you call. |

## Iteration loop

Plugin authoring is bumpy on the first plugin (figuring out the
ABI, getting capabilities right) but smooth after. The recommended
loop:

1. Edit `main.go`. Bump the manifest's `version` field if you
   want to install side-by-side with the previous build (rollback
   protection rejects identical-version reinstalls under the same
   signer).
2. `./build.sh`
3. `stado plugin install .`
4. `stado plugin run [flags] my-plugin-<version> <tool> '<args>'`

Periodically:

- `stado plugin gc` — sweep older versions per (signer, name)
  group. Default `--keep=1`. Dry-run by default; pass `--apply`.
  Trust-store entries and rollback pins are preserved, so a
  freshly-deleted older version still cannot be reinstalled.

## Common authoring patterns

### Read a file from the operator's repo

```go
//go:wasmimport stado stado_fs_read
func stadoFsRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

// In your tool's RunE:
const cveIndexPath = "notes/cve_index.md"
buf := make([]byte, 1<<20)
pathBytes := []byte(cveIndexPath)
n := stadoFsRead(
    uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
    uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(cap(buf)),
)
```

Manifest: `"capabilities": ["fs:read:."]`. Run with
`stado plugin run --workdir=$PWD <id> ...` so `notes/cve_index.md`
resolves against the operator's repo, not the plugin's install
dir.

### Wrap the bundled webfetch with a cache

The `webfetch-cached` plugin in `~/Dokumenty/htb-writeups/plugins/`
is the canonical example — wraps `stado_http_get` behind a
SHA-256-keyed disk cache. Manifest declares
`net:http_get` + the cache directory as
`fs:read:/abs/cache` and `fs:write:/abs/cache`. Run with
`--with-tool-host` so `stado_http_get` is wired up.

### Emit progress for long-running tools

Tools that take more than ~2 seconds should emit progress so the
operator sees they're alive. The `stado_progress` import is a
no-cap, fire-and-forget operator-visibility channel:

```go
//go:wasmimport stado stado_progress
func stadoProgress(textPtr, textLen uint32) int32

// Inside your tool:
msg := []byte(fmt.Sprintf("checking host %d/%d", i, total))
stadoProgress(uint32(uintptr(unsafe.Pointer(&msg[0]))), uint32(len(msg)))
```

The TUI surfaces these as `PROGRESS [plugin] text` lines in the
sidebar; `stado plugin run` prints them to stderr. No capability
needed; payload bounded to 4 KiB. The model only sees the final
tool result — progress is operator UX, not agent input.

### Extract a JSON field without bundling a parser

`stado_json_get` extracts one value from a JSON document by dotted
path; saves ~50 KiB of bundled parser per plugin and runs at
native speed. Useful for picking one field out of an HTTP response:

```go
//go:wasmimport stado stado_json_get
func stadoJSONGet(jsonPtr, jsonLen, pathPtr, pathLen, outPtr, outMax uint32) int32

// Pull "user.id" out of an API response.
out := make([]byte, 256)
n := stadoJSONGet(
    uint32(uintptr(unsafe.Pointer(&body[0]))), uint32(len(body)),
    uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
    uint32(uintptr(unsafe.Pointer(&out[0]))), uint32(cap(out)),
)
// out[:n] = `"alice"` (canonical JSON; strings keep quotes)
```

Path syntax is dotted with array indices: `user.tags.0`. No
capability needed.

### Persist state across tool calls within a session

`stado_instance_*` is a per-Runtime in-memory KV store. State
survives across calls within one stado process; cleared at session
end. Per-plugin namespaced — you can't read another plugin's keys.

```go
// Capabilities: state:read, state:write
sdkSet("session_token", tokenBytes)
sdkGet("session_token") // returns the bytes, or nil
```

Bound: 1 MiB per value, 16 MiB per plugin. For state that needs
to survive a stado restart, use the operator secret store
(`stado_secrets_*`, capability `secrets:read[:<glob>]` /
`secrets:write[:<glob>]`).

### Use as an override for a bundled tool

```toml
# config.toml
[tools]
overrides = { webfetch = "webfetch-cached-0.1.0" }
```

When `[tools].overrides` is set, the bundled `webfetch` is
replaced by your installed plugin. The LLM sees one tool named
`webfetch`; the agent runtime routes to the plugin instead of the
built-in implementation. This is the strongest way to deploy a
plugin — the plugin doesn't need to be in the LLM's prompt, it's
just *the* `webfetch`.

## Auditability invariant — do not bypass the trace

stado's git trace ref (`refs/sessions/<id>/trace`, EP-0004) records
every tool call as part of the session's audit log. **This invariant
is non-negotiable.** Any code path that mutates session state must
commit through the trace path so the audit log stays complete; a
silent bypass — even an "optimisation" that batches commits or a
"convenience" that side-steps a write — voids the audit guarantee
for that session.

The session-compaction work briefly broke this invariant by
accident; it was caught + remedied. The reminder is forward-looking:
when you write a plugin (or, more importantly, when you propose a
new core feature), ask:

- *Does this mutate session state?* (file writes inside the worktree;
  fork rpc; memory-store appends from `memory:write` capability;
  llm:invoke responses)
- *If yes, does the mutation flow through the existing
  trace-committing path?* (`stadogit.Session.Commit`, the agent
  loop's tool-call wrapper, etc.)

If the answer is "yes, mutates" + "no, bypasses trace", the design
is wrong. Plugins inherit this discipline by default — the host
imports that mutate (`stado_fs_write`, `stado_exec_bash` via the
agent loop, `stado_session_fork`, `stado_llm_invoke`) all commit
through the trace. A plugin that wires up its own out-of-band
mutation channel (e.g., shells out via `stado_exec_bash` to a
network sink that writes elsewhere) is the regression vector.
Capability gating bounds what a plugin can do; the trace ref
records what it actually did.

For operator-tooling commands (gc, doctor, install) the trace-ref
invariant doesn't apply — they're operator actions, not agent
actions, and they live outside the per-session ref namespace. But
any new agent-callable tool (whether bundled, plugin, or MCP)
must respect this.

## Inspecting an existing plugin

For a plugin you didn't write (a teammate's, an example bundled
under `plugins/examples/`, or one you forgot the details of):

```sh
stado plugin doctor <id>           # surfaces + capabilities + suggested invocation
stado plugin verify <plugin-dir>   # signature + sha256 + rollback verification
```

`doctor` is the operator-facing UX — it tells you what to do with
the plugin. `verify` is the security-facing UX — it tells you
whether to trust the plugin.

## Common errors

| Error | Cause | Fix |
|-------|-------|-----|
| `plugin host has no tool runtime context` | Plugin imports a bundled tool (`stado_http_get` etc.) but `plugin run` didn't get `--with-tool-host` | Pass `--with-tool-host`. EP-0028. |
| `stado_fs_read failed: ... no such file or directory` | Capability is `fs:read:.` but Workdir resolved to the install dir, not your repo | Pass `--workdir=$PWD`. EP-0027. |
| `plugin <id> declares exec:bash` (refusal) | EP-0028 won't supply a no-op sandbox.Runner | Run via `stado run` / TUI instead. The agent runtime has the real Runner. |
| `verify: rollback detected — manifest <new> < last seen <old>` | You're trying to install an OLDER version after a newer one | Bump the version. Rollback protection is intentional — once a higher version exists for a signer, lower versions can't be reinstalled. |
| `installed: trust: signer <fpr> not pinned` | First install from this signer | `stado plugin trust <pubkey-hex> "<name>"` then retry. |
| Plugin works one-shot via `plugin run` but not in TUI | TUI loads plugins listed under `[plugins].background` or invoked via `/plugin:<id>`; `[tools].overrides` is what you want for replacing bundled tools | Add the override to `config.toml`. |

## Related documents

- [`docs/plugins/abi-reference.md`](../plugins/abi-reference.md) —
  systematic ABI reference (memory model, return-code conventions,
  typed handles, JSON envelope, capability vocabulary index,
  manifest schema, lifecycle). Read this end-to-end once when you
  start writing plugins.
- [`docs/plugins/host-imports.md`](../plugins/host-imports.md) —
  function-by-function reference for every wasm host import (~70
  in total), grouped by Tier, with capability gates and ABI
  signatures. The first place to look when "I need the WASM tool
  to do X but the host only exposes Y."
- [`docs/commands/plugin.md`](../commands/plugin.md) — exhaustive
  per-command reference.
- [EP-0002](../eps/0002-all-tools-as-plugins.md) — why every tool
  is a plugin (architecture rationale).
- [EP-0006](../eps/0006-signed-wasm-plugin-runtime.md) — the
  signing + verification protocol.
- [EP-0027](../eps/0027-repo-root-discovery.md) — repo-root
  discovery and why `--workdir` exists.
- [EP-0028](../eps/0028-plugin-run-tool-host.md) — `--with-tool-host`
  + the `exec:bash` refusal rule.
- [`SECURITY.md`](../../SECURITY.md) — plugin-publish cookbook,
  trust model, distribution recommendations.
- [`plugins/examples/`](../../plugins/examples/) — opt-in example
  plugins covering `hello`, `hello-go`, `session-inspect`,
  `auto-compact`, the `approval-*-go` family for bundled-tool
  approval wrappers, and `session-recorder`.
