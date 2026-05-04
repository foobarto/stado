---
ep: 0029
title: Config-introspection host imports — `cfg:*` capability vocabulary
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Companion to v0.26.0 + the lean-core architectural steer.
see-also: [0002, 0005, 0006, 0028]
---

# EP-0029: Config-introspection host imports — `cfg:*` capability vocabulary

## Problem

stado's design north star (EP-0002, lean core) wants most
functionality in WASM plugins. Operator-tooling commands like
`stado plugin doctor`, `stado plugin gc`, and `stado plugin info`
fit that model conceptually — they read manifests, classify
capabilities, render reports, gc orphans — none of it requires
access to the runtime's hot path. They could all live as plugins
with `fs:read:<state-dir>/plugins/` (or `fs:read+write:` for gc).

The blocker is a missing primitive: **plugins have no way to learn
what `<state-dir>` is.** The state directory varies by operator:
default `$XDG_DATA_HOME/stado/` falls back to `~/.local/share/stado/`,
but `XDG_DATA_HOME` overrides; users on Atomic Fedora may have
`/var/home/<user>/...` paths via `/home → /var/home`. A plugin
that hardcodes `~/.local/share/stado/plugins` is broken on every
non-default install; a plugin that takes the path as an argument
shifts the awkward bootstrap onto operator scripts.

The same problem applies to other config introspection (`config_dir`,
`config_path`, `worktree_dir`, `user_repo_root`). Each is a small
read-only string the host already knows; a plugin that knows it can
do its job; without it, the operator-tooling has to live in core.

## Goals

- Introduce a `cfg:*` capability vocabulary for read-only
  introspection of stado's configured paths.
- Ship the first concrete capability — `cfg:state_dir` — that
  unblocks operator-tooling plugins (doctor, gc, info migrations).
- Keep the surface tightly bounded: `cfg:state_dir` returns one
  string; nothing more. Adding more is a fresh capability per
  field, opted into individually.
- Match the existing host-import shape (write-into-caller-buffer,
  -1 on truncation, capability-gated registration so unauthorized
  plugins fail at link time rather than runtime).

## Non-goals

- A plugin-writable config surface. That's a much wider attack
  surface — config writes can change provider endpoints, default
  models, plugin trust pins. Out of scope.
- Bulk config dumps (`cfg:dump`). Each capability returns ONE
  field; dump-everything is a coarser surface than EP-0005's
  capability discipline allows.
- Process-level introspection (`cfg:cwd`, `cfg:hostname`,
  `cfg:env`). Those are operator environment, not stado config;
  if a plugin needs them, it should declare them via WASI
  primitives, not stado capabilities.

## Design

### Capability vocabulary

```
cfg:<field-name>
```

Each `cfg:<name>` capability:

- maps to one bool field on `runtime.Host` (`CfgStateDir`,
  `CfgConfigDir`, etc.),
- maps to one host import (`stado_cfg_<name>`),
- returns one string (the configured value of that field).

The host import only registers when the bool is true. Unauthorized
plugins fail at wasm link time (`unknown import:
stado_cfg_state_dir`), not at runtime invocation — operators who
use `stado plugin doctor` to inspect a plugin will see the
capability and the failure mode together.

### `cfg:state_dir` (concrete capability shipped with this EP)

```
//go:wasmimport stado stado_cfg_state_dir
func stadoCfgStateDir(bufPtr, bufCap uint32) int32
//   → bytes written to buf, or -1 on truncation / oversize value
```

Behaviour:

- When `host.CfgStateDir` is false (no capability): the import is
  not exported. Plugin link fails.
- When `host.CfgStateDir` is true and `host.StateDir` is set: the
  import writes the value to the buffer and returns the byte
  length.
- When `host.CfgStateDir` is true and `host.StateDir` is empty
  (caller didn't populate; rare): returns 0. Plugin sees an empty
  string and can fall back to whatever degraded path it has.
- When the value exceeds `bufCap` or
  `maxPluginRuntimeCfgValueBytes` (4 KiB): returns -1.

### Host-side wiring

Each top-level caller of `pluginRuntime.NewHost` is responsible for
populating `host.StateDir` from its own `cfg.StateDir()`:

- `cmd/stado/plugin_run.go` — populates from `config.Load()`.
- `internal/runtime/bundled_plugin_tools.go` — populates from the
  embedded runtime's config.
- `internal/runtime/plugin_overrides.go` — same.

The set is unconditional (cheap string copy); the host import is
only registered when the manifest declares the capability, so this
is a no-op for plugins that don't.

### Future capabilities

Each is its own EP-0029-shape additive change — bool field on
Host, register function in `host_cfg.go`, parser case in
`NewHost`, host caller populates the value. Likely candidates:

| Capability | Returns |
|-----------|---------|
| `cfg:config_dir` | `$XDG_CONFIG_HOME/stado/` (or fallback) |
| `cfg:config_path` | `<config_dir>/config.toml` |
| `cfg:worktree_dir` | `<state-dir>/worktrees/` |
| `cfg:user_repo_root` | the host's `host.Workdir`'s repo root |
| `cfg:plugin_install_dir` | `<state-dir>/plugins/` |

None ship in this EP. Each would be its own EP-extension when
operator-tooling plugins demand them.

## Migration / rollout

Pure additive change. No existing capability shapes change. No
existing manifest fails to load (the parser falls through unknown
`cfg:<name>` capabilities silently).

The first concrete consumer is the future migration of `stado plugin
doctor` and `stado plugin gc` from `cmd/stado/` to a bundled plugin
under `plugins/default/` or `plugins/examples/`. That migration is
out of scope for this EP — it's a follow-up that proves the
capability works end-to-end on a real consumer.

## Failure modes

- **Capability declared but `host.StateDir` not populated.** Plugin
  reads "" and falls back to whatever it has. Logged at host warn
  level so operators notice. Non-fatal — the plugin's degraded path
  may still produce useful output.
- **Value exceeds plugin's buffer.** Plugin retries with a bigger
  buffer (the standard pattern for write-into-buffer host imports).
  In practice state-dir paths are well under 1 KiB and the
  4 KiB cap is generous.
- **Plugin declares `cfg:state_dir` but never calls
  `stado_cfg_state_dir`.** No-op. The capability declaration alone
  costs nothing at runtime.
- **Operator declines to trust a plugin signer that requests
  `cfg:state_dir`.** Plugin install refuses; same as any other
  capability. The operator's review at install time is the
  authorisation gate — `cfg:state_dir` itself doesn't grant any
  capability the operator hasn't authorised.

## Test strategy

- `internal/plugins/runtime/host_test.go` `TestNewHost_ParsesCapabilities`
  extended with `cfg:state_dir` + assertion on `h.CfgStateDir`.
- New `internal/plugins/runtime/host_cfg_test.go`:
  - `TestRegisterCfgImports_Smoke` — installs the cfg imports
    against a runtime when the cap is declared; verifies clean
    registration.
  - `TestRegisterCfgImports_NotRegisteredWithoutCap` — verifies
    the import is NOT registered when the cap is absent (otherwise
    a plugin without the cap could still link the import).
- End-to-end consumer test deferred to the doctor/gc migration EP
  (not yet written). Until then, the unit tests above cover the
  primitive's contract.

## Open questions

- Should `cfg:*` capabilities support a glob form (`cfg:*` to grant
  all)? Position: **no**. Capability discipline (EP-0005) wants
  each fact to be opted into explicitly. A `cfg:*` glob lets a
  signer's plugin learn fields the operator didn't think they were
  granting.
- Should the host imports return errors for "value not configured"
  vs "capability not granted"? Currently both surface as -1 from
  the host import (capability not granted = link failure at
  instantiate; value not configured = 0-byte read or runtime -1).
  Position: keep separate. Link failure on no-capability is the
  right loud signal; runtime degradation on no-value is the
  right quiet signal.
- Should we ship `cfg:config_dir` alongside `cfg:state_dir`?
  Position: **no, not in this EP**. YAGNI — the only operator-tooling
  candidate that needs config-dir specifically is a hypothetical
  `stado config doctor` that doesn't exist yet. Ship `cfg:state_dir`
  alone, prove the pattern via the doctor/gc migration, then add
  more capabilities as concrete consumers demand them.

## Decision log

### D1. Each `cfg:*` field is its own capability, no globs

- **Decided:** `cfg:state_dir`, `cfg:config_dir` (future), etc.
  are each a separate capability. No `cfg:*`, no `cfg:read:*`.
- **Alternatives:** glob; tiered (`cfg:read` / `cfg:read:state_dir`);
  bulk dump.
- **Why:** EP-0005 §"Goals" wants capabilities expressed as explicit
  surfaces. A glob hides which fields the plugin actually reads;
  the operator's review at install time is meaningless if the
  capability tells them "could read anything".

### D2. Host import shape mirrors `stado_log` / `stado_fs_read`

- **Decided:** `stado_cfg_<name>(bufPtr, bufCap) → int32`. Plugin
  allocates the buffer; host writes; returns byte length or -1 on
  truncation.
- **Alternatives:** allocate-host-side and return a (ptr, len) tuple;
  use stado's existing JSON-payload shape.
- **Why:** consistency with the existing read-style imports. The
  caller-allocates pattern keeps memory management on the plugin
  side, which simplifies the host code and avoids "who frees this?"
  questions across the wasm boundary.

### D3. Capability-not-declared = link failure, not runtime -1

- **Decided:** `registerCfgImports` only registers
  `stado_cfg_state_dir` when `host.CfgStateDir` is true. Plugins
  without the cap fail at wasm link time.
- **Alternatives:** always register; check the cap at runtime and
  return -1.
- **Why:** loud failures at instantiation are easier for operators
  to debug than silent -1s during a turn. A plugin that imports a
  capability it didn't declare is a manifest bug; surface it
  immediately.

### D4. `host.StateDir` populated unconditionally by callers, even
when cap is absent

- **Decided:** the top-level callers (`cmd/stado/plugin_run.go`,
  the bundled-tool wrappers) always populate `host.StateDir` from
  their config. The host import is gated by the cap, but the
  underlying value is always present.
- **Alternatives:** populate only when the manifest declares the
  cap (saves a string copy).
- **Why:** simpler control flow. The string copy is free. Avoids a
  class of bugs where a plugin declares the cap, the host caller
  forgets to populate, and the plugin sees "" — better to always
  populate and let the cap-gated import handle the policy.

## Related

- EP-0002 — All Tools as WASM Plugins. Lean-core north star that
  motivates `cfg:*`: operator-tooling that currently lives in core
  (`plugin doctor`, `plugin gc`) can move to plugins once they can
  read `<state-dir>`.
- EP-0005 — Capability-Based Sandboxing. The discipline that
  `cfg:<field>` follows (no globs, explicit per-field opt-in,
  enforcement in the runtime not in prompts).
- EP-0006 — Signed WASM Plugin Runtime. The trust model that
  bounds who can ship a `cfg:state_dir`-declaring plugin
  (operator must pin the signer).
- EP-0028 — `plugin run --with-tool-host` + HOME-rooted MkdirAll.
  Sibling capability vocabulary work; same architectural pattern
  (small, opt-in, well-documented refusal modes).
