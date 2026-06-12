## v0.64.3 — audit downgrade fix + reviewer-flagged follow-ups — 2026-06-12

Closes a parked audit-signature finding and the sibling/doc-drift items the
v0.64.2 PR reviews surfaced.

### Security

- **Audit signature v1-downgrade closed (Codex C8/P).** `audit.VerifyV2` tried
  the identity-bound v2 payload, then fell back to v1 (which binds only
  tree+parents+body). With sidecar write, an attacker could copy a genuine v1
  commit's `(tree, parents, body, signature)` into a new commit with a
  **rewritten author/timestamp** and still have it verify under the operator's
  key. `VerifyV2` no longer falls back to v1 — only the identity-bound v2
  payload is accepted. `ExtractSignature` now rejects a body carrying more than
  one `Signature:` trailer (anti trailer-injection), and `audit verify` flags a
  duplicate-trailer commit as invalid rather than unsigned.
  - **Behavior change:** genuinely pre-v2 (legacy v1) audit commits no longer
    verify. They are reported distinctly as `LEGACY-V1` (not tampered) with a
    "re-sign to verify under v2" note. All v2 history (everything signed since
    the scheme bump) is unaffected. An optional re-sign migration is planned.
- **Memory delete-tombstone laundering fully closed.** `delete` → `reject`/
  `approve`/`upsert`/`edit` were already guarded; this adds the missing
  `propose`-over-a-deleted-id path (reachable from the plugin `memory:propose`
  bridge), which folded a tombstone back to `candidate` and let a follow-on
  `approve` resurrect a queryable, prompt-injectable memory.

### TUI

- **`/loop` status-bar indicator.** A running loop now shows `↻ loop` (or
  `↻ loop (5m)` for a timed loop) in the status bar — the affordance EP-0036
  promised but never shipped.
- **Budget token caps surface everywhere.** A token-only budget (the
  local-runner case where USD cost is always 0) now gets a proactive warn
  block, token usage/caps in the `/context` status and `/status` modal, and the
  always-on sidebar gauge — previously all four were USD-only. When both USD and
  token caps are set, the sidebar shows whichever is under more pressure.
- **`/fleet` picker `last:` column is width-bounded** (was unbounded, overflowing
  the modal on a long tool name).

### CLI

- **`audit export` / display + stats commands surface real storage errors.**
  `audit export` already errored on an unknown id; the same swallow-on-resolve
  pattern is now classified across `session show` / `session logs` (which
  surface a real git-storage error instead of printing `(unset)` / ignoring it)
  and the `agents` / `stats` / `usage` aggregators (which warn to stderr and
  continue rather than silently skipping a corrupt sidecar).

