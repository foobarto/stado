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
3. **No expansion of public API surface.** All structural splits
   reduce or hold steady; never grow.
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
| 1.2 | Test coverage        | Sandbox runner composition test                       | M    | Pending |
| 1.3 | Test coverage        | `internal/runtime/fleet_bridge.go` lifecycle tests    | M    | Pending |
| 2.1 | Tier A — A2          | `workdirpath` API simplification (`Resolver` type)    | M    | Pending |
| 2.2 | Tier A — A1          | `Model` struct + `model_render.go` consolidation      | L    | Pending |
| 2.3 | Tier A — A3          | `model_update`/`commands`/`stream` dispatcher split   | L    | Pending |
| 3.1 | Tier B — B1          | `config.go` validation extraction                     | S    | Pending |
| 3.2 | Tier B — B2          | `bundled_plugin_tools.go` schema builder              | S    | Pending |
| 3.3 | Tier B — B3          | Bridge lifecycle/wiring unification                   | M    | Pending |

Total: 9 items. Cost: 6×M + 2×L + 1×S, ballpark a few weeks of focused
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
4. **End of Phase 2.3 (A3)**. Update handler split. Mechanical move
   into `tui/behavior/`; smallest semantic risk in Tier A.
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

**Per-bridge specifics** (in addition to the four contracts):
- **SessionBridge.** Append-block, get-block-by-id, list-blocks,
  set-metadata. Assert the block-id seam (caller-supplied vs
  bridge-supplied) matches the existing wire format.
- **MemoryBridge.** Read / write / list / search semantics; the
  read-after-write-within-call invariant if it exists.
- **ApprovalBridge.** Outcome encoding (approved / denied /
  asked-but-no-bridge → defined default). Test the deny-on-nil
  default explicitly.
- **ChoiceBridge.** Same shape as ApprovalBridge: the no-bridge
  default; the typed-choice round-trip.
- **FleetBridge.** Spawn / list / send-message / read-messages /
  cancel. Assert that ID typing is preserved through the bridge.

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

### 1.2 Sandbox runner composition test

**What ships.** A single `sandbox_composition_test.go` that exercises
multiple sandbox layers in concert (Linux: `landlock + seccomp +
bwrap` over the same target binary; macOS: `sandbox-exec`). Today's
per-runner tests assert each layer in isolation; a misconfigured
policy could pass each but fail in composition.

**Files in scope.** Read-only refs:
- `internal/sandbox/runner.go` (interface).
- `internal/sandbox/landlock_linux.go`.
- `internal/sandbox/seccomp_linux.go`.
- `internal/sandbox/bwrap_linux.go`.
- `internal/sandbox/sbexec_darwin.go`.
- Existing per-runner tests (`landlock_linux_test.go`, etc.) — must
  remain green; this phase doesn't touch them.

**File added.** `internal/sandbox/composition_test.go`.

**Test scenarios.** Table-driven, each scenario:
1. Compose a multi-layer policy (e.g., landlock-deny-FS +
   seccomp-allow-net + bwrap-uid-isolation).
2. Run a small built-in test target (a `_testmain`-style helper
   binary, or invoke a simple shell command in a sandbox-spawned
   subprocess).
3. Assert the composed effect: e.g., FS-write to `/etc` denied
   despite seccomp allowing the syscall, because landlock blocks
   the open.
4. Assert error mapping: which layer reported the denial
   (this is what an operator sees in `stado audit`).

Minimum scenarios:
- FS-deny via landlock under bwrap; expect EACCES.
- Net-deny via seccomp + landlock-allow; expect ECONNREFUSED or
  EPERM as the runner currently maps it.
- macOS: sandbox-exec policy denies a write under bwrap-equivalent
  isolation; expect the runner's standard denial path.
- The "composed-but-empty" case: all layers default-allow → call
  succeeds (negative control).

**Skip discipline.**
- `t.Skip` cleanly when a layer is unavailable on the host:
  - Linux: skip if `bwrap` not in PATH, or if seccomp/landlock not
    supported by kernel (probe via small stub).
  - macOS: skip if `sandbox-exec` is missing (rare but possible
    in containers).
  - Windows: entire test file uses `//go:build linux || darwin`.
- A skip is not a failure but is logged so CI dashboards can
  notice the matrix has gaps.

**Verification.**
- [ ] `go test ./internal/sandbox/... -race` passes on Linux dev box.
- [ ] Same on macOS dev box.
- [ ] Composition test produces a clear skip line on hosts without
      a layer; no false-fail.
- [ ] Existing per-runner tests untouched and still green.

### 1.3 `internal/runtime/fleet_bridge.go` lifecycle tests

**What ships.** A test file for `fleet_bridge.go` covering the bridge
between subagent runtime and the FleetBridge plugin host import.
**Note:** `fleet.go` already has 21 tests in `fleet_test.go` —
that's not the gap. The gap is `fleet_bridge.go` (4461 bytes, no
test file). This was caught in the consult round; the original
brief was wrong.

**File added.** `internal/runtime/fleet_bridge_test.go`.

**Coverage targets.** The four contracts above (capability /
nil / forwarding / cancel) plus FleetBridge specifics:

- **Sync spawn.** Caller waits for spawn to complete; assert pid /
  agent-id surfaces; assert error path when spawn fails (e.g.
  capability denied, parent agent unknown).
- **Cancellation.** Cancel mid-spawn → ctx.Err returned; runtime
  state cleaned (no orphan record).
- **Message offsets.** `read-messages` with an offset returns
  messages from the right point; offset > current → empty,
  not error; offset < 0 → defined error.
- **Missing-agent paths.** All ops on an unknown agent ID return
  the typed "not found" error, not panic, not nil.
- **Concurrency.** Parallel spawn requests don't corrupt the
  agent registry; a cancelled spawn's slot is reused.

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
that consolidates the 38-public-function API into ~6 cohesive
methods. The legacy functions stay as **thin wrappers** around
`Resolver` for the duration of the migration; they're deleted
when call-site churn is low and all production callers have
moved to the new API. The migration does not change behaviour:
every legacy call lands on the same code path it does today.

**The legacy surface.** Inventory at HEAD:
`Resolve`, `RootRel`, `OpenReadFile`,
`ReadRegularFileNoSymlinkLimited`,
`ReadRootRegularFileLimited`,
`ReadRegularFileUnderUserConfigLimited`,
`OpenRegularFileUnderUserConfig`,
`OpenRegularFileNoSymlink`,
`ReadRegularFileUnderUserConfigNoLimit`,
`MkdirAllUnderUserConfig`, `MkdirAllNoSymlinkUnder`,
`MkdirAllRootNoSymlink` (declared twice — see open question),
`MkdirAllNoSymlink`, `OpenRootNoSymlink`,
`OpenRootUnderUserConfig`, `OpenRootNoSymlinkUnder`,
`RemoveAllNoSymlink`, `WriteFile`, `WriteRootFileAtomic`,
`WriteRootFileAtomicExactMode`, `RootRelForWrite`,
`Glob`, `GlobLimited`, plus internals.

**Target shape.** A `Resolver` parameterised by a trust anchor
(workdir root, user config dir) with these primitives:

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
```

`ResolveOpt` is a functional-options type that absorbs the
`Limited` / `NoSymlink` / `UnderUserConfig` / `Root` axes that
today's name-explosion encodes in function names:
`WithLimit(n int64)`, `WithSymlinks(bool)`, `WithAnchor(workdir | userConf | root)`.

**Why options over names.** The current API multiplies one
behaviour axis (limit, symlink mode, anchor) into a separate
function each. Options make the axis explicit at the call site,
shrink the public surface, and let new axes (e.g. EP-0040's
`WithFsync(bool)` for atomic-write durability tuning) land
without inventing yet another function.

**Migration strategy** (Codex's call — adopt verbatim):
1. Land `Resolver` + the new methods alongside the legacy
   functions. Legacy functions get rewritten as one-liners on
   top of `Resolver` (preserving behaviour and the public
   signature). Confirm via tests + smoke runs.
2. **Audit `internal/mcpbridge`** — Gemini flagged likely
   "safety leakage" where MCP-side path resolution duplicates
   workdirpath logic. Fold the audit into A2; don't break
   into a separate phase. If MCP currently resolves paths
   without going through workdirpath, the migration is to make
   it use `Resolver`.
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
- [ ] mcpbridge audit produced a defined outcome (either a
      migration to `Resolver` or a written rationale for why
      mcpbridge stays separate).
- [ ] Smoke: `stado --help`, `stado run --help`,
      `stado plugin install --help` all exit 0.

### 2.2 (A1) `Model` struct + `model_render.go` consolidation

**What ships.** The TUI `Model` struct shrinks; overlay state
moves into the existing `internal/tui/overlays/` package
(which already has `center.go`, `help.go`); pickers consolidate
into a `internal/tui/pickers/` package (creating it if not
already present, otherwise consolidating into existing structure).
`model_render.go` shrinks correspondingly: per-overlay rendering
moves with the overlay; per-picker rendering moves with the
picker. `Model.View()` becomes a thin orchestrator.

**Framing — consolidation, not new packages.** Codex's correction:
the relevant package directory (`internal/tui/overlays/`) already
exists. A1 is moving sprawl from `model.go` and `model_render.go`
*into* that existing package, not creating a parallel structure.
Where a sub-area doesn't yet have a package, A1 creates it; where
it does, A1 moves work into it.

**The big picture.**
- `Model` today: ~120+ field-equivalent lines mixing chat state,
  TUI lifecycle, picker overlays, approval/choice flows, sidebar
  management, background plugin orchestration, loop/monitor.
- After A1: `Model` holds chat state + lifecycle + a single
  `activeOverlay Overlay` slot (interface, not pointer to typed
  struct) + the sidebar state. Pickers are children of an
  `OverlayPicker` implementation.

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
1. Define `Overlay` interface in `internal/tui/overlays/overlay.go`.
2. Move `Help` (already an overlay-shaped thing) onto the new
   interface. One commit.
3. Move `Status` overlay. One commit per overlay until done.
4. Migrate `Model` to hold one `Overlay` slot (or an ordered slice
   if overlay layering matters — see open question "OverlayPicker design").
5. Pickers: extract `internal/tui/pickers/` (or use existing
   `modelpicker` package as anchor). Each picker becomes its own
   type implementing the picker contract. `OverlayPicker` wraps
   one.
6. `model_render.go` per-overlay/per-picker render code follows
   each move. `View()` becomes ~50 lines.

**Files touched.**
- `internal/tui/model.go` — significant shrink.
- `internal/tui/model_render.go` — significant shrink.
- `internal/tui/model_status_modal.go`, `model_quit_confirm.go`,
  `model_help.go` (and their test files) — move to overlay package.
- `internal/tui/overlays/` — gain new files per overlay.
- `internal/tui/pickers/` (or existing separate picker packages:
  `agentpicker`, `fleetpicker`, `modelpicker`, `personapicker`,
  `sessionpicker`, `taskpicker`, `themepicker`) — at A1 design time,
  confirm whether "pickers consolidate" means adding a shared
  interface to the 8 existing separate packages (preferred) or
  merging them into one. The plan intends the former.
- TUI tests adjust import paths but assertions stay the same.

**Risk.** Largest mechanical churn in the program. Risk vector:
test suite passes but a corner case (overlay-over-overlay,
ESC-while-streaming, etc.) regresses subtly. Mitigations:
- Migrate one overlay at a time; checkpoint after each.
- `stado run` smoke check at each commit.
- Existing TUI test files (`*_test.go`) remain a safety net.

**Verification.**
- [ ] `go test ./internal/tui/... -race` passes.
- [ ] `Model` struct field count reduced (target: < 60).
- [ ] `model_render.go` LoC reduced (target: < 800 from 1937).
- [ ] One `Overlay` interface; >= 5 implementations.
- [ ] Smoke: `stado run` opens, ESC closes overlays, Q quits.
- [ ] Help / Status / Approval / Choice / Picker each tested
      against the `Overlay` interface (not the old typed shape).

### 2.3 (A3) `model_update`/`commands`/`stream` dispatcher split

**What ships.** The `tea.Model.Update` handler — currently split
across `model_update.go` (1544 LoC), `model_commands.go` (1728),
`model_stream.go` (1625) — moves into a `internal/tui/behavior/`
package, one file per message family. `model_update.go` becomes
a thin dispatcher: ~100 lines that route by message type.

**The split.**
- `tui/behavior/commands.go` — slash-command and user-input
  message handlers.
- `tui/behavior/stream.go` — provider streaming + tool-call
  flow handlers.
- `tui/behavior/picker_response.go` — picker selection / dismiss
  handlers.
- `tui/behavior/lifecycle.go` — init / quit / window-resize.
- `tui/behavior/tools.go` — tool invocation + result handlers.

Each handler has the same shape:
```go
func HandleSlashCommand(m *tui.Model, msg SlashCommandMsg) (tui.Model, tea.Cmd)
```

The dispatcher in `model_update.go`:
```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case SlashCommandMsg:    return behavior.HandleSlashCommand(&m, msg)
    case StreamDeltaMsg:     return behavior.HandleStreamDelta(&m, msg)
    // ...
    }
}
```

**The Update handler today.** model_update.go's giant
type-switch covers ~30+ message variants. Inventory pass at
phase start: extract a list of every `case` arm, name the
target behavior file, commit-by-commit move plan.

**Optional: telemetry wrapper.** Gemini flagged that
`model_update.go` may have heavy otel/telemetry calls inline
that bloat the handlers. *Verify before committing.* If true,
introduce `tui/behavior/telemetry.go` with `WithTelemetry(handler)`
wrapper; otherwise skip.

**Files touched.**
- `internal/tui/model_update.go` — shrinks to dispatcher.
- `internal/tui/model_commands.go` — content moved out, file
  may delete.
- `internal/tui/model_stream.go` — same.
- `internal/tui/behavior/` — new package.

**Approach.**
1. Inventory pass: list every `case` arm in `Update`. For each,
   target file under `behavior/`.
2. Extract one message family at a time. One commit per family.
3. After each extraction, run the TUI tests for that family.
4. Last commit: `model_update.go` becomes the dispatcher.

**Risk.** Lower than A1 — the moves are mostly mechanical, the
test files for `model_update`/`commands`/`stream` already
separate concerns. Risk: a handler that mutates `Model` fields
in non-obvious ways doesn't survive the move (the `*Model`
pointer signature should preserve this, but verify).

**⚠ Import-cycle pre-flight (decide before execution).** Handlers
that accept `*tui.Model` cannot live in `internal/tui/behavior/`
as a separate package: `tui` (which houses `model_update.go`)
would import `behavior`, and `behavior` would import `tui` for the
`Model` type — Go import cycle. The existing `overlays/` package
avoids this by never importing `tui`. Two options:

1. **Keep files in `package tui`** — place `handler_commands.go`,
   `handler_stream.go`, etc. directly in `internal/tui/`. No
   subdirectory, no new package. The dispatcher in
   `model_update.go` is still thin (~100 lines); the per-family
   grouping is by filename rather than package. This is consistent
   with the existing split across `model_commands.go` /
   `model_stream.go`. *Recommended.*

2. **Extract `Model` to a sub-package** (`internal/tui/model/`)
   so both `tui` and `behavior` can import it without a cycle.
   More upheaval; only warranted if the package boundary is
   needed for reasons beyond file organisation.

Confirm with operator before A3 execution starts.

**Verification.**
- [ ] `go test ./internal/tui/... -race` passes.
- [ ] `model_update.go` < 200 LoC (dispatcher only).
- [ ] `behavior/` package: one file per message family,
      each < 500 LoC.
- [ ] All handler signatures match `func(*Model, Msg) (Model, tea.Cmd)`.
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
- `internal/plugins/runtime/bridge.go` (new) — `Bridge` interface.
- `internal/plugins/runtime/host.go` — Init/Dispose orchestration.
- `internal/plugins/runtime/host_session.go`,
  `host_memory.go`, `host_ui.go`, `host_agent.go` — implement
  `Bridge`.

**Approach.**
1. Define `Bridge` interface.
2. Add `Init`/`Dispose` to each concrete bridge — initially
   no-ops where there's nothing to do.
3. `Host` calls `Init` on each registered bridge during setup;
   `Dispose` on teardown. Today's nil-bridge defense becomes
   one check at registration, not every call.
4. The contract tests from Phase 1.1 protect this — they assert
   the four contracts (cap gate / nil / forwarding / cancel)
   regardless of whether the lifecycle goes through `Bridge.Init`.

**What this is NOT.**
- Not a generic operations dispatcher.
- Not a registry that erases bridge types.
- Not a refactor of the operation method signatures.

**Verification.**
- [ ] `go test ./internal/plugins/runtime/... -race` passes.
- [ ] All 1.1 contract tests untouched and green.
- [ ] `Host` setup is shorter (one loop, not five).
- [ ] No new type-assertion hot paths introduced.
- [ ] `host.go` LoC reduced.

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

- **`MkdirAllRootNoSymlink` declared twice** in workdirpath. Real
  duplication or a typo in my legacy-API inventory? Verify at A2
  start; if real, the resolver migration deletes one form.
- **`OverlayPicker` design.** Whether overlays form a stack (modal-
  over-modal) or are mutually exclusive. Today's `Model` has
  multiple `*Picker.Visible` flags simultaneously — meaning
  multiple can be true. Decide at A1 design time whether that's
  a bug to fix or a feature to preserve.
- **Telemetry wrapper for A3.** Verify `model_update.go` actually
  has heavy inline otel/telemetry before extracting a wrapper.
  If telemetry is lightweight, skip the wrapper.
- **A2 cherry-pick to main.** Specifically when. Probably right
  after merge checkpoint 2 (end of A2). Confirm with operator
  before cherry-picking.

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
  EP-0040 up cleanly. A1 must precede A3 because the Update
  split's destination (`tui/behavior/`) is cleaner once `Model`
  has shed overlay/picker state.

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

- **Decided:** `Resolver` lands alongside the legacy 38-fn API;
  legacy functions become one-line wrappers; callers migrate
  per-package; legacy deleted at end of A2.
- **Alternatives:** big-bang rewrite; new package alongside
  workdirpath (deprecated original).
- **Why:** workdirpath is the safety surface for every fs touch.
  Wrapper migration preserves exact behaviour while the public
  API shrinks; per-package migration commits stay small and
  reviewable.

### D7. A1 framed as consolidation, not new packages

- **Decided:** A1 moves work into existing `internal/tui/overlays/`
  + (likely existing) picker package, rather than creating new
  parallel packages.
- **Alternatives:** new `tui/ui/` umbrella package; flat structure.
- **Why:** the destination directories already exist (verified:
  `tui/overlays/help.go`, `tui/overlays/center.go`). Moving into
  them costs less and matches the project's small-focused-package
  posture.

### D8. Per-merge-checkpoint smoke check

- **Decided:** every checkpoint runs `stado --help`,
  `stado run --help`, and one full `stado run` smoke session.
- **Alternatives:** rely on test suite + lint; manual smoke at
  end only.
- **Why:** structural splits can break wiring (binding TUI
  message types to the wrong handlers, e.g.) in ways the unit
  tests don't catch. A smoke check is cheap and catches the
  worst class of regression.

## Related

- `docs/eps/0040-bundled-local-inference.md` — A2 (workdirpath
  simplification) sets up the cleaner API the EP-0040 manager
  will use for binary, model, and state file paths.
- `docs/eps/0037-tool-dispatch-and-operator-surface.md` — context
  for the bundled-tool schema literals (B2).
- `docs/eps/0034-background-agents-fleet.md` (Superseded) /
  EP-0038 — context for `internal/runtime/fleet.go` /
  `fleet_bridge.go` and the FleetBridge plugin host import (1.3).
