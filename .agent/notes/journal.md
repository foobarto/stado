# Journal — refactor+quality-2026-q2

Append-only. Newest at the bottom.

---

## 2026-05-07 — session start
- Mode: Feature / Phase 1.1 (bridge contract tests).
- Plan committed (3 rounds of consult+fix with codex/gemini).
  Last commit: `ef55cb9`.
- About to read existing bridges (host_session.go, host_memory.go,
  host_ui.go, host_agent.go, host.go) and existing test files
  (bridge_test.go, memory_bridge_test.go, host_ui_choose_test.go,
  session_caps_test.go, host_test.go) before sketching the harness.
  Several test files already exist that overlap with 1.1's scope —
  need to figure out what's covered vs. what 1.1 adds.

## 2026-05-07 — context loaded; harness design

What's already covered in `internal/plugins/runtime/`:
- `host_test.go` — NewHost manifest parsing.
- `session_caps_test.go` — session/llm cap parsing + a `fakeBridge`
  with call counters; explicitly notes "we can't invoke wasm host
  imports without a wasm module" and tests indirectly.
- `memory_bridge_test.go` — LocalMemoryBridge implementation
  round-trip (not the host import gate).
- `host_ui_choose_test.go` — `decodeChooseRequest` validator only.

What 1.1 needs to add: tests for the host-import dispatch logic
itself — capability-gate, nil-bridge, exact forwarding, cancel.

Design call: bridge contract tests use a real wazero runtime +
`InstallHostImports`. After registration, the host module's
exports are reachable via `rt.Wazero().Module(NamespaceStado)
.ExportedFunction(name)` and callable from Go.

Two-tier tests:
- **Memory-free** (capability-gate, nil-bridge): the closure
  short-circuits before touching `mod.Memory()`. Calling the
  exported function with arbitrary i32 args produces the -1
  return without dereferencing memory. Test directly.
- **Memory-using** (exact forwarding): need a thunk wasm module
  that imports the host imports + exports a caller. Hand-write
  bytes — minimal module per import, embed in the harness.

Cancel contract: where the bridge takes ctx, test that the
bridge's recorded ctx is the same one passed to the host import,
and that ctx cancellation reaches the bridge. Where the bridge
doesn't take ctx (SessionBridge.ReadField), the contract is N/A
for that method.

Starting with the harness file now.

## 2026-05-08 — A2 2.1.f: Bazzite RemoveAll gap fix

Operator (correctly) flagged that the strict-RemoveAll callers
in cmd/stado/session.go, cmd/stado/agents.go,
cmd/stado/plugin_gc.go, cmd/stado/plugin_install.go, and
internal/tui/model_sessions.go all walk through paths under
HOME/XDG (worktrees, plugin install dirs). On Atomic Fedora /
Bazzite where `/home → /var/home` is a system symlink, the
strict-from-/ walk rejects at the `/home` component.

This is a PRE-EXISTING bug in the legacy `RemoveAllNoSymlink` —
EP-0028 added the *UnderUserConfig family for read/open/mkdir
to fix exactly this case but RemoveAll was never given an
Under-equivalent.

Decision: treat the fix as in-scope for A2 (carve-out from
non-goal #1). Reasons:
- Operationally broken on a real and growing class of host
  (Atomic Fedora derivatives are the recommended Linux
  installation in many environments).
- Fix shape is bounded — one method + 5 caller sites — using
  the Under-trust-anchor pattern EP-0028 already validated.
- Symmetry argument cuts the wrong way: forcing the fix into a
  separate spec defers it for symmetry's sake while real users
  hit it.

Implementation:
- workdirpath/userconfig_resolver.go: added RemoveAll(path)
  method, documented as the EP-0028 RemoveAll companion.
- workdirpath/workdirpath.go: added unexported
  removeAllUnderUserConfig helper. Mirrors the existing
  *UnderUserConfig pattern: anchor lookup; if no anchor,
  falls back to RemoveAllNoSymlink (strict from /); else
  opens parent via OpenRootUnderUserConfig and RemoveAll
  within the *os.Root.
- workdirpath/userconfig_resolver_test.go: 5 new tests
  including the symlinked-HOME Bazzite case (verifies removal
  works via the symlink AND that the real path is gone).

Caller migrations (5 sites, all switching from
NewStrictResolver().RemoveAll to NewUserConfigResolver().RemoveAll):
- cmd/stado/session.go:249,294 (worktree delete)
- cmd/stado/agents.go:134 (agent kill worktree)
- cmd/stado/plugin_gc.go:145 (plugin gc)
- cmd/stado/plugin_install.go:146 (install rollback)
- internal/tui/model_sessions.go:236 (TUI worktree delete)

Plan updated: D13 captures the carve-out + alternatives
considered. Phase 2.1 staging gains a 2.1.f step.

## 2026-05-07 — A2 invariant check: workdirpath ≠ plugin extensibility

Operator reminder: stado's invariant is "primitives, not policies"
— the runtime exposes primitives so plugins/callers compose their
own trust models. Re-consulted codex + gemini specifically against
the 4-type design.

**Resolution:** the invariant applies to the
**plugin-facing host-import surface** (stado_fs_*,
stado_proc_*, stado_net_*; documented in
`docs/eps/0002-all-tools-as-plugins.md` and the architectural
reset notes). Those ARE the primitives plugins compose.

`internal/workdirpath` is NOT the plugin extensibility surface.
It's the runtime's internal confinement layer that backs the
host-import implementations after capability gating. Plugins
never import workdirpath; they call host imports.

Codex confirmed this distinction directly. Gemini argued the
invariant also applies to internal subsystems wanting different
trust models, but the architectural docs are clear: the runtime's
own confinement is allowed to be opinionated. If a plugin-facing
fs-primitive layer is ever needed beyond the existing host
imports, it belongs in a new `pkg/fsprim` — not retrofitted
into workdirpath.

**Action:** added a Scope-clarification block at the top of D12
documenting this. No code change needed; the 4 types stand.

The future `pkg/fsprim` direction is captured here so a future
session knows where to start IF that need arises (no current
caller demands it).

## 2026-05-07 — A2 round-final review fixes

Round-final consultation (codex + gemini) on the landed types:
- Both validated the 2.1.d → 2.1.Y deferral as correct (codex:
  "you are not rationalizing"; gemini: "correct engineering
  decision").
- Both flagged Resolver.OpenRegularFile semantic mismatch — its
  doc promised regular-file rejection but the underlying
  OpenReadFile delegate doesn't enforce it. Fixed: added a
  post-Stat() check that rejects non-regular files. Behavior
  expansion at the new API surface (legacy OpenReadFile is
  unaffected).

Test-coverage gaps applied:
- Resolver: NUL rejection (strict path; AllowMissing has a
  legacy quirk where the parent-search fallback surfaces NUL
  as ENOENT-like → succeeds; documented but not asserted).
- Resolver: OpenRegularFile rejects directory targets,
  WriteFileAtomic rejects directory targets.
- RootResolver: WriteFileAtomic rejects symlink target +
  directory target; ReadFileLimited rejects directory targets;
  no-leak under absolute-path / parent-traversal escape attempts
  on both Mkdir and Write.
- UserConfigResolver: outside-anchor strict-fallback rejects
  symlinks (no anchor → no chain-above-anchor leeway).
- UserConfigResolver: discriminating longest-anchor test —
  XDG_STATE_HOME (longer) vs HOME (shorter), where choosing
  HOME would surface the .local/state symlink as
  below-anchor and reject; choosing XDG_STATE_HOME accepts it.

Plan cleanup: removed stale "create behavior-matrix doc" file
listing per codex (matrix was deferred when 2.1.d was deferred).

Tests count after round-final: 56 new tests (was 49) + 29 legacy.
Stable under `-count=5 -race`. Full repo `go test ./...` green.

Round-final pitfall flag (codex) for caller migration:
"don't let path-under-HOME push strict / plugin-sandbox
callers into UserConfigResolver." Plugins / sandbox callers
should use StrictResolver regardless of where the path lives.

## 2026-05-07 — 2.1.b/c landed; 2.1.d deferred to 2.1.Y

UserConfigResolver (10 tests) and StrictResolver + Under (16
tests) both committed. All 4 types now exist alongside legacy.
49 new tests across 4 types + 29 legacy tests, all green under
`-count=5 -race`.

Started 2.1.d (wrapper rewrite) — wrote a fresh resolver.go
with impls moved inline, then realized:

1. The impl-move requires extracting LEAF helpers (openRootNoSymlink,
   mkdirAllRootNoSymlink) that are currently exported. These
   touch ALL 4 type families' implementations.
2. The "wrapper rewrite" doesn't add functional value — both
   forms (legacy with impls, legacy as 1-line wrappers) work
   identically until legacy is deleted.
3. Doing the impl-move now means doing it AGAIN at 2.1.Y when
   legacy is removed. Doing it once at 2.1.Y is simpler.
4. Caller migrators during 2.1.e..N see legacy in its familiar
   form if the impls don't move yet. Easier to follow git history.

Reverted the resolver.go inline-impl draft; restored the
delegating form. Updated plan: 2.1.d marks deferred, 2.1.Y
bundles impl-move with deletion. New plan flow:
- 2.1.aa: mcpbridge audit (done)
- 2.1.a/b/c: types land with tests (done)
- 2.1.d: deferred to 2.1.Y
- 2.1.e..N: caller migration (next session)
- 2.1.X: Deprecated: markers
- 2.1.Y: delete legacy + inline impls into new types

This is a clean stopping point for A2 in this session. Future
sessions pick up at 2.1.e (caller migration is the bulk of
remaining work).

## 2026-05-07 — 2.1.a Resolver + RootResolver landed

Both new types compile and pass their own tests (23 new tests).
Legacy unchanged; existing 29 tests still pass. Stable under
`-count=10 -race`.

Methods are thin delegators to the legacy implementations during
this migration window. The dependency flips at 2.1.d: legacy
becomes one-line wrappers around the new types.

Canary-caller decision: gemini round-2 wanted a canary migration
per type for ergonomic feedback before the API locks in at 2.1.d.
After surveying the actual call-site distribution, I'm deferring
canaries to 2.1.e (broad migration). Reason: the highest-traffic
legacy functions (OpenRootUnderUserConfig: 43 calls,
MkdirAllUnderUserConfig: 21) are user-config flavor — landing
in 2.1.b. Most workdir / *os.Root callers use the legacy fns
ONCE per function and have no implicit workdir field, so
constructing a Resolver per call is genuinely more verbose than
the legacy form. The ergonomic win comes when a caller holds
the resolver as a long-lived struct field, which is a refactor
better done as part of broad migration.

API ergonomic review at desk-check time:
- `Resolver`: clean, but most callers will need light refactoring
  to take advantage (store the resolver as a field rather than
  constructing per call).
- `RootResolver`: independent constructor is the right call.
  Callers with a long-lived *os.Root (e.g.
  `internal/runtime/conversation.go`) will see immediate
  reduction. One-shot callers won't.
- `OpenRegularFile` (read-only) feels right; the explicit
  read-only-ness signals intent at the call site.
- No `OpenFile(flags)` was rejected for good reason — it would
  invite O_CREATE/O_TRUNC drift.

## 2026-05-07 — A2 design pivot + 2.1.aa mcpbridge audit

Two rounds of code-level consultation (codex + gemini) on A2's
design. Both rounds independently rejected D12's original
"single Resolver + WithAnchor options" approach. Convergent
recommendation: 4 narrow types (Resolver / UserConfigResolver /
StrictResolver / RootResolver) per security policy, no options-
based dispatch, no path-shape dispatch, no generic OpenFile(flags),
RootResolver independently constructible. Plan + D12 rewritten
(commit 26ed948) before any code lands.

2.1.aa mcpbridge audit complete. Verified
`internal/mcpbridge` has ZERO filesystem operations. The package
is 109 lines of pure JSON-over-RPC bridge to external MCP
servers — no `workdirpath` import, no `filepath.*`, no
`os.Open/ReadFile/WriteFile/Mkdir/Stat`, no `EvalSymlinks`, no
`*os.Root`. The round-3 "audit mcpbridge for safety leakage"
flag is fully addressed: there is no leakage to fix.

Conclusion for A2 verification: "mcpbridge audit produced a
defined outcome" → "no fs/workdirpath usage; out of scope".
mcpbridge stays untouched through the entire A2 phase.

## 2026-05-07 — Phase 1 round-4 review fixes

Codex + gemini round-4 review caught real gaps in Phase 1 work.
All "must-fix" items applied:

- Sandbox T2 false-positive risk: `Available()=LookPath` isn't
  enough (codex saw bwrap on PATH but T2 still failing in their
  env). Added `tier2ReadyRunners(t)` probe that runs a benign
  `true` through the runner and skips T2 with a clear log if
  the probe fails. Negative control test now uses the probed
  list.
- Sync spawn cancel orphan: plan promised "runtime state cleaned
  (no orphan record)"; implementation deliberately doesn't (spawn
  goroutine uses RootCtx, not caller ctx). Added test asserting
  the actual behaviour (entry remains in registry with running
  status after caller cancel). Plan edited to match.
- ReadMessages offset gaps: added since>current, since<0, and
  since-forwarded tests. Documents current behaviour where since
  is echoed through unchanged; plan edited to note alignment is
  out of scope.
- FleetBridge cancel-prop: added cancel-prop tests for AgentList,
  AgentSendMessage, AgentCancel (previously only Spawn and
  ReadMessages were covered).
- Forwarding fields: AgentSpawn forwarding now covers Persona /
  ParentSession / AllowedTools / SandboxProfile / Ephemeral.
  LLMInvoke covers Persona / System / Temperature.
  AgentReadMessages asserts forwarded Since (was 0; now 5).
- Plan edits: Phase 1.1 SessionBridge / MemoryBridge / Approval /
  Choice / Fleet specifics rewritten to match HEAD interfaces.
  Phase 1.3 cancellation, message-offset, and missing-agent
  sections updated to reflect actual behaviour.

Deferred (per Bartosz's instruction):
- WASM encoder hardening (duplicate names, negative NumParams,
  zero-result thunks). No real hazard at current scale.
- Channel-signalled cancel pattern (replacing time.Sleep). Works
  today; opportunistic refactor.
- Phase 2.1 path-traversal hardening — fold into A2 spec at start.

## 2026-05-07 — Phase 1.3 complete

FleetBridgeAdapter contract tests cover the layer the plan was
specifically worried about (no test file existed for fleet_bridge.go;
fleet_test.go covered the underlying *Fleet, not the adapter).

17 tests covering all 5 adapter methods + concurrency (20-way
parallel spawn). Stable under -count=10 -race.

Plan-vs-reality: Fleet.Cancel is documented idempotent (returns
nil for unknown IDs). Plan called for typed not-found error.
Aligning would be a behavior change; existing callers depend on
idempotency. Kept current behavior; documented divergence in the
test for future-fix follow-up.

Race detector caught a latent issue in the package's existing
fakeSpawner (gotPrompt write isn't atomic). Existing tests run
serially so they don't hit it. My concurrency test would have, so
I added a tiny race-safe `concurrentSpawner` for that test only —
didn't touch fakeSpawner per "don't fix things you weren't asked
to fix."

Phase 1 is now complete. Ready for merge checkpoint #1.

## 2026-05-07 — Phase 1.2 complete

Runner contract test in
`internal/sandbox/runner_contract_test.go` follows the round-3
plan revision: Tier 1 covers command-construction + exec
allow-list semantics (every available runner); Tier 2 covers
runtime FS enforcement (only BwrapRunner here on Linux, would
extend to SbxRunner on macOS).

Tier 2 design choice: subprocess result inspected via
`cmd.Run()` returning `*exec.ExitError` for denied writes and
checking the host-visible filesystem for the un-created file.
This matches the round-3 reframe ("subprocess exit code, not
return value from `Command`") because `Runner.Command` returns
only `*exec.Cmd`, never a denial — denial happens inside the
spawned bwrap/sandbox-exec process.

Composition parked as
`.agent/specs/open/sandbox-multilayer-composition.md`. Verified
against source: `runner_linux.go::detectList` returns only
`BwrapRunner` and `NoneRunner`; landlock/seccomp exist as modules
but aren't wired.

## 2026-05-07 — Phase 1.1 complete

All 5 bridges have contract tests for all 4 contracts. Pattern:
recordingFooBridge with mu+atomic counters, ctx capture, blocking
flags. cap-gate / nil-bridge tests run with allBridgeImports
thunk shape; forwarding tests stage payloads in thunk memory and
assert recorder + return buffer. Cancel tests use context.WithTimeout
and assert on the recorder's captured ctx.Err() (the closure return
races with wazero's CloseOnContextDone).

Test counts: SessionBridge 11, MemoryBridge 9, ApprovalBridge 6,
ChoiceBridge 6, FleetBridge 13. Total 47 (the harness probe is
counted in the SessionBridge file).

Plan-vs-reality findings logged here for B3:
- SessionBridge ops in plan ("Append-block, get-block-by-id,
  list-blocks, set-metadata") don't match reality. Actual:
  NextEvent / ReadField / Fork / InvokeLLM. Tests adapted.
- ChoiceBridge return convention is bytes-encoded (positive ok,
  negative error msg via encodeToolSidePayload). Tests assert
  the sign + the error message text on cap-deny / nil-bridge
  paths. Other bridges use plain -1 sentinel.

## 2026-05-07 — probe failed, need wasm encoder

Wazero panics with "calling ExportedFunction is forbidden on host
modules" when you try to invoke a host import directly. The probe
test confirmed this. So the harness can't take the easy path.

Path forward: a small in-Go wasm encoder that builds a thunk
module per test session. The thunk imports the stado host imports
we want to test and exports a Go-callable thunk per import. The
thunk has its own linear memory the harness can pre-populate
before invoking, satisfying the forwarding-contract tests too.

This is more infrastructure but covers all four contracts with
the same shape. Estimating ~150 LoC for a focused encoder
supporting i32 params/results + memory + simple thunks. Wasm
binary format is straightforward enough.

## 2026-05-08 (session 2) — 2.1.e finished, 2.1.X landed

Picked up from STATE.md's "Up next when resuming" list.

### Drift from prior state file

State file said cmd/stado had 5 files left (`learning.go`,
`plugin_init.go`, `selfupdate.go`, `session_export.go`,
`session_fork.go`). Two surprises once I started the
verification grep:

1. **plugin_install.go was only partly migrated.** State file
   marked it done, but the 2.1.f commit only touched the one
   `RemoveAll` call. Three other calls remained
   (`OpenRootUnderUserConfig` ×2, `MkdirAllUnderUserConfig`).
   Fixed in `dcaf422`.
2. **Test files weren't migrated.** Around 13 callers of the
   leaf helper `workdirpath.OpenRootNoSymlink` lived in
   tests across `fs/`, `skills/`, `state/git/`,
   `tui/render/`, `cmd/stado/`. Earlier 2.1.e batches
   migrated production code in those packages but didn't
   sweep the tests. Routed all to
   `NewStrictResolver().OpenRoot` (same delegation under the
   hood — `strict_resolver.go:85`). One self-contained
   commit, `05ca9c9`.

Verification grep after the two commits returns only the
allowed new-API + repo-discovery identifiers:

```
74 workdirpath.NewUserConfigResolver
24 workdirpath.NewRootResolver
15 workdirpath.New
14 workdirpath.NewStrictResolver
 5 workdirpath.LooksLikeRepoRoot
 3 workdirpath.FindRepoRoot
 1 workdirpath.FindRepoRootOrEmpty
```

The single comment hit on `workdirpath.Resolve` in
`host_lsp.go` (where it referenced "lspfind's
workdirpath.Resolve") was tightened to `Resolver.Resolve` to
match the actual call shape in `lspfind/*.go`.

### Pattern hits worth noting

- **`replace_all=true` is not whitespace-aware.** First pass
  on `session_fork.go` used the same `old_string` for two
  `MkdirAllUnderUserConfig` calls that differed only in
  leading indent (line 91 inside a closure, line 153 at
  top-level). The replace_all only matched the indented one;
  line 153 needed a separate edit. Lesson: when batching
  with replace_all, check the diff stat afterwards
  (or the post-edit grep) before assuming it caught
  everything.
- **plugin_init.go's function-pointer pattern was
  ergonomic.** Original code used `write :=
  workdirpath.WriteRootFileAtomic` then
  `write = workdirpath.WriteRootFileAtomicExactMode`
  conditionally. With method values bound to a single
  `*RootResolver`, the same shape works:
  `rr := workdirpath.NewRootResolver(root); write :=
  rr.WriteFileAtomic; if exactMode { write =
  rr.WriteFileAtomicExactMode }`. Method values on a shared
  receiver have identical signatures, so the `func` variable
  binds cleanly. Codex/gemini round-2 had questioned how
  variable-typed delegations would translate; this is the
  proof.

### 2.1.X Deprecated markers

23 markers, one per exported legacy fn, last paragraph of
each godoc:

```
// Deprecated: use <new-API equivalent> instead. Removed in 2.1.Y.
```

Each names the specific replacement (`New(workdir).Resolve`,
`NewStrictResolver().OpenRoot`, etc.). The `OpenReadFile`
marker also notes the new `Resolver.OpenRegularFile`
additionally rejects non-regular files (the round-final fix
from 2026-05-07) so a future migrator who hits a behavior
diff knows it's intentional.

Lint check: `golangci-lint run ./internal/workdirpath/...`
returns 0 issues. Staticcheck SA1019 doesn't flag
same-package deprecated use, so the in-package delegations
(new resolver methods → legacy fns until 2.1.Y) don't
trigger the warning. Out-of-tree callers (forks, pre-merge
branches) will get the warning if they re-introduce a
legacy call.

Full repo `go test ./...` green. Doc-only commit `816ebd8`.

### Tooling note (not actionable)

`golangci-lint run ./...` panics with "file requires newer
Go version go1.26 (application built with go1.25)". The
tool's bundled toolchain is older than something in the
codebase — pre-existing tooling state, unrelated to this
session's edits. Per-package runs work fine. Probably worth
a separate ticket to bump the lint binary, but not in this
batch.

### Next when resuming

2.1.Y. Pre-flight: re-run the verification grep
(`grep -rEho 'workdirpath\.[A-Z]' --include='*.go' | grep -v
internal/workdirpath` should return only `New*Resolver` /
`New` / `LooksLikeRepoRoot` / `FindRepoRoot{,OrEmpty}`).
Then move every legacy function body into the corresponding
new-type method, delete the legacy exports, and consolidate
the 29 legacy tests with the 56 new-type tests. Most of this
is mechanical — the new methods are already thin delegators,
so the move is copy-the-body + delete the wrapper. Internal
helpers (`splitAbsoluteRoot`, `userTrustAnchor`,
`removeAllUnderUserConfig`, `writeRootFileAtomic`) stay
unexported in whichever file makes sense.

## 2026-05-08 (session 2 cont.) — 2.1.Y landed

Picked the **rename** interpretation of "delete legacy +
inline impls" rather than the literal copy-into-methods
interpretation. Rationale:

- The legacy bodies are the security-critical no-symlink
  walks — moving them around the codebase invites subtle
  breakage. Their git history is load-bearing for blame on
  any future security review.
- Cross-fn calls within the legacy code are dense
  (`OpenRegularFileNoSymlink` calls `OpenRootNoSymlink`,
  `MkdirAllUnderUserConfig` calls `MkdirAllNoSymlink` and
  `MkdirAllNoSymlinkUnder`, etc.). Inlining into methods
  would either:
  (a) duplicate the no-symlink walk loop across 4+ methods,
  or
  (b) introduce cross-resolver method calls (e.g.
      `UserConfigResolver.MkdirAll` calling
      `NewStrictResolver().MkdirAll(...)`), which is
      ergonomic noise.
- The state file's framing — "this is mostly a copy +
  rename" + "current methods are thin delegators" + "one
  mechanical commit" — reads as compatible with the rename
  approach. The public API surface is the only thing that
  must be the resolver methods, not the implementations.

Implementation:

- 23 legacy exported fns renamed lowercase
  (`Resolve→resolveWorkdir`, `OpenRootNoSymlink→
  openRootNoSymlink`, `MkdirAllUnderUserConfig→
  mkdirAllUnderUserConfig`, etc.). Internal cross-references
  updated. Deprecated markers from 2.1.X dropped (the fns
  no longer exist publicly so the marker is moot).
- 4 trivial wrappers around already-private workers
  collapsed entirely:
  `WriteRootFileAtomic`/`WriteRootFileAtomicExactMode` (both
  wrappers around `writeRootFileAtomic(...,bool)`), `Glob`
  (wrapper around `GlobLimited` with default cap), and
  `GlobLimited` (wrapper around `globLimited`). Resolver
  methods call the underlying private workers directly.
- Resolver methods updated: each is now a one-line delegator
  to the new lowercase helper. The resolver files are pure
  API surface.
- `workdirpath_test.go` updated: in-package tests use the
  lowercase names. Kept the file rather than deleting —
  it includes `TestResolve_RelativeWorkdirIsNotEscape` and
  `TestRootRelForWrite_RelativeWorkdir`, which are
  regression tests for the v0.26.0 release-build failure
  (Go 1.25's `EvalSymlinks` shape preservation). Belt-and-
  braces alongside the 56 new-type tests is cheap.

Verification: grep clean, `go build ./...` clean, full
`go test ./...` green, `go test -race -count=2
./internal/workdirpath/...` green, lint clean for the
package, `stado --help` + `stado run --help` smoke render.

Commit: `492e0de refactor(workdirpath): 2.1.Y — delete
legacy public surface`. 6 files, +193 / −300.

### Stopping here

The state file's next steps are 2.2 (A1) and 2.3 (A3). A1 is
the largest-churn phase in the program (Model struct +
overlays + 8 pickers across 9+ commits, with explicit
operator-facing decisions in commit 1 — overlay slotting,
picker interface location). A3 is more mechanical but needs
an upfront inventory of the `Update` type-switch.

Both are multi-hour phases. Stopping the autonomous run here
so the operator can:
1. Run merge-checkpoint-#2 smoke (per the plan).
2. Confirm direction on A1's overlay-slotting decision (the
   stack default is defensible but worth confirming) before
   committing.
3. Optionally split A1 / A3 across separate sessions for
   context-window hygiene.

A2 (the workdirpath consolidation) is fully complete.

