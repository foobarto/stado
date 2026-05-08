# State

**Mode:** Feature / Refactor (program continuation)

**Current item:** A2 fully complete; 2.2 (A1) is next.
**Plan:** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## Where we are at end of 2026-05-08 (session 2 — autonomous run)

### Phase 1 — DONE (sessions on 2026-05-07)
- 1.1 Bridge contract tests (47 tests).
- 1.2 Sandbox runner contract test (13 sub-tests).
- 1.3 FleetBridgeAdapter contract tests (17 tests).

### Phase 2.1 (A2) — DONE
- 2.1.aa mcpbridge audit: cleared.
- 2.1.a/b/c: 4 new types landed alongside legacy.
- 2.1.d: deferred to 2.1.Y, completed.
- 2.1.e: full caller migration (production + tests).
  - `dcaf422` (cmd/stado tail) + `05ca9c9` (test-file leftovers).
- 2.1.f Bazzite RemoveAll fix: `b1b0b23`.
- 2.1.X Deprecated markers: `816ebd8`.
- **2.1.Y legacy deletion: `492e0de`.** All 23 legacy public
  fns gone — bodies moved to lowercase package-private helpers
  (rename approach: preserves git history of the
  security-critical no-symlink walks). 4 wrappers
  (`WriteRootFileAtomic*`, `Glob`, `GlobLimited`) collapsed
  into resolver method bodies directly.

### Verification at 2.1.Y commit
- `grep -rEho 'workdirpath\.[A-Z][a-zA-Z]*' --include='*.go'
  | grep -v internal/workdirpath` returns ONLY:
  `New*Resolver`, `New`, `LooksLikeRepoRoot`, `FindRepoRoot`,
  `FindRepoRootOrEmpty`. ✅
- `go build ./...` clean. ✅
- `go test ./...` green. ✅
- `go test -race -count=2 ./internal/workdirpath/...` green. ✅
- `golangci-lint run ./internal/workdirpath/...` 0 issues. ✅
- Smoke: `stado --help` + `stado run --help` render. ✅

## Up next when resuming

**Merge checkpoint #2** (per cross-cutting plan §"Merge
checkpoints"): one full `stado run` smoke-session pass before
starting A1. This is a manual smoke (not just `--help`).

Then **Phase 2.2 (A1) — `Model` struct + `model_render.go`
consolidation.** This is the largest mechanical-churn phase in
the program. Read `docs/superpowers/plans/2026-05-07-refactor-program.md`
§2.2 in full before starting. Key things the plan calls out:

1. **Overlay slotting decision** in commit 1: stack vs single
   slot. Plan default is stack (preserves multi-visible
   behaviour). Decide at A1 design time after auditing
   today's `*Picker.Visible` flags for cases that legitimately
   coexist; document rationale in commit 1.
2. **Picker contract location**: `internal/tui/overlays/` if it
   doesn't import any picker package, else new
   `internal/tui/pickers/` leaf package. Audit imports first.
3. **Migration order is fixed** (8 steps in the plan).
   Per-overlay commit, then per-picker commit, with a smoke
   check at each. Don't batch.
4. **Smoke check shape**: `stado run` opens, ESC closes
   overlays, Q quits, multi-visible combinations preserved.

Then **Phase 2.3 (A3) — `model_update`/`commands`/`stream`
dispatcher split.** Lower mechanical risk than A1. Steps:
1. Inventory pass: every `case` arm in `Update`'s type-switch
   gets a target `handler_*.go` file.
2. Per-message-family extraction commits (5 expected:
   `handler_commands.go`, `handler_stream.go`,
   `handler_picker_response.go`, `handler_lifecycle.go`,
   `handler_tools.go`).
3. Last: `model_update.go` becomes the dispatcher (<200 LoC).

Then Phase 3 (Tier B): B1, B2, B3 per plan §3.

## Branch state

```
worktree-refactor+quality-2026-q2 (this worktree)
└─ Phase 1 (3 commits) → green
└─ A2 design + plan revisions
└─ A2 type-landing (4 types + 56 tests)
└─ A2 caller migration (production + tests, full sweep)
└─ 2.1.f Bazzite fix
└─ 2.1.X Deprecated markers
└─ 2.1.Y legacy deletion (492e0de)  ← here
```

Last commit: `492e0de refactor(workdirpath): 2.1.Y — delete legacy public surface`

## Blocked / open

- Nothing blocking. A1 ready to start when operator wants.
- A1's overlay-slotting decision is the only thing that wants
  operator input ahead of time, and even that has a
  defensible default (stack).

## Recent

- 2026-05-08 (session 2 autonomous): completed 2.1.e tail
  (cmd/stado + test files), 2.1.X Deprecated markers, 2.1.Y
  legacy deletion. A2 fully done. 4 commits this session
  (dcaf422, 05ca9c9, 816ebd8, 4313e7b state-update,
  492e0de 2.1.Y).
- 2026-05-08 (session 1): 2.1.f Bazzite fix.
- 2026-05-07: A2 type-landing + invariant check.
- 2026-05-07: Phase 1 complete (47 + 13 + 17 = 77 tests).
