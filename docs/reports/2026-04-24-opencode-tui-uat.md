# opencode TUI UAT Report — 2026-04-24

Updated: 2026-04-25 for stado `v0.21.1`.

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

Follow-up status reviewed against stado `v0.21.1` source and the real
PTY harness `hack/tmux-uat.sh all`.

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

| Area | stado `v0.22.0` status | Remaining gap |
|---|---|---|
| Landing view | Implemented | Logo remains heavier than opencode's first screen. |
| Command discovery | Implemented | Provider setup is available from the model picker; richer status-modal remediation remains. |
| Model picker | Partial | Current marker, provider labels, recents, favorites, default persistence, and `Ctrl+A` setup exist; true provider connect/OAuth remains future work. |
| Sessions | Partial | Switch, new, rename, fork, and delete are in the TUI; inactive background sessions and per-session draft/scroll caches remain future work. |
| Agents | Partial | Do, Plan, and BTW are picker rows with status visibility; subagent/spawn workers are not implemented. |
| Inline `@` completion | Partial | Agents, sessions, skills, and files are grouped; docs and symbols remain future work. |
| Sidebar calmness | Implemented | Logs/risk/debug details are hidden until `/debug`; richer debug drilldown can still improve. |
| Turn metadata | Implemented | Assistant turn footers show agent, model/provider, duration, tool count, token delta, and cost delta when available. |
| Themes | Partial | Built-in theme picker exists; opencode still has a broader catalog and light/dark affordances. |
| Status modal and LSP state | Partial | `/status` and `Ctrl+X S` show runtime health and LSP readiness; deeper provider/plugin health remains future work. |
| Footer density | Implemented | Footer now includes cwd, branch, session identity, version, usage, cost, and command hint when width allows. |
| tmux UAT harness | Implemented | Landing-view assertions are current and green. |

## What opencode Does Better

### 1. The First Screen Is Quieter

opencode's landing view uses whitespace, a small ANSI logo, a single
left-accented input surface, and only two hints: `tab agents` and
`ctrl+p commands`. It feels ready without looking busy.

stado's new landing view is directionally right, but the current logo is
visually heavier. At `200x50`, stado's startup screen is dominated by
the logo art, while opencode keeps the input as the primary object.

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

stado now exposes Do, Plan, and BTW through an agent picker and shows
the active agent in the input and sidebar. The remaining gap is the
actual subagent/spawn tool and a coherent permission UI for spawned or
plugin-backed workers.

### 6. Inline `@` Completion Is Unified

Typing `@` in opencode shows agent options (`@explore`, `@general`) and
then fuzzy file paths as the query narrows. The completion list remains
visually attached to the input rather than becoming a separate modal.

stado now groups agents, sessions, skills, and files in the same inline
`@` surface. opencode's remaining advantage is simplicity; stado's next
steps are docs and symbols without making the picker slow or noisy.

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
usage is available. Future work can make those footers expandable into
trace detail.

### 9. Theme Switching Is In-App

opencode exposes theme switching in the command palette and a dedicated
theme picker. stado now has bundled themes and a TUI picker, but
opencode still has a broader catalog and clearer light/dark shortcuts.

## Improvement Backlog For stado

### P0 — Product-Shape Gaps

1. **Implement the subagent/spawn tool.**
   EP-0013 is now the biggest remaining product-shape gap. The TUI has
   an agent picker, but there is still no model-visible tool for bounded
   parallel agent work.

2. **Complete multi-session state caching.**
   EP-0014 covers switch/new/rename/fork/delete. The remaining work is
   preserving per-session draft, scroll, and inactive background state
   without hidden mutation.

### P1 — UX Quality

4. **Extend inline `@` to docs and symbols.**
   EP-0020 now covers agents, sessions, skills, and files. Docs and
   symbols should be added only with bounded indexing and clear grouping.

5. **Tone down the landing logo.**
   The landing view works, but opencode still has better visual balance:
   the input is the primary object and the logo does less work.

6. **Broaden theme parity.**
   EP-0022 has a catalog and picker. Add a wider built-in set and a
   direct light/dark toggle if users keep reaching for it.

### P2 — Polish And Parity

7. **Make status modal rows actionable.**
   EP-0023 shows status. The next slice should link rows to focused
   commands or remediation, especially provider, plugin, MCP, and OTel
   rows.

8. **Add expandable turn trace details.**
   EP-0021 footers are implemented. An optional expansion could show
   failed tools, cache deltas, and trace links without making the normal
   transcript noisy.

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

- Advance EP-0013 from placeholder/partial into an implementable
  subagent runtime design.
- Extend EP-0019 from manual setup hints toward provider connect/OAuth
  where providers support it.
- Extend EP-0020 with docs/symbol result semantics and indexing limits.
- Extend EP-0014 with per-session cached UI state and inactive-session
  execution policy.

## Verdict

opencode's main advantage is product coherence. Startup, commands,
model selection, agents, sessions, status, themes, and file mentions all
feel like parts of one keyboard-first interface. stado has now copied
many of the visible workflows while keeping its stronger safety/audit
posture. The remaining work is less about adding another picker and more
about closing the real runtime gaps: subagents, provider remediation,
session state caching, and fast context discovery.
