---
ep: 0028
title: stado plugin run --with-tool-host + HOME-rooted MkdirAll
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Companion to the shakedown patches in branch shakedown-r3.
see-also: [0005, 0006, 0027]
---

# EP-0028: `stado plugin run --with-tool-host` + HOME-rooted MkdirAll

## Problem

Two distinct but adjacent gaps in the plugin authoring + boot
surfaces, both surfaced during the WASM-plugin shakedown round.

### Gap 1: `plugin run` can't exercise plugins that import bundled tools.

`internal/plugins/runtime/host.go` defines `host.ToolHost` (a
`tool.Host`) that the bundled-tool host imports
(`stado_http_get`, `stado_exec_bash`, `stado_fs_tool_*`,
`stado_lsp_*`, `stado_search_*`) call through to. It is set by the
agent loop (`internal/runtime/bundled_plugin_tools.go`,
`plugin_overrides.go`) and by the in-process bundled tools, but
**not** by `cmd/stado/plugin_run.go`. Result: a plugin authored to
wrap a bundled tool — like the `webfetch-cached` plugin built in
the shakedown round, which calls `stado_http_get` to fetch a URL
and disk-cache the result — cannot be tested from the CLI.
Operator gets:

```
{"error":"stado_http_get returned -1; ensure plugin manifest declares net:http_get"}
```

The actual cause is `host.ToolHost == nil` in
`internal/plugins/runtime/tool_imports.go`. The error message is
honest but useless: there's no flag the operator can flip and no
documented workaround short of building the plugin into a real
`stado run` agent loop.

### Gap 2: `MkdirAllNoSymlink` rejects `/home` when it's a symlink.

The strict from-`/` no-symlink walker `MkdirAllNoSymlink` was
designed for adversarial paths (untrusted input, in-repo writes
guarded by Landlock). The user reported it rejecting their HOME
during config-dir creation:

```
$ ./stado
Error: config: create config dir: directory component is a symlink: home
```

On Fedora Atomic / Silverblue / `rpm-ostree`-based variants,
`/home` is a symlink to `/var/home`. The strict walker treats this
as an adversarial symlink and refuses to proceed. Same problem
applies to every HOME-rooted MkdirAll in the codebase: state dir,
worktree dir, audit-key dir, memory store dir, plugin install dir,
plugin state files. Fixing only one would leave the rest broken.

## Goals

- Wire `host.ToolHost` from `stado plugin run --with-tool-host`
  so plugins authored against the bundled-tool host imports can be
  tested end-to-end without dragging in the agent runtime.
- Keep capability gating in the manifest, not in the flag — the
  operator opting in does not widen what the plugin can do; it just
  enables the bridge for capabilities the manifest already declares.
- Make HOME-rooted `MkdirAll` work on systems where `/home` (or any
  ancestor of HOME / XDG dirs) is an OS-level symlink, while
  preserving the strict no-symlink defense for in-repo / in-tmpdir
  paths that are still adversarial.

## Non-goals

- Granting `--with-tool-host` plugins access to `exec:bash` /
  `exec:shallow_bash`. Those need a `sandbox.Runner` that
  `plugin run` can't supply, and EP-0005 §"Non-goals" forbids
  substituting human approval for runtime policy. Plugins that
  wrap bash must continue to run via `stado run` / TUI.
- A general "trust this entire path chain" override flag. Trust
  decisions are tied to the operator's environment (HOME, XDG_*),
  which we read from the OS, not from CLI args.
- Touching the strict `MkdirAllNoSymlink` semantics — it stays
  available for the truly-untrusted callers (host-import FS writes
  inside the plugin sandbox).

## Design

### `--with-tool-host` flag on `plugin run`

```go
// cmd/stado/plugin_run.go
pluginRunCmd.Flags().BoolVar(&pluginRunWithToolHost, "with-tool-host", false, ...)
```

Behaviour:

1. After loading the manifest and constructing `host =
   pluginRuntime.NewHost(*m, workdir, nil)`, refuse the run if the
   plugin declares `exec:bash` (or `exec:shallow_bash`). Error
   message points the operator at `stado run`.
2. If the refusal didn't trigger, instantiate a minimal
   `pluginRunToolHost` and assign it to `host.ToolHost`.
3. The wasm runtime, host imports, and module instantiation
   proceed unchanged.

### Minimal `pluginRunToolHost` (`cmd/stado/plugin_run_tool_host.go`)

Implements `tool.Host` with these defaults:

- `Workdir() string` — returns the path the operator picked via
  `--workdir` (or the install dir if `--workdir` is unset).
- `Approve(...)` → `DecisionAllow`. Single-shot CLI; the operator
  authorised the call by typing it. Approval is NOT a substitute
  for runtime policy — capability gates still apply (host imports
  check the manifest's declared capabilities regardless).
- `PriorRead` / `RecordRead` — no-op. Read-dedup belongs to the
  agent loop's read-log; one-shot invocations have no loop.
- Deliberately NO `Runner() sandbox.Runner` extension. Bash uses
  duck-typing (`interface { Runner() sandbox.Runner }`); a host
  that doesn't implement it makes bash fall back to running
  unsandboxed. EP-0005 forbids that, hence the manifest-level
  refusal above.

### HOME-rooted MkdirAll: trust-anchor model

New helper in `internal/workdirpath`:

```go
func MkdirAllUnderUserConfig(path string, perm os.FileMode) error
func OpenRootUnderUserConfig(path string) (*os.Root, error)
```

Algorithm:

1. Compute `anchor = userTrustAnchor(absPath)` — the longest of
   `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`,
   `XDG_CACHE_HOME`, `os.UserHomeDir()` that is a prefix of
   `absPath`.
2. If no anchor matches → fall back to `MkdirAllNoSymlink`. This
   preserves the defense for tests that operate in `/tmp/...`
   (paths that have nothing to do with HOME), and for in-repo
   writes that should still see the strict check.
3. If the anchor doesn't exist as a directory → create it via
   `os.MkdirAll` (no symlink rejection — the chain UP TO the
   operator's anchor is the operator's environment, not
   adversarial). On a fresh container `/var/home` may not exist;
   on Fedora Atomic `/home` is a symlink to `/var/home`. Both are
   operator-supplied path realities, not attacks.
4. Walk anchor → leaf via `MkdirAllNoSymlinkUnder`, which still
   rejects symlinks in any path component below the anchor. So an
   in-user-space attacker who plants a symlink at
   `<state-dir>/memory/` to redirect writes to `/etc` is still
   rejected.

`OpenRootUnderUserConfig` mirrors the same anchor logic for opening
existing trees.

Updated 13 call sites:

- `internal/config/config.go`, `write_defaults.go` (config dir)
- `internal/runtime/session.go` (worktree dir, ×2)
- `internal/audit/key.go`, `minisign.go` (signing keys)
- `internal/memory/store.go`, `context.go`, `session.go`
- `internal/plugins/installed.go`, `state_file.go`, `manifest.go`
- `internal/runtime/session_summary.go`

The strict `MkdirAllNoSymlink` / `OpenRootNoSymlink` stay in place
for `internal/plugins/runtime/host_fs.go` (FS writes from inside
the plugin sandbox — genuinely adversarial input).

### `userTrustAnchor` — what counts as operator-supplied

```go
candidates := []string{}
for env in {XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_STATE_HOME, XDG_CACHE_HOME}:
    if v := os.Getenv(env); v != "":
        candidates = append(candidates, v)
if h, _ := os.UserHomeDir(); h != "":
    candidates = append(candidates, h)
return longest c in candidates such that absPath starts with c+"/"
```

Why these specifically: each is a path the operator picked at OS
or shell-init time. The user's `XDG_DATA_HOME=/srv/foo` is a
deliberate choice; we shouldn't second-guess `/srv` for symlinks.
Same for `~`. Anything outside this set falls through to strict
mode, which protects callers like the in-tmpdir
`TestStoreAppendRejectsParentSymlink` test that genuinely needs
the strict check.

## Migration / rollout

`--with-tool-host` is an additive opt-in flag. Default
`plugin run` behaviour is unchanged: `host.ToolHost == nil` and
bundled tool imports return the documented "no tool runtime
context" error. Plugins that don't import bundled tools (the
authoring template, `htb-cve-lookup`) work identically with or
without the flag.

`MkdirAllUnderUserConfig` is an additive helper. Existing strict
`MkdirAllNoSymlink` callers are unchanged unless explicitly
migrated. The 13 call sites this EP migrates were chosen because
they all operate on HOME-rooted paths and were known to break on
Atomic Fedora; tests that exercise the strict-from-`/` path stay
green because their fixture paths fall outside any trust anchor.

## Failure modes

- **Operator passes `--with-tool-host` with a plugin declaring
  `exec:bash`.** Refused before runtime instantiation with an
  actionable error: "use `stado run` instead". No half-execution.
- **Operator passes `--with-tool-host` without a plugin that uses
  bundled tools.** Flag has no observable effect (host.ToolHost
  is set but the plugin never calls a bundled-tool import). Same
  as not passing the flag.
- **Anchor exists but is owned by another user (multi-tenant
  hosts).** `os.MkdirAll(anchor, 0o700)` fails with EACCES; error
  surfaces to the operator. No silent fallback.
- **`os.UserHomeDir()` returns "" (broken environment).** Anchor
  list falls back to XDG envs only; if those are also empty, the
  fallback strict path applies. Same failure mode the strict
  walker had.

## Test strategy

- New unit test `TestPluginRunToolHost_Surface` in
  `cmd/stado/plugin_run_tool_host_test.go` asserts the host
  satisfies `tool.Host` and returns the expected default
  behaviours (allow approval, no read-dedup, workdir round-trip).
- New integration test `TestPluginRun_WithToolHost_RefusesExecBash`
  builds a fake plugin with `exec:bash` capability, installs it,
  attempts `plugin run --with-tool-host`, asserts the refusal
  error mentions both `exec:bash` and `stado run`.
- End-to-end validation: `webfetch-cached-0.1.0` (the shakedown
  plugin in `~/Dokumenty/htb-writeups/plugins/webfetch-cached/`)
  was run against `https://example.com` with and without
  `--with-tool-host`. Without the flag → documented "no tool
  runtime context" error. With the flag, first call → cache miss
  → real HTTP → cache file written under
  `notes/.cache/webfetch/<sha>.json`. Second call → cache hit, no
  network. Confirmed cache file size, sha-keyed filename, and
  body integrity.
- HOME-rooted MkdirAll: full repo `go test ./...` passes after
  the migration. The pre-existing parent-symlink-rejection tests
  in `internal/memory`, `internal/plugins`, `internal/runtime`,
  `internal/config` continue to pass because their fixture paths
  in `t.TempDir()` fall outside any trust anchor and trigger the
  strict-mode fallback.

## Open questions

- Should we eventually plumb a real `sandbox.Runner` into
  `plugin run` so `exec:bash` plugins can be tested too? Position:
  yes, but as a separate EP. Requires importing chunks of the
  agent runtime (`internal/sandbox`, the policy intersection
  machinery) into `cmd/stado/plugin_run.go`, which is a bigger
  scope change than this EP wants to absorb.
- Should `userTrustAnchor` accept additional anchors (`$TMPDIR`?
  `$STADO_CONFIG_DIR`?) Position: not yet. Keep the surface
  minimal until concrete user reports show a need.
- Should the EP-0027 helpers (`LooksLikeRepoRoot`, `FindRepoRoot`)
  also gain a "trust anchor" variant for the same Atomic Fedora
  reason? Position: not needed — those don't create paths, they
  walk for `.git` from CWD; the strict-from-`/` check there
  rejects bogus `.git` dirs (the EP-0027 fix), and HOME being a
  symlink doesn't break the walk semantically.

## Decision log

### D1. Refuse `exec:bash` rather than supply an unsandboxed `Runner`

- **Decided (v0.26.0):** under `--with-tool-host`, plugins declaring
  `exec:bash` (or `exec:shallow_bash`) are refused at runtime
  setup time with an actionable error.
- **Alternatives:** ship a no-op runner (bash runs unsandboxed);
  add a `--allow-unsafe-bash` escape hatch; plumb the agent's
  `sandbox.Runner` into `plugin run`.
- **Why:** EP-0005 §"Non-goals" forbids substituting human
  approval for runtime policy. A no-op runner trivially violates
  that. An escape-hatch flag is the same violation in nicer
  packaging. The proper fix (plumb a real runner) is a bigger
  scope; refusing-with-pointer-to-`stado-run` is honest about the
  current limitation without weakening the security model.

- **Resolved (v0.27.0):** the third alternative — plumb a real
  runner — landed. `cmd/stado/plugin_run.go` now wires
  `sandbox.Detect()` into `pluginRunToolHost.Runner`, so
  `exec:bash` plugins run under the same bwrap / sandbox-exec
  confinement as the agent loop. The refusal is now narrowed to:
  *manifest declares `exec:bash` AND `Detect()` returns NoneRunner*.
  EP-0005 still holds — we don't substitute the operator's CLI
  invocation for a real syscall filter; we just stop refusing
  cases where a real syscall filter IS available. NoneRunner hosts
  (Linux without bwrap, macOS without sandbox-exec, Windows
  always) still get the explicit refusal with an install hint.

### D2. Trust anchor = HOME ∪ XDG_*_HOME, not arbitrary paths

- **Decided:** `userTrustAnchor` reads only `XDG_*_HOME` env vars
  and `os.UserHomeDir()`; nothing CLI-driven, nothing config-file
  driven.
- **Alternatives:** read a `trust_paths` field from
  `config.toml`; honour a `STADO_TRUST_PATHS` env var.
- **Why:** the relevant trust signal is "this is the path the
  operator's OS/shell environment said to use". Letting users
  add arbitrary trusted paths via config invites the same kind
  of footgun that EP-0005 §"Non-goals" rejects (config as a
  capability extension).

### D3. Anchor that doesn't exist gets `os.MkdirAll`, not the no-symlink walker

- **Decided:** when the trust anchor itself doesn't exist, create
  it via `os.MkdirAll(anchor, perm)` (which follows symlinks in
  the chain UP to anchor). Then walk no-symlink from anchor down.
- **Alternatives:** require the anchor to exist (return error);
  walk the chain UP from anchor to find a longer existing
  ancestor, then walk no-symlink from there.
- **Why:** the anchor's own ancestors are the operator's
  environment; we already trusted them by treating XDG / HOME as
  trust roots. Treating them as adversarial-walk targets just to
  create one missing intermediate is internally inconsistent. The
  test case `TestConfigInit_WritesPrivateFile` exercises this:
  test sets `XDG_CONFIG_HOME=<tmp>/config` (doesn't exist),
  expects config dir creation to succeed; it does, with the
  `os.MkdirAll(anchor)` path.

### D4. Migrate 13 call sites, not just the reported one

- **Decided:** sweep every HOME-rooted `MkdirAllNoSymlink` /
  `OpenRootNoSymlink` caller to use the new variant.
- **Alternatives:** fix only `internal/config/config.go` (the
  reported failure); add a deprecation warning when others fail
  to nudge users.
- **Why:** the user's `/home`-symlink scenario breaks all 13 the
  same way. Fixing only one means the binary errors out one step
  later (state dir creation in `OpenSession`, audit key dir, etc.)
  on the same systems. Sweeping is the same code change repeated;
  the per-site review confirmed each is HOME-rooted (no surprises).

### D5. Multi-probe regression test, not single `--version` smoke

- **Decided:** `hack/test-on-fedora-atomic.sh` fans out across
  multiple boot-touching commands inside a `bwrap` namespace that
  simulates `/home → /var/home`. Adding a probe is one line in a
  `PROBES=()` array.
- **Alternatives:** smoke-test only `stado --version` (what
  v0.26.0 release verified manually, prior to the test existing);
  defer the test until a CI matrix can run it on a real Atomic VM.
- **Why:** D4's "swept 13 sites" claim was true for *the patch
  series*, not for the codebase as a whole. The first regression
  test only ran `--version`, which prints from a Cobra hook before
  any FS work — so it passed even with the bug present, and the
  v0.26.0 release shipped with most boot paths still broken. v0.26.1
  switched the probe to `config-path` (config.Load triggers
  MkdirAll), which uncovered the system-prompt-template tree
  (4 missed call sites). v0.26.2 fanned out to `doctor / session
  list / audit verify` which uncovered the audit-key + sidecar
  tree (2 more files). v0.26.3 then statically audited every
  remaining strict-from-/ caller, classified each by path source,
  and migrated the 11 boot-time HOME-rooted ones in a single
  commit. The lesson — codified into the test itself — is that
  every introduction of a new safety primitive must be backed by
  a test that exercises **the actual boot surface**, not the
  cheapest possible invocation. New boot-touching subcommands
  added in the future should add a corresponding `PROBES=()` entry
  the same iteration.

### D6. Wrappers (named `*UnderUserConfig`), not composed `Expand + Strict`

- **Decided:** keep the wrapper API surface
  (`MkdirAllUnderUserConfig`, `OpenRootUnderUserConfig`,
  `ReadRegularFileUnderUserConfigLimited`) instead of exposing a
  single `ExpandOperatorLayout(path) → realPath` helper that
  callers compose with the strict primitives.
- **Alternatives:** a single `ExpandOperatorLayout` helper +
  call-site composition (`real, _ := Expand(p); MkdirAllNoSymlink(real)`).
- **Why:** internally the wrappers ARE composed shape — they call
  `os.OpenRoot(anchor)` (which OS-resolves operator-layout
  symlinks like `/home → /var/home` via path resolution) and then
  `MkdirAllNoSymlinkUnder` / `OpenRootNoSymlinkUnder` for the
  strict walk under the anchor. With ~25+ call sites across
  config/runtime/audit/plugins/memory/state/git/tasks/tui/...,
  the wrapper shape pays for itself: the name encodes the threat
  model in the call site, and a developer can't accidentally skip
  the expand step (the failure mode of the composed shape) and
  silently re-introduce the boot bug. The composed shape would be
  preferable if the trust-model decision varied per call site or
  if mix-and-match (sometimes resolve, sometimes not) was needed —
  here the call pattern is uniform.

## Related

- EP-0005: capability-based sandboxing — defines the
  "approval ≠ policy" boundary that motivates the `exec:bash`
  refusal.
- EP-0006: signed WASM plugin runtime — defines the host imports
  that `--with-tool-host` newly enables.
- EP-0027: repo-root discovery — sibling EP from the same
  shakedown sequence; both arose from "the strict
  `os.Stat(.git)` / strict no-symlink walk are too lenient or too
  strict for one of their callers".
- Shakedown patches: branch `shakedown-r3`.
- Dogfood writeup: `~/Dokumenty/dogfood/stado/2026-05-04-shakedown-round3-tool-host.md`.
