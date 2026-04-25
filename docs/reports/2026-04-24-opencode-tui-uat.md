# opencode TUI UAT Report — 2026-04-24

Updated: 2026-04-25 for stado `main` after the L4 subagent and TUI
polish slices.

## Scope

This report compares the local opencode TUI against stado's current TUI
and translates the stronger opencode interaction patterns into stado
improvement work.

Original opencode comparison tested locally:

- opencode `1.14.22`
- stado `v0.4.2`
- terminal harness: `tmux 3.5a`, `200x50`
- opencode command: `opencode --pure --model lmstudio/qwen/qwen3.6-35b-a3b`
- model: LM Studio local `qwen/qwen3.6-35b-a3b`

Follow-up status reviewed against stado `main` source and the real PTY
harness `hack/tmux-uat.sh all`.

The opencode UAT created one temporary session titled `Greeting`; it was
deleted after the pass.

## Scenarios Run

| Scenario | Result | Notes |
|---|---:|---|
| Startup landing view | Pass | Centered logo, compact input, cwd + version footer. |
| Command palette (`ctrl+p`) | Pass | Searchable grouped commands with shortcut hints. |
| Model picker | Pass | Favorites, recent models, current selection, provider names, connect/favorite hints. |
| Agent switch (`tab`, `/agents`, command palette) | Pass | Build/Plan are first-class agents, not only modes. |
| Status modal (`ctrl+x s`) | Pass | Shows MCP, formatter, plugin status in one low-noise overlay. |
| Session picker (`ctrl+x l`) | Pass | Search, delete, rename hints; empty state is clean. |
| New session (`ctrl+x n`) | Pass | Immediate reset to landing without leaving TUI. |
| Inline `@` completion | Pass | Shows agents first, then fuzzy file matches as query narrows. |
| Slash completion | Pass with caveat | `/` opens inline command suggestions. Exact command entry was less obvious in tmux; selection stays command-first. |
| Live local-model turn | Pass | User block, streamed response, duration/status row, sidebar context update. |
| Typing while/after response | Pass | Input remained editable and retained typed text after response completion. |
| Theme picker (`ctrl+x t`) | Pass | Many built-in themes plus selected-state marker. |
| CLI session/provider/agent listings | Pass | Session list, credentials list, and agent permission output are available outside TUI. |

## Current stado Follow-up

Since the original `v0.4.2` comparison, stado has closed most of the
visible TUI workflow gaps:

| Area | stado `main` status | Remaining gap |
|---|---|---|
| Landing view | Implemented | Compact sampled banner keeps the prompt primary; remaining work is subjective polish. |
| Command discovery | Implemented | Provider setup and status remediation hints exist; true provider connect/OAuth remains future work. |
| Model picker | Partial | Current marker, provider labels, recents, favorites, default persistence, and `Ctrl+A` setup exist; true provider connect/OAuth remains future work. |
| Sessions | Partial | Switch, new, rename, fork, delete, per-session draft/scroll caches, and per-session provider/model state are in the TUI; inactive background execution policy remains future work. |
| Agents | Partial | Do, Plan, and BTW are picker rows; `spawn_agent` supports read-only and scoped worker children with explicit adoption. Client-side adoption affordances remain future work. |
| Inline `@` completion | Partial | Agents, sessions, skills, docs, Go symbols, and files are grouped; broader language symbols and indexing policy remain future work. |
| Sidebar calmness | Implemented | Logs/risk/debug details are hidden until `/debug`; richer debug drilldown can still improve. |
| Turn metadata | Implemented | Assistant turn footers show compact metadata and can expand into token, cache, tool, and trace details. |
| Themes | Partial | Built-in picker, light/dark shortcuts, custom-theme rows, and markdown style control exist; broader bundled theme catalog remains future work. |
| Status modal and LSP state | Partial | `/status` and `Ctrl+X S` show runtime health, LSP readiness, action hints, and trace IDs; deeper live provider/plugin/MCP health remains future work. |
| Footer density | Implemented | Footer now includes cwd, branch, session identity, version, usage, cost, and command hint when width allows. |
| tmux UAT harness | Implemented | Landing-view assertions are current and green. |

## What opencode Does Better

### 1. The First Screen Is Quieter

opencode's landing view uses whitespace, a small ANSI logo, a single
left-accented input surface, and only two hints: `tab agents` and
`ctrl+p commands`. It feels ready without looking busy.

stado's landing view now samples the embedded banner down to a compact
fixed-height mark and falls back to the wordmark in cramped terminals.
The remaining difference is visual taste, not a missing workflow.

### 2. Commands Are Discoverable Without Slash-Command Knowledge

`ctrl+p` opens a command palette that is grouped by purpose and shows
shortcuts inline:

- Suggested: switch model
- Session: open editor, switch session, new session
- System: plugins, status, theme, light mode

stado now has an inline `/` suggestion surface and a grouped `Ctrl+P`
command palette with shortcut labels. opencode still feels more curated
because provider connection, credentials, themes, sessions, and system
status read as one command vocabulary.

### 3. Model Selection Has Memory And Provider Actions

opencode's model picker is notably stronger:

- current model is marked
- favorites appear first
- recent models are separated
- provider display names are shown
- `ctrl+a` connects a provider
- `ctrl+f` favorites a model

stado now marks the current model, persists recents and favorites,
shows provider labels, saves model selections as new defaults, and uses
`Ctrl+A` for provider-specific setup hints. opencode still leads on
true provider connect flows for services that can authenticate from the
picker itself.

### 4. Sessions Are A TUI Workflow, Not A CLI Detour

opencode supports `ctrl+x l` for session search/switch and `ctrl+x n`
for new session. The session picker also advertises delete and rename
shortcuts.

stado now has a searchable in-TUI session manager with switch, new,
rename, fork, and confirmed delete. opencode still sets the target for
background/inactive session handling and cached per-session UI state.

### 5. Agents Are First-Class UI Objects

opencode presents Build and Plan as agents. The active agent appears in
the input status, `tab` changes it, and `opencode agent list` exposes
agent permissions.

stado now exposes Do, Plan, and BTW through an agent picker, shows the
active agent in the input and sidebar, and exposes `spawn_agent` for
read-only and scoped worker child sessions. The remaining gap is richer
client-side adoption UI and clearer permission surfaces for spawned or
plugin-backed workers.

### 6. Inline `@` Completion Is Unified

Typing `@` in opencode shows agent options (`@explore`, `@general`) and
then fuzzy file paths as the query narrows. The completion list remains
visually attached to the input rather than becoming a separate modal.

stado now groups agents, sessions, skills, docs, Go symbols, and files
in the same inline `@` surface. opencode's remaining advantage is
simplicity; stado's next steps are broader language symbols and indexing
limits without making the picker slow or noisy.

### 7. The Post-Turn Sidebar Is Sparse And Useful

After the first turn, opencode switches to a chat layout with a right
sidebar showing:

- session title
- context tokens and percentage
- cost
- LSP activation state

stado now keeps logs, risk internals, sandbox details, and other debug
diagnostics hidden until `/debug` is enabled. opencode is still a useful
calmness reference for how little the normal sidebar should need to say.

### 8. Status Rows Teach What Happened

Each opencode response ends with a compact row like:

`Plan · qwen/qwen3.6-35b-a3b · 896ms`

That makes agent, model, and duration visible without opening logs.
stado now renders compact assistant turn footers with agent,
model/provider, duration, tool count, token delta, and cost delta when
usage is available. `Shift+Tab` expands the latest assistant footer into
token, cache, tool, and trace details without making normal transcript
rows noisier.

### 9. Theme Switching Is In-App

opencode exposes theme switching in the command palette and a dedicated
theme picker. stado now has bundled themes, a TUI picker, direct
light/dark/toggle shortcuts, current custom-theme rows, and custom
markdown style control. opencode still has a broader catalog.

## Improvement Backlog For stado

### P0 — Product-Shape Gaps

1. **Refine spawned-worker adoption ergonomics.**
   EP-0013 now has read-only and scoped worker `spawn_agent` support,
   lifecycle events, and explicit `stado session adopt`. The next gap is
   turning the adoption command into a smoother TUI/headless/ACP client
   workflow.

2. **Define inactive-session execution policy.**
   EP-0014 covers switch/new/rename/fork/delete plus draft/scroll
   restore. The remaining work is provider state and whether inactive
   sessions can keep streaming without hidden mutation.

### P1 — UX Quality

4. **Broaden inline symbol support carefully.**
   EP-0020 now covers agents, sessions, skills, docs, Go symbols, and
   files. Additional languages should be added only with bounded
   indexing and clear grouping.

5. **Keep validating landing balance in PTYs.**
   The sampled banner is much closer to opencode's visual balance; keep
   the real tmux harness current as terminal sizes and banner art
   change.

6. **Broaden theme parity.**
   EP-0022 has a catalog and picker. Add a wider built-in set and a
   direct light/dark toggle if users keep reaching for it.

### P2 — Polish And Parity

7. **Add deeper live health details where snapshots expose them.**
   EP-0023 now links rows to focused commands or remediation. Future
   slices should add provider/plugin/MCP health detail once stable
   snapshots expose it.

8. **Broaden expanded turn details from trace data.**
   EP-0021 now has `Shift+Tab` expansion. Future slices can add richer
   failed-tool summaries and external trace links when the telemetry
   surface carries enough stable data.

## Do Not Copy Blindly

- opencode's permissions output shows very broad defaults in this local
  environment. stado should keep its stronger sandbox/audit posture.
- The `tab agents` hint was slightly ambiguous in the UAT because it
  toggled Build/Plan agent state rather than opening a chooser. stado
  should label that action precisely.
- Slash completion in tmux made exact command execution less obvious
  than the command palette path. stado should keep slash completion
  predictable and favor explicit command-palette execution.
- opencode keeps its logs/debug data out of the default sidebar. stado
  should copy that calmness, but keep a fast debug path because stado's
  sandbox/trace model is more operationally explicit.

## Recommended Next EP Work

- Dogfood EP-0013 worker adoption with a loaded local model, then refine
  TUI/headless/ACP adoption affordances from the transcript.
- Extend EP-0019 from manual setup hints toward provider connect/OAuth
  where providers support it.
- Extend EP-0014 with provider-state handling and inactive-session
  execution policy.
- Extend EP-0020 beyond Go symbols only after a bounded indexing policy
  is clear.

## Verdict

opencode's main advantage is product coherence. Startup, commands,
model selection, agents, sessions, status, themes, and file mentions all
feel like parts of one keyboard-first interface. stado has now copied
many of the visible workflows while keeping its stronger safety/audit
posture. The remaining work is less about adding another picker and more
about closing the remaining runtime and integration gaps: worker
adoption ergonomics, provider remediation, inactive-session policy, and
broader but still bounded context discovery.
