# State

**Mode:** Feature (TODO/BUGS drained; ready for operator review)

**Current item:** Goal `2026-05-09-clear-drafted-todo-bugs-features`
delivered all 5 ACs across 9 commits on `main`. TODO.md / BUGS.md /
PLAN.md current product gaps now only show items explicitly listed
under the goal's Non-goals (Windows sandbox v2 deferred for
hardware reasons; sandbox-multilayer-composition spec parked; PR Q
external infra deferred). Detailed evidence in
`.claude/goals/2026-05-09-clear-drafted-todo-bugs-features/
progress.md`.

## Goal completion summary (2026-05-09)

| AC | Slice / commit | Outcome |
|----|----------------|---------|
| AC3 | `b3ca142` | `tools.describe` & all native meta-tools dispatch correctly via `stado tool run` |
| AC1+AC2 | `ae21aaa` | bundled `web.fetch` propagates host's structured errors instead of "returned -1" |
| AC4 spec | `7048886` | F9b decomposed into 12 ACs / 6 phases |
| AC4 F9b.1 | `897b457` | host scaffolding — `stado_ui_render` + cap + decode + 14 tests |
| AC4 F9b.2 | `e5e3444` | TUI renderer — `panel_render.go` + bridge + 14 tests |
| AC4 F9b.3 | `40b8adc` | ACP wire — `session/update kind=panel` + 3 tests |
| AC4 F9b.4 | `697a233` | MCP wire — `CallToolResult.StructuredContent` + ASCII fallback + 7 tests |
| AC4 F9b.5 | `f1830df` | headless wire — `session.update kind=panel` (spec deviation: skipped `--ui-render-file`) + 3 tests |
| AC4 F9b.6 + AC5 | (this commit) | `render-demo-go/` example + `host-imports.md` + TODO closure + spec → done/ |

## Worktree merge-ready (prior 2026-Q2 refactor)

The earlier refactor program completion still applies — the
worktree at `.claude/worktrees/refactor+quality-2026-q2` is
merge-ready pending the operator's interactive `stado run` smoke
session (D8). That program is independent of this goal; it
predates the 2026-05-09 work above.

**Plan (refactor):** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## Where we are at end of 2026-05-08 (autonomous session 4)

### Program completion summary

| Phase | Status | Anchor commits |
|-------|--------|----------------|
| Phase 1 (test coverage) | DONE (earlier sessions) | 1.1 / 1.2 / 1.3 |
| 2.1 (A2) workdirpath consolidation | DONE end-to-end | through `492e0de` |
| 2.3 (A3) model_update dispatcher split | DONE | `6f2208f` |
| 3.1 (B1) config.go split | DONE | `8ea8ab1` |
| 3.2 (B2) bundled-tool schema builder | DONE | `bfcf586` + `1c28fa4` |
| 3.3 (B3) bridge lifecycle | SKIPPED (audit captured) | — |
| 2.2 (A1) Model + overlays | DONE (8 in-package extractions) | `06574a6` `321d8c3` `3e36adb` `0c9eaaf` `19c93df` `20fc54f` |
| Smaller wins addendum | DONE (audit + dead-interface cleanup + tests) | `e1fc00f` `e68df32` |
| Polish pass (CHANGELOG, EP history, race tests) | DONE | `0968f60` `a164852` |

### Smaller wins addendum (post-program cleanup)

Audited the plan's "smaller wins addendum" list. Findings:

- `auto_prune_after` TODO — already decided in B1 to keep
  (removal is a behaviour change; wiring is a feature).
- `toolCategorized` interface — found ZERO implementations
  (plan said "single"). All 5 type-assertion sites were
  unreachable dead code; the two meta tools that depended on
  it (`tools__list_categories`, `tools__in_category`) silently
  always returned empty lists. Rewired to
  `LookupToolMetadata(name).Categories` per EP-0037 §C —
  bug fix on top of cleanup. Commit `e1fc00f`.
- "Single-impl interfaces" list (Runner / CommitSigner /
  Spawner / SSHCommitSigner / InboxAwareSpawner) — audit
  showed each is a real interface with multiple impls or a
  deliberate cycle-avoidance shim. Plan listing was stale;
  none warrant inlining.
- "Other actionable TODOs (3 in non-test code total)" — actual
  count is 1 (`cmd/stado/plugin_update_verify.go:94`), and
  it's a feature ask (query GitHub/GitLab API) not a refactor
  cleanup.

### A1 outcome

`internal/tui/model_render.go`: 1937 → 302 LoC (-1635). Eight new
in-package files, each focused on one concern:

| File | LoC | What |
|------|-----|------|
| sidebar.go | 430 | sidebar render + width helpers + sidebarLine + shortSessionID |
| landing.go | 203 | pre-first-turn screen (banner, hint, footer) |
| quit_confirm.go | 72 | Ctrl+D confirmation popup |
| input_box.go | 63 | bordered chat-input area + border-tone |
| approval.go | 195 | plugin approval drawer + key handler |
| choice.go | 206 | plugin choice drawer + key handler |
| status_bar.go | 260 | bottom status row + git probe + token% |
| blocks_render.go | 312 | conversation-block rendering + cache |

`model_render.go` (302 LoC) holds: View() + layout() + utilities
(modelOrPlaceholder, bannerFor, humanize, cacheHitRatio,
truncate, trimSeed, prettyJSON).

A1 scope deviation from §2.2 (unified Overlay/Picker interfaces)
captured as **D14** in the plan's decision log. Codex + Gemini
consulted on the scope; both confirmed the heterogeneity reading
and the per-concern-extraction approach.

### B3 outcome

Skipped. Audit found no bridge has Init/Dispose/Close work; setup
is 5 distinct constructor signatures with no shared shape; no log
line uses a hypothetical Name(). Adding ceremony without
consolidation doesn't earn its weight. Captured in journal.

## Up next when resuming

1. **Merge checkpoint #2** (per cross-cutting plan §"Merge
   checkpoints" / D8): run a full interactive `stado run`
   smoke session. Specifically verify:
   - Landing screen renders + ctrl+p opens command palette
   - Sidebar toggle works (key matches existing binding)
   - First turn streams, tool calls render, blocks scroll
   - Status bar shows model + tokens + cwd correctly
   - ESC closes overlays, Q quits cleanly
   - Approval drawer (trigger via plugin or trip a tool) works
   - Quit confirm popup centres correctly
2. **Full lint pass** outside the autonomous sandbox (the
   bundled `golangci-lint` panicked here on a Go 1.26 vs 1.25
   toolchain mismatch — pre-existing tooling state, not
   refactor-related).
3. If merge-ready: PR + merge + tag a minor release. Per
   `CHANGELOG.md` + `CLAUDE.md` "Release versioning" — minor
   release for the user-visible structural changes.

## Branch state

```
worktree-refactor+quality-2026-q2 (this worktree)
└─ Phase 1 (3 commits) → green
└─ A2 fully consolidated through 2.1.Y
└─ A3 dispatcher split (handler_*.go family)
└─ B1 config.go split into 3 focused files
└─ B2 schema builder + 34 tools migrated
└─ A1 model_render.go split into 8 per-concern files (1937 → 302 LoC)
└─ B3 skipped (audit)
```

Last commit: `20fc54f` (A1 final extraction — block rendering).
+ this state-update commit.

## Verification across the program

Every commit in this run:
- `go build ./...` clean
- `go test ./internal/<touched-package>/...` green
- After A1: `go test ./internal/tui/...` green at every
  extraction
- Full repo `go test ./...` green at session boundaries
- Smoke: `stado --version` + `stado --help` + `stado run --help`
  render

End-of-program:
- `go test -race -count=1 ./...` — 71 packages, zero failures.

Not verified autonomously:
- Interactive `stado run` smoke (operator's pre-merge step)
- Full `golangci-lint run ./...` (per-package clean; tree-wide
  panic on toolchain mismatch — golangci-lint built with Go
  1.25, runtime is 1.26, unrelated to changes)

## Blocked / open

Nothing blocking. The program is complete pending operator's
interactive smoke + merge.

## Recent (last 4 sessions)

- 2026-05-08 (autonomous session 4): A1 completion via 8 in-
  package extractions. Pre-flight scope decision consulted with
  codex + gemini; final outcome consulted with codex (no
  pushback except "record the scope change in plan D14" — done).
  Plan §2.2 acceptance criteria delta captured in D14.
- 2026-05-08 (autonomous session 3): A3 + B1 + B2 landed; B3
  skipped with audit; A1 audited and deferred (then unblocked
  in session 4).
- 2026-05-08 (session 2): A2 finished through 2.1.Y; legacy
  public surface deleted.
- 2026-05-08 (session 1): 2.1.f Bazzite RemoveAll fix.
- 2026-05-07: A2 type-landing + invariant check.
- 2026-05-07: Phase 1 complete (47 + 13 + 17 = 77 tests).
