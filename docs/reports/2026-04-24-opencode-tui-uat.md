# opencode TUI UAT Report — 2026-04-24

## Scope

This report compares the local opencode TUI against stado's current TUI
and translates the stronger opencode interaction patterns into stado
improvement work.

Tested locally:

- opencode `1.14.22`
- stado `v0.4.2`
- terminal harness: `tmux 3.5a`, `200x50`
- opencode command: `opencode --pure --model lmstudio/qwen/qwen3.6-35b-a3b`
- model: LM Studio local `qwen/qwen3.6-35b-a3b`

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

stado has slash commands and a palette, but opencode's command surface
does a better job of teaching the keyboard model. The shortcuts are not
hidden in docs or a help overlay; they are next to the action.

### 3. Model Selection Has Memory And Provider Actions

opencode's model picker is notably stronger:

- current model is marked
- favorites appear first
- recent models are separated
- provider display names are shown
- `ctrl+a` connects a provider
- `ctrl+f` favorites a model

stado's model picker is functional, but it should grow favorites,
recents, provider connection status, and a direct credentials/connect
path.

### 4. Sessions Are A TUI Workflow, Not A CLI Detour

opencode supports `ctrl+x l` for session search/switch and `ctrl+x n`
for new session. The session picker also advertises delete and rename
shortcuts.

stado has strong session internals and CLI commands, but the TUI still
treats session management mostly as something to do outside the running
app. EP-0014 already points in the right direction; opencode validates
that this should be a top-priority TUI feature.

### 5. Agents Are First-Class UI Objects

opencode presents Build and Plan as agents. The active agent appears in
the input status, `tab` changes it, and `opencode agent list` exposes
agent permissions.

stado has Do/Plan modes and planned subagent work, but the UI model is
less coherent. Treating "Do", "Plan", future subagents, and plugin
workers as selectable agents would make the model easier to understand.

### 6. Inline `@` Completion Is Unified

Typing `@` in opencode shows agent options (`@explore`, `@general`) and
then fuzzy file paths as the query narrows. The completion list remains
visually attached to the input rather than becoming a separate modal.

stado already has `@` file completion. The gap is breadth and grouping:
opencode uses the same affordance for agents and files, making context
injection feel like one workflow.

### 7. The Post-Turn Sidebar Is Sparse And Useful

After the first turn, opencode switches to a chat layout with a right
sidebar showing:

- session title
- context tokens and percentage
- cost
- LSP activation state

stado's sidebar contains more operational detail: risk, agent, repo,
logs, context/cost. That is useful during debugging, but opencode's
default is easier to scan. stado should make logs/debug detail
collapsible or secondary so the normal sidebar stays calm.

### 8. Status Rows Teach What Happened

Each opencode response ends with a compact row like:

`Plan · qwen/qwen3.6-35b-a3b · 896ms`

That makes agent, model, and duration visible without opening logs.
stado should add a similarly compact per-turn footer for model,
provider, agent/mode, duration, and tool count.

### 9. Theme Switching Is In-App

opencode exposes theme switching in the command palette and a dedicated
theme picker. stado supports a TOML theme file, but it lacks a TUI
picker and built-in theme catalog.

## Improvement Backlog For stado

### P0 — Product-Shape Gaps

1. **Implement TUI multi-session management.**
   Build EP-0014 as a searchable overlay with switch, new, rename,
   delete, fork, and resume. `ctrl+x l` / `ctrl+x n` equivalents are
   worth copying.

2. **Promote modes/subagents into an agent picker.**
   Fold Do/Plan, future subagents, and plugin workers into one
   "agent" mental model. Show active agent in the input status and
   sidebar. Extend EP-0013 with TUI affordances, not just a spawn tool.

3. **Upgrade the model picker.**
   Add favorites, recents, provider labels, current marker, and provider
   connect/credential actions. Persist favorites in config/state.

4. **Calm the default sidebar.**
   Keep context/cost/session/agent visible, but move logs and risk
   diagnostics behind a toggle or debug panel. The normal TUI should not
   feel like a log console.

5. **Refresh the tmux UAT harness for the new landing view.**
   `hack/tmux-uat.sh all` currently fails because it expects the sidebar
   on startup. That assertion is stale after the landing-view change.

### P1 — UX Quality

6. **Add an opencode-style command palette.**
   Keep slash commands, but make `ctrl+p` the main command discovery
   path with groups, fuzzy search, and shortcut labels beside each
   command.

7. **Unify inline context insertion.**
   Extend `@` completion beyond files: agents, sessions, symbols, and
   possibly docs/skills. Use grouped results so users can learn one
   insertion pattern.

8. **Add per-turn footer metadata.**
   Render compact turn metadata after each assistant response: agent,
   model, provider, duration, token delta, tool count, and cost delta.

9. **Add session auto-title.**
   opencode renames the UAT session to `Greeting` after a simple
   greeting. stado has `/describe`, but automatic first-turn titles
   would improve session lists and future multi-session switching.

10. **Add theme catalog and TUI picker.**
    Keep TOML overrides for power users, but ship several named themes
    and expose them from the command palette. Include light/dark mode.

### P2 — Polish And Parity

11. **Add a status modal.**
    opencode's `ctrl+x s` modal gives a fast summary of MCP servers,
    formatters, and plugins. stado could show providers, MCP/plugin
    health, sandbox, OTel, and auto-compact status.

12. **Make LSP state visible in plain language.**
    opencode says "LSPs will activate as files are read." stado should
    show similarly clear LSP/tool readiness rather than only low-level
    details.

13. **Improve footer density.**
    opencode's bottom row packs cwd, branch, tokens, command hint, and
    version into a quiet status line. stado's footer should aim for the
    same scan speed.

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

- Extend EP-0014 with concrete TUI session overlay behavior.
- Extend EP-0013 with agent picker semantics and agent-permission UI.
- Add a new EP for model/provider UX if we want favorites, recents, and
  credential connection to be a tracked product decision.
- Add a small testing task for the stale tmux landing assertion.

## Verdict

opencode's main advantage is not a single feature; it is product
coherence. Startup, commands, model selection, agents, sessions, status,
themes, and file mentions all feel like parts of one keyboard-first
interface. stado has stronger safety/audit ambitions and many of the
building blocks already exist, but the TUI still exposes too much of the
implementation shape. The highest-value next work is to turn stado's
existing capabilities into calm, searchable, in-app workflows.
