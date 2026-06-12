## v0.64.0 — hook-mutation audit provenance, memory long-term gaps — 2026-06-12

Bundles the memory long-term-gap closures (#122), the fakelsp build-artifact
removal (#123), and hook-mutation provenance in the signed audit chain (#125).

### CLI

- **Hook-mutation provenance in the audit chain (#125).** v0.63.0 lifecycle
  hooks could rewrite a tool result (`post_tool` mutate) before the audited
  `Result-SHA` was computed, and a `pre_tool` deny wasn't committed at all —
  so a hook could silently alter or hide what the signed chain recorded. Now
  a `post_tool` mutate records TWO linked trace commits — the original raw
  result, then a mutation commit (parenting it) carrying `Original-Result-SHA`
  + `Mutated-By-Hook` — and a `pre_tool` deny writes its own trace commit
  (`Deny-Reason` + `Denied-By-Hook`). Both the original and mutated bytes are
  stored as git blobs (4 MiB cap, SHA-only fallback on overflow), inspectable
  via audit / `/tree`. The mutated result stays the canonical value the model
  saw; the original is audit-only provenance. Purely additive — no existing
  v0.62/v0.63 signature is rewritten; old chains keep verifying.
- **`stado audit verify` mutation linkage.** Verify validates each
  original→mutated provenance link (parent `Result-SHA` == `Original-Result-SHA`,
  blob-hash cross-check when blob-backed) as a DISTINCT anomaly class from
  signature failure — a broken link reports `MUTATION-LINK-BROKEN` and exits
  non-zero, but never marks a signature Invalid. The link is keyed on
  `Mutated-By-Hook` (an empty-origin mutation is validated too).
- **`stado session logs` annotation.** A mutation tip renders `⟳ mutated by
  <hook>` (highlighted) with its original parent dimmed so the pair reads as
  one event; a denied call renders `⊘ denied: <reason>`. Hook-supplied strings
  are control-char stripped.
- **`stado memory compact` (#122).** Atomically rewrites the memory log to its
  folded state (fail-closed on concurrent growth, OOM-guarded snapshot).

### TUI

- **`/tree` mutation/deny badge (#125).** Session and turn rows show a compact
  `⟳N` / `⊘N` badge counting how many tool results a `post_tool` hook rewrote
  and how many calls a `pre_tool` hook vetoed inside them. Zero → no badge.

### Memory (#122)

- **Session-scope inheritance.** A session now sees the session-scoped
  memories of every session it forked from (ancestry walked from the sidecar
  fork forest; plugin query JSON can't forge it).
- **Terminal delete tombstone.** `delete` folds a `deleted` tombstone (visible
  in list/show/export, excluded from retrieval); `approve` refuses to
  resurrect it (re-propose instead), `reject` stays reversible.
- **Graceful degradation past the size cap.** Reads degrade (the recovery
  surface never bricks); writes stay refused over the cap.
- **Retrieval enabled by default** (an explicit `[memory].enabled = false`
  still opts out).
- **Plugin bridge default-denies session scope** — strips plugin-supplied
  `session_id` / ancestry and pins reads to repo+global, closing a session_id
  forge.

### Fixes / Infra

- **Removed a committed 2.9 MB `fakelsp` test binary (#123).** A stray build
  artifact swept into the repo root during the v0.63.0 build; the lspfind
  tests compile the stub from source. Added `/fakelsp` to `.gitignore`.
  Resolves the Scorecard BinaryArtifacts alert #71 (HIGH).
- **Don't stamp mutation-provenance trailers onto the tree commit (#125).** A
  mutating tool + `post_tool` mutate hook leaked the provenance trailers onto
  the tree-ref commit, making `audit verify` report `MUTATION-LINK-BROKEN` on
  legitimate chains. The errored original half of a mutation pair now renders
  faint+red in `session logs`.

