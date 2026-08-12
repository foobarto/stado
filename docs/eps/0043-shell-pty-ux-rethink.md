---
ep: 0043
title: Shell PTY-UX rethink — read modes, no lock, labeled sessions
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-06-14
implemented-in: v0.74.1
see-also: ["[EP-0041](./0041-shell-pty-tool-naming.md)", "[EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md)", "[EP-0037](./0037-tool-dispatch-and-operator-surface.md)"]
history:
  - date: 2026-06-14
    status: Draft
    note: >
      Drafted after the operator observed agents not reaching for the bundled
      `shell` PTY tools and `shell.screenshot` actively confusing them. Folds
      screenshot into `read {mode: auto|stream|screen}` (auto via the vt100
      alternate-screen flag), drops the single-attach lock and removes the
      attach/detach tools (read/write reach a session by integer id), and makes
      `spawn` accept a description surfaced by a broad self-identifying `list`.
  - date: 2026-06-14
    status: Accepted
    note: >
      Design settled through operator Q&A — swung from heavy session-token /
      owner-scoping to the simple integer-id model (D7); `terminal:open` is the
      security boundary. Implemented on branch ep-shell-pty-ux-rethink: read
      mode fold + AltScreen auto-detect, attach lock + attach/detach tools
      removed, spawn description + broad list. Spawning-PID in `list` (Slice 5)
      deferred to a fast-follow. Flip to Implemented at the v0.74.0 tag.
  - date: 2026-06-14
    status: Implemented
    note: >
      Shipped in v0.74.1. Pre-release adversarial review added D10 (mode:"auto"
      drains the raw ring when it renders a screen, guarded against an active
      Read/Expect consumer) and made `shell.signal` accept signal names
      ("SIGINT") host-side, not just integers. Docs synced (host-imports,
      abi-reference, EP index, CHANGELOG). NOTE: the v0.74.0 tag's release run
      failed GoReleaser's windows_amd64 cross-compile (the shell.signal name
      map used Unix-only syscall.SIG* constants — fixed by moving them behind a
      //go:build unix file), so the content actually published as v0.74.1.
---

# EP-43: Shell PTY-UX rethink — read modes, no lock, labeled sessions

## Problem

Agents do not reach for the bundled `shell` plugin's persistent-PTY tools
without explicit instructions, and `shell.screenshot` in particular confuses
them. The tool *descriptions* (`internal/runtime/bundled_plugin_tools.go`) are
actually detailed — the failure is **structural**:

1. **Ceremony.** The PTY surface is eleven tools, including a `spawn → attach →
   write → read → … → detach → destroy` dance gated by a single-attach lock.
   Facing that versus a one-shot `bash`, an agent picks `bash`. Worse: across
   the one-shot `stado tool run` path (PTYs are daemon-hosted to survive
   invocations), `spawn` then `write` hits `ErrNotAttached` — "you need to
   attach first" — because nothing auto-attaches. The lock's only real job was
   serializing concurrent writers, a rare edge.
2. **`screenshot` fights its own description.** The *name* implies an image, so
   an agent scanning tool names skips it when it wants to "read shell output."
3. **read-vs-screenshot is a premature choice.** Two tools answer "get output"
   — `read` returns the raw byte stream; `screenshot` the rendered vt100 screen
   — forcing the agent to pre-decide before it knows which it needs.

This EP keeps the capability (persistent PTY is genuinely valuable for REPLs,
installers, full-screen TUIs, SSH, and long-running processes) and removes the
friction. It deliberately stays **simple** — no per-session access-control
machinery (see Non-goals). It is the foundation for the planned `ssh`
connection-manager plugin (separate EP), which inherits this read model.

## Goals

- **Fold `shell.screenshot` into `shell.read`** via a `mode` parameter, so "get
  output" is one obvious verb and the agent need not pre-decide.
- **Auto-detect the right representation** so a bare `read` "just works": raw
  stream for line-oriented shells, rendered screen for full-screen programs.
- **Remove the attach lock and the `attach`/`detach` tools** — `read`/`write`
  work directly by id. Fewer tools, no ceremony, and the "attach first" error
  goes away.
- **Make tool descriptions situation-first** — name *when* to use a persistent
  shell vs one-shot `bash`.
- **Make sessions self-identifying** — a `spawn` description + a richer
  `shell.list` so orphaned sessions are visible and triageable.
- Clean break (pre-1.0): remove the `shell.screenshot` tool; no alias/shim.

## Non-goals

- **Per-session access control / isolation.** We considered unguessable session
  tokens, owner-scoping `list`, and a see/drive split, and **deliberately
  dropped them** — too heavy for a single-user tool, and owner-*hiding* `list`
  fought session visibility (orphans would go invisible). Integer ids + no lock
  is the chosen simplicity; see Risk for the accepted trade-off. A multi-tenant
  isolation story, if ever needed, is a future EP.
- Changing the underlying single-attach *mechanism* beyond removing its use as
  an access gate.
- The `ssh` connection-manager plugin — its own EP, building on this one.
- Host/daemon-PID self-awareness ("don't kill the host") — its own small EP
  (`RuntimeContext` injection), complementary but separate.
- Removing the host-side snapshot import or the TUI's snapshot rendering (both
  stay; `read mode:screen` and the TUI use them).

## Design

### read with a mode (the fold)

`shell.read` gains an optional `mode` field:

```
shell.read { id, mode?: "auto" | "stream" | "screen", max_bytes?, timeout_ms? }
```

- **`auto` (default)** — the host checks the vt100 emulator's alternate-screen
  flag (`vt10x.State.Mode() & ModeAltScreen`, queryable under the session lock at
  snapshot time — `internal/plugins/runtime/pty/screen.go`). If a full-screen
  program is active (it switched to the alternate screen buffer via DEC private
  mode 1049/1047/47 — vim, htop, less, an installer), `read` returns the
  **rendered screen**; otherwise the **raw incremental stream**. For an ordinary
  line-oriented shell (no alt-screen) this is byte-for-byte today's `read`, so
  the default is backward-compatible in spirit.
- **`stream`** — force the raw incremental byte stream (today's `read`).
- **`screen`** — force the rendered screen (today's `screenshot`).

**Discriminated response.** Because `auto` can return either shape, the response
carries a discriminator:

- stream: `{ "kind": "stream", "data_b64": "...", "n": N, "eof"?: bool }`
- screen: `{ "kind": "screen", "text": "...", "cols": C, "rows": R,
  "cursor": {x,y,visible}, "title": "..." }`

`stream`/`screen` keep today's field shapes under `read`/`screenshot`; only the
`kind` discriminator is added.

**Localized, no ABI change.** `stado_terminal_read` and `stado_terminal_snapshot`
are already *separate* host imports. The fold is pure wasm-side dispatch in
`plugins/bundled/shell/main.go` (`stadoToolRead`): on `screen` (or `auto` with
the host reporting alt-screen), call the snapshot import; else the read import.
Host import signatures unchanged.

### Drop the attach lock

Remove the access-gate use of the single-attach lock:

- **`read` / `write` / `read_until` work directly by integer id** — no
  attachment required. In the host imports (`registerPTYRead`,
  `registerPTYWrite`, and the expect path in `host_pty.go`), drop the
  `ErrNotAttached` gate.
- **Remove the `shell.attach` and `shell.detach` tools** (and their host
  imports' gate role). This cuts two tools and the ceremony — directly serving
  the discoverability goal — and eliminates the "attach first" error.
- **Session ids stay integers** (the existing `atomic.AddUint64(&m.nextID, 1)`
  counter). The id is the non-secret reference *and* the handle for
  read/write/destroy. No tokens, no owner-scoping.
- The lock's only real job was serializing concurrent writers; with it gone two
  simultaneous writers to one PTY could interleave — a rare edge we accept (see
  Risk). `Snapshot`/`read mode:screen` were already attach-free.

### Labeled, self-identifying sessions

- **`spawn` gains an optional `description`** (free text, may be empty) — what
  the session is *for* ("tail prod app logs", "debug the failing migration").
  The session stores it. The `spawn` tool description asks agents to provide one
  ("describe what this shell is for — used to identify and clean up sessions")
  but never requires it (empty allowed; never blocks a spawn).
- **`shell.list` is broad and self-identifying.** It lists **all** sessions in
  scope (nothing hidden — orphans stay visible) with: `id · description · cmd ·
  alive · age` (+ buffered/exit_code). The `description` answers "what is this
  shell for?", so a list reads like real work vs stale and `shell.destroy`
  cleans the stale ones. (Shipped.)
- **Spawning-PID in `list` — DEFERRED (follow-up).** The original plan added the
  PID of the process that called `spawn`. Deferred because (a) for the
  `stado tool run` path the spawning CLI exits immediately, so its PID is a dead
  *provenance* record, not a live orphan signal; (b) the clean plumbing is a
  real decision — daemon-side `SO_PEERCRED` (the hook already exists at
  `internal/daemon/server.go:263`, but needs threading through the DispatchTool
  callback → Host → registerPTYCreate) vs a CLI-injected (spoofable, but it's
  only informational) `spawner_pid` arg. The `description` already delivers the
  identification value; the PID is a deliberate fast-follow, not worth rushing.

### Situation-first descriptions

Rewrite the PTY tool descriptions (`bundled_plugin_tools.go`) to lead with the
decision rule, e.g. `shell.spawn`: *"Open a persistent shell session. Use this —
not one-shot `bash` — for interactive programs (REPLs, ssh, db clients), full-
screen TUIs (vim, htop), anything that prompts, and long-running processes you
monitor. Returns {id}; use shell.read / shell.write directly."* `shell.read`:
document `mode` + the auto behavior. Drop all "Requires attach" notes.

### Clean break: remove the `shell.screenshot` tool

Remove the agent-facing `shell__screenshot` registration
(`bundled_plugin_tools.go`), its `tool_metadata.go` entry, and its
`cmd/stado/tool_run.go` PTY-gated-list entry. The `stado_tool_screenshot`
wasm-side **export is removed too** — `read mode:"screen"` renders via the
`stado_terminal_snapshot` host import, which **stays** (also consumed by the
TUI). No alias, no deprecation window (pre-1.0).

## Migration / rollout

- Clean break, no shim: `shell.screenshot` → `shell.read mode:"screen"` (or just
  `read` + auto). `shell.attach`/`shell.detach` are gone — `spawn` then
  `read`/`write` directly. Update all in-tree callers + any prompt/skill that
  referenced `screenshot`/`attach`.
- Daemon / MCP server: no `screenshot` references; the attach-gate removal is in
  the shared host imports. TUI: unaffected (uses the host snapshot import).
- CHANGELOG: note `shell.screenshot` + `shell.attach`/`detach` removal, the
  `read` `mode` param, the `spawn` `description`, and the `list` fields.

## Risk

- **No per-shell isolation.** Integer id + no lock means any caller holding the
  `terminal:open` capability can `read`/`write`/`destroy` any shell in scope
  (e.g. iterate ids). Accepted: this is a single-user tool, and PTY access is
  already gated behind the `terminal:open` capability (a plugin without it can't
  touch PTYs at all). A malicious/buggy `terminal:open` plugin reaching another
  shell is the residual; revisit only if a multi-tenant story arrives.
- **Concurrent-writer interleave.** Two simultaneous writers to one PTY can
  interleave input. Rare in single-agent use; accepted for the simplicity win.
- **Auto-detect ambiguity.** A program using cursor addressing without the
  alternate screen buffer reads as "stream" under `auto` — exactly today's
  behavior; `mode:"screen"` is the explicit escape hatch.

## Test strategy (done definition)

- Host/render tests for `read` at `mode: stream | screen | auto`, the last
  driving a real full-screen (alt-screen) program and an ordinary command,
  asserting the correct `kind` + content.
- No-lock flow: `spawn → read`/`write` with **no** attach call works; `attach`/
  `detach` tools are gone from the surface (contract test).
- `shell.list` shows `description` (+ cmd/alive/age) and stays broad so orphans
  are visible. (`spawning-PID` deferred to a fast-follow — see Design / Slice 5.)
- pty-bridge E2E (project TUI rule): a full-screen program via `read mode:auto`
  returns the rendered screen; line-oriented returns the stream. **Cover the
  `stado tool run` one-shot path explicitly** (the case that surfaced the
  attach-first wart).
- Contract test: `shell.screenshot` gone from the tool surface.
- `make` build, full suite, `-race`, staticcheck/golangci, `./stado install
  --force`; docs (tui.md / features / config) + CHANGELOG; security sweep before
  release.

## Open questions

- `auto` wiring: extend the snapshot response with the alt-screen flag and let
  `read mode:auto` always call snapshot (returning a screen render or a "stream"
  signal), vs. a flag-check then a second call — fewest round-trips, decided at
  implementation.
- Keep SVG reachable under `mode:"screen"` (`svg:true`) or drop from the agent
  path entirely? Leaning: drop from the default, keep behind explicit `svg:true`.
- Spawning-PID capture: pass the CLI PID in the dispatch request, or read it via
  `SO_PEERCRED` on the daemon socket (unspoofable but more plumbing; it's
  informational, so the request field is likely enough).

## Decision log

- **D1 — fold vs separate tool.** Fold `screenshot` into `read mode`; the
  confusing name goes away, one verb gets output.
- **D2 — `mode` param, default `auto`.** Fewer tools; agent doesn't pre-decide.
- **D3 — auto-detect signal.** The vt100 alternate-screen-buffer flag
  (`ModeAltScreen`), authoritative, not a heuristic.
- **D4 — situation-first descriptions.** Lead with *when* to use.
- **D5 — clean break on `shell.screenshot`.** Remove the tool, no shim; host
  snapshot import + wasm export stay.
- **D6 — drop the attach lock; remove `attach`/`detach`.** `read`/`write` work
  by id. Kills the "attach first" error and cuts two tools (serves
  discoverability). The lock only serialized concurrent writers — a rare edge
  traded for simplicity.
- **D7 — keep integer session ids; no tokens / owner-scoping.** We explored
  unguessable tokens + owner-scoped `list` + see/drive split and dropped them as
  too heavy for a single-user tool (and owner-hiding `list` fought orphan
  visibility). `terminal:open` already gates PTY access; that's the security
  boundary.
- **D8 — `spawn` gains an optional `description`.** Free text, may be empty;
  surfaced in `list`; the tool description asks for it but never requires it.
- **D9 — `shell.list` is broad + self-identifying.** All sessions in scope, with
  `description` (+ cmd/alive/age), so orphans are visible and triageable rather
  than hidden. (Spawning-PID deferred to a fast-follow — see Design.)
- **D10 — `mode:"auto"` drains the raw ring when it renders a screen.** Surfaced
  in review: with `auto` as the default, polling a full-screen program (vim/htop)
  returns grid renders while the raw byte ring keeps accumulating; once the
  program leaves the alternate buffer the next `auto`/`stream` read would dump
  the whole escape backlog. So the `auto`→screen path discards the pending ring
  bytes (`Manager.DiscardPending`) after rendering — the grid (vt10x) is a
  separate sink, so the render is unaffected. Explicit `mode:"screen"` stays a
  non-draining peek (the TUI overlay polls it read-only).

## Related

- EP-41 Shell PTY tool naming — read_until and screenshot affordance (this
  revises the screenshot affordance + the attach model EP-41 lived alongside).
- EP-38 ABI v2, bundled wasm tools, and runtime surface.
- EP-37 Tool dispatch, naming, and operator surface.
- Follow-ups (separate EPs): `ssh` connection-manager plugin; host/daemon-PID
  self-awareness in `RuntimeContext`.
