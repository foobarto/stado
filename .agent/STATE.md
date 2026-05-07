# State

**Mode:** Feature / Refactor (workdirpath migration in progress)

**Current item:** Phase 2.1 (A2) — workdirpath consolidation
**Plan:** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## Where we are at end of 2026-05-07/08

### Phase 1 — DONE
- 1.1 Bridge contract tests: 47 tests across 5 plugin-host bridges.
- 1.2 Sandbox runner contract test: 13 sub-tests (Tier 1 + Tier 2 wired).
- 1.3 FleetBridgeAdapter contract tests: 17 tests.
- Composition spec parked at
  `.agent/specs/open/sandbox-multilayer-composition.md`.

### Phase 2.1 (A2) — workdirpath, partially done
- 2.1.aa mcpbridge audit: cleared as no fs/workdirpath usage.
- 2.1.a/b/c: 4 new types landed alongside legacy
  (`Resolver`, `UserConfigResolver`, `StrictResolver`,
  `RootResolver`). 56 new tests across the 4 types; 29 legacy
  tests still green.
- 2.1.d: deferred to 2.1.Y (impl-move bundles with deletion).
- 2.1.e: 17 of 21 packages migrated (~117 of ~155 call sites).
  Remaining: cmd/stado (partway through — session.go,
  filewrite.go, plugin_sign.go, agents.go, plugin_gc.go,
  plugin_install.go done; learning.go, plugin_init.go,
  selfupdate.go, session_export.go, session_fork.go pending).
- 2.1.f: **Bazzite RemoveAll gap fix.** Added
  `UserConfigResolver.RemoveAll` (one method + 5 retrofitted
  callers). 5 new tests including the simulated `/home →
  /var/home` Bazzite case. D13 documents the carve-out from
  non-goal #1.

### Up next when resuming

1. Finish cmd/stado migration. Pending files:
   - cmd/stado/learning.go (4 calls)
   - cmd/stado/plugin_init.go (4 calls — has function-pointer
     pattern: `write := workdirpath.WriteRootFileAtomic`)
   - cmd/stado/selfupdate.go (2 calls)
   - cmd/stado/session_export.go (1 call)
   - cmd/stado/session_fork.go (2 calls)
2. Run `grep -rEho "workdirpath\.[A-Z]" --include="*.go" |
   grep -v internal/workdirpath` to confirm only
   `New*Resolver`, `LooksLikeRepoRoot`, `FindRepoRoot`,
   `FindRepoRootOrEmpty` remain.
3. Phase 2.1.X: mark legacy `Deprecated:` markers on the 23
   exported legacy fns (one tag-style commit; compiler
   warnings flag remaining usage in any out-of-tree code).
4. Phase 2.1.Y: delete legacy + inline impls into the new
   types in one mechanical commit. The 29 existing legacy
   tests get replaced by parallel tests on the new methods
   (already written; just rename if needed). Verification:
   `grep -rn "workdirpath\.[A-Z]"` returns only the new-API
   methods + `repodisco` exports.
5. Per-merge-checkpoint smoke: `stado --help`, `stado run --help`,
   one full `stado run` smoke session pass at checkpoint #2.
6. Then phases 2.2 (A1) + 2.3 (A3) per the plan.

## Branch state

```
worktree-refactor+quality-2026-q2 (this worktree)
└─ Phase 1 (3 commits) → all green
└─ A2 design + plan revisions (round-A2 / round-2 / round-final / invariant)
└─ A2 type-landing (4 types + 56 tests)
└─ A2 caller migration (10 commits, 17 packages)
└─ 2.1.f Bazzite fix
```

Last commit: `b1b0b23 fix(workdirpath): 2.1.f — UserConfigResolver.RemoveAll closes Bazzite gap`

## Blocked / open

- Nothing blocking. Resume by picking up the cmd/stado
  remaining files. Pattern is well-established at this point.

## Recent

- 2026-05-08: 2.1.f Bazzite gap fix. Operator caught that
  RemoveAllNoSymlink fails on Atomic Fedora derivatives;
  EP-0028 fixed it for read/open/mkdir but missed RemoveAll.
  Now fixed in-scope.
- 2026-05-07: A2 type-landing complete. Multiple consultation
  rounds with codex + gemini to validate the 4-type design.
  Invariant check confirmed workdirpath is internal runtime
  confinement, not plugin extensibility surface (D12 scope
  clarification).
- 2026-05-07: Phase 1 complete (47 + 13 + 17 = 77 tests).
