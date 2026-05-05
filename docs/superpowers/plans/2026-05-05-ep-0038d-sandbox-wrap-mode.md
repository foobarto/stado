# EP-0038d: Sandbox Wrap-Mode Re-Exec — Retroactive Plan

> **Status:** Retroactive — implementation already landed on `main` in
> commit `21143d4` (`feat(ep-0038c/d): agent surface (FleetBridge +
> stado_agent_* imports + agent wasm module) + sandbox wrap-mode re-exec`).
> This document exists to keep the plan archive aligned with the code,
> per the `2026-04-23-retroactive-eps-design.md` convention.
>
> **Owner:** Codex
> **Status:** Implemented
> **Spec:** `docs/eps/0038-abi-v2-bundled-wasm-and-runtime.md` §I

**Goal:** Implement the `[sandbox] mode = "wrap"` profile from EP-0038 §I:
when set, stado detects an installed sandboxing wrapper (`bwrap` →
`firejail` → `sandbox-exec`), builds a wrapper invocation matching
`[sandbox]` config (binds, network, env allow-list, proxy), and re-execs
itself under that wrapper. Recursion is prevented via the
`STADO_REWRAPPED=1` env var. `mode = "external"` validates that the
operator already started stado inside a wrapper.

**Architecture:** Single-file implementation in `internal/sandbox/wrap.go`.
`WrapConfig` mirrors the relevant `[sandbox]` fields without importing
the config package (cycle-free). `MaybeRewrap` is called early in `main`
(before any tool execution). Per-wrapper invocation builders translate
`WrapConfig` into the wrapper's CLI surface.

**Tech Stack:** Go, `os/exec`, the system's wrapper binary
(bwrap/firejail/sandbox-exec).

---

## File Map (as actually landed)

| File | Status | Purpose |
|---|---|---|
| `internal/sandbox/wrap.go` | Created | `MaybeRewrap`, `WrapConfig`, per-wrapper builders |
| `internal/sandbox/runner.go` | Modified | `Detect()` order: bwrap → firejail → sandbox-exec → none |
| `internal/sandbox/runner_linux.go` | Modified | Linux-specific wrapper detection |
| `internal/sandbox/runner_darwin.go` | Modified | macOS-specific wrapper detection |
| `internal/sandbox/sbx_profile.go` | Created | macOS sandbox-exec profile generator |
| `internal/config/config.go` | Modified | `[sandbox]` config section — `Mode`, `Wrap.BindRO`, `Wrap.BindRW`, `Wrap.Network`, `HTTPProxy`, `AllowEnv`, `RefuseNoRunner`, `Wrap.Runner` |
| `cmd/stado/main.go` | Modified | Calls `sandbox.MaybeRewrap` before regular cobra dispatch |

---

## Tasks (retroactive — done)

### Task 1: `[sandbox]` config schema
- ✅ `internal/config/config.go:Sandbox` — fields per NOTES + EP-0038 §I.
- ✅ `RefuseNoRunner` toggles between warn-loud-and-run vs hard-refuse
  when no wrapper is available.
- ✅ Subsection `[sandbox.wrap]` for binds + network + runner override.

### Task 2: `MaybeRewrap` re-exec primitive
- ✅ `internal/sandbox/wrap.go:MaybeRewrap` — switch on `cfg.Mode`:
  - `"off"` / `""` → no-op.
  - `"external"` → if not already wrapped → error with install hint.
  - `"wrap"` → if not already wrapped → detect wrapper, build cmd, exec.
- ✅ `STADO_REWRAPPED=1` env var prevents recursion. `looksWrapped()`
  is a defense-in-depth check (e.g. detects bwrap-injected
  `BWRAP_USER_NS` paths).

### Task 3: Wrapper invocation builders
- ✅ `bwrap` builder: `--bind / --ro-bind` from config, `--unshare-net` for
  `network = "off"`, `--share-net` for `"host"`, mount-namespace defaults.
- ✅ `firejail` fallback: best-effort translation of bind config.
- ✅ macOS `sandbox-exec` profile via `sbx_profile.go` SBPL emitter.

### Task 4: Capability subset propagation (`AllowEnv`, `HTTPProxy`)
- ✅ Env scrubbing: only `cfg.AllowEnv` survives the rewrap (plus stado's
  own essential vars).
- ✅ `HTTPProxy` injected as `HTTP_PROXY` / `HTTPS_PROXY` in the child env.

### Task 5: Tests
- ✅ `internal/sandbox/runner_linux_test.go` covers Linux wrapper detection.
- ✅ `internal/sandbox/seccomp_linux_test.go` covers seccomp + landlock.
- ✅ Smoke test in `wrap.go` defense-in-depth: rejects nested rewrap.

---

## Open follow-ups (still needed)

- **`/sandbox` slash command** — landed (visible in `model_commands.go`),
  but the output renderer is bare. NOTES locked: *"Current sandbox state:
  mode, proxy, env allow-list, namespace status."* Verify each field is
  printed.

- **Sandbox-aware `plugin doctor`** — locked in NOTES: *"check declared
  caps vs `[sandbox]` constraints"*. Not implemented; `plugin doctor`
  doesn't cross-reference sandbox config.

- **Network namespace + slirp4netns / pasta** for `network = "namespaced"`
  — `pasta_linux.go` exists but the integration is incomplete; namespaced
  network without proxy is currently equivalent to `network = "off"` for
  most cases.

## Verification

```bash
# 1. wrap.go compiles + has tests.
go test ./internal/sandbox/... -run TestWrap

# 2. /sandbox slash present.
grep -n '"/sandbox"' internal/tui/model_commands.go

# 3. RefuseNoRunner gate works (covered by
# TestPluginRun_WithToolHost_ExecBashGate_NoSandbox in cmd/stado).
go test ./cmd/stado/... -run TestPluginRun_WithToolHost
```
