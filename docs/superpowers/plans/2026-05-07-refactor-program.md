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

**Target shape (revised round-A2 review, 2026-05-07).** Two
rounds of code-level consultation rejected the original
"single `Resolver` + `WithAnchor` options" design as semantic
flattening — the legacy package's anchor types encode different
*security policies*, not runtime flavoring. End-state API is
4 narrow types:

```go
// Workdir-anchored, symlink-resolving confinement.
// Wraps the 7 workdir-flavor legacy fns (Resolve, RootRel,
// OpenReadFile, WriteFile, RootRelForWrite, Glob, GlobLimited).
type Resolver struct { /* unexported */ }
func New(workdir string) (*Resolver, error)

func (r *Resolver) Resolve(path string) (abs string, err error)
func (r *Resolver) ResolveAllowMissing(path string) (abs string, err error)
func (r *Resolver) RootRel(path string) (root, rel string, err error)
func (r *Resolver) RootRelForWrite(path string) (root, rel string, err error)
func (r *Resolver) OpenRegularFile(path string) (*os.File, error)
func (r *Resolver) WriteFileAtomic(path string, data []byte, perm os.FileMode) error
func (r *Resolver) Glob(pattern string) ([]string, error)
func (r *Resolver) GlobLimited(pattern string, maxStored int) (matches []string, total int, err error)

// HOME / XDG longest-anchor walk; chain ABOVE anchor follows
// system symlinks, chain BELOW anchor is no-symlink. Wraps the 5
// user-config legacy fns. Constructor uses environment lookup
// (XDG_CONFIG_HOME / XDG_DATA_HOME / XDG_STATE_HOME /
// XDG_CACHE_HOME / HOME) — preserves the existing
// userTrustAnchor logic exactly. When the requested path has no
// covering anchor, falls back to strict no-symlink semantics
// (matches OpenRootUnderUserConfig / MkdirAllUnderUserConfig).
type UserConfigResolver struct { /* unexported */ }
func NewUserConfigResolver() *UserConfigResolver

func (uc *UserConfigResolver) OpenRoot(path string) (*os.Root, error)
func (uc *UserConfigResolver) OpenRegularFile(path string) (*os.File, error)
func (uc *UserConfigResolver) ReadFileLimited(path string, maxBytes int64) ([]byte, error)
func (uc *UserConfigResolver) ReadFileNoLimit(path string) ([]byte, error)
func (uc *UserConfigResolver) MkdirAll(path string, perm os.FileMode) error

// Strict no-symlink walk from absolute root, no anchor. Wraps
// the 5 strict-flavor legacy fns + 2 ancestor-walk fns via
// derivation.
type StrictResolver struct { /* unexported */ }
func NewStrictResolver() *StrictResolver

func (s *StrictResolver) OpenRoot(path string) (*os.Root, error)
func (s *StrictResolver) OpenRegularFile(path string) (*os.File, error)
func (s *StrictResolver) ReadFileLimited(path string, maxBytes int64) ([]byte, error)
func (s *StrictResolver) MkdirAll(path string, perm os.FileMode) error
func (s *StrictResolver) RemoveAll(path string) error

// Under returns a derived StrictResolver scoped to the given
// trusted ancestor — the chain UP TO the ancestor is opened
// via os.OpenRoot (system symlinks accepted), below is strict
// no-symlink. Wraps MkdirAllNoSymlinkUnder / OpenRootNoSymlinkUnder.
func (s *StrictResolver) Under(ancestor string) (*StrictResolver, error)

// Caller-owned *os.Root handle. Independently constructible —
// no Resolver dependency, no AtRoot derivation. The RootResolver
// BORROWS the handle; the caller is responsible for closing the
// underlying *os.Root. Wraps the 4 *os.Root-relative legacy fns.
type RootResolver struct { /* unexported */ }
func NewRootResolver(root *os.Root) *RootResolver

func (rr *RootResolver) ReadFileLimited(name string, maxBytes int64) ([]byte, error)
func (rr *RootResolver) WriteFileAtomic(name string, data []byte, perm os.FileMode) error
func (rr *RootResolver) WriteFileAtomicExactMode(name string, data []byte, perm os.FileMode) error
func (rr *RootResolver) MkdirAll(path string, perm os.FileMode) error
```

**Why 4 types instead of 1.** Both reviewers (codex + gemini)
independently rejected the original options-based design: the
trust models are *not just runtime flavoring*. `WithAnchor(...)`
papers over different security policies and invites "policy
soup" (e.g. `WithSymlinks(true)` conflicting with user-config's
hardcoded no-symlink-below rule). 4 types make the security
boundary explicit at every call site.

Explicit decisions:

- **No `WithAnchor` option.** Call sites pick the resolver by
  semantic ownership (workdir / user-config / strict / `*os.Root`).
- **No path-shape dispatching.** A "smart" Resolver that picks
  policy by path shape would re-introduce the ambiguity this
  refactor is removing.
- **No generic `OpenFile(flags int)`.** Legacy `Open*` fns are
  read-only by design — they don't accept `os.O_CREATE` /
  `os.O_TRUNC`. Generic flags invite unsafe combinations through
  safety surfaces. Methods are semantic: `OpenRegularFile`
  (read-only, regular-file, no final symlink, SameFile check),
  `WriteFileAtomic`, `OpenRoot`, `MkdirAll`, `RemoveAll`,
  `RemoveAll`.
- **No `WithSymlinks(bool)`.** Symlink policies aren't binary —
  workdir resolves, user-config splits at anchor, strict refuses
  all. Encoded in the type, not a flag.
- **`RootResolver` independently constructed.** No `r.AtRoot()`;
  callers do `NewRootResolver(root)` directly. Avoids the "fake
  resolver state" anti-pattern (codex round-2 flag).
- **`StrictResolver.Under(ancestor)` is a method, not a 5th type.**
  The trust model (no-symlink walk) is identical; only the
  ceiling changes.
- **Behavior preserved exactly.** NUL-byte rejection in entry
  methods; abs-before-`EvalSymlinks` ordering (Go 1.25+
  preserves relative input shape); `os.SameFile` TOCTOU check
  on every `Open` primitive.

**`repodisco.go` stays out of scope.** `LooksLikeRepoRoot` /
`FindRepoRoot` / `FindRepoRootOrEmpty` are anchor *discovery*,
not *resolution under an anchor*. Separate concern. The 4-type
API doesn't absorb them.

**Migration strategy.**

0. **2.1.aa pre-flight — `internal/mcpbridge` audit.** Codex
   round-2 confirmed mcpbridge has no fs calls or
   `workdirpath` usage in current source — the audit is likely
   a one-line "no API impact" finding. Doing it BEFORE 2.1.a
   prevents an API-shaping exception from being discovered
   after the types land.
1. **2.1.a — `Resolver` + `RootResolver`.** Land both with
   security tests (NUL injection, path traversal, symlink
   escapes, TOCTOU). Migrate ONE canary caller per type for
   ergonomic feedback. Legacy untouched.
2. **2.1.b — `UserConfigResolver`.** Preserves XDG/HOME
   longest-anchor selection + strict fallback. Tests + 1
   canary caller. Legacy untouched.
3. **2.1.c — `StrictResolver` + `Under(ancestor)`.** Tests + 1
   canary caller. Legacy untouched.
4. **2.1.d — DEFERRED.** *Original plan* called for legacy to be
   rewritten as one-line wrappers around the new types here, with
   a behavior-matrix doc gating the rewrite. *Revised at 2.1.d
   start, 2026-05-07:* the rewrite is mostly code reshaping with
   limited functional benefit until legacy is deleted. The impl-
   move now bundles with 2.1.Y — when legacy is deleted, the
   impls inline into the new types in one commit instead of
   being shuffled twice. Rationale:
   - The 49 new tests (across 4 types) + 29 legacy tests already
     encode every contract the matrix would document.
   - During 2.1.e..N caller migration, having impls in their
     familiar legacy form (workdirpath.go) keeps git blame /
     git log readable for migrators.
   - At 2.1.Y the deletion + impl-move happen mechanically: each
     legacy fn's body becomes the corresponding new-type method's
     impl; the exported legacy symbol disappears.
   The new types continue delegating to legacy through 2.1.e..N.
5. **2.1.e..N — Broad caller migration**, batched 4-6 commits
   by package family. New types continue calling legacy
   internally; only the call-site is updated.
6. **2.1.f — Bazzite/Atomic-Fedora `RemoveAll` gap fix
   (in-scope behavior fix).** EP-0028 added the
   `*UnderUserConfig` family for read/open/mkdir because
   Atomic Fedora hosts have `/home → /var/home` as a system
   symlink, and the strict-from-/ walk rejects at `/home`.
   `RemoveAllNoSymlink` was never given an Under-equivalent —
   `cmd/stado session delete`, `stado plugin gc`, plugin-
   install rollback, agent kill, and TUI worktree delete all
   walk through this code path and fail on Bazzite. Adds
   `UserConfigResolver.RemoveAll` (anchor-walk above /
   no-symlink below / strict-fallback for non-HOME paths) and
   migrates the 5 caller sites from `StrictResolver.RemoveAll`
   to `UserConfigResolver.RemoveAll`. Captured as a *behavior
   fix in scope* — the program's "no behavior changes" non-goal
   has a carve-out for this case because the new behavior is
   what EP-0028 already established for the rest of the API
   surface; RemoveAll was simply forgotten.
7. **2.1.X — Mark legacy `Deprecated:`.**
8. **2.1.Y — Delete legacy + inline impls into new types.**
   The wrapper-rewrite the original plan called for at 2.1.d
   happens here, alongside removal of the now-unused exported
   symbols. Each legacy fn's body lands in the matching new-type
   method; legacy file shrinks to zero exports. Verify zero
   `workdirpath.<LegacyFn>(` references remain.

**Files touched.**
- `internal/workdirpath/workdirpath.go` (legacy; wrapper
  rewrite at 2.1.d).
- `internal/workdirpath/resolver.go` (new — `Resolver`).
- `internal/workdirpath/resolver_test.go` (new).
- `internal/workdirpath/root_resolver.go` (new — `RootResolver`).
- `internal/workdirpath/root_resolver_test.go` (new).
- `internal/workdirpath/userconfig_resolver.go` (new).
- `internal/workdirpath/userconfig_resolver_test.go` (new).
- `internal/workdirpath/strict_resolver.go` (new).
- `internal/workdirpath/strict_resolver_test.go` (new).
- *(originally:* `.agent/notes/workdirpath-behavior-matrix.md`
  *to be created at 2.1.d. Skipped per the 2.1.d → 2.1.Y
  deferral — the 49 new tests across 4 types already encode
  every contract the matrix would document. If the impl-move
  at 2.1.Y surfaces a missing axis, write the matrix then.)*
- Per-call-site migration: any package that imports
  `internal/workdirpath` (21 packages identified at A2 start;
  re-inventory before broad migration).

**Risk.** workdirpath is the safety surface for every fs touch.
Any regression here surfaces as a security finding, not a test
failure. Mitigations:
- 2.1.aa-c land 4 new types ALONGSIDE legacy — legacy unchanged
  through that window, existing 29 tests stay green.
- Behavior matrix at 2.1.d enumerates legacy semantics; test
  additions match it exactly.
- Wrappers preserve exact public signatures during migration;
  existing 29 tests act as bit-compatibility suite.
- Per-call-site migration commits stay small (per-package).
- Round-A2 review (codex + gemini) explicitly called out:
  NUL-byte rejection, abs-before-`EvalSymlinks` ordering,
  `os.SameFile` TOCTOU, anchor-equality, overlapping
  HOME/XDG longest match, symlinked HOME, outside-anchor
  fallback. All in test scope.

**Verification.**
- [ ] `go test ./internal/workdirpath/... -race` passes.
- [ ] Every legacy test case in `workdirpath_test.go` has a
      parallel through the new types (workdir → `Resolver`,
      user-config → `UserConfigResolver`, strict → `StrictResolver`,
      `*os.Root` → `RootResolver`).
- [ ] Security tests cover: NUL-byte rejection, path traversal
      (`../`), symlink escapes (parent + final), TOCTOU
      `os.SameFile` invariant, abs-before-`EvalSymlinks`
      ordering, anchor-equality cases, overlapping HOME/XDG
      longest match, symlinked HOME, outside-anchor fallback.
- [ ] `go vet ./...` clean.
- [ ] All call sites migrated; no `workdirpath.<LegacyFn>(`
      references remain in production code.
- [ ] mcpbridge audit (2.1.aa) produced a defined outcome:
      either "no fs/workdirpath usage; no API impact" finding,
      a migration to one of the new resolvers, or a parked
      follow-up spec for non-trivial divergence.
- [ ] Cross-platform path parity: tests run on Linux *and*
      Windows. Use `filepath.FromSlash` / `filepath.ToSlash`
      consistently; reject any test that hard-codes
      `/`-separated paths in a path-comparison. Windows-
      specific edge cases (drive prefixes, UNC paths, reserved
      names) keep working through the new API.
- [ ] `internal/workdirpath/repodisco.go` untouched (out of scope;
      anchor *discovery*, not *resolution*).
- [ ] `RootResolver` borrows the `*os.Root` (caller-owned close
      semantics documented at the constructor).
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

### D12. A2 splits into 4 narrow types, no options-based dispatch

> **Scope clarification (round-A2-invariant review,
> 2026-05-07).** Stado's overall philosophy is "primitives, not
> policies": the runtime exposes a **plugin-facing host-import
> surface** (stado_fs_*, stado_proc_*, stado_net_*, etc.) that
> plugins compose into their own trust models (per
> `docs/eps/0002-all-tools-as-plugins.md` and the EP-0037 /
> EP-0038 architectural reset). That invariant applies to the
> plugin extensibility surface.
>
> `internal/workdirpath` is **not** the plugin extensibility
> surface — it's the runtime's own internal confinement layer
> that backs the host-import implementations after the
> capability gate has run. Plugins don't import workdirpath;
> they call host imports which call workdirpath internally.
>
> The 4-type design therefore encodes **runtime confinement
> policies**, not stado's general security philosophy. Picking
> opinionated types here is correct: the runtime needs concrete
> trust decisions when turning a path into a syscall, and
> "policy soup" (an options-based generic Resolver) was the
> exact failure mode round 1 + round 2 review caught.
>
> If plugin-facing fs primitives are ever needed beyond the
> existing host-import set, they belong in a separate
> `pkg/fsprim` or new host imports — not bolted onto
> workdirpath retroactively.

- **Decided (revised round-A2 review, 2026-05-07):** the new
  API has 4 types — `Resolver` (workdir),
  `UserConfigResolver` (HOME/XDG longest-anchor walk),
  `StrictResolver` (no-symlink from `/`, plus
  `Under(ancestor)` derivation), `RootResolver` (`*os.Root`
  handle, **independently constructed via `NewRootResolver`**;
  no Resolver dependency). Each type exposes only semantic
  methods (`OpenRegularFile`, `WriteFileAtomic`, `OpenRoot`,
  `MkdirAll`, `RemoveAll`, etc.) — no generic `OpenFile(flags)`,
  no `WithAnchor`, no `WithSymlinks(bool)`.
- **Alternatives considered:**
  (a) Original D12: single `Resolver` + `RootResolver` with
      `WithAnchor` / `WithSymlinks` options.
  (b) Combined Resolver that dispatches policy by path shape
      (path under workdir → workdir; HOME/XDG → user-config;
      else → strict).
  (c) Add a 5th `TrustedAncestorResolver` type for the 2 `Under`
      legacy fns.
  (d) Generic `OpenFile(flags int, perm os.FileMode)` unifying
      Open + Write semantics.
- **Why:** Two rounds of code-level review (codex + gemini)
  independently rejected (a). The legacy package's anchor types
  encode different *security policies* — workdir
  symlink-resolves, user-config splits at HOME/XDG anchor,
  strict refuses all symlinks. `WithAnchor` / `WithSymlinks`
  flatten distinct trust models into runtime flags and invite
  "policy soup" (e.g. `WithSymlinks(true)` colliding with
  user-config's hardcoded no-symlink-below rule). 4 types make
  the security boundary explicit at every call site.
  - (b) rejected: dispatching by path shape re-introduces the
    ambiguity this refactor is removing. A symlinked HOME or a
    workdir under XDG_DATA_HOME would silently change semantics.
  - (c) rejected: trust model under an ancestor is identical to
    `StrictResolver` (no-symlink walk); only the ceiling
    changes. Method-on-type is sufficient.
  - (d) rejected: legacy `Open*` fns are read-only by design —
    they don't accept `O_CREATE` / `O_TRUNC`. Generic flags
    invite unsafe combinations through safety surfaces.
- **Cost:** API surface end-state is 4 types instead of 2.
  Trade-off: callers must explicitly pick the security boundary
  (slightly more verbose at construction) but the boundary is
  legible at every call site.

The original "options-based" design also caused round-3 to
miss the `*os.Root` legacy functions entirely (the prior draft
made them un-wrappable). Switching to types-per-policy resolves
that and surfaces the user-config / strict distinctions cleanly.

### D13. RemoveAll gap fix is a behavior-change carve-out for A2

- **Decided (2.1.f, 2026-05-08):** `UserConfigResolver.RemoveAll`
  is added to the new API and the 5 strict-RemoveAll caller
  sites (`cmd/stado/session.go`, `cmd/stado/agents.go`,
  `cmd/stado/plugin_gc.go`, `cmd/stado/plugin_install.go`,
  `internal/tui/model_sessions.go`) are migrated from
  strict-from-/ to user-config-aware. Net behavior: paths under
  `/home/user` (the Atomic-Fedora layout where `/home → /var/home`)
  are now removable; non-HOME paths still walk strict-from-/.
- **Why this is a carve-out from non-goal #1:** the fix
  generalizes EP-0028's existing Atomic-Fedora stance to the
  RemoveAll operation. The legacy `RemoveAllNoSymlink` was the
  ONE strict-from-/ holdover after EP-0028 added Under-variants
  for read/open/mkdir; it was forgotten in that pass. Operationally,
  `stado session delete` and friends are broken on Bazzite /
  Silverblue / Kinoite hosts today. Treating this as a
  separate behavior-fix spec would defer the bug indefinitely
  for symmetry that doesn't serve users.
- **Alternatives considered:**
  (a) Park as a separate spec and finish A2 first. Rejected —
      the bug is observable today on a major class of host
      (Atomic-Fedora derivatives), and the fix shape is bounded
      (one method + 5 caller sites) and uses the
      Under-trust-anchor pattern EP-0028 already validated.
  (b) Add `StrictResolver.RemoveAll` an "anchor-aware mode"
      flag. Rejected — this is the policy-soup pattern round-A2
      review explicitly rejected.
- **Test coverage:** 5 new tests in
  `userconfig_resolver_test.go` covering the symlinked-HOME
  Bazzite case, final-symlink rejection, idempotency on
  missing paths, in-user-space symlink-below-anchor rejection,
  outside-anchor strict-fallback parity.
- **Plugin impact:** none. Plugins call host imports; this is
  internal-runtime-confinement behavior. The bug existed in the
  internal RemoveAll path that backs `stado_*` operations
  invoked from cmd/stado / TUI, not plugin-facing semantics.

### D14. A1 scope: per-concern extraction, not unified Overlay/Picker interfaces

- **Decided (A1, 2026-05-08):** A1 lands as 6 in-package
  extractions of `internal/tui/model_render.go` —
  `sidebar.go`, `landing.go`, `quit_confirm.go`, `input_box.go`,
  `approval.go`, `choice.go`, `status_bar.go`, `blocks_render.go`.
  The unified `Overlay` interface from §2.2 is NOT implemented;
  the 8-picker shared `Picker` interface is NOT implemented.
  `model_render.go` shrinks from 1937 to 302 LoC (well under
  the §2.2 < 800 target).
- **Why the scope shift:** audit of the 5 "overlays" in §2.2
  (Help, Status, QuitConfirm, Approval, Choice) showed three
  distinct composition models in `View()`:
  - Help / Status — full-screen takeovers (early returns).
  - QuitConfirm — centred popup over an already-built base
    via `overlays.CenterOver`.
  - Approval / Choice — persistent layout-adjusting drawers
    that subtract height from the chat area
    (`m.approvalCardHeight()` / `m.choiceDrawerHeight()`)
    *before* the conversation viewport is sized.
  A unified `Overlay.View(width, height)` interface either
  flattens this heterogeneity (behaviour change, explicit
  non-goal) or is fictional uniformity that `View()` can't
  call through. Same shape for the 8 pickers: their selection
  side effects (slash → `handleSlash`; agent → `setAgentMode`;
  model → provider-switch logic; session → rename/delete/fork
  branches; etc.) are genuinely different — a `Picker` interface
  covering only `Visible / Update / Close` doesn't simplify the
  load-bearing code.
- **Alternatives considered:**
  (a) Force all 5 under one `Overlay` interface with a method
      that returns "I take over" vs. "I just contribute height"
      vs. "I wrap a base". Rejected — the contract is not
      really one interface; readers would have to memorise the
      three composition modes anyway.
  (b) Define `Overlay` covering only the full-screen takeover
      variants (Help, Status, QuitConfirm). Rejected —
      QuitConfirm wraps a base (`CenterOver`), Help/Status
      replace the base; even those three aren't uniform. The
      interface would be 3 lines and add ceremony.
  (c) Define `Picker` for the 8 picker packages. Rejected —
      see selection-side-effect divergence above.
- **Why this still meets the program goal:** the §2.2 LoC
  target is met (1937 → 302). The dispatcher and per-concern
  files give readers the same navigability the interface
  would have provided, without inventing structure that
  doesn't reflect real uniformity. Codex + Gemini consultation
  prior to landing confirmed the heterogeneity reading and the
  scope decision.
- **Trade-off:** an out-of-tree caller that wanted to
  programmatically enumerate overlays (e.g. for accessibility
  inspection) has no `Overlay` interface to range over. There
  are no such callers today; if one shows up, define the
  interface narrowly then.
- **Smoke testing:** the autonomous run could only verify
  `go build ./...` + `go test ./internal/tui/...` + `--help`
  / `--version` smoke. The plan's per-A1-commit interactive
  `stado run` smoke (D8) is the operator's pre-merge step.
- **Acceptance criteria delta:** plan §2.2 verification list's
  "One Overlay interface; >= 5 implementations" and "One
  Picker interface in its leaf package; 8 implementations"
  are NOT met — they're superseded by this decision. The other
  verification items (`go test -race`, model_render LoC drop,
  smoke combinations preserved) hold.

## Related

- `docs/eps/0040-bundled-local-inference.md` — A2 (workdirpath
  simplification) sets up the cleaner API the EP-0040 manager
  will use for binary, model, and state file paths.
- `docs/eps/0037-tool-dispatch-and-operator-surface.md` — context
  for the bundled-tool schema literals (B2).
- `docs/eps/0034-background-agents-fleet.md` (Superseded) /
  EP-0038 — context for `internal/runtime/fleet.go` /
  `fleet_bridge.go` and the FleetBridge plugin host import (1.3).
