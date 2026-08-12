---
ep: 24
title: TUI Footer Density
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
implemented-in: v0.24.0
type: Standards
created: 2026-04-24
see-also: ["EP-0021", "EP-0023"]
history:
  - date: 2026-04-25
    status: Implemented
    note: Footer cwd now renders as repo-relative `repo/subdir` when the TUI is inside a git worktree, falling back to home-relative paths outside repos.
  - date: 2026-04-25
    status: Partial
    note: Chat status row now appends a cached `*` marker to the git ref when the worktree has uncommitted changes.
  - date: 2026-04-24
    status: Partial
    note: Chat status row now combines compact cwd, branch, version, usage, cost, and command hints.
  - date: 2026-04-25
    status: Partial
    version: v0.21.1
    note: Chat status row now includes the active session label, falling back to a short session id when unlabeled.
  - date: 2026-07-09
    status: Implemented
    note: >
      Doc correction (planned-vs-code audit). The body says branch detection
      "also handles worktree `.git` files that point at a gitdir." That
      handling was removed in the 2026-04-25 hardening commit 66179209 ("fix:
      harden rooted file access"): status_bar.go now bails when `.git` is a
      file rather than a directory, so inside a linked `git worktree` checkout
      the branch segment is silently empty. Re-adding gitdir-following (safely)
      is a small follow-up; until then the EP body over-states the capability.
---

> **Relationships:** **See also:** [EP-0021](./0021-assistant-turn-metadata.md), [EP-0023](./0023-status-modal.md)

# EP-24: TUI Footer Density

## Problem

The first-run landing footer showed cwd and version, but the active chat
footer only showed usage and command hints. Opencode keeps repo context,
branch, tokens, cost, commands, and version visible in one quiet row,
which reduces sidebar dependence and improves scan speed.

## Goals

- Keep cwd, git branch, and version visible in the chat footer when
  there is room.
- Preserve the existing usage, cost, queue, budget, and command hint
  signals.
- Fall back cleanly on narrow terminals.

## Non-goals

- Replacing the sidebar.
- Adding a full VCS status indicator.
- Running git commands on every render.

## Design

The status row now has two segments:

- Left: compact repo-relative cwd (`repo/subdir`) or home-relative cwd
  outside a git worktree, current branch or detached short SHA with `*`
  for uncommitted worktree changes, active session label or short id,
  and stado version.
- Right: busy/error/queue/budget state, tokens, cost, and `ctrl+p`
  command hint.

Branch detection reads `.git/HEAD` directly and also handles worktree
`.git` files that point at a gitdir. If the terminal is too narrow, the
left segment is omitted and the right segment remains right-aligned.

## Test Strategy

- UAT-style unit coverage asserts that cwd, branch, session identity,
  and command hints coexist on a wide footer.
- Existing status-row tests continue to cover streaming, error, queued,
  cache, and cost signals.

Dirty-state checks are cached briefly so the renderer does not invoke
git on every frame.

## Open Questions

- None.
