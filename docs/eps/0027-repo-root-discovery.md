---
ep: 0027
title: Repo-root discovery — single predicate, single helper
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Companion to the shakedown patch in branch shakedown-2026-05-04.
see-also: [0004]
---

# EP-0027: Repo-root discovery — single predicate, single helper

## Problem

Six places in the codebase walk a parent chain looking for the user's
git working tree, and each one carries the same naive predicate:

```go
if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
    return dir
}
```

Sites: `cmd/stado/session_lookup.go` (×2 — `findRepoRoot` and
`findRepoRootForLand`), `cmd/stado/learning.go` (×2 —
`findCurrentWorktreeRoot` and `findCurrentRepoRoot`),
`internal/runtime/session.go` (`FindRepoRoot`),
`internal/memory/context.go` (`findRepoRoot`),
`internal/memory/session.go` (anonymous walk).

The predicate over-accepts. **Any** entry under the parent named
`.git` — empty directory, half-deleted state from a botched
`rm -rf project`, mount-point artefact, leftover from another tool's
test fixtures — counts as a repo root. Concrete failure observed
2026-05-04: a stray empty `/tmp/.git/` directory caused
`TestSessionGC_ApplyActuallyDeletes` and four `TestLearningCLI_*`
tests to fail deterministically. Each test wrote a session worktree
under `t.TempDir()` (which lives under `/tmp`), then walked back up
to find the repo root. The walk hit `/tmp/.git` first and returned
`/tmp` as the user-repo pin — a value that neither matched what
production code computed nor what the assertions expected.

The same bug exists in production: any user with a stray `.git/`
in a parent of their CWD will get sessions pinned to the wrong
directory. The user-repo pin is what session GC, sidecar
discovery, and prompt-context retrieval all key off, so a wrong
pin silently degrades several features at once.

A second issue: the six implementations are subtly different.
`findRepoRoot` and `findCurrentWorktreeRoot` are identical except
for variable names. `findCurrentRepoRoot` interleaves a
`readCurrentRepoPin` lookup. `findRepoRootForLand` returns `""` on
miss while the others return `start`. `internal/memory/session.go`
inlines the walk with no function name at all. Keeping these in
sync requires touching six files for any change.

## Goals

- One predicate that distinguishes a real git working tree from an
  empty `.git/` artefact, used everywhere a repo-root walk happens.
- One implementation of the parent-chain walk, parameterised on the
  "fall back to start" vs "return empty" decision.
- Both live in `internal/workdirpath`, since that package is already
  the home for path-confinement primitives and is imported by every
  caller already.
- Existing function names at the call sites stay the same so the rest
  of the codebase doesn't churn.

## Non-goals

- Replacing the broader user-repo pin protocol (EP-0004 §"sidecar"
  describes the pin file at `<worktree>/.stado/user-repo`). The pin
  remains the source of truth for session worktrees; this EP only
  changes how the bootstrapping walk decides "this directory looks
  like a repo".
- Validating git tree integrity. We don't run `fsck` or check that
  refs are coherent — the goal is to filter out false-positives like
  empty `.git/` dirs, not to be `git-rev-parse --show-toplevel`.
- Handling worktree paths under `cfg.WorktreeDir()`. Those have their
  own pin file and `resolveUserRepo` in
  `internal/runtime/session.go` already short-circuits before walking;
  that machinery is unchanged.

## Design

Two new exported symbols in `internal/workdirpath`:

```go
// LooksLikeRepoRoot reports whether dir appears to be the root of a
// git working tree. Accepts either:
//   1. <dir>/.git is a regular file or symlink-to-file (gitfile pointer
//      for linked worktrees and submodule checkouts).
//   2. <dir>/.git is a directory containing a HEAD entry.
//
// Empty .git/ directories return false — they are the false-positive
// pattern this helper exists to reject.
func LooksLikeRepoRoot(dir string) bool

// FindRepoRoot walks up from start looking for a repo root. Returns
// the discovered root, or `start` (canonicalised) if no repo found —
// matching the historical "fall back to cwd" semantics.
func FindRepoRoot(start string) string

// FindRepoRootOrEmpty walks up from start looking for a repo root.
// Returns "" if none found — the variant `findRepoRootForLand`
// historically used to enforce "must be in a real repo".
func FindRepoRootOrEmpty(start string) string
```

Why HEAD specifically: every git working tree (regular, bare-as-tree,
linked worktree gitdir) creates HEAD on init. Other markers
(`objects/`, `refs/`, `config`) appear at different points in the
lifecycle. HEAD is the cheapest universally-present signal.

Why two `Find*` variants: callers have legitimately different
needs. Session bootstrapping wants to keep working when the user
runs `stado session list` from outside any repo (returns cwd as
fallback). The "land my session output onto a branch" path wants to
hard-fail if there's no real repo (returns `""`). Keeping both
variants in the helper avoids re-introducing the divergence we just
killed.

Each of the six call sites becomes a one-liner that delegates to the
helper. The local function names (`findRepoRoot`,
`findCurrentWorktreeRoot`, etc.) stay so callers throughout the
codebase don't need editing.

## Migration / rollout

Behaviour is **stricter, not looser**. Users with a stray `.git/`
directory in some parent of their CWD will see different
behaviour: sessions previously pinned to that wrong parent will now
pin to either the next real repo up the chain or to CWD itself.
The pin is recomputed on each `OpenSession` so the change applies
on the next stado invocation; existing pinned sessions in
`<worktree>/.stado/user-repo` are unaffected (they're already pinned
to a concrete path).

Test fixtures that mock a repo by `os.MkdirAll(<dir>/.git)` need
updating to also write `<dir>/.git/HEAD`. The shakedown patch
introduces a tiny `makeFakeRepo(t, dir)` helper in
`internal/memory/context_test.go` and replaces 11 fixture sites in
that file. The other test files surveyed
(`internal/tui/instructions_test.go`,
`internal/runtime/resume_test.go`, etc.) don't actually exercise
the predicate (they construct paths the production code uses
directly without walking) and don't need changes — confirmed by
running the full `./...` test suite after the refactor: only the
three `internal/memory` tests broke, and only those needed
fixture updates.

## Failure modes

- **Surprising "this isn't a repo" result.** A user with a
  partially-deleted `.git/` (no HEAD) used to get sessions pinned
  to that directory; they'll now get sessions pinned to CWD or the
  next real ancestor. This is strictly more correct: a `.git/`
  without HEAD isn't a working tree. Surface: pin path drift between
  versions, visible in `stado session list --long` output. Mitigation:
  the user-repo pin in each existing session worktree is read from
  disk before any walk happens, so already-bound sessions are
  unaffected. New sessions pin to the new (correct) path.

- **Symlinked `.git`.** `LooksLikeRepoRoot` follows the symlink once
  via `os.Stat`. A symlink to a missing target returns false (not a
  repo). A symlink to a real working tree is accepted. A symlink loop
  is bounded by the OS's own loop-detection in `os.Stat`. None of
  these are regressions — the previous predicate would also have
  followed the symlink via `os.Stat`.

## Test strategy

- `internal/workdirpath/repodisco_test.go` covers the predicate:
  empty `.git/` (rejected), `.git/` with HEAD (accepted), gitfile
  pointer (accepted), missing `.git` (rejected), symlink to broken
  target (rejected). Plus `FindRepoRoot` walks past empty parent
  `.git` to a real one further up.
- The pre-existing failing tests
  (`TestSessionGC_ApplyActuallyDeletes`, `TestLearningCLI_Document*`)
  validate the consolidation end-to-end. They fail on `main` due to
  the `/tmp/.git` pollution; they pass after this change because the
  predicate now rejects the polluting empty dir.
- The full repo `go test ./...` was rerun after the refactor; the
  only failure is `TestRun_RejectsEscapingPath` in
  `internal/tools/astgrep` which is environmental (`ast-grep` not on
  PATH) and unrelated.

## Open questions

- Should we ALSO check for a couple more git-internal markers
  (`objects/`, `refs/`) to defend against more pathological "half a
  git dir" states? Position: no, until we observe one in the wild.
  HEAD is the cheap universal marker; over-tightening invites its
  own false-negatives (e.g. partial-clone in progress).
- Should `LooksLikeRepoRoot` follow the gitfile pointer to verify
  the target gitdir is real? Position: no for the same reason —
  the gitfile pointer is git's own indirection mechanism and
  trusting it matches what `git rev-parse` does.

## Decision log

### D1. Predicate is "exists + (file OR has HEAD)", nothing more

- **Decided:** accept `.git` as a regular file or symlink-to-file
  (gitfile pointer); accept `.git` as a directory iff it contains a
  HEAD entry; reject everything else.
- **Alternatives:** check additional markers (`objects/`, `refs/`,
  `config`); shell out to `git rev-parse --show-toplevel`; require
  HEAD to parse as a valid ref.
- **Why:** HEAD-presence is the cheapest universally-true marker
  across all working-tree shapes (init'd, bare-as-tree, linked
  worktree, submodule). Shelling out kills the in-process walk's
  performance and adds a `git` runtime dependency. Parsing HEAD
  rejects in-progress states (mid-detached-HEAD, dangling refs)
  that we have no reason to reject. Over-tightening turns a real
  bug fix into a real regression.

### D2. Helper lives in `internal/workdirpath`

- **Decided:** add `LooksLikeRepoRoot`, `FindRepoRoot`, and
  `FindRepoRootOrEmpty` to `internal/workdirpath`.
- **Alternatives:** new package `internal/repodisco`; add to
  `internal/runtime`; add to `internal/state/git`.
- **Why:** every caller already imports `internal/workdirpath` for
  symlink-aware path primitives, so no import cycle risk and no new
  package overhead. `internal/runtime` would create import cycles
  (memory and learning import runtime, not the other way round).
  `internal/state/git` is the wrong layer (it's the sidecar
  abstraction; consumer-facing helpers belong in workdirpath).

### D3. Two `Find*` variants, not one

- **Decided:** keep `FindRepoRoot` (fallback to start) and
  `FindRepoRootOrEmpty` (return "" on miss) as separate exported
  functions. Local names (`findRepoRoot`, `findRepoRootForLand`)
  stay in place at call sites.
- **Alternatives:** single function with a `fallback string` parameter;
  return `(string, bool)`.
- **Why:** the parameterised variant invites mistakes (caller
  passes wrong fallback). The `(string, bool)` variant is correct but
  forces every caller to handle the boolean. Two named functions
  encode the policy in the function choice — harder to misuse.

### D4. Test fixtures get a small helper, not a wholesale change

- **Decided:** add `makeFakeRepo(t, dir)` to
  `internal/memory/context_test.go` and replace the 11 inline
  `os.MkdirAll(<dir>/.git)` calls with the helper. Other test files
  surveyed don't exercise the walk and don't need changes.
- **Alternatives:** update every `os.MkdirAll(.git)` site repo-wide
  preemptively; introduce a shared test helper in a new
  `testutil` package.
- **Why:** scope discipline. The bug is in the predicate; the only
  tests that broke from tightening it are the ones that walk through
  the predicate. A repo-wide preemptive update would touch ~20 sites
  with no functional benefit. A shared `testutil` package adds an
  internal package for one helper; not worth the indirection until
  three places need it.

## Related

- EP-0004: git-native sessions and audit. Defines the user-repo pin
  protocol that this EP's helper bootstraps.
- Shakedown patch: branch `shakedown-2026-05-04`, commits
  `feat(plugin): add --workdir flag and root --version` and
  the repo-discovery commit that follows.
- Dogfood writeup: `~/Dokumenty/dogfood/stado/2026-05-04-shakedown-stado-patches.md`.
