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
the same ABI; the bundled `hello-zig` example is ~800 bytes.

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
wasm-import boundary. Every capability falls into one of four
buckets:

| Capability shape | What it gates | Required surface |
|------------------|---------------|------------------|
| `fs:read:/abs/path`, `fs:write:/abs/path` | `stado_fs_read` / `stado_fs_write` to that absolute path | Any |
| `fs:read:.`, `fs:read:./sub` | Same, but resolved against the host's `Workdir` | `plugin run --workdir=$PWD` (default workdir is the plugin's install dir, not the operator's CWD — EP-0027) |
| `net:http_get`, `net:<host>` | `stado_http_get` (URL fetch, markdown-converting) | `plugin run --with-tool-host` (no flag → "plugin host has no tool runtime context" error — EP-0028) |
| `net:http_request`, `net:http_request:<host>` | `stado_http_request` (generic HTTP — methods, headers, body, base64-binary-safe) | `plugin run --with-tool-host` |
| `net:http_request_private` | Loosens `stado_http_request`'s dial guard to permit RFC1918 / loopback / link-local / CGNAT destinations. Multicast / unspecified / reserved / docs ranges still refused. Off by default — opt-in only. | `plugin run --with-tool-host` |
| `exec:search`, `exec:ast_grep`, `lsp:query` | Bundled search / lsp tool imports | `plugin run --with-tool-host` |
| `exec:bash`, `exec:shallow_bash` | `stado_exec_bash` | TUI / `stado run` only — `plugin run` refuses, EP-0028 (the `sandbox.Runner` is not available) |
| `session:read`, `session:fork`, `session:observe` | Session-aware reads + fork RPC | `plugin run --session <id>` |
| `llm:invoke`, `llm:invoke:<budget>` | Outbound LLM calls from the plugin | `plugin run --session <id>` (uses the session's provider) |
| `memory:propose`, `memory:read`, `memory:write` | Append-only memory store | `plugin run --session <id>` (or any agent loop) |
| `ui:approval` | Approval bridge | TUI / headless agent loop only |

`stado plugin doctor` automates this table — run it against any
installed plugin and the report will tell you exactly what flags
to pass.

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
