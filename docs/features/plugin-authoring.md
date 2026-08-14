# Plugin authoring

A walkthrough of the stado plugin lifecycle from "I want a custom
tool" to "the LLM can call it." Pulls together the surface area
documented across `stado plugin --help`, EP-0006, EP-0027, and
EP-0028 into one go-from-zero-to-shipping path.

## Design north star — lean core, plugin everything

stado's core stays small on purpose. **All stado-owned model-visible product
behavior belongs in WASM plugins** (EP-0002 and EP-0066), with the core
providing only:

- the wasm runtime + capability sandbox,
- foundational bridges WASM cannot provide for itself (filesystem/process
  access, broker-bound sessions, broker-owned artifacts and journals,
  retained agents/mailboxes, operator UI, scheduling, sandbox, and git audit),
- the plugin lifecycle CLI (`init`, `sign`, `trust`, `install`,
  `gc`, `doctor`) and generic `stado tool run` execution path,
- and signed-distribution machinery.

Core seams are deliberately primitive: the host delivers bounded,
session-anchored observations and lifecycle callbacks; plugins request narrow
capability-gated effects.
Business logic stays on the plugin side so it can be swapped, upgraded, or
replaced without teaching native stado one application's workflow.

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
│   init <name>   │    │ (compile │    │ plugin   │    │ tool     │
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

**Remote installs** (`stado plugin install github.com/owner/repo@1.2.3`)
verify the owner's anchor key on first sight (interactive prompt, or
`--trust-anchor` for non-interactive). After an expected key rotation,
`stado plugin untrust-anchor <host/owner>` then reinstall. Signer-level
`stado plugin trust` still applies for local-directory installs.

### Project-local plugins (opt-in)

Repos may ship wasm under `.stado/plugins/`. Stado **does not** autoload
them unless the operator sets in **user** config:

```toml
[plugins]
allow_project_plugins = true
```

Default is `false` (EP-0044). The gate cannot be set from project
config. When off, stderr names the skipped directory once per process.
Global installs under `$XDG_DATA_HOME/stado/plugins/` are unaffected.

## Step 4 — Run

```sh
stado plugin installed
# my-plugin-0.1.0  author=Your Name  tools=1  caps=1

stado plugin doctor my-plugin-0.1.0
# Prints: which surfaces this plugin runs on, with the exact flags
# to pass. Use this when `tool run` returns errors and you want
# to know which knob to flip.

stado tool run <tool> '<json-args>'
```

If `tool run` produces a message like `stado_http_request returned
-1` or `stado_fs_read failed`, run `plugin doctor` against the
plugin id — it will tell you whether you need `--workdir`,
`--session`, or to use the TUI / `stado run` instead. (The tool host
is always attached now, so bundled-tool imports no longer need a flag.)

## Capabilities and the surface they require

The manifest declares capabilities that the host enforces at the
wasm-import boundary. The full vocabulary is catalogued in the
[ABI reference §8](../plugins/abi-reference.md#8-capability-vocabulary);
this table covers the most common groups and the plugin-run
surface each requires.

| Capability shape | What it gates | Required surface |
|------------------|---------------|------------------|
| `fs:read:/abs/path`, `fs:write:/abs/path` | `stado_fs_read` / `stado_fs_write` to that path | Any |
| `fs:read:.`, `fs:read:./sub` | Same, but resolved against `Workdir` | `tool run --workdir=$PWD` (default workdir is the plugin's install dir, not the operator's CWD — EP-0027) |
| `net:http_request[:<host>]` | `stado_http_request` and `_request_stream` | Any (tool host always attached) |
| `net:http_request_private` | Loosens dial guard to RFC1918 / loopback / link-local / CGNAT. Off by default. | Any (tool host always attached) |
| `net:http_client` | Stateful HTTP client with cookie jar (`stado_http_client_*`) | Any (tool host always attached) |
| `net:dial:tcp:<host>:<port>`, `:udp:`, `:unix:<path>` | Outbound `stado_net_dial` (TCP / UDP / Unix). Private addresses still need `net:http_request_private`. | Any |
| `net:listen:tcp:<host>:<port>`, `:udp:`, `:unix:<path>` | Server-side `stado_net_listen` (verbatim host:port match — no implicit `127.0.0.1 ⊂ 0.0.0.0`) | Any |
| `exec:proc[:<binary-glob>]` | `stado_proc_*` and `stado_exec`; add `bundled-bin:<name>` for rg/ast-grep | TUI / `stado run` when spawning subprocesses (sandbox runner needed) |
| `exec:pty[:<binary-glob>]` | PTY-backed shell sessions (`stado_pty_*`) | TUI / `stado run` (screen via `shell.read` `mode: screen`, not a separate screenshot tool — EP-0043) |
| `session:read`, `session:fork`, `session:observe` | Session reads + fork RPC | `tool run --session <id>` |
| `llm:invoke[:<token-budget>]` | Outbound LLM calls | `tool run --session <id>` (uses the session's provider) |
| `artifact:propose:<local-kind>`, `artifact:edit:<local-kind>` | Propose a broker-owned artifact or candidate version for a manifest-declared local kind | `tool run --session <id>` (or a broker-attached agent loop) |
| `artifact:read:<qualified-kind-pattern>`, `artifact:observe:<qualified-kind-pattern>` | Query or observe explicit qualified kinds within broker scope/sensitivity policy | `tool run --session <id>` (or a broker-attached agent loop) |
| `session:journal:append`, `session:projection:read` | Durable lifecycle journal and caller-scoped projection | Persistent lifecycle application |
| `session:schedule` | Leased holds and typed pause/stop proposals | Persistent lifecycle application with broker scheduling |
| `session:complete` | Durable successful-completion transition; distinct from pause/stop | Persistent lifecycle application with broker scheduling |
| `session:input:route` | Durably claim an exact immutable input for asynchronous review, then deliver or defer it for the application's exact worker run | Persistent TUI lifecycle application with broker-owned input routing |
| `session:worker:request`, `session:worker:resume`, `session:worker:cancel` | Request, resume-request, or cancel a bounded application-owned worker recurrence; activation and resume activation are native-only | Persistent TUI lifecycle application with broker scheduling |
| `timer:schedule` | Durable timer schedule/cancel | Persistent lifecycle application |
| `state:read[:<key-glob>]`, `state:write[:<key-glob>]` | Process-lifetime in-memory KV (`stado_instance_*`) | Any |
| `secrets:read[:<name-glob>]`, `secrets:write[:<name-glob>]` | Operator secret store (`stado_secrets_*`) | Any |
| `tool:invoke[:<name-glob>]` | Plugin calls other registered tools (`stado_tool_invoke`) | Any (depth-limited; v0.75.2 audit path when executor pinned) |
| `agent:spawn`, `agent:list`, `agent:read`, `agent:send`, `agent:cancel` | The matching sub-agent operation (`stado_agent_*`); capabilities do not imply one another | TUI / `stado run` with a fleet bridge |
| `agent:spawn:configure` | Additional attenuation for signed provider/model/thinking/effort overrides on `stado_agent_spawn`; no credential access and no operation without `agent:spawn` | TUI / `stado run` with a configured fleet bridge |
| `dns:resolve` | `stado_dns_resolve` | Any |
| `crypto:hash`, `compress` | Stateless format helpers (hash, hmac, gzip, zlib) | Any |
| `cfg:state_dir` | Read state-dir path (`stado_cfg_state_dir`) | Any |
| `bundled-bin` | Read bundled binaries (`stado_bundled_bin`) | Any |
| `ui:approval` | Approval bridge (`stado_ui_approve`) | TUI / headless agent loop only |
| `ui:choice`, `ui:print`, `ui:render` | Generic operator choice, text, and structured-panel bridges | Interactive/session surfaces; transport fallback is import-specific |

EP-0064 additionally defines operation-scoped lifecycle, journal, mailbox,
retained-agent, and scheduling capabilities for persistent applications. The
TUI dispatcher owns one serialized instance; do not substitute ambient files
or native application-specific wrappers on unsupported surfaces.

`stado plugin doctor` automates this table — run it against any
installed plugin and the report will tell you exactly what flags
to pass.

### Manifest extras (v0.36+)

| Field | Purpose |
|---|---|
| `requires` | Array of `"<plugin-name>"` or `"<name> >= <ver>"` — install fails if a dep is missing. |
| `tools[].categories` | Array of category tags (`file`, `network`, `code-search`, …). Operators can add `[tools].autoload_categories = ["file"]` to surface tools by category instead of by name. |
| `artifact_kinds` | EP-0063 local kind names, bounded object-root JSON Schemas, and optional deterministic index projections. Covered by the signed manifest digest. |
| `lifecycle` | EP-0064 point/event subscriptions, failure policy, and bounded callback timeout for the persistent TUI application instance. |
| `commands` | Signed operator command declarations. Optional `timeout_ms` (maximum 15 minutes) applies only to that serialized command; zero inherits the lifecycle timeout. |
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
4. `stado tool run [flags] <tool> '<args>'`

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
`stado tool run --workdir=$PWD <tool> ...` so `notes/cve_index.md`
resolves against the operator's repo, not the plugin's install
dir.

### Wrap the bundled webfetch with a cache

Use `net:http_request[:<host>]` + `stado_http_request` (the legacy
`stado_http_get` / `net:http_get` caps are removed). A disk cache is
just `fs:read` / `fs:write` on a cache directory plus HTTP request caps
for the upstream host.

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
sidebar; `stado tool run` prints them to stderr. No capability
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

stado's git trace ref (`refs/sessions/<id>/trace`, EP-0004) records every
model-facing tool call. Broker-owned artifacts, journals, mailboxes, and
scheduling state use the broker's hash-chained WAL under EP-0059. **Neither
authority may be bypassed.** The two ledgers serve different state, and a plugin
does not get to replace either with a convenient file under `cfg:state_dir`.

The session-compaction work briefly broke this invariant by
accident; it was caught + remedied. The reminder is forward-looking:
when you write a plugin (or, more importantly, when you propose a new core
feature), ask:

- *Does this mutate the audited worktree or conversation?* Keep the call on the
  executor and session trace/tree path.
- *Does this mutate an artifact or lifecycle record?* Use the typed broker
  import (`stado_artifact_*` today; EP-0064 journal/mailbox/scheduling imports as
  they land), with loader-supplied runtime identity and stable idempotency.

If the answer is “yes, mutates” and “neither authority records it,” the design
is wrong. Plugins inherit the tool-call trace when invoked through the normal
executor. `stado_artifact_*` calls additionally submit through an opaque
broker-issued application binding to the broker single writer; they never open
its WAL or fall back to a local JSONL
file. Capability gating bounds what a plugin may request, while the appropriate
ledger records what actually happened.

For operator-tooling commands (gc, doctor, install) the trace-ref
invariant doesn't apply — they're operator actions, not agent
actions, and they live outside the per-session ref namespace. But
any new agent-callable tool (whether bundled, plugin, or MCP)
must respect this.

## Inspecting an existing plugin

For a plugin you didn't write (a teammate's, an example from
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins), or one
you forgot the details of):

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
| `plugin host has no tool runtime context` | Historic error when a plugin imported a bundled tool without `--with-tool-host` (EP-0028) | No longer reachable: `stado tool run` always attaches the tool host (EP-0038). |
| `stado_fs_read failed: ... no such file or directory` | Capability is `fs:read:.` but Workdir resolved to the install dir, not your repo | Pass `--workdir=$PWD`. EP-0027. |
| `verify: rollback detected — manifest <new> < last seen <old>` | You're trying to install an OLDER version after a newer one | Bump the version. Rollback protection is intentional — once a higher version exists for a signer, lower versions can't be reinstalled. |
| `installed: trust: signer <fpr> not pinned` | First local install from this signer | `stado plugin trust <pubkey-hex> "<name>"` then retry. Remote installs use owner anchor TOFU — see `plugin.md`. |
| Plugin works one-shot via `tool run` but not in TUI | TUI loads plugins listed under `[plugins].background` or invoked via `/plugin:<id>`; `[tools].overrides` is what you want for replacing bundled tools | Add the override to `config.toml`. |

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
- [EP-0028](../eps/0028-plugin-run-tool-host.md) — historical note on
  `--with-tool-host` (now default); PTY/exec plugins need TUI/`stado run`.
- [`SECURITY.md`](../../SECURITY.md) — plugin-publish cookbook,
  trust model, distribution recommendations.
- [foobarto/stado-plugins](https://github.com/foobarto/stado-plugins) — opt-in
  example plugins covering `hello`, `hello-go`, `session-inspect`,
  the `approval-*-go` family for bundled-tool approval wrappers, and
  `session-recorder`.
