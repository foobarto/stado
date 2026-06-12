## v0.64.2 — TUI polish round 2 (tree / monitor / split, recovery flows) + faster CI — 2026-06-12

Round 2 of TUI-usability fixes across the v0.64 feature surface — three merged
batches (#133, #134, #135) plus a CI-speed change (#132). Most were found by a
multi-agent UAT campaign that drove each surface and reproduced defects
test-first; two more were caught by review on the fix PRs themselves.

### Security

- **Memory delete-tombstone can no longer be laundered.** `delete` → `reject` →
  `approve` resurrected a deleted memory into a queryable, prompt-injectable
  approved entry, defeating the deleted-guard (EP-0015's terminal-tombstone
  invariant). `reject` now refuses a deleted item, like `approve`. (#135)
- **`/fleet` picker no longer leaks invalid UTF-8.** The prompt column
  byte-sliced untrusted entry strings, cutting mid-rune on CJK/emoji prompts and
  emitting raw continuation bytes (and a wrong display width) to the terminal;
  truncation is now display-width + grapheme aware, and the row is sized to the
  modal so it can't overflow the border. (#135)

### TUI

- **`/monitor` streams live.** It buffered all stdout and flushed only on
  process exit, so `tail -f` / `ping` showed nothing until they terminated;
  each line now arrives as it is read (EP-0036's per-line contract). `/monitor
  stop` no longer double-reports a spurious "process exited", and a stale
  completion from a stopped monitor can no longer clear a newly-started one
  (generation-tagged instances). `/loop stop` with no active loop now says "no
  active loop" instead of falsely "loop stopped". (#135)
- **`/tree` render fidelity.** The modal header stays one row at common widths;
  a deep fork node's `⑂ turn N` origin tag and provenance badge survive label
  truncation and never spill the modal (the left column is now clamped to its
  budget); the peek label keeps its "not a point-in-time snapshot" clarifier;
  and the per-turn ⟳N/⊘N hook-mutation/deny badges are no longer off-by-one on
  real session data (session totals were always correct). (#134, #135)
- **`/split` panes are click-to-expand.** Clicks in split view resolved against
  stale single-view line ranges (wrong block, or no-op), and tool blocks in the
  activity pane were unclickable; each pane now maps clicks through its own
  range table. (#135)
- **Tool output wraps at the width boundary.** Long unbroken tokens in the
  tool-output panel overflowed the frame; they now hard-wrap (display-width
  aware) and preserve leading indentation (so hashline reads stay aligned).
  (#134)
- **Landing / onboarding** render correctly at common geometries; the
  Enter-while-busy steer affordance is documented in `/help` + the input hint;
  the `/provider` modal leaves a margin at narrow widths; `/debug` reports an
  accurate message. (#134)
- **Budget recovery handles token caps.** A token-cap breach printed a USD-only
  block message (`cost $0.00 ≥ hard cap $0.00 … edit [budget].hard_usd`) and
  `/budget` showed token caps as `(unset)`; both now name the binding cap and
  its config knob. (#135)
- **`/alias` help row** no longer wraps onto multiple lines in the `?` overlay,
  restoring the name/description column alignment. (#135)
- **LSP sidebar + CLI error-UX** correctness. (#133)

### CLI

- **`stado auth`** reports honest set/unset for local-runner providers and pins
  the env var in `unset`. (#134)
- **`audit export <id>`** now errors on an unknown session id instead of exiting
  0 with empty output (the B8 footgun, previously fixed only for
  `audit verify`) — no silent data loss for SIEM ingestion. (#135)

### Infra

- **Faster PR CI.** Race tests and the cross-compile build matrix moved to
  post-merge (push to `main`) and out of the required PR checks; the PR `test`
  job runs non-race. PR feedback dropped from ~458s to ~126s. `release.yml`
  self-runs `go test -race` before publishing so tagged artifacts stay
  race-gated. (#132)

