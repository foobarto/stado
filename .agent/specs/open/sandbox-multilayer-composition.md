# Multi-layer sandbox composition (parked from Phase 1.2)

**Parked by:** Phase 1.2 of the 2026-Q2 refactor program.
**Status:** Open / parked. No work scheduled.

## What ships

A production runner (or runner composition) that stacks
`landlock + seccomp + bwrap` over a single target subprocess on
Linux, plus a contract test verifying composed denial — a
write-deny via landlock, a syscall-deny via seccomp, and PID/IPC
isolation via bwrap, all on the same `*exec.Cmd`.

## Why this isn't done today

`internal/sandbox/runner_linux.go::detectList` returns only
`[BwrapRunner, NoneRunner]`. `landlock_linux.go` and
`seccomp_linux.go` exist as standalone modules but are not wired
into the production runner path:

- `BwrapRunner` does not invoke landlock or seccomp; it relies on
  bwrap's bind-mount semantics for FS isolation and on
  `--unshare-net` for net deny. The source comment in
  `runner_linux.go:15` flags landlock/seccomp integration as
  follow-up work.
- A dedicated `LandlockRunner` / `SeccompRunner` does not exist;
  the per-module tests
  (`landlock_linux_test.go` / `seccomp_linux_test.go`) exercise
  the underlying syscalls in isolation, not against a real
  target binary.

Phase 1.2 of the refactor program ships only contract tests for
`BwrapRunner` and `NoneRunner` because writing a composition test
today would require *writing the integration first* — which
violates the program's "no behaviour changes" non-goal.

## Scope when this lands

- A `composedRunner` (or extension to `BwrapRunner`) that:
  1. Constructs the bwrap command line as today.
  2. Wraps it so the spawned process has landlock rulesets
     applied via `prctl(PR_SET_NO_NEW_PRIVS)` + landlock syscalls
     before exec, scoped to `Policy.FSRead` / `Policy.FSWrite`.
  3. Applies a seccomp BPF program restricting syscalls to a
     defined safe set, plus the network-restriction rules
     matching `Policy.Net`.
- A composition contract test asserting:
  - FS-write denied via landlock under bwrap → EACCES.
  - Net-deny via seccomp + landlock-allow → ECONNREFUSED or
    EPERM (verify which the runner currently maps to).
  - "Composed-but-empty" → call succeeds (negative control).
- Skip discipline: per-layer skip if kernel doesn't support
  landlock or seccomp. No false-fail in CI.

## Out of scope

- Windows composition (job objects).
- macOS composition (sandbox-exec already does FS + net in one
  layer; nothing to compose).

## Why it's worth doing eventually

`bwrap` alone gives FS + net isolation but not syscall-level
defence. A plugin author can run an obscure syscall (e.g.
`io_uring`, `bpf`) inside bwrap that bwrap doesn't filter.
seccomp + landlock plug those gaps. The current threat model
treats wasm sandboxing as the primary guard for plugins; the exec
runner is the secondary guard for shell/native invocations
launched *from* plugins. Composition closes the gap when the
exec path is the attack surface.

## Pre-conditions

- A user-visible policy axis that exposes "I need composition" —
  today there's no plugin manifest field for it.
- Decision on whether `LandlockRunner` is a separate runner type
  selected by `detectList` over `BwrapRunner`, or whether bwrap
  gains optional landlock/seccomp wrapping behind a config flag.

## Related

- Phase 1.2 of `docs/superpowers/plans/2026-05-07-refactor-program.md`
- `internal/sandbox/runner_linux.go` (line 15-19 comment).
- `internal/sandbox/landlock_linux.go`, `seccomp_linux.go`
