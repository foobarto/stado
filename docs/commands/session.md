# `stado session`

Create, list, fork, and land git-native agent sessions. Every
conversation with stado lives inside a session; the session is the
unit of audit, fork, and isolation.

## What it does

A session is:

- **A UUID** — printed by `session new`, returned in JSON-RPC.
- **A worktree** — checked out at `$XDG_STATE_HOME/stado/worktrees/<uuid>/`.
  The agent's cwd. Mutations land here, not in your repo.
- **Two git refs** in the sidecar bare repo
  (`$XDG_DATA_HOME/stado/sessions/<repo-id>.git`):
  - `refs/sessions/<uuid>/tree` — the executable history (mutating
    calls only, diff-then-commit for `bash`).
  - `refs/sessions/<uuid>/trace` — the full audit log, one commit
    per tool call with structured trailers (Tool-Call/Tool-Args/
    Tool-Result/Tokens-In/Tokens-Out/Cost-USD/Duration-MS).
- **A turn tag** series — `refs/sessions/<uuid>/turns/<N>` marks
  each turn boundary. `session fork --at turns/<N>` forks from one.

Your repo's `.git` is never touched until you explicitly
`session land`.

## Why it exists

Most agent tools treat the repo as a scratch pad. Stado wants three
properties simultaneously:

1. **Your working tree stays pristine.** Agent mutations land in a
   separate worktree. You review before promoting.
2. **Every action is replayable.** `session tree` shows the turn-by-
   turn history. `session fork --at turns/<N>` spawns a new session
   starting from an earlier state — cheap experimentation with bad
   outputs.
3. **The audit log is tamper-evident.** Both refs are Ed25519-signed
   over a canonical framing; `stado audit verify` walks them and
   refuses to continue at the first bad commit.

A sidecar bare repo (alternates-linked to your `.git/objects`) keeps
disk use near-zero: shared object store, dedicated refs, zero risk
of colliding with your own branches.

## Subcommands

### `session new`

Creates a session + worktree. Prints the UUID on stdout, the
worktree path on stderr (so `$()` capture yields just the UUID).

```sh
id=$(stado session new)
echo "new session: $id"
```

### `session list` (`ls`)

Lists sessions for the current repo. Default hides zero-turn +
zero-message rows (test-run detritus); `--all` shows everything.
Columns: `SESSION ID · LAST ACTIVE · TURNS · MSGS · COMPACT · STATUS · DESCRIPTION`.

### `session show <id-or-label>`

Prints refs + worktree + turn count + latest commit summary + usage
(tokens, cost, wall time). Uses `resolveSessionID` — accepts a full
UUID, a UUID prefix (≥8 chars), or a description substring.

### `session describe <id> [text]`

Attach a human-readable label: `stado session describe abc1 "react
refactor"`. Stored in `<worktree>/.stado/description`; surfaces in
`session list`, `session show`, and the TUI sidebar.

Forms: `session describe <id> <text>` (set), `session describe <id>
--clear` (remove), `session describe <id>` (read).

### `session resume <id-or-label>`

`cd`s into the session's worktree and boots the TUI there. The TUI
replays the conversation from `<worktree>/.stado/conversation.jsonl`
so you pick up mid-chat. Same lookup resolver as `show`.

### `session attach <id>`

Prints the worktree path so you can `cd $(stado session attach
<id>)` from a shell. Doesn't launch the TUI — use this when you
want a plain shell in the agent's working tree.

### `session delete <id>`

Removes the session's refs + worktree + conversation.jsonl.
Idempotent (deleting a missing session is a stderr warning, not an
error).

### `session fork <id> [--at <turns/N|sha>]`

Creates a new session branched from an existing one. Without `--at`
the fork points at the parent's `tree` HEAD; `--at turns/3` forks
from the end of turn 3. Useful for "that answer went wrong, try
again from before the bad tool call."

### `session revert <id>`

Reset the session's worktree + tree ref to an earlier commit, on a
new child session. Leaves the parent session intact.

### `session tree <id>`

Interactive turn-history browser. Arrow keys navigate; `f` forks
from the highlighted turn. PTY-backed (teatest validates navigation
+ fork behaviour end-to-end).

### `session land <id> <branch>`

Push the session's `tree` HEAD to your user repo as `<branch>`.
This is the ONLY step that writes to your `.git`. Explicit + gated.

### `session export <id> [-o <file>]`

Render the conversation as markdown (default) or raw JSONL (`--format
jsonl`). `-o session.md` writes to a file; otherwise stdout. Markdown
output has per-role headers, fenced tool-call/result bodies,
thinking blocks as blockquotes (signature stripped).

### `session search <query>`

Grep across every session's persisted conversation. Case-insensitive
substring by default; `--regex` switches to RE2. Scope with
`--session <id>`; cap with `--max N`. Output shape:
`session:<id> msg:<n> role:<role>  <excerpt>`.

### `session logs <id> [-f]`

Render the session's `trace` ref as a scannable one-line-per-tool-
call feed. `-f` / `--follow` live-tails new commits; `--interval`
tunes poll frequency (default 500ms). `--limit N` caps. Fills the
gap between `session show` (summary) and `audit export` (JSONL).

### `session gc [--apply]`

Sweep zero-turn + zero-message sessions older than `--older-than`
(default 24h). Dry-run by default; pass `--apply` to actually delete.
Live sessions (pid alive) are always skipped.

## Worked examples

**Morning start, pick up yesterday's session:**
```sh
stado session list              # find the one you want
stado session resume react      # description-substring match
# ... TUI opens in the session's worktree
```

**Fork an earlier good state:**
```sh
stado session tree abc1         # navigate to a healthy turn, press f
# new session cut from that point; original untouched
```

**Compare a session's conversation against production:**
```sh
stado session export abc1 -o session.md
# or pipe into a diff tool / LLM-reviewer
```

**Promote the agent's changes to a branch:**
```sh
stado session land abc1 feature/react-refactor
# refs/heads/feature/react-refactor now points at session's tree HEAD
git checkout feature/react-refactor  # in your repo
```

## Config

Relevant `config.toml` sections:

- `[budget]` — warn/hard caps on cumulative cost (see
  [budget.md](../features/budget.md)).
- `[context]` — soft/hard context-window thresholds.
- `[tools]` — trim the bundled tool surface (see
  [../../README.md#configuring-tools--sandboxing](../../README.md#configuring-tools--sandboxing)).

Session data layout:

| Where | What |
|-------|------|
| `$XDG_DATA_HOME/stado/sessions/<repo-id>.git` | Sidecar bare repo |
| `$XDG_STATE_HOME/stado/worktrees/<uuid>/` | Session worktree |
| `<worktree>/.stado/conversation.jsonl` | Append-only persisted conversation |
| `<worktree>/.stado/description` | Optional human label |
| `<worktree>/.stado-pid` | Owning process PID (for `agents list`) |
| `<worktree>/.stado-span-context` | W3C traceparent (for forked sessions' span linking) |

## Gotchas

- **Forking a live session**: the parent is left running. Both sessions
  write to the same bare repo but different refs, so no conflict. The
  fork's trace ref is seeded from the fork point.
- **`session land`** pushes a SINGLE ref. Turn tags and the trace ref
  stay in the sidecar repo — they're not promoted to your user repo.
- **Zero-turn session cleanup**: `session list` hides them by default
  but the worktrees stay on disk. Run `session gc --apply` periodically.
- **`session compact`** (the CLI form) is an advisory stub — use
  `/compact` inside the TUI. Compaction in `stado run`/headless is
  planned (PLAN.md §11.3).
