# Sandboxing

Four control layers combine the WASM runtime with Linux kernel mechanisms.
Signed WASM capabilities are enforced at the host-import boundary. The kernel
layers apply when their required host mechanisms are available; process paths
without the full stack and their fail/fallback behavior are explicit below.

## Why sandboxing

A coding agent runs untrusted output — both from the LLM (which can
be prompt-injected) and from any tool it calls into (which can be
exploited). The acceptable posture isn't "the agent promises to be
careful"; it's "the kernel prevents it from touching anything the
user didn't authorise."

Stado's capability commitment applies to WASM host imports: an undeclared
operation is refused before the guest can perform it. Subprocess namespace and
syscall containment additionally depend on an enforcing Linux runner; the
absence and defense-in-depth fallbacks are not treated as equivalent to the
full posture.

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

In v0.57.0+, the host-default sandbox policy is **on by default** for `stado
run` and every other orchestrator entry point. When Landlock is available it
applies to the entire `run` process and confines writes to the launch cwd +
`/tmp`. TUI shell commands use bubblewrap when that runner is available. On a
direct `run` path where both layers are active, the child inherits the parent's
FS ruleset and gets its own bwrap mount namespace on top. Pass
`--no-sandbox` to disable the host-default WASM process/PTY policy and
Landlock. `--no-sandbox` is a **persistent root flag**, not a `run`-only one —
it applies on every entry point (`stado --no-sandbox` for the TUI, `stado run
--no-sandbox`, `stado mcp-server --no-sandbox`, etc.). Configured stdio MCP
servers are separate: they retain the runner derived from their declared
capabilities and `sandbox.Detect()`.

`stado doctor` reports:
- `Landlock available` — kernel ≥ 5.13
- `Landlock unavailable` — kernel too old OR binary refused; falls
  back with a one-time advisory.

## Layer 2 — bubblewrap + conditional seccomp BPF (Linux exec)

With bubblewrap available, host-default WASM process/PTY calls launch inside a
new mount + pid + ipc namespace. Those calls fail if their requested policy
has no enforcing runner. Configured stdio MCP servers use the generic detected
runner and therefore fall back to `NoneRunner` when bubblewrap is unavailable.

On the normal bubblewrap paths a seccomp filter strips common escape routes.
The filter is skipped for host-allowlist networking through `pasta`; filter
setup failure warns and continues under bubblewrap without seccomp:

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
provides — `session:read`, `session:fork`, `fs:read`, `fs:write`, the generic
token-bounded `provider:invoke:<N>` primitive, the generic `artifact:*` surface,
and `stado_log`. Each
import is capability-gated against the signed plugin manifest. Artifact
requests use an opaque broker-issued binding injected by the host bridge; the
guest never sees that binding and its JSON cannot supply authority fields
([EP-0063](../eps/0063-plugin-defined-harness-artifacts.md)).

The manifest is Ed25519-signed by the author. Installation checks
the signature against the pinned trust store. Revocation is
supported via `[plugins].crl_url` — stado refuses to install or
run anything on the revocation list.

## Credential masking

For a broker-mediated sandboxed session the default profile binds the
operator's home directory read-only for ergonomics. That would leave the
per-user SSH key directory reachable, so the runner masks that credential
directory:

- **The SSH key directory is masked.** The runner shadows the key
  directory with an empty `tmpfs`, then re-binds the safe files
  (`known_hosts`, the ssh `config`) read-only on top. The private keys
  inside become unreadable from the sandbox even though home is bound
  read-only — they can't be exfiltrated.

This is wired into `sandbox.Policy.Mask`. Masks combine as a union because
masking is a restriction: either side can hide more, but neither can unmask a
path hidden by the other.

Stado does not forward `$SSH_AUTH_SOCK`, expose a generic host-socket bind
capability, or include broad `/run` access in the autonomous process profile.
Sandbox environment filtering drops SSH-agent variables even when a guest asks
to keep them. When the active socket is inside a mounted subtree such as
OpenSSH's usual `/tmp/ssh-*/agent.*`, the socket's containing directory is
masked while the rest of persistent scratch remains available. Git-over-SSH
therefore needs credentials provisioned outside Stado or an explicitly
unsandboxed operator workflow. Short-lived, narrowly scoped SSH credential
provisioning is intentionally a separate system concern rather than a Stado
sub-agent role.

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

Linux is the only supported platform now and through v1. The full supported
containment path, when its host mechanisms are available, is Landlock +
bubblewrap/namespaces + conditional seccomp, with a private `pasta` network
namespace and CONNECT allowlist proxy where host-scoped egress is granted.
Darwin and Windows are outside the build/runtime/packaging contract and carry
no current security promise
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
