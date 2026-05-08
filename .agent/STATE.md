# State

**Mode:** Feature / Refactor (program continuation, autonomous run)

**Current item:** A1 + post-A1 wins remain; ready for operator review.
**Plan:** `docs/superpowers/plans/2026-05-07-refactor-program.md`

## Where we are at end of 2026-05-08 (autonomous session 3)

### Completed phases
| Phase | Status | Anchor commits |
|-------|--------|----------------|
| Phase 1 (test coverage) | DONE | 1.1 / 1.2 / 1.3 — earlier sessions |
| 2.1 (A2) workdirpath consolidation | DONE end-to-end through 2.1.Y | `492e0de` |
| 2.3 (A3) model_update dispatcher split | DONE | `6f2208f` |
| 3.1 (B1) config.go validation extraction | DONE | `8ea8ab1` |
| 3.2 (B2) bundled-tool schema builder | DONE | `bfcf586` + `1c28fa4` |
| 3.3 (B3) bridge lifecycle | **SKIPPED with documented audit** (no real consolidation opportunity) |
| 2.2 (A1) TUI Model + overlays | **STARTED with audit, deferred to operator** (heterogeneity blocks plan as written) |

### Session-3 commits (this run)

```
1c28fa4 refactor(runtime): 3.2 (B2) — migrate bundled-tool schemas to schema package
bfcf586 feat(runtime/schema): 3.2 (B2) — schema builder for bundled tools
8ea8ab1 refactor(config): 3.1 (B1) — split config.go into focused files
6f2208f refactor(tui): 2.3 (A3) — model_update split into per-family handlers
```

Plus this state-update commit + journal entry (next).

### B3 audit (skipped) — short version

Plan called for a `Bridge` interface with `Init/Dispose/Name`. Audit:
- No bridge has Close/Dispose work — all 5 are stateless wrappers.
- No bridge has Init work — they're constructed inline at each
  call site with their dependencies.
- Setup is 5 distinct field assignments with different
  constructor signatures (e.g. `NewSessionBridge(sess, prov,
  model)` vs. `NewLocalMemoryBridge(stateDir, owner)`); no
  shared shape to factor into a loop.
- No log line uses a hypothetical `Name()`; nil-bridge checks
  return `-1` silently.

Adding `Init/Dispose/Name` as no-ops would be ceremony without
removing duplication. Decision: skip. The journal entry has the
full reasoning if revisited later.

### A1 audit (deferred to operator) — short version

Plan envisioned 5 overlays (Help, Status, QuitConfirm, Approval,
Choice) all moving to `internal/tui/overlays/` under a unified
`Overlay` interface. Audit of `model_render.go` reveals **heterogeneous shapes**:

- **Help / Status** — full-screen takeovers; `View()` returns
  early with the overlay's render output. Already match
  `overlays/help.go`'s shape.
- **QuitConfirm** — inline modal mid-`View()` at line 218 of
  model_render.go. Different from full-screen.
- **Approval / Choice** — persistent UI elements that *adjust
  the chat-area layout* (height calculations subtract
  `m.approvalCardHeight()` and `m.choiceDrawerHeight()`). They
  aren't overlays in the modal-takeover sense; they're
  layout-contributing components.

Forcing all 5 under one `Overlay` interface would either:
a) Require behaviour changes to flatten the heterogeneity
   (e.g. making Approval full-screen) — explicitly out of
   scope per "no behaviour changes".
b) Produce a fictional-uniformity interface where Approval /
   Choice satisfy the contract but their `View(width, height)`
   call doesn't compose with the rest of `Model.View()` the
   way Help / Status do.

Plus: A1 is the largest mechanical churn in the program and
"every commit needs a `stado run` smoke check" per D8 — interactive
testing this autonomous run cannot perform.

Defer to operator with these notes; the plan section §2.2 on
"overlay slotting decision" should incorporate the heterogeneity
finding before commit 1.

### Where things stand

```
worktree-refactor+quality-2026-q2 (this worktree)
└─ Phase 1 (3 commits) → green
└─ A2 fully consolidated through 2.1.Y
└─ A3 dispatcher split (handler_*.go family)
└─ B1 config.go split into 3 focused files
└─ B2 schema builder + 34 tools migrated
└─ B3 skipped (audit)
└─ A1 deferred (audit captured)
```

Last commit before this state file: `1c28fa4`.

## Up next when resuming

1. **A1**: walk through the audit above with operator. Decide:
   - Whether to flatten Approval/Choice into the same Overlay
     contract as Help/Status (behaviour change, requires
     separate plan), or
   - Define a narrower Overlay interface that covers only the
     full-screen-takeover variants (Help, Status, QuitConfirm),
     leaving Approval/Choice as layout-adjusting components.
2. **8 picker packages → Picker interface**: independent of
   the overlay decision; the Picker contract location decision
   from the plan still applies. Could land standalone.
3. **model_render.go shrink**: the LoC target (<800 from 1937)
   is mostly about block rendering / sidebar / viewport
   composition, not overlays. Can be its own pass.
4. **Smaller wins addendum** (per plan):
   - `auto_prune_after` field — leave as-is per B1 commit's
     reasoning (removal is a behaviour change; wiring is a
     feature).
   - Single-impl interfaces in state/git/runtime/sandbox/subagent
     — defer unless natural.
5. **Merge checkpoint #2**: full `stado run` smoke session
   pass. Required before A1 starts (interactive testing of
   the heterogeneous overlay behaviour).

## Branch state

Last commit: `1c28fa4` (B2 migration), plus the upcoming state
update commit.

## Blocked / open

- A1 needs operator-side decision on how to handle the
  heterogeneity (see audit above).
- B3 considered closed per audit — operator can reopen if a
  future caller introduces real bridge lifecycle.

## Recent (last 2 sessions)

- 2026-05-08 (autonomous session 3): A3 + B1 + B2 landed; B3
  skipped with audit; A1 audited and deferred.
- 2026-05-08 (session 2): A2 finished through 2.1.Y;
  Deprecated markers added; legacy public surface deleted.
- 2026-05-08 (session 1): 2.1.f Bazzite RemoveAll fix.
- 2026-05-07: A2 type-landing + invariant check.
- 2026-05-07: Phase 1 complete (47 + 13 + 17 = 77 tests).
