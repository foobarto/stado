# Code-quality refactor program — 2026-Q2

**Branch:** `worktree-refactor+quality-2026-q2`
**Worktree:** `.claude/worktrees/refactor+quality-2026-q2`
**Status:** Drafted 2026-05-07; execution pending
**Author:** Bartosz Ptaszynski <bartosz@foobarto.me>

A focused code-quality refactor sweep: backfill load-bearing test
coverage, then perform three structural splits (workdirpath, TUI Model,
TUI Update handlers), then three contained refactors (config
validation, plugin tool schemas, plugin host bridge wiring). All work
lives on one long-lived branch with five merge checkpoints — each
checkpoint independently green and reviewable.

No behaviour changes. No new EPs. No new features. If a phase grows
into a behaviour change or earns its way into an EP, that's a signal
to stop and re-scope before continuing.

---

## Goals

- Reduce review-time friction in the tightest packages (`internal/tui/`,
  `internal/workdirpath/`).
- Backfill the test gaps that today let bridge / sandbox / subagent
  regressions slip through to integration.
- Set up `internal/workdirpath` for EP-0040 (managed-inference manager
  writes lots of fs paths) without spreading the old API.
- Eliminate the duplicated patterns called out in the 2026-05-07
  refactor analysis: validation boilerplate, schema literals, bridge
  wiring sprawl.

## Non-goals

1. **No behaviour changes.** A refactor that "fixes a bug as a side
   effect" gets parked into a separate fix plan or EP.
2. **No EPs.** Operator-locked. Plan files only.
3. **No expansion of public API surface (end-state).** All
   structural splits reduce or hold steady measured at phase end.
   *Acknowledged trade-off:* Phase 2.1 (A2) temporarily grows the
   `workdirpath` exported surface during the wrapper-window
   migration — `Resolver` lands alongside the legacy 23 functions
   before legacy is deleted. Net surface at end of A2 shrinks
   versus today; intermediate commits intentionally do not.
4. **No new dependencies.** All work uses stdlib + existing imports.
5. **No "while I'm here" cleanup** beyond what each phase explicitly
   scopes. Smaller wins go in the addendum at the end of this plan,
   tackled last or rolled into adjacent commits when natural.
6. **No CI rule changes.** Lint config, race-detector flags, coverage
   thresholds — all unchanged. If the refactor needs new CI behaviour,
   open a separate plan.

## Program summary

| #   | Phase                | Item                                                  | Cost | Status |
|-----|----------------------|-------------------------------------------------------|------|--------|
| 1.1 | Test coverage        | Plugins/runtime bridges — contract tests              | M    | Pending |
| 1.2 | Test coverage        | Sandbox runner contract test (wired layers only)      | S    | Pending |
| 1.3 | Test coverage        | `internal/runtime/fleet_bridge.go` lifecycle tests    | M    | Pending |
| 2.1 | Tier A — A2          | `workdirpath` API simplification (`Resolver` type)    | M    | Pending |
| 2.2 | Tier A — A1          | `Model` struct + `model_render.go` consolidation      | L    | Pending |
| 2.3 | Tier A — A3          | `model_update`/`commands`/`stream` dispatcher split   | L    | Pending |
| 3.1 | Tier B — B1          | `config.go` validation extraction                     | S    | Pending |
| 3.2 | Tier B — B2          | `bundled_plugin_tools.go` schema builder              | S    | Pending |
| 3.3 | Tier B — B3          | Bridge lifecycle/wiring unification                   | M    | Pending |

Total: 9 items. Cost: 5×M + 2×L + 2×S, ballpark a few weeks of focused
work. The plan does not commit to a wall-clock duration; each phase
ships when it ships.

## Cross-cutting

### Branch hygiene

- Working branch: `worktree-refactor+quality-2026-q2`.
- **48-hour main sync.** When `main` moves, rebase or merge within
  ~48 hours. Don't let the delta grow. If a sync produces a non-
  trivial conflict in active code, the conflicting phase pauses
  until resolved.
- **Cherry-pick permission for Phase 2.1 (A2).** Once `workdirpath`
  reaches a stable, reviewed state with all callers migrated, A2's
  commits may be cherry-picked to `main` ahead of the rest of the
  program. EP-0040 (managed inference) is downstream of `workdirpath`
  and benefits from the cleaner API.
- All other phases land via the merge-checkpoint flow below.

### Merge checkpoints

Five points where the long-lived branch produces a green, reviewable
slice. Each checkpoint is a logical PR boundary, even if multiple
checkpoints batch into one merge.

1. **End of Phase 1** (after 1.1+1.2+1.3). Tests-only diff; trivial to
   review; no production code changed.
2. **End of Phase 2.1 (A2)**. workdirpath simplification + caller
   migration. Behavioural surface unchanged; the public-API surface
   shrinks.
3. **End of Phase 2.2 (A1)**. Model + render consolidation. Largest
   single mechanical churn. Visible only in diff; runtime behaviour
   identical.
4. **End of Phase 2.3 (A3)**. Update handler split into
   `handler_*.go` files in `package tui`; smallest semantic risk
   in Tier A.
5. **End of Phase 3** (after 3.1+3.2+3.3). Tier-B sweep. Tests from
   1.1 protect the B3 unification.

### Commits

- One logical change per commit. No mixing mechanical moves with
  semantic fixes.
- Commit-message convention: existing project style
  (`refactor(scope): summary`). Per-commit scope examples:
  `refactor(workdirpath): introduce Resolver as wrapper around legacy API`,
  `test(plugins/runtime): contract test suite for Session bridge`.
- Each commit must compile and pass `go test ./... -race` for the
  packages it touches.

### Verification

- **Per commit:** `go build ./... && go vet ./...`.
- **Per merge checkpoint:** `go test ./... -race && golangci-lint run`.
- **Smoke check at each Tier-A boundary:** `stado --help` and
  `stado run --help` exit 0 (catches wiring breaks the test suite
  doesn't catch).
- No coverage-threshold gate; existing CI rules apply.

---

## Phase 1 — Test coverage

The brief: add **contract tests** that survive any plausible refactor
of the same code, not shape tests that lock current internals in.
A contract test asserts an invariant the caller depends on; a shape
test asserts a method-set or field layout. The B3 refactor at the end
of the program is the worked example — the bridge contract tests in
Phase 1.1 must remain valid after B3 unifies wiring.

### 1.1 Plugins/runtime bridges — contract tests

**What ships.** Per-bridge test files under
`internal/plugins/runtime/` that exercise the four contracts every
bridge satisfies, plus per-bridge specifics. Builds confidence for
the B3 refactor and gives bridge-only bugs a place to surface as
unit failures rather than session timeouts.

**Bridges in scope.** Per `internal/plugins/runtime/`:
- `host_session.go` — SessionBridge.
- `host_memory.go` — MemoryBridge.
- `host_ui.go` — Approval and Choice bridges.
- `host_agent.go` — FleetBridge.

**The four contracts** (every bridge asserts these):

1. **Capability gate.** When the calling plugin's manifest doesn't
   carry the required capability, the bridge call returns the
   capability-denied error path *without* invoking the underlying
   stado primitive. Mock the primitive; assert it isn't called.
2. **Nil-bridge behavior.** Host constructed with the bridge field
   nil must return a defined error (not nil-deref panic) on bridge
   calls. Today this is implicit; assert it.
3. **Exact forwarding.** When capability + bridge are present,
   the bridge forwards arguments unchanged to the primitive and
   returns its result unchanged. Use a primitive fake that records
   args verbatim.
4. **Cancel propagation.** When the caller's `context.Context` is
   cancelled, the bridge returns `ctx.Err()` and the primitive
   call (if started) sees the cancellation. Use a blocking
   primitive fake; cancel mid-call; assert.

**Per-bridge specifics** (in addition to the four contracts).
*Updated 2026-05-07 to match the actual interfaces at HEAD; the
prior wording listed methods that don't exist on these bridges.*
- **SessionBridge.** `NextEvent` (ctx-bound poll), `ReadField`
  (named stringly-typed fields: `message_count` / `token_count` /
  `session_id` / `last_turn_ref` / `history`), `Fork`,
  `InvokeLLM`. Plus `stado_llm_invoke` host import. Assert the
  forwarded `LLMInvokeOpts` carry every per-call field
  (Persona / Model / System / MaxTokens / Temperature) and that
  the host's `llmTokensUsed` budget counter increments by the
  bridge-reported `tokensUsed`.
- **MemoryBridge.** `Propose` / `Query` / `Update` (note: 3
  methods, not the prior plan's 4). Verify forwarded payload
  bytes are unchanged vs. what the import staged into wasm
  memory; assert the bridge call reaches the bridge for every
  cap path.
- **ApprovalBridge.** Outcome encoding (allow=1 / deny=0 /
  bridge-error→-1 / cap-deny→-1 / nil-bridge→-1). Test all five
  paths.
- **ChoiceBridge.** Single-select / multi-select / cancelled
  responses round-trip through wasm memory. cap-deny and
  nil-bridge return *negative* bytes-written via
  `encodeToolSidePayload` (not -1) — the message text staged at
  the response buffer is part of the contract; assert it.
- **FleetBridge.** Spawn / list / send-message / read-messages /
  cancel. Assert that the full `AgentSpawnRequest` (Prompt,
  Model, Async, Ephemeral, ParentSession, AllowedTools,
  SandboxProfile, Persona) reaches the bridge unchanged. Cancel
  contract applies to every method that takes ctx (i.e. all
  five).

**Files added.**
- `internal/plugins/runtime/host_session_bridge_test.go`
- `internal/plugins/runtime/host_memory_bridge_test.go`
- `internal/plugins/runtime/host_ui_approval_bridge_test.go`
- `internal/plugins/runtime/host_ui_choice_bridge_test.go`
- `internal/plugins/runtime/host_agent_bridge_test.go`
- `internal/plugins/runtime/bridge_testharness_test.go` — shared
  fakes + capability-gate helper. Naming convention: `*_bridge_test.go`
  for per-bridge suites; harness file is `bridge_testharness_test.go`.

**Approach.**
- Build out `bridge_testharness_test.go` first: tiny `newTestHost(t)`
  helper that wires a `Host` with chosen bridges and a known
  capability set. A capability-gate helper that takes a manifest
  and returns "with cap" / "without cap" host pairs.
- Per-bridge: stub the primitive (e.g. SessionBridge calls into a
  `sessionRecorder`); assert the four contracts; then add per-bridge
  specifics.
- Where bridges return `int32`-encoded errors (today's convention
  for some), encode the contracts in terms of the user-visible
  result (approved/denied), not the int32 — that survives B3.

**Verification.**
- [ ] `go test ./internal/plugins/runtime/... -race` passes.
- [ ] All five bridges have all four contract tests.
- [ ] No bridge test references a typed Host field; all access
      goes through the bridge interface or constructor.
- [ ] `go test ./internal/plugins/runtime/... -count=10 -race` for
      flake check.

### 1.2 Sandbox runner contract test (wired layers only)

**Scope correction (round-2 review).** The original brief assumed
`landlock + seccomp + bwrap` are composed today over the same
target. Verified against HEAD: `internal/sandbox/runner_linux.go`
detects only `BwrapRunner` and `NoneRunner`; landlock and seccomp
exist as separate runners but composition is explicitly flagged
as follow-up work in source comments. Writing a composition test
today would mean writing the integration — which violates the
"no behaviour changes" non-goal.

**What ships (revised).** A small `runner_contract_test.go` that
asserts the contract `runner.Runner` provides for the runner
selected at HEAD on each platform — `BwrapRunner` on Linux,
`runner_darwin` on macOS, `NoneRunner` as fallback. The contract
is: deny path X under policy Y produces error Z; allow path X
under policy Y exits 0; the error reported maps to a defined
runner-side type (this is what `stado audit` surfaces).

**Files in scope.** Read-only refs:
- `internal/sandbox/runner.go` (interface).
- `internal/sandbox/runner_linux.go`.
- `internal/sandbox/runner_darwin.go`.
- `internal/sandbox/landlock_linux.go` — single-runner tests only.
- `internal/sandbox/seccomp_linux.go` — single-runner tests only.
- Existing per-runner tests (`runner_linux_test.go`,
  `landlock_linux_test.go`, `seccomp_linux_test.go`) — must remain
  green; this phase doesn't touch them.

**File added.** `internal/sandbox/runner_contract_test.go`.

**What `Runner` actually exposes.** `Runner.Command(ctx, p, name,
args, env)` returns an `*exec.Cmd` configured for the policy plus
`(nil, sandbox.Denied{...})` when the exec allow-list rejects
the binary name. Filesystem and network denials happen at
runtime inside the spawned process — they surface as non-zero
exit codes / kernel-mapped errors, not as a synchronous error
from `Command`. `NoneRunner` doesn't enforce policy at all
(documented as "unsandboxed" in source); it just builds the
command. The contract test must reflect this split.

**Test scenarios — split by enforcement layer:**

*Tier 1 — applies to every runner (`BwrapRunner`, `NoneRunner`,
`runner_darwin`'s sandbox-exec runner). Construction-only,
no subprocess execution required:*
1. **Command shape.** `Command` returns an `*exec.Cmd` with the
   resolved binary path, the policy-derived env, and the runner's
   expected wrapper invocation (e.g. `bwrap` flags for
   `BwrapRunner`, no wrapper for `NoneRunner`).
2. **Exec allow-list denial.** Policy with non-empty `Exec`
   that excludes `name` returns `(nil, sandbox.Denied{Reason:
   "exec ... not in allow-list"})` — same shape on every runner
   (the check lives in `ResolveBinary`, shared by all).
3. **Exec allow-list pass.** Same policy with `name` allowed
   returns `*exec.Cmd` without error.

*Tier 2 — applies only to runners that actually enforce
(`BwrapRunner` on Linux; `runner_darwin`'s sandbox-exec runner
on macOS). Each test runs the built command against a temp dir:*
4. **FS-write denied.** Build a command with `BwrapRunner` /
   sandbox-exec that writes to a tempdir path *not* listed in
   `Policy.FSWrite`. Run it. Subprocess exits non-zero; the
   subprocess's stderr / exit code is the assertion target —
   not a return value from `Command`.
5. **FS-write allowed.** Same shape, but tempdir is in
   `Policy.FSWrite`. Subprocess exits 0.
6. **Negative control.** Default-allow policy on a benign
   command (e.g. `/usr/bin/true`). Subprocess exits 0 on every
   runner including `NoneRunner`.

The Tier 1 / Tier 2 split is the corrective from round-3
review: the original "FS-deny returns typed error" scenario was
factually wrong about what `Runner.Command` does.

**Skip discipline.**
- Tier 1 tests run on every host that compiles the file (Linux
  / macOS via `//go:build linux || darwin`). NoneRunner is
  always available; BwrapRunner / sandbox-exec construction
  works without the binary being installed (the sandbox
  binary's absence makes it `Available()=false` but doesn't
  block `Command()`).
- Tier 2 tests skip per runner:
  - BwrapRunner Tier-2 tests skip if `bwrap` not in PATH.
  - sandbox-exec Tier-2 tests skip if `sandbox-exec` missing
    (rare on macOS, possible in containers).
  - NoneRunner has no Tier 2 (nothing to enforce).
- Windows: file uses `//go:build linux || darwin`.

**Park as separate spec.** Multi-layer composition
(`landlock + seccomp + bwrap` stacked over one target) requires
production wiring that doesn't exist today. Capture it in
`.claude/specs/open/sandbox-composition.md` (or equivalent) at
the end of 1.2 with what we'd want once it lands. **Do not let
1.2 write that integration.**

**Verification.**
- [ ] `go test ./internal/sandbox/... -race` passes on Linux dev box.
- [ ] Same on macOS dev box (if available).
- [ ] Tier 1 tests run on every host (no `t.Skip` from missing
      sandbox binary at this tier).
- [ ] Tier 2 tests skip cleanly per runner when the binary is
      missing; never fail-by-skip.
- [ ] Tier 2 FS-write assertions inspect the *subprocess result*
      (exit code / stderr), not a return value from `Command`
      (which only returns `*exec.Cmd`).
- [ ] Existing per-runner tests untouched and still green.
- [ ] Follow-up spec for landlock+seccomp+bwrap composition
      written and committed.

### 1.3 `internal/runtime/fleet_bridge.go` lifecycle tests

**What ships.** A test file for `fleet_bridge.go` covering the bridge
between subagent runtime and the FleetBridge plugin host import.
**Note:** `fleet.go` already has 21 tests in `fleet_test.go` —
that's not the gap. The gap is `fleet_bridge.go` (4461 bytes, no
test file). This was caught in the consult round; the original
brief was wrong.

**File added.** `internal/runtime/fleet_bridge_test.go`.

**Coverage targets.** The four contracts above (capability /
nil / forwarding / cancel) plus FleetBridge specifics. *Updated
2026-05-07 to match HEAD behaviour; the prior wording asserted
behaviour the implementation deliberately doesn't have.*

- **Sync spawn.** Caller waits for spawn to complete; assert
  agent-id + session-id + status surface; assert error path when
  the spawner returns an error (test wraps with `agent error: ...`).
- **Cancellation.** Cancel mid-sync-spawn → ctx.Err returned to
  the caller. *The fleet entry is intentionally NOT cleaned* —
  `Fleet.Spawn`'s goroutine uses the long-lived `RootCtx`, not
  the caller's ctx. A plugin can fire-and-forget a sync spawn
  and the agent still completes. Test asserts the entry remains
  in the registry with status=running after caller cancellation.
- **Message offsets.** `since=0` baseline; `since=N>0` forwards
  N into both `bridge.Since` and the result's `Message.Offset`
  field. *`since>current` and `since<0` are not validated by the
  current implementation* — `since` is echoed through unchanged.
  Tests document current behaviour; aligning with the original
  plan's "empty for offset>current; error for offset<0" intent
  is captured for follow-up but not in scope for this refactor
  program.
- **Missing-agent paths.** Get / ReadMessages / SendMessage all
  return typed "not found" errors. *Cancel is documented
  idempotent (returns nil for unknown IDs)* — see fleet.go:279.
  Aligning Cancel with the others would be a behaviour change
  callers depend on; tests document the current contract.
- **Concurrency.** Parallel spawn requests don't corrupt the
  agent registry; all spawned IDs are distinct and appear in
  AgentList. (Note: the package's existing `fakeSpawner` has a
  latent race in `gotPrompt` under parallel use; tests
  introduce a local race-safe spawner rather than fixing the
  shared fixture.)

**Approach.** Write a fleet-specific mini-harness inline in
`fleet_bridge_test.go` — `_test.go` files aren't importable across
packages, so the 1.1 harness in `internal/plugins/runtime/` can't
be shared. Replicate the `newTestHost` and capability-gate helper
pattern; keep the fake signatures consistent with 1.1's so B3
produces no surprises.

**Verification.**
- [ ] `go test ./internal/runtime/... -race -run Fleet` passes.
- [ ] Race detector clean under `-count=20` for the
      concurrency scenarios.
- [ ] Existing `fleet_test.go` untouched.

---

## Phase 2 — Tier A (structural splits)

### 2.1 (A2) `workdirpath` API simplification — `Resolver` type

**What ships.** A new `Resolver` type in `internal/workdirpath/`
that consolidates the legacy public-function API into ~6 cohesive
methods. The legacy functions stay as **thin wrappers** around
`Resolver` for the duration of the migration; they're deleted
when call-site churn is low and all production callers have
moved to the new API. The migration does not change behaviour:
every legacy call lands on the same code path it does today.

**The legacy surface.** Inventory at HEAD: 23 exported functions
in `internal/workdirpath/workdirpath.go` (verified 2026-05-07).
The package's other file, `repodisco.go`, exports 3 unrelated
helpers (`LooksLikeRepoRoot`, `FindRepoRoot`,
`FindRepoRootOrEmpty`) — they're **out of scope** for A2; they
do repo-root discovery, not path resolution under a confinement
boundary. Don't migrate them, don't fold them into `Resolver`.

**Re-inventory step at A2 start:** run
`grep -E '^func [A-Z]' internal/workdirpath/workdirpath.go | wc -l`
(file-scoped — `go doc -all ./internal/workdirpath` would also
list `repodisco.go` exports and inflate the count to 26). Confirm
the count + names match this plan before drafting migration
commits. The prior plan version cited "38 functions" and a
duplicate `MkdirAllRootNoSymlink` declaration — both wrong. Sample of the
actual surface (non-exhaustive):
`Resolve`, `RootRel`, `OpenReadFile`,
`ReadRegularFileNoSymlinkLimited`,
`ReadRootRegularFileLimited`,
`ReadRegularFileUnderUserConfigLimited`,
`OpenRegularFileUnderUserConfig`,
`OpenRegularFileNoSymlink`,
`ReadRegularFileUnderUserConfigNoLimit`,
`MkdirAllUnderUserConfig`, `MkdirAllNoSymlinkUnder`,
`MkdirAllRootNoSymlink`, `MkdirAllNoSymlink`,
`OpenRootNoSymlink`, `OpenRootUnderUserConfig`,
`OpenRootNoSymlinkUnder`, `RemoveAllNoSymlink`,
`WriteFile`, `WriteRootFileAtomic`,
`WriteRootFileAtomicExactMode`, `RootRelForWrite`,
`Glob`, `GlobLimited`.

**Target shape.** A `Resolver` parameterised by a trust anchor
(workdir root, user config dir) with these primitives, plus a
small `RootResolver` derivation for the 7 legacy `*os.Root`
functions:

```go
type Resolver struct {
    workdir   string
    userConf  string
    // private symlink/confinement state; not exported
}

func New(workdir, userConfDir string) (*Resolver, error)

func (r *Resolver) Resolve(rel string, opts ...ResolveOpt) (abs string, err error)
func (r *Resolver) RootRel(abs string) (root, rel string, err error)
func (r *Resolver) OpenFile(rel string, flags int, perm os.FileMode, opts ...ResolveOpt) (*os.File, error)
func (r *Resolver) ReadFile(rel string, opts ...ResolveOpt) ([]byte, error)
func (r *Resolver) WriteFileAtomic(rel string, data []byte, perm os.FileMode, opts ...ResolveOpt) error
func (r *Resolver) MkdirAll(rel string, perm os.FileMode, opts ...ResolveOpt) error
func (r *Resolver) RemoveAll(rel string, opts ...ResolveOpt) error
func (r *Resolver) Glob(pattern string, opts ...ResolveOpt) ([]string, error)

// AtRoot returns a derivation of Resolver scoped to the given
// *os.Root. Methods on the result use `root` as their
// confinement anchor instead of workdir/userConf. Used to
// migrate the 7 legacy `Root*` functions
// (OpenRootNoSymlink, MkdirAllRootNoSymlink, WriteRootFileAtomic,
// WriteRootFileAtomicExactMode, ReadRootRegularFileLimited,
// OpenRootUnderUserConfig, OpenRootNoSymlinkUnder) without
// expanding the `ResolveOpt` axis to carry an *os.Root pointer.
func (r *Resolver) AtRoot(root *os.Root) *RootResolver

type RootResolver struct {
    root    *os.Root
    // private symlink/confinement state; not exported
}

func (rr *RootResolver) OpenFile(rel string, flags int, perm os.FileMode, opts ...ResolveOpt) (*os.File, error)
func (rr *RootResolver) ReadFile(rel string, opts ...ResolveOpt) ([]byte, error)
func (rr *RootResolver) WriteFileAtomic(rel string, data []byte, perm os.FileMode) error
func (rr *RootResolver) MkdirAll(rel string, perm os.FileMode) error
```

`ResolveOpt` is a functional-options type that absorbs the
`Limited` / `NoSymlink` / `UnderUserConfig` axes that today's
name-explosion encodes in function names: `WithLimit(n int64)`,
`WithSymlinks(bool)`, `WithAnchor(workdir | userConf)`. The
`*os.Root` axis lives on `RootResolver` instead — it's a
long-lived handle, not a per-call modifier, and threading it
through `ResolveOpt` would force every call site to package an
unrelated value.

**Why a separate `RootResolver` over `WithRoot(*os.Root)`.** The
legacy `*os.Root` functions take the root as their *first
positional arg*; semantically the root is the operation's
anchor, not a flag. A derivation method (`r.AtRoot(root)`) makes
that explicit at the call site and keeps the option set
homogeneous. Per round-3 review.

**Why options over names.** The current API multiplies one
behaviour axis (limit, symlink mode, anchor) into a separate
function each. Options make the axis explicit at the call site,
shrink the public surface, and let new axes (e.g. EP-0040's
`WithFsync(bool)` for atomic-write durability tuning) land
without inventing yet another function.

**Migration strategy** (Codex's call — adopt verbatim):
1. Land `Resolver`, `RootResolver`, and the new methods alongside
   the legacy functions. Legacy functions get rewritten as
   one-liners on top of the appropriate type (the 16
   workdir/userConf functions wrap `Resolver`; the 7 `Root*`
   functions wrap `RootResolver`). Behaviour and public
   signatures preserved. Confirm via tests + smoke runs.
2. **Audit `internal/mcpbridge`** — Gemini flagged likely
   "safety leakage" where MCP-side path resolution duplicates
   workdirpath logic. Fold the audit into A2; don't break
   into a separate phase. If MCP currently resolves paths
   without going through workdirpath, the migration is to make
   it use `Resolver`. **Escape hatch:** if the audit reveals
   non-trivial divergence (mcpbridge has its own confinement
   model, or migration would require behaviour-changing logic
   to unify), park as a separate spec rather than expanding
   A2. The bar is mechanical migration; anything else gets
   captured for follow-up.
3. Migrate high-value callers (per-package, in dependency order):
   `internal/runtime/`, `internal/plugins/runtime/`,
   `internal/tools/`, `internal/mcpbridge`, `internal/state/`,
   `internal/skills/`, the rest. One commit per package.
4. Once all production callers use `Resolver`, mark legacy
   functions `Deprecated:` with a one-release window.
5. Delete legacy functions in the commit before checkpoint #2
   closes — assuming no out-of-tree callers (this is internal/).

**Files touched.**
- `internal/workdirpath/workdirpath.go` (the big one).
- `internal/workdirpath/resolver.go` (new).
- `internal/workdirpath/resolver_test.go` (new).
- Per-call-site migration: any package that imports
  `internal/workdirpath`. Inventory at migration time via
  `go list -deps ./... | grep workdirpath`-style probe.
- `internal/mcpbridge/*` for the audit — likely a small change;
  scope confirmed at audit time.

**Risk.** workdirpath is the safety surface for every fs touch.
Any regression here surfaces as a security finding, not a test
failure. Mitigations:
- Wrappers preserve exact behaviour during migration.
- Existing `workdirpath_test.go` tests run unchanged for the
  whole migration window.
- New `resolver_test.go` reproduces every legacy test case
  through the new API.
- Per-call-site migration commit is mechanical and reviewable.

**Verification.**
- [ ] `go test ./internal/workdirpath/... -race` passes.
- [ ] Every legacy test case has a parallel through `Resolver`.
- [ ] `go vet ./...` clean.
- [ ] All call sites migrated; no `workdirpath.<LegacyFn>(`
      references remain in production code.
- [ ] mcpbridge audit produced a defined outcome (migration to
      `Resolver`, written rationale for staying separate, or a
      parked follow-up spec for non-trivial divergence).
- [ ] Cross-platform path parity: `Resolver` test cases run on
      Linux *and* Windows. Use `filepath.FromSlash` /
      `filepath.ToSlash` consistently; reject any test that
      hard-codes `/`-separated paths in a path-comparison.
      Windows-specific edge cases that the legacy 23-fn API
      currently handles (drive prefixes, UNC paths, reserved
      names) keep working through the new API.
- [ ] `internal/workdirpath/repodisco.go` untouched (out of scope
      for A2; its 3 exports `LooksLikeRepoRoot`, `FindRepoRoot`,
      `FindRepoRootOrEmpty` are repo-discovery, not path
      confinement).
- [ ] Smoke: `stado --help`, `stado run --help`,
      `stado plugin install --help` all exit 0.

### 2.2 (A1) `Model` struct + `model_render.go` consolidation

**What ships.** The TUI `Model` struct shrinks; overlay state
moves into the existing `internal/tui/overlays/` package
(which already has `center.go`, `help.go`). The 8 existing picker
packages stay separate; a small shared `Picker` interface lands
either in `internal/tui/overlays/` or a new leaf
`internal/tui/pickers/` package — see "picker contract location"
below. `model_render.go` shrinks correspondingly: per-overlay
rendering moves with the overlay; per-picker rendering moves
with the picker. `Model.View()` becomes a thin orchestrator.

**Framing — consolidation, not new packages.** The relevant package
directory (`internal/tui/overlays/`) already exists. A1 is moving
sprawl from `model.go` and `model_render.go` *into* that existing
package, not creating a parallel structure. The 8 picker packages
(`agentpicker`, `filepicker`, `fleetpicker`, `modelpicker`,
`personapicker`, `sessionpicker`, `taskpicker`, `themepicker`)
remain as separate packages; A1 adds a shared interface they all
implement, no merging.

**The big picture.**
- `Model` today: ~120+ field-equivalent lines mixing chat state,
  TUI lifecycle, picker overlays, approval/choice flows, sidebar
  management, background plugin orchestration, loop/monitor.
- After A1: `Model` holds chat state + lifecycle + an
  `overlayStack []Overlay` (or `activeOverlay Overlay` — see
  "overlay slotting decision" below) + the sidebar state.
  Pickers are children of an `OverlayPicker` implementation that
  wraps the existing picker packages via the shared interface.

**Overlay slotting decision (decide before first A1 commit).**
Today's `Model` has multiple `*Picker.Visible` flags that may
simultaneously be true. Two options:
- **Single slot (`activeOverlay Overlay`).** Simpler, but breaks
  any flow where two overlays coexist. Could be a behaviour change.
- **Stack (`overlayStack []Overlay`).** Preserves current
  multi-visible behaviour as the default. Marginally more state
  to manage; ESC pops the top.

**Default = preserve current behaviour.** Choose the stack unless
an audit at A1 design time confirms multi-visible was always a bug
nobody depended on; in that case open a separate behaviour-fix
plan. Don't silently flip semantics inside a "no behaviour
changes" refactor.

**Picker contract location.** The shared `Picker` interface must
live in a leaf package — one that the 8 picker packages can
import without creating cycles. Options:
- Inside `internal/tui/overlays/` if `overlays/` itself doesn't
  end up importing any picker package.
- A new `internal/tui/pickers/` package that holds *only* the
  interface and shared types; never imports a picker package.

The interface package's import set must stay narrow (stdlib +
`bubbletea` types). If you find yourself wanting to import a
specific picker into the interface package, you've put logic in
the wrong layer.

**Overlay interface.**

```go
// internal/tui/overlays/overlay.go (new)
type Overlay interface {
    Visible() bool
    View(width, height int) string
    Update(msg tea.Msg) (Overlay, tea.Cmd)
    // OnDismiss runs when the overlay is closed (e.g. ESC).
    OnDismiss() tea.Cmd
}
```

Implementations:
- `overlays.Help{}` — already exists in shape; tighten to interface.
- `overlays.Status{}` — extracted from `model.go` / `model_status_modal.go`.
- `overlays.QuitConfirm{}` — extracted.
- `overlays.Approval{}` — extracted from `model.go` approval state.
- `overlays.Choice{}` — extracted similarly.
- `overlays.Picker{}` — wrapper around the picker package.

**Migration order.**
1. Decide overlay slotting (single vs. stack). Default: stack.
   Documented in commit 1 with rationale.
2. Define `Overlay` interface in `internal/tui/overlays/overlay.go`.
3. Define `Picker` interface in its leaf location (see "picker
   contract location"). One commit.
4. Move `Help` (already an overlay-shaped thing) onto the new
   `Overlay` interface. One commit.
5. Move `Status` overlay. One commit per overlay until done
   (Status, QuitConfirm, Approval, Choice).
6. Migrate `Model` to hold the chosen overlay slot/stack. One
   commit.
7. Add the shared `Picker` interface implementation to each of
   the 8 picker packages: `agentpicker`, `filepicker`,
   `fleetpicker`, `modelpicker`, `personapicker`,
   `sessionpicker`, `taskpicker`, `themepicker`. One commit per
   picker. `OverlayPicker` wraps any picker via this interface.
8. `model_render.go` per-overlay/per-picker render code follows
   each move. `View()` becomes ~50 lines.

**Files touched.**
- `internal/tui/model.go` — significant shrink.
- `internal/tui/model_render.go` — significant shrink.
- `internal/tui/model_status_modal.go`, `model_quit_confirm.go`,
  `model_help.go` (and their test files) — move to overlay package.
- `internal/tui/overlays/` — gain new files per overlay (and
  possibly the `Picker` interface).
- `internal/tui/pickers/` — *new leaf package* if `overlays/`
  isn't a fit; holds only the `Picker` interface and shared
  types.
- The 8 existing picker packages — gain a small file
  implementing the shared `Picker` interface; no other change.
- TUI tests adjust import paths but assertions stay the same.

**Risk.** Largest mechanical churn in the program. Risk vector:
test suite passes but a corner case (overlay-over-overlay,
ESC-while-streaming, etc.) regresses subtly. Mitigations:
- Migrate one overlay at a time; checkpoint after each.
- `stado run` smoke check at each commit.
- Existing TUI test files (`*_test.go`) remain a safety net.

**Verification.**
- [ ] `go test ./internal/tui/... -race` passes.
- [ ] `Model` struct field count reduced (verify baseline at A1
      design time; target a meaningful reduction, not a
      fixed number — current pre-A1 count is the baseline).
- [ ] `model_render.go` LoC reduced (target: < 800 from 1937).
- [ ] One `Overlay` interface; >= 5 implementations.
- [ ] One `Picker` interface in its leaf package; 8
      implementations (one per existing picker package).
- [ ] Picker interface package imports nothing from the 8 picker
      packages (no cycles).
- [ ] Smoke: `stado run` opens, ESC closes overlays, Q quits.
      If multi-visible overlays exist today, the same combinations
      still display after migration (the slotting decision
      preserves current behaviour).
- [ ] Help / Status / Approval / Choice / Picker each tested
      against the `Overlay` interface (not the old typed shape).

### 2.3 (A3) `model_update`/`commands`/`stream` dispatcher split

**What ships.** The `tea.Model.Update` handler — currently split
across `model_update.go` (1544 LoC), `model_commands.go` (1728),
`model_stream.go` (1625) — splits into one file per message
family inside `package tui` (in `internal/tui/`, *not* a
subdirectory). `model_update.go` becomes a thin dispatcher
(~100 lines) that routes by message type to per-family handler
functions.

**Why same package, not a subpackage.** Round-1 review caught
that handler functions which accept and return `*tui.Model`
cannot live in `internal/tui/behavior/`: `tui` imports
`behavior` (for the dispatch call), and `behavior` imports `tui`
(for the `Model` type) — Go import cycle. The existing
`overlays/` package avoids this by never importing `tui` and
only accepting non-Model arguments; handler functions don't
have that option. Solutions considered and rejected: extracting
`Model` to its own subpackage (too much blast radius for a
no-behaviour-change refactor); narrowing handler signatures via
interfaces (`Model` is too big for a clean interface). The
honest boundary for handlers that need full `*Model` is
filename-level, not package-level.

**The split (filenames, all in `package tui`).**
- `handler_commands.go` — slash-command and user-input message
  handlers.
- `handler_stream.go` — provider streaming + tool-call flow
  handlers.
- `handler_picker_response.go` — picker selection / dismiss
  handlers.
- `handler_lifecycle.go` — init / quit / window-resize.
- `handler_tools.go` — tool invocation + result handlers.

Each handler has the shape:
```go
func handleSlashCommand(m *Model, msg SlashCommandMsg) (Model, tea.Cmd)
```

(unexported — same package, no need for `tui.Model` qualifier
or capitalised symbol).

The dispatcher in `model_update.go`:
```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case SlashCommandMsg:    return handleSlashCommand(&m, msg)
    case StreamDeltaMsg:     return handleStreamDelta(&m, msg)
    // ...
    }
}
```

**Naming convention.** `handler_*.go` for behavior; render code
stays in the `model_render.go` family. The filename prefix is
the only separation now that everything's in `package tui` —
keep it strict so the boundary between event processing and
rendering remains visible to readers.

**The Update handler today.** `model_update.go`'s giant
type-switch covers ~30+ message variants. Inventory pass at
phase start: extract a list of every `case` arm, name the
target handler file, commit-by-commit move plan.

**Optional: telemetry wrapper.** Round-1 review flagged that
`model_update.go` may have heavy inline otel/telemetry calls.
*Verify before committing.* If true, introduce a
`withTelemetry(handler)` wrapper in `handler_lifecycle.go` (or
a new `handler_telemetry.go`); otherwise skip.

**Files touched.**
- `internal/tui/model_update.go` — shrinks to dispatcher.
- `internal/tui/model_commands.go` — content moved into
  `handler_commands.go`; file may delete (or be renamed).
- `internal/tui/model_stream.go` — same shape, into
  `handler_stream.go`.
- `internal/tui/handler_*.go` — new files in `package tui`.

**Approach.**
1. Inventory pass: list every `case` arm in `Update`. For each,
   target a `handler_*.go` file.
2. Extract one message family at a time. One commit per family.
3. After each extraction, run the TUI tests for that family.
4. Last commit: `model_update.go` becomes the dispatcher.

**Risk.** Lower than A1 — the moves are mostly mechanical, the
test files for `model_update`/`commands`/`stream` already
separate concerns. Risk: a handler that mutates `Model` fields
in non-obvious ways doesn't survive the move (the `*Model`
pointer signature should preserve this, but verify).

**Verification.**
- [ ] `go test ./internal/tui/... -race` passes.
- [ ] `model_update.go` < 200 LoC (dispatcher only).
- [ ] `handler_*.go`: one file per message family, each < 500
      LoC.
- [ ] All handler signatures match
      `func(*Model, Msg) (Model, tea.Cmd)`.
- [ ] No new directory under `internal/tui/` (no `behavior/`
      subpackage).
- [ ] Smoke: full session flow works (open → slash command →
      stream response → tool call → quit).

---

## Phase 3 — Tier B (small, contained refactors)

### 3.1 (B1) `config.go` validation extraction

**What ships.** A `ValidationFunc(path string) error` type and
a small registry in `internal/config/`; per-field validators
become entries in the registry. Per-field error wraps consolidate
into one wrap on the loader. Caller-facing API of
`config.LoadOrDefault` unchanged.

**Files touched.**
- `internal/config/config.go` — validation logic extracted.
- `internal/config/validation.go` (new) — registry + helpers.
- `internal/config/validation_test.go` (new) — unit tests for
  each validator in isolation.
- **Bonus:** check `internal/toolinput` (Gemini's flag) — if it
  validates schemas with similar boilerplate, refactor it to use
  the same registry. If yes: this becomes part of B1; if no:
  document why in the decision log.

**Approach.**
1. Define `ValidationFunc` type + registry struct.
2. Extract one validator at a time
   (`validateSystemPromptTemplate`, `validateSandboxPolicy`,
   path-stat-helpers).
3. Replace inline validation calls in `LoadOrDefault` with a
   single registry-driven loop.
4. Reduce the boilerplate `fmt.Errorf("load config: %w", err)`
   wraps to one wrap at the loader.
5. `internal/toolinput` audit + integration if applicable.

**Verification.**
- [ ] `go test ./internal/config/... -race` passes.
- [ ] Each validator has a unit test (in isolation).
- [ ] `config.go` LoC reduced (target: < 800 from 985).
- [ ] toolinput integration done OR rationale captured in decision log.

### 3.2 (B2) `bundled_plugin_tools.go` schema builder

**What ships.** A `SchemaBuilder` type with
fluent helpers (`StringField`, `ObjectFromProps`, `WithRequired`)
in a new `internal/runtime/schema/` (or similar small package).
Each bundled wasm tool's schema migrates from inline
`map[string]any` literals to a builder call. No tool semantics
change.

**Files touched.**
- `internal/runtime/schema/schema.go` (new).
- `internal/runtime/schema/schema_test.go` (new).
- `internal/runtime/bundled_plugin_tools.go` — per-tool schema
  migrations.

**Approach.**
1. Define `SchemaBuilder` API. Test it standalone.
2. Migrate one tool's schema at a time (~20 tools). One commit
   per tool, or batch by tool family (fs__*, shell__*, rg__*, etc.).
3. After all migrations, the inline `map[string]any` literals
   are gone.

**Verification.**
- [ ] `go test ./internal/runtime/... -race` passes.
- [ ] No `map[string]any` schema literal remains in
      `bundled_plugin_tools.go` (grep check).
- [ ] Existing tool-call tests untouched and green.

### 3.3 (B3) Bridge lifecycle/wiring unification

**What ships.** A common `Bridge` interface in
`internal/plugins/runtime/`, but **scoped to lifecycle and
wiring only** — not operation method-set unification. Per
Codex's review: replacing the typed bridge fields
(`SessionBridge`, `MemoryBridge`, etc.) with a generic registry
+ type assertions hides clarity. Keep the typed accessors.
Unify only the things that *are* common: setup ordering,
nil-safety, disposal.

**Target interface.**

```go
type Bridge interface {
    // Name returns a stable identifier for diagnostics
    // and capability-gate keys.
    Name() string
    // Init prepares the bridge for use; called before any
    // operation. Returns error if the bridge can't function
    // (e.g., missing dependency).
    Init(ctx context.Context) error
    // Dispose releases any held resources. Must be idempotent.
    Dispose() error
}
```

Each concrete bridge (Session, Memory, Approval, Choice, Fleet)
implements `Bridge` *in addition to* its typed operations.
Host setup uses `Bridge.Init`; teardown uses `Dispose`. Operations
stay typed.

**Files touched.**
- `internal/plugins/runtime/bridge_lifecycle.go` (new) — `Bridge`
  interface. **Note:** the existing `bridge.go` (verified at
  HEAD, 9396 bytes) holds production session-bridge implementations
  and stays as-is; pick a different filename for the new
  interface to avoid the collision.
- `internal/plugins/runtime/host.go` — Init/Dispose orchestration.
- `internal/plugins/runtime/host_session.go`,
  `host_memory.go`, `host_ui.go`, `host_agent.go` — implement
  `Bridge`.

**Approach.**
1. Define `Bridge` interface in `bridge_lifecycle.go`.
2. Add `Init`/`Dispose` to each concrete bridge — initially
   no-ops where there's nothing to do.
3. `Host` calls `Init` on each registered bridge during setup;
   `Dispose` on teardown. **Per-call nil-bridge checks stay.**
   Round-1 review caught a contract conflict: `Host` fields are
   public, so callers can construct or mutate a `Host` with a
   nil bridge after `Init`. A single registration-time check
   would be a behaviour change (the per-call nil → `-1` return
   path is observable today and asserted by 1.1's contract).
   B3 unifies the *lifecycle* (Init/Dispose/Name) only;
   call-path safety is unchanged.
4. The contract tests from Phase 1.1 protect this — they assert
   the four contracts (cap gate / nil / forwarding / cancel)
   regardless of whether the lifecycle goes through `Bridge.Init`.

**What this is NOT.**
- Not a generic operations dispatcher.
- Not a registry that erases bridge types.
- Not a refactor of the operation method signatures.
- Not a removal of per-call nil-bridge defense.

**Verification.**
- [ ] `go test ./internal/plugins/runtime/... -race` passes.
- [ ] All 1.1 contract tests untouched and green (especially the
      nil-bridge contract — must still hold after B3).
- [ ] `Host` setup is shorter (one loop, not five).
- [ ] No new type-assertion hot paths introduced.
- [ ] `host.go` LoC reduced.
- [ ] Per-call `host.<X>Bridge == nil` defenses still present in
      every host import (grep check).

---

## Smaller wins addendum

Tackled last, opportunistically rolled into adjacent commits when
natural:

- **`internal/config/config.go:94`** — "Auto-prune execution not yet
  wired" TODO. Either wire it in B1 or remove the `auto_prune`
  config field. Decide during B1.
- **`internal/runtime/meta_tools.go` `toolCategorized` interface** —
  single implementation; eliminate. Roll into B2.
- **Single-impl interfaces**: `internal/state/git/session.go`,
  `internal/sandbox/runner.go`, `internal/runtime/fleet.go`,
  `internal/subagent/tool.go`. Review each: inline if there's no
  test stub depending on it. Cosmetic; defer unless natural.
- **Other actionable TODOs** (3 in non-test code total). Triage
  during the relevant phase.

## Open questions

- ~~`MkdirAllRootNoSymlink` declared twice~~ **Resolved
  (2026-05-07).** Verified against HEAD: appears once. Plan's
  earlier inventory was wrong; no migration action needed.
- **Telemetry wrapper for A3.** Verify `model_update.go` actually
  has heavy inline otel/telemetry before extracting a wrapper.
  If telemetry is lightweight, skip the wrapper.
- **A2 cherry-pick to main.** Specifically when. Probably right
  after merge checkpoint 2 (end of A2). Confirm with operator
  before cherry-picking.

(The A1 overlay-slotting decision moved out of this list during
the round-2 review pass — it now lives in Phase 2.2's "Overlay
slotting decision" section with a recommended default.)

## Decision log

### D1. No EPs

- **Decided:** all 9 items land as a single umbrella plan file;
  no EPs.
- **Alternatives:** EPs for Tier A only; one mega-EP for the
  program; EPs for everything.
- **Why:** operator preference. Standards-EP overhead doesn't earn
  its way for a behaviour-preserving refactor sweep. The
  decision-log entries here cover the architectural calls that
  EPs would otherwise capture.

### D2. One worktree, one branch

- **Decided:** `worktree-refactor+quality-2026-q2` is the long-
  lived branch; all 9 items land here; merged via 5 checkpoints
  (possibly cherry-picking A2 early).
- **Alternatives:** branch-per-item; worktree-per-item; one
  big PR.
- **Why:** operator-locked. The 5-checkpoint discipline +
  48h main-sync addresses the entanglement risk both reviewers
  flagged.

### D3. Tier A order: A2 → A1 → A3

- **Decided:** workdirpath first, Model split second, Update
  split third.
- **Alternatives:** A1 first (biggest payoff first); A3 first
  (warmup); A2 → A3 → A1.
- **Why:** A2 is the smallest of the three and has the most
  downstream callers (every fs touch); doing it first sets
  EP-0040 up cleanly. A1 precedes A3 because the Update split
  references `Model` field shapes — doing A1 first means A3
  works against a stable struct rather than chasing field
  renames mid-extraction. (Earlier plan versions justified the
  ordering on `tui/behavior/` package cleanliness; that
  rationale is stale since A3 now stays in `package tui`. The
  state-shape stability argument is the live one.)

### D4. Phase 1.1 as contract tests, not shape tests

- **Decided:** the test suite asserts the four invariants
  (capability gate / nil / forwarding / cancel) every bridge
  satisfies, not the current 5-bridge typed shape.
- **Alternatives:** shape tests; defer 1.1 until B3 lands;
  pull B3 forward to Phase 1 (Gemini's recommendation).
- **Why:** contract tests survive B3 unchanged; they act as the
  spec for B3 rather than a barrier. Codex's framing wins over
  Gemini's "B3 first" because it preserves the original ordering
  while solving the same problem.

### D5. B3 scoped to lifecycle/wiring only

- **Decided:** the unified `Bridge` interface covers Init,
  Dispose, Name. Operation method sets stay typed per bridge.
- **Alternatives:** full method-set unification (single
  `Bridge.Invoke(method, args)` shape); no unification at all
  (defer B3).
- **Why:** Memory ≠ Approval semantically; replacing typed
  fields with type assertions hides clarity. Codex's caveat
  adopted verbatim.

### D6. workdirpath migration via wrappers

- **Decided:** `Resolver` lands alongside the legacy 23-fn API
  (verified count at HEAD, 2026-05-07; not 38 as earlier plan
  versions claimed); legacy functions become one-line wrappers;
  callers migrate per-package; legacy deleted at end of A2.
- **Alternatives:** big-bang rewrite; new package alongside
  workdirpath (deprecated original).
- **Why:** workdirpath is the safety surface for every fs touch.
  Wrapper migration preserves exact behaviour while the public
  API end-state shrinks; per-package migration commits stay
  small and reviewable.
- **Trade-off:** API surface temporarily grows during the
  wrapper window (`Resolver` + 23 legacy funcs). Non-goal #3
  acknowledges this; the surface ends below today's at A2 end.

### D7. A1 framed as consolidation, not new packages

- **Decided:** A1 moves work into existing `internal/tui/overlays/`,
  adds a shared `Picker` interface to the 8 existing picker
  packages (no merge), and may add a single new leaf package
  (`internal/tui/pickers/`) for the picker interface only if
  `overlays/` isn't a fit.
- **Alternatives:** new `tui/ui/` umbrella package; flat structure;
  merging the 8 picker packages into one.
- **Why:** the destination directories already exist (verified:
  `tui/overlays/help.go`, `tui/overlays/center.go`). Moving into
  them costs less and matches the project's small-focused-package
  posture. Merging the 8 picker packages would be its own
  refactor unrelated to Model shrinking; out of scope.

### D8. Per-merge-checkpoint smoke check

- **Decided:** every checkpoint runs `stado --help`,
  `stado run --help`, and one full `stado run` smoke session.
- **Alternatives:** rely on test suite + lint; manual smoke at
  end only.
- **Why:** structural splits can break wiring (binding TUI
  message types to the wrong handlers, e.g.) in ways the unit
  tests don't catch. A smoke check is cheap and catches the
  worst class of regression.

### D9. A3 stays in `package tui` (no `behavior/` subpackage)

- **Decided:** Update-handler split lands as `handler_*.go`
  files inside `internal/tui/` (same `package tui` as the
  dispatcher), not in a new `internal/tui/behavior/` subpackage.
- **Alternatives:** (a) new `behavior/` subpackage as originally
  drafted; (b) extract `Model` to `internal/tui/model/` so both
  `tui` and `behavior` import it without cycle; (c) narrow
  handler signatures via interfaces.
- **Why:** Handler functions need full `*tui.Model`. Option (a)
  causes `tui` ↔ `behavior` import cycle. Option (b) is too much
  blast radius for a no-behaviour-change refactor — `Model` has
  many private fields and methods the rest of `tui` uses.
  Option (c) requires defining an interface large enough to
  cover what the handlers need, which approaches `Model`
  itself. Filename-level boundary delivers A3's value (thin
  dispatcher, per-family files) at the lowest cost. Both
  reviewers (round 1 and round 2) validated.

### D10. 1.2 scoped to wired layers; composition parked

- **Decided:** Phase 1.2 tests only the runner contract for the
  layer wired in production today (`BwrapRunner` /
  `runner_darwin` / `NoneRunner`). Multi-layer composition
  (`landlock + seccomp + bwrap` stacked) gets a follow-up spec.
- **Alternatives:** (a) write the composition test and the
  integration to back it (rejected — violates non-goal #1);
  (b) drop 1.2 entirely (rejected — there's still a contract
  worth testing for the wired layer).
- **Why:** Round-2 review verified that `runner_linux.go`
  detects only `BwrapRunner` and `NoneRunner`, with composition
  flagged as follow-up in source. Writing the composition test
  would require writing the integration first, which is a
  separate piece of work.

### D11. B3 preserves per-call nil-bridge defense

- **Decided:** B3's unified `Bridge` interface covers Init,
  Dispose, Name only. Per-call `host.<X>Bridge == nil` checks
  in every host import stay as-is.
- **Alternatives:** (a) move nil defense to a single
  registration-time check (original draft); (b) make `Host`
  fields private with accessors that nil-check.
- **Why:** Host fields are public, so callers can construct or
  mutate `Host` after `Init`. A registration-only check would
  let a later nil cause a panic — observable behaviour change.
  Phase 1.1's nil-bridge contract test would also fail under
  (a). Option (b) is a separate, much larger refactor (touches
  every host import + every caller); out of scope.

### D12. A2 splits `Resolver` and `RootResolver`

- **Decided:** the new API has two types — `Resolver` (anchored
  on workdir + userConfDir) and `RootResolver` (anchored on
  `*os.Root`), reached via `r.AtRoot(root)`. The 7 legacy
  `Root*` functions wrap `RootResolver`; the other 16 wrap
  `Resolver`.
- **Alternatives:** (a) single `Resolver` with
  `WithRoot(*os.Root)` option; (b) embed an optional `*os.Root`
  field on `Resolver` directly.
- **Why:** `*os.Root` is a long-lived handle, not a per-call
  modifier. Threading it through `ResolveOpt` would force every
  call site to package an unrelated value (option a) or split
  state across "is there a root?" branches (option b). A
  derivation method makes the anchor explicit at the call site
  and keeps `ResolveOpt` homogeneous. Caught in round-3 review;
  the original draft made the legacy `Root*` functions
  un-wrappable.

## Related

- `docs/eps/0040-bundled-local-inference.md` — A2 (workdirpath
  simplification) sets up the cleaner API the EP-0040 manager
  will use for binary, model, and state file paths.
- `docs/eps/0037-tool-dispatch-and-operator-surface.md` — context
  for the bundled-tool schema literals (B2).
- `docs/eps/0034-background-agents-fleet.md` (Superseded) /
  EP-0038 — context for `internal/runtime/fleet.go` /
  `fleet_bridge.go` and the FleetBridge plugin host import (1.3).
