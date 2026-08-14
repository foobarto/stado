# Sandboxing

Four layers, enforced by the Linux kernel and the wasm runtime — never by
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
- Write: confined to the launch cwd + `/tmp`. `bash` can
  build, edit its own scratch files, swap temp directories; it
  cannot `echo > ~/.ssh/authorized_keys`.
- Network: Landlock does not control egress. The broker-projected subprocess
  policy supplies the network decision to bubblewrap: `NetDenyAll` unshares
  the network namespace and `NetAllowHosts` uses the layer 3 proxy path.
  In-process HTTP host imports remain governed by wasm capabilities rather
  than Landlock.

In v0.57.0+, the sandbox is **on by default** for `stado run` and
every other orchestrator entry point. Landlock applies to the
entire `run` process; writes are confined to the launch cwd +
`/tmp`. The TUI launches shell commands via bubblewrap (layer 2)
which composes with Landlock — the child inherits the parent's
FS ruleset AND gets its own bwrap mount namespace on top. Pass
`--no-sandbox` to disable both layers (the runner becomes
`NoneRunner` and Landlock is skipped). `--no-sandbox` is a
**persistent root flag**, not a `run`-only one — it works on every
entry point (`stado --no-sandbox` for the TUI, `stado run
--no-sandbox`, `stado mcp-server --no-sandbox`, etc.).

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
normal program execution (open/read/write/exec/fork/wait/…). Anything
not on the allowlist returns EPERM.

Bwrap runs on any Linux kernel ≥ 3.8 (the user-namespace baseline).
Stado detects bwrap vs. alternatives at boot; `stado doctor` prints
the runner in use.

## Layer 3 — `pasta` + CONNECT proxy (Linux host-allowlist egress)

An in-process HTTPS-CONNECT proxy is available for sandboxed
subprocess policies that declare `net:<host>`. On Linux, the wrapped
subprocess runs under `pasta --splice-only` and only the proxy port is
forwarded into the private network namespace. Proxy-aware clients still
honor `HTTP_PROXY` / `HTTPS_PROXY`, but the reachability boundary is now
kernel-visible, not just an env-var convention.

The proxy is still matched against the capability list:

- `net:api.github.com` — allow a specific host
- `net:allow` — allow ANY host (noisy stderr warning when set)
- `net:deny` — explicit deny
- (absence) — implicit deny

The proxy refuses CONNECTs that don't match. On Linux, direct loopback
or raw-TCP bypasses are blocked by the private `pasta` netns because
only the proxy port is forwarded. Plain HTTP is still outside the
CONNECT proxy itself.

## Layer 4 — wazero (wasm plugins)

Third-party stado plugins ship as wasm binaries, executed inside
`wazero`. Wasm is already sandboxed by construction (memory-safe,
no raw syscalls), so the kernel layer is unnecessary. What plugins
CAN do is expressed through host imports the stado runtime
provides — `session:read`, `session:fork`, `fs:read`, `fs:write`,
`llm:invoke`, the generic `artifact:*` surface, and `stado_log`. Each
import is capability-gated against the signed plugin manifest. Artifact
requests use an opaque broker-issued binding injected by the host bridge; the
guest never sees that binding and its JSON cannot supply authority fields
([EP-0063](../eps/0063-plugin-defined-harness-artifacts.md)).

The manifest is Ed25519-signed by the author. Installation checks
the signature against the pinned trust store. Revocation is
supported via `[plugins].crl_url` — stado refuses to install or
run anything on the revocation list.

## Credential masking + ssh-agent forwarding

For a broker-mediated sandboxed session the default profile binds the
operator's home directory read-only for ergonomics. That would leave the
per-user SSH key directory reachable, so two default-on halves close the
gap (decision 2026-06-13):

- **The SSH key directory is masked.** The runner shadows the key
  directory with an empty `tmpfs`, then re-binds the safe files
  (`known_hosts`, the ssh `config`) read-only on top. The private keys
  inside become unreadable from the sandbox even though home is bound
  read-only — they can't be exfiltrated.

- **The ssh-agent socket is forwarded.** When the host has
  `$SSH_AUTH_SOCK` set, the runner binds that unix socket into the
  sandbox and re-exports `SSH_AUTH_SOCK`, so `git` over ssh works from a
  sandboxed tool call. **Only the socket crosses the boundary — key
  bytes never enter the sandbox.** The agent signs on the host; the
  sandbox only asks it to.

This is wired into `sandbox.Policy` as two fields: `Mask` (paths to
render unreadable; combined as a union — masking is a restriction) and
`Sockets` (host unix sockets to bind; combined as an intersection — a
bind is an allow a guest can only narrow). The startup banner notes when
the agent is forwarded.

**Accepted residual.** A compromised or prompt-injected agent in a
forwarded session could abuse the socket to sign arbitrary git
operations (push/commit) for the session's lifetime. The key itself is
never exposed. The eventual hardening — a short-lived, fetch-only,
approval- and taint-gated git sub-agent that alone holds the socket
(EP-0050 phase 7) — is not built here; the operator accepts the residual
for the pragmatic main-session forwarding.

## Capability vocabulary

Used in `[mcp.servers.<name>].capabilities` and in WASM plugin manifests.
Individual families are surface-specific; `artifact:*` is a WASM host-import
capability, not an MCP subprocess capability.

| Grammar | Example | Meaning |
|---------|---------|---------|
| `fs:read:<path>` | `fs:read:/etc/hosts` | Read a specific file or directory |
| `fs:write:<path>` | `fs:write:/tmp` | Write under a specific path |
| `net:<host>` | `net:api.github.com` | Reach only the allowlist proxy path for that host |
| `net:allow` | — | Unrestricted egress (loud warning) |
| `net:deny` | — | Block all egress (default for unlisted) |
| `exec:<binary>` | `exec:/usr/bin/git` | Invoke a specific binary |
| `env:<VAR>` | `env:GITHUB_TOKEN` | Inherit an env var into the child |
| `artifact:propose:<local-kind>` | `artifact:propose:review-contract` | Propose a candidate of a kind declared by this plugin |
| `artifact:read:<qualified-kind-pattern>` | `artifact:read:github.com/acme/reviewer#*` | Query explicitly selected qualified kinds within broker scope/sensitivity policy |
| `artifact:edit:<local-kind>` | `artifact:edit:review-contract` | Propose a new candidate version of this plugin's kind |
| `artifact:observe:<qualified-kind-pattern>` | `artifact:observe:github.com/acme/reviewer#review-contract` | Record a bounded observation for an allowed qualified kind |

**Default deny, opt-in allow.** For wasm plugins, an empty capability
list means "no host privileges." For stdio MCP servers, an empty
capability list is refused at startup instead of falling back to caller
privileges.

## Platform coverage

Linux is the only supported platform now and through v1. The supported
containment path is Landlock + bubblewrap/namespaces + seccomp, with a private
`pasta` network namespace and CONNECT allowlist proxy where host-scoped egress
is granted. Existing Darwin or Windows source remnants are unsupported, carry
no current security promise, and may be removed without a compatibility period
([EP-0065](../eps/0065-linux-only-platform-scope.md)).

## Turning knobs

The quickest path to "sandbox everything more tightly":

```toml
# Narrow the tool set.
[tools]
enabled = ["read", "grep", "ripgrep", "ast_grep"]  # read-only agent

# Use approval-wrapper plugins for tools that need a human gate.
# The native tool set itself is controlled by [tools].

# Token guardrail so a runaway cannot consume an unbounded session budget.
[budget]
warn_tokens = 100000
hard_tokens = 500000
```

Combined with the v0.57.0 default sandbox (Landlock writes confined
to cwd + `/tmp`, bubblewrap mount namespace per tool call), that's:
read-only tools, filesystem narrowing on Linux, and a 500,000-token hard cap.
Hard to misfire and still useful for diagnosis work.

## Gotchas

- **Landlock returning `unavailable`** on a new kernel usually means
  stado's binary was built against a different unistd ABI. `stado
  doctor` reports specifically what's wrong.
- **Linux `net:<host>` needs `pasta`.** If a Linux subprocess or MCP
  server with host allowlists fails to start, install or upgrade the
  `passt` package so `pasta --splice-only` is available.
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
