# Sandboxing

Three layers, enforced by the kernel and the wasm runtime — never by
trust. Every tool invocation, every shell command, every MCP server
starts inside the cage; escape requires a capability declaration
that stado maps to a concrete policy.

## Why sandboxing

A coding agent runs untrusted output — both from the LLM (which can
be prompt-injected) and from any tool it calls into (which can be
exploited). The acceptable posture isn't "the agent promises to be
careful"; it's "the kernel prevents it from touching anything the
user didn't authorise."

Stado's commitment: the set of things the agent's tools can
actually do at runtime is **strictly bounded** by the declared
capability set. Tests exist for the negative path — attempts to
`fs:write:/etc/passwd` without a matching capability are refused at
the syscall level.

## Layer 1 — Landlock (Linux filesystem)

Kernel ≥ 5.13. Filesystem ruleset applied at process start:

- Read: permitted over the whole repo so `grep`/`glob`/`read` work
  naturally. Prevents writes anywhere else, even under home.
- Write: confined to the session's worktree + `/tmp`. `bash` can
  build, edit its own scratch files, swap temp directories; it
  cannot `echo > ~/.ssh/authorized_keys`.
- Network: the built-in `bash` tool defaults to deny-all when it runs
  through the sandbox runner. Host-allowlist networking is only for
  subprocess policies that explicitly declare `net:<host>`.

`stado run --sandbox-fs` applies the ruleset to the entire `run`
process. The TUI launches shell commands via bubblewrap (layer 2)
which composes with Landlock — the child inherits the parent's
FS ruleset AND gets its own bwrap mount namespace on top.

`stado doctor` reports:
- `Landlock available` — kernel ≥ 5.13
- `Landlock unavailable` — kernel too old OR binary refused; falls
  back with a one-time advisory.

## Layer 2 — bubblewrap + seccomp BPF (Linux exec)

Every `bash` tool call and every MCP stdio server is launched
inside a new mount + pid + ipc namespace via bubblewrap, with a
seccomp filter that strips the common escape routes:

- `ptrace` — no attach-to-sibling or host processes
- `mount` / `umount2` — no mount tricks
- `bpf` — no attaching kernel tracers
- `modify_ldt` / `arch_prctl` (restricted) — no TLS shenanigans
- `reboot` / `kexec_load` — obvious escape hatches

The filter allowlist is conservative: standard POSIX calls for
normal program execution (open/read/write/exec/fork/wait/…) plus
network syscalls used by the CONNECT proxy. Anything not on the
allowlist returns EPERM.

Bwrap runs on any Linux kernel ≥ 3.8 (the user-namespace baseline).
Stado detects bwrap vs. alternatives at boot; `stado doctor` prints
the runner in use.

## Layer 3 — CONNECT-allowlist proxy (network)

An in-process HTTPS-CONNECT proxy is available for sandboxed
subprocess policies that declare `net:<host>`. Proxy-aware clients
that honor `HTTP_PROXY` / `HTTPS_PROXY` are matched against the
capability list:

- `net:api.github.com` — allow a specific host
- `net:allow` — allow ANY host (noisy stderr warning when set)
- `net:deny` — explicit deny
- (absence) — implicit deny

The proxy refuses CONNECTs that don't match. This is not yet a
universal egress firewall: raw TCP clients and plain HTTP clients
that ignore proxy settings are outside this enforcement path while
the process still shares the host network namespace. Stado uses this
today as a host-allowlist wedge for proxy-aware subprocesses, not as
a complete network sandbox.

## Layer 4 — wazero (wasm plugins)

Third-party stado plugins ship as wasm binaries, executed inside
`wazero`. Wasm is already sandboxed by construction (memory-safe,
no raw syscalls), so the kernel layer is unnecessary. What plugins
CAN do is expressed through host imports the stado runtime
provides — `session:read`, `session:fork`, `fs:read`, `fs:write`,
`llm:invoke`, `stado_log`. Each import is capability-gated against
the plugin manifest.

The manifest is Ed25519-signed by the author. Installation checks
the signature against the pinned trust store. Revocation is
supported via `[plugins].crl_url` — stado refuses to install or
run anything on the revocation list.

## Capability vocabulary

Used in `[mcp.servers.<name>].capabilities` and in wasm plugin
manifests:

| Grammar | Example | Meaning |
|---------|---------|---------|
| `fs:read:<path>` | `fs:read:/etc/hosts` | Read a specific file or directory |
| `fs:write:<path>` | `fs:write:/tmp` | Write under a specific path |
| `net:<host>` | `net:api.github.com` | CONNECT to a specific host |
| `net:allow` | — | Unrestricted egress (loud warning) |
| `net:deny` | — | Block all egress (default for unlisted) |
| `exec:<binary>` | `exec:/usr/bin/git` | Invoke a specific binary |
| `env:<VAR>` | `env:GITHUB_TOKEN` | Inherit an env var into the child |

**Default deny, opt-in allow.** For wasm plugins, an empty capability
list means "no host privileges." For stdio MCP servers, an empty
capability list is refused at startup instead of falling back to caller
privileges.

## Platform coverage

| Platform | Filesystem | Exec | Network |
|----------|-----------|------|---------|
| Linux | Landlock | bwrap + seccomp | CONNECT proxy |
| macOS | sandbox-exec `.sb` profile generated from capabilities | same `.sb` profile | CONNECT proxy |
| Windows | v1 unsandboxed (warning); v2 job objects + restricted tokens planned | same as FS | CONNECT proxy |

Windows v2 is deferred — the WinWarnRunner emits a one-time
`stderr` advisory at first tool call. If you're running stado for
production work on Windows today, be aware that filesystem and exec
isolation aren't enforced there yet. macOS uses sandbox-exec profiles
generated from the same capability list.

## Turning knobs

The quickest path to "sandbox everything more tightly":

```toml
# Narrow the tool set.
[tools]
enabled = ["read", "grep", "ripgrep", "ast_grep"]  # read-only agent

# Prompt on every tool call.
[approvals]
mode = "prompt"

# Cost guardrail so a runaway can't rack up spend.
[budget]
warn_usd = 0.50
hard_usd = 2.00
```

Combined with `stado run --sandbox-fs`, that's: read-only tools,
every call is approved interactively, $2 hard cap. Hard to misfire
and still useful for diagnosis work.

## Gotchas

- **Landlock returning `unavailable`** on a new kernel usually means
  stado's binary was built against a different unistd ABI. `stado
  doctor` reports specifically what's wrong.
- **The CONNECT proxy doesn't handle plain HTTP.** Tools that need
  to fetch non-TLS URLs must be outside the sandbox or use TLS.
- **`net:allow` is visible in `doctor` output.** The loud stderr
  advisory is designed to be noticed; if you see it in a long-lived
  session you didn't intend to run unsandboxed, check your MCP
  server configs.
- **Stdio MCP servers now require capabilities.** If one fails to
  start with a "capabilities are required" error, add the smallest
  `fs:*`, `net:*`, `exec:*`, and `env:*` set it actually needs.
- **Capability enforcement is runtime, not compile-time.** A tool can
  declare more than it uses; the extras are unused surface, not
  automatic risk. A tool cannot declare less and do more — the
  kernel stops it.
