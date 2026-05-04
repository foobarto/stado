---
ep: 0034
title: Background agents fleet — spawn, observe, terminate
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Captures the user-fired feature ask captured at the end of the v0.28.0 dogfood loop.
see-also: [0013, 0014, 0033]
---

# EP-0034: Background agents fleet — spawn, observe, terminate

> **Status: Draft.** No code yet. The `spawn_agent` tool +
> `SubagentRunner` already provide the synchronous fork-a-session
> primitive; the work is wrapping it in async lifecycle + a TUI
> fleet view + per-entry termination.

## Problem

The user wants to delegate work to background agents from the
TUI — type `/spawn "go investigate target X"`, immediately get
the prompt back, and check progress later. Today there is no
TUI-driven path for this:

- `spawn_agent` exists as an LLM tool: invoked **by the model**
  during a turn, runs the child synchronously to completion (see
  `internal/runtime/subagent.go:80`'s `SubagentRunner.SpawnSubagent`).
  The parent's tool call doesn't return until the child finishes.
  Useful for "delegate this subtask," not for "fire off a
  long-running background investigation."
- The session picker (`Ctrl+X L`) lists every session ever, with
  no liveness signal — the user can't see "which sessions are
  running right now" at a glance.
- There's no termination affordance for sessions other than
  Ctrl+C in the active session.

The fleet UX the user described:
1. Fire off agents from the TUI and keep working.
2. List all running agents on demand with status + last activity.
3. Inspect an entry to see what the agent is currently doing.
4. Terminate an agent.

## Goals

- **`/spawn <prompt>`** — slash command that forks a child
  stadogit session, runs the agent loop in a goroutine, returns
  control to the user immediately. Uses the configured provider
  + model + tool registry by default; flags for override.
- **`/fleet`** — modal listing all background agents with status
  (running / blocked-on-approval / completed / cancelled / error),
  age, last tool call, last assistant text snippet.
- **Terminate** from the fleet modal — cancel the running goroutine's
  context, mark as cancelled. Existing audit trail preserved.
- **View** an entry from the fleet modal — switch the main session
  to the child's session id (read-only navigation; the existing
  session-switch path).
- **Persistence within the stado process.** Liveness state is
  in-memory; restart drops the registry. Persisted audit
  (stadogit) survives restart.

## Non-goals

- **No multi-process spawning.** Children run as goroutines in the
  same stado process for v1. The existing daemon work (CLAUDE.md
  refers to a "background-agents fleet" disabled by default) can
  layer on later as a phase B.
- **No cross-machine fleet.** Single-host.
- **No automated retries / restart / scheduling.** Schedule lives
  in the existing `/loop` + `/schedule` skills; this EP is about
  observable on-demand spawns.
- **No live progress streaming** between child and parent. The
  fleet view samples the child's most-recent transcript blocks
  via `LastActivity` snapshots; events flow into the audit trail
  as usual but not into a live event bus. (A live bus is a
  potential phase B.)
- **No fork-and-merge UX in this EP.** The existing
  `stado session adopt` flow stays the canonical way to bring
  child changes into the parent's tree.

## Design

### Liveness registry

New `internal/runtime/fleet.go`:

```go
type FleetEntry struct {
    SessionID    string         // child stadogit session id
    Prompt       string         // initial user prompt that started this
    Provider     string
    Model        string
    StartedAt    time.Time
    EndedAt      time.Time      // zero when running
    Status       FleetStatus    // running | blocked | completed | cancelled | error
    LastActivity time.Time      // updated on every tool call / text delta
    LastTool     string         // most recent tool name
    LastText     string         // last ~80 chars of assistant text
    cancel       context.CancelFunc
    done         <-chan struct{}
    err          error          // populated when Status == error
}

type Fleet struct {
    mu      sync.Mutex
    entries map[string]*FleetEntry
}

func (f *Fleet) Spawn(prompt string, opts SpawnOptions) (*FleetEntry, error)
func (f *Fleet) Cancel(sessionID string) error
func (f *Fleet) Get(sessionID string) (*FleetEntry, bool)
func (f *Fleet) List() []FleetEntry
```

Spawn wraps `SubagentRunner.SpawnSubagent` in a goroutine that:
1. Creates a `context.WithCancel` rooted at the fleet's lifetime.
2. Stores the cancel fn + done channel in the entry.
3. Calls `SpawnSubagent` with progress-emitting hooks that update
   the entry's `LastActivity` / `LastTool` / `LastText`.
4. On return, sets `EndedAt` and `Status` (completed / cancelled
   / error) under the mutex.

### Slash commands

- `/spawn <prompt...>` — fires `m.fleet.Spawn(prompt, ...)` and
  appends a system block to the parent transcript:
  `"spawned bg agent <id>; /fleet to view"`. Uses the parent's
  provider + model + registry.
- `/spawn --provider <name> --model <id> <prompt...>` — overrides.
  Argv-style flag parsing kept minimal.
- `/fleet` — opens the FleetPicker modal. Shows entries sorted by
  status (running first), then by `StartedAt` desc.

### Fleet modal (TUI)

New `internal/tui/fleetpicker/`. Pattern mirrors the model picker:

- Search by id / prompt / status.
- Per-entry display: `<status-pill> <short-id> <model> <age> <last-activity>`.
  Body shows `LastTool` + `LastText` truncated.
- Keybindings:
  - `enter` → switch main session to child's session id (existing
    session-switch path; child's transcript becomes visible).
  - `ctrl+x` → terminate (cancel the entry's context).
  - `esc` / `ctrl+g` → close the modal (no action).
  - `ctrl+c` → close the modal (popup-close semantics).

Refresh: the modal polls `m.fleet.List()` every 500ms while open
so the user sees status transitions in real time.

### Status transitions

```
        Spawn
          │
          ▼
       Running ─────┬──────► Completed
          │         │           (goroutine exit, no error)
          │         │
          │         ├──────► Cancelled
          │         │           (Cancel called; ctx.Err == Canceled)
          │         │
          │         └──────► Error
          │                     (goroutine returned non-context error)
          │
          ▼
       Blocked
       (host.Approve hit;
        approval bridge wired
        into the entry — phase B,
        deferred for v1)
```

For v1 we collapse blocked → running (no approval-aware UI in the
fleet modal). Approvals fire in the parent's flow via the existing
auto-approve hostAdapter, so blocked-on-approval doesn't actually
manifest unless the child requests human approval — out of scope.

## Migration / rollout

Single-iteration ship. No config gate; the slash commands are
opt-in (the user has to type `/spawn`). `m.fleet` initialised
lazily on first use so existing sessions pay no startup cost
when they don't use the feature.

## Failure modes

- **Spawn fails before the goroutine starts.** `Fleet.Spawn`
  returns the error synchronously; the slash command surfaces
  it as a `system` block in the transcript.
- **Spawn fails inside the goroutine.** Entry transitions to
  `Status=error` with the error stored; visible in the modal.
- **Stado exits with running entries.** Each entry's context is
  rooted at `m.rootCtx`; Stado's clean-exit path cancels rootCtx
  → all entries cancel cooperatively. The audit trail preserves
  partial progress.
- **Two `/spawn` calls with overlapping work.** Each spawn forks a
  fresh stadogit session id — no clash.
- **Termination raced with completion.** `Cancel` is idempotent
  via the `done` channel guard; if the goroutine already exited
  cleanly, Cancel is a no-op. Status field reflects whichever
  terminal state landed first.

## Test strategy

- **Unit:** Fleet.Spawn / Cancel / List with a fake SubagentRunner
  (returns canned events). Verify status transitions + entry
  fields.
- **Unit:** Slash command parsing — `/spawn`, `/spawn --provider X
  prompt`, malformed forms.
- **TUI:** `/fleet` opens modal, lists current entries, status
  pills render correctly. Reuse the existing UAT picker test
  framework.
- **Integration:** end-to-end spawn from TUI:
  1. `/spawn "echo hello via bash"` → entry appears in fleet.
  2. Wait until status = completed.
  3. Switch session to child id, verify transcript contains the
     bash invocation + result.
  4. Re-run + cancel mid-flight, verify status = cancelled and
     audit trail preserved.

## Open questions

- **Concurrency limit.** Should the fleet cap at N concurrent
  agents to avoid resource exhaustion? Probably yes; default 4,
  configurable. Not blocking v1 — add when it bites.
- **Status pill colours.** Theme conventions for running /
  blocked / completed / cancelled / error. Reuse existing block
  kinds where possible.
- **Approval bridge for children.** Phase B — when a child hits
  `host.Approve`, the fleet view should surface the approval
  request. Today's auto-approve host means this rarely fires;
  defer until it becomes a real problem.

## Decision log

### D1. Single-process goroutines, not subprocess

- **Decided:** child agents run as goroutines in the same stado
  process for v1. Multi-process is phase B.
- **Alternatives:** spawn each as a separate `stado` subprocess
  (matching the daemon work); use the existing
  `disableBackgroundAgents` config knob to gate.
- **Why:** the goroutine path exists today (`SubagentRunner`).
  Wrapping in async is straightforward. Multi-process introduces
  IPC + shared-state coordination that doesn't carry weight for
  the basic "fire off a few investigations and check on them"
  use case. If long-running agents start hogging memory or
  blocking GC the multi-process path is a clean upgrade.

### D2. Polling-based fleet modal, not event subscription

- **Decided:** the modal polls `Fleet.List()` every 500ms while
  visible.
- **Alternatives:** event bus pushed updates to the modal via
  bubbletea messages.
- **Why:** simpler to ship, no concurrency gotchas. 500ms is
  imperceptible for status updates. Switch to event-driven when
  per-entry live transcript streaming becomes a feature (which
  is its own EP).

### D3. `/spawn` and `/fleet` as slash commands, not new
keybindings

- **Decided:** discoverable via the slash palette; no dedicated
  keybinding.
- **Alternatives:** `Ctrl+X F` for fleet, `Ctrl+X S` for spawn
  (clash — Status uses Ctrl+X S).
- **Why:** v1 traffic is exploratory; users find the commands
  via the palette. If usage gets heavy, add keybindings (similar
  to how /model has Ctrl+X M).

### D4. Use the parent's provider + model by default

- **Decided:** `/spawn <prompt>` uses the active session's
  provider + model unless overridden via `--provider` /
  `--model`.
- **Alternatives:** require explicit provider on every spawn.
- **Why:** lowest-friction — the user has already configured the
  active session; spawned agents inherit that context unless they
  want different. Override flags keep the escape hatch.

### D5. No live transcript streaming in v1

- **Decided:** the fleet modal shows `LastTool` + `LastText`
  snapshots updated on each tool call / text delta, not a live
  feed.
- **Alternatives:** stream the child's events into the parent's
  TUI in real time.
- **Why:** out of scope for "observe what an agent is doing." A
  snapshot every ~half-second is enough for the user to see "it's
  reading file X, last said Y, still running." A live feed
  needs an event bus and a per-entry pane in the modal — phase B.

## Related

- [EP-0013: Subagent Spawn Tool](./0013-subagent-spawn-tool.md) —
  defines `spawn_agent` + the synchronous fork primitive this EP
  builds on.
- [EP-0014: Multi-Session TUI](./0014-multi-session-tui.md) — the
  existing session picker / switch flow this EP reuses for "view
  an entry."
- [EP-0033: Responsive frontline (supervisor + worker lanes)](./0033-responsive-supervisor-worker-lanes.md) —
  unrelated UX direction (supervisor + worker on the SAME
  conversation), but the multi-agent intuition is shared.
- `internal/runtime/subagent.go` — `SubagentRunner.SpawnSubagent`
  is the synchronous primitive wrapped by `Fleet.Spawn`.
