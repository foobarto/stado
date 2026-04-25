---
ep: 24
title: TUI Footer Density
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-24
see-also: [21, 23]
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
---

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
