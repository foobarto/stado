# L3 Checkpoint - 2026-04-25

This note preserves the autonomous-loop state after the 2026-04-25 L3 pass.

## Released

- `v0.21.2` (`a8fc02f`) - refreshed the opencode TUI UAT report and reprioritized remaining TUI gaps.
- `v0.22.0` (`4b118cc`) - added provider setup guidance from the model picker with `Ctrl+A`.
- `v0.23.0` (`79720bc`) - preserved per-session draft and scroll state during in-process session switching.

## Verification

- Local gates for `v0.23.0` passed:
  - `go test ./...`
  - `golangci-lint run ./...`
  - `goreleaser check`
  - `hack/tmux-uat.sh all`
  - `git diff --check`
- Remote CI and release workflows passed for `v0.23.0`.
- Local `./stado version` was rebuilt from the clean `v0.23.0` tag before this checkpoint note was created.

## Current State

- Branch: `main`
- Last functional release: `v0.23.0`
- Worktree was clean before this handoff update.
- No known blocker was left open from the session-draft/scroll slice.

## Next Candidates

Highest-value follow-up work:

1. EP-13 subagent/spawn tool implementation.
2. EP-14 remaining multi-session behavior: provider state per session and inactive-session execution policy.
3. EP-20 inline `@` completion polish for docs/symbol context.
4. Landing view refinement: stado ANSI logo density, theme fit, and startup focus behavior.

Continue with one bounded slice at a time, run the relevant local gates, and apply the release rule from `CLAUDE.md` before tagging.
