## v0.78.0 — adaptive context and durable learning — 2026-08-12

### UX / CLI / TUI

- **`stado learn` and `/learn` close the evidence-backed learning loop.** A
  bounded reviewer analyzes deterministic mistake and correction signals from a
  completed trajectory and proposes versioned lesson candidates. Candidate
  inspection is available from CLI and TUI, while activation requires a fresh,
  one-use operator-origin grant bound to the exact artifact and scope.
- **The pre-v1 `stado learning` noun is removed.** Legacy stores remain
  inspectable through compatibility lifecycle subcommands under `stado learn`
  and migrate idempotently with `stado learn migrate`.
- **Session context is directly inspectable.** `stado session state`, `signals`,
  and `journal` expose bounded structured state, deterministic learning signals,
  and canonical chronology without promoting model assertions into authority.

### Runtime

- **Memory and session research gain isolated agent paths.** The
  `memory__research` and `session__research` tools search authorized corpora with
  bounded catalog/search/open operations. Parents receive a synthesis and
  precise digest-bound citations rather than the explored raw corpus.
- **Retained subagents can resume historical work under a new identity.** Durable
  admissions, immutable fork points, broker epochs, leases, recursive budgets,
  attenuated capability ceilings, retained handles, messaging, cancellation,
  and bounded supervision make long-running children recoverable without using
  historical context as authority.
- **Retrieval feedback is measurable before it is automatic.** Exposure,
  opening, citation, and evaluated outcomes feed versioned shadow comparisons;
  `stado learn retrieval-report` reports them without changing active retrieval
  or demoting mandatory security guidance.

### Persistence / Security

- **Harness artifacts are versioned, scoped, and searchable.** Memory and lesson
  records carry immutable host-bound scope, tags, groups, evidence, provenance,
  sensitivity, expiry, and review state. A disposable SQLite FTS5 projection
  accelerates lookup while the hash-chained broker event log remains canonical.
- **The broker now supplies the durability substrate for adaptive context.** An
  exclusive single-writer lock, broker epochs, atomic WAL transactions,
  idempotent jobs, operator grants, and reserve/commit/release budget accounting
  fence restarts and concurrent processes. Private and secret artifact bodies
  remain outside ordinary full-text indexing and prompt eligibility.
- **State, journal, mailbox, and lifecycle events share one crash-consistent
  chronology.** Control events cannot be forged as agent messages, receiver
  inputs commit exactly once before arbitrary effects, and recovery resumes the
  recorded turn instead of reinjecting it as a new message.
- **gRPC is updated to 1.82.1.** This removes the reachable GO-2026-6061 xDS
  RBAC and HTTP/2 transport vulnerability reported by the release gate.

### Documentation

- Added the adaptive-context guide and accepted EP-0052 through EP-0059 after
  adversarial product, security, and distributed-systems review. Updated the
  command guides, slash-command reference, README, roadmap, and homepage for the
  shipped v0.78.0 behavior.

