# State

**Mode:** Feature / Refactor (workdirpath migration)

**Current item:** Phase 2.1.Y next — legacy deletion + impl-move
**Plan:** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## Where we are at end of 2026-05-08 (session 2)

### Phase 1 — DONE
- 1.1 Bridge contract tests: 47 tests across 5 plugin-host bridges.
- 1.2 Sandbox runner contract test: 13 sub-tests (Tier 1 + Tier 2 wired).
- 1.3 FleetBridgeAdapter contract tests: 17 tests.
- Composition spec parked at
  `.agent/specs/open/sandbox-multilayer-composition.md`.

### Phase 2.1 (A2) — through 2.1.X
- 2.1.aa mcpbridge audit: cleared.
- 2.1.a/b/c: 4 new types landed alongside legacy.
- 2.1.d: deferred to 2.1.Y (impl-move bundles with deletion).
- 2.1.e: **DONE.** All in-tree caller migrations to new API.
  Final tail (cmd/stado + test-file leftovers) committed in
  `dcaf422` and `05ca9c9`. Verification grep passes:
  `grep -rEho 'workdirpath\.[A-Z][a-zA-Z]*' --include='*.go'
  | grep -v internal/workdirpath` returns only
  `New*Resolver`, `New`, `LooksLikeRepoRoot`, `FindRepoRoot`,
  `FindRepoRootOrEmpty`.
- 2.1.f Bazzite RemoveAll fix landed in `b1b0b23`.
- 2.1.X: **DONE.** Deprecated markers on all 23 exported legacy
  fns (`816ebd8`). `golangci-lint run
  ./internal/workdirpath/...` returns 0 issues — same-package
  use isn't flagged, so the markers don't disturb the
  delegating implementations.

### Up next when resuming

1. **Phase 2.1.Y** — delete legacy + inline impls into the new
   types in one mechanical commit.
   - Pre-flight: re-run the verification grep to confirm
     2.1.e didn't drift between sessions.
   - Move every legacy function body into the corresponding
     new-type method (Resolver.\*, UserConfigResolver.\*,
     StrictResolver.\*, RootResolver.\*). Where multiple
     legacy fns map to one method (e.g.
     `OpenRootUnderUserConfig` + `OpenRootNoSymlinkUnder`
     used internally), pick the cleanest single
     implementation; current methods are thin delegators so
     this is mostly a copy + rename.
   - Delete legacy exported symbols. Internal helpers
     (`splitAbsoluteRoot`, `userTrustAnchor`,
     `removeAllUnderUserConfig`, `writeRootFileAtomic`)
     stay or move into the relevant resolver file as
     unexported.
   - Replace the 29 legacy tests with parallel tests on the
     new methods. The 56 new-type tests already cover the
     surface; this is mostly renaming/dedup.
   - Verification after: `grep -rn "workdirpath\.[A-Z]"`
     returns ONLY new-API methods + `repodisco` exports.

2. **Per-merge-checkpoint smoke** at checkpoint #2:
   `stado --help`, `stado run --help`, one full `stado run`
   smoke session pass.

3. Then phases 2.2 (A1) + 2.3 (A3) per the plan.

## Branch state

```
worktree-refactor+quality-2026-q2 (this worktree)
└─ Phase 1 (3 commits) → green
└─ A2 design + plan revisions
└─ A2 type-landing (4 types + 56 tests)
└─ A2 caller migration (production + tests, full sweep)
└─ 2.1.f Bazzite fix
└─ 2.1.X Deprecated markers (816ebd8)
```

Last commit: `816ebd8 docs(workdirpath): 2.1.X — Deprecated markers on 23 legacy fns`

## Blocked / open

- Nothing blocking. 2.1.Y is the natural next batch.

## Recent

- 2026-05-08 (session 2): finished 2.1.e — completed cmd/stado
  caller migration (5 production files + 1 plugin_install
  carry-over the prior state file marked "done" but had only
  RemoveAll touched). Discovered ~13 test-file callers of
  `workdirpath.OpenRootNoSymlink` left untouched by earlier
  2.1.e batches (`fs/budget_test.go`, `state/git/materialize_test.go`,
  `skills/skills_test.go`, `tui/render/render_test.go`,
  `cmd/stado/plugin_install_test.go`); all migrated to
  `NewStrictResolver().OpenRoot`. One doc-comment fix in
  `host_lsp.go` (referenced `workdirpath.Resolve`, now
  `Resolver.Resolve`). 2.1.X Deprecated markers added in a
  doc-only commit; lint-clean for the package.
- 2026-05-08 (session 1): 2.1.f Bazzite gap fix.
- 2026-05-07: A2 type-landing complete + invariant check.
- 2026-05-07: Phase 1 complete (47 + 13 + 17 = 77 tests).
