# Bridge-UAT Test Plan

Companion to `bridge_uat_test.go`. Maps stado TUI surfaces to
bridge-UAT scenarios, ranked by signal-per-second. Drafted
2026-05-09 after the F9b panel-emit test landed.

## What this harness is for

Three TUI test layers exist in this repo. Each catches a different
class of regression; pick based on what fails when the test fails:

| Layer | Catches | Misses |
|-------|---------|--------|
| `internal/tui/uat_*_test.go` (teatest) | bubbletea Update-loop logic, message routing, state transitions | escape codes, terminal width, alt-screen redraws, color tone application, multi-line wrap layout |
| `hack/tmux-uat.sh` | termios + cancelreader regressions, real PTY input timing, tmux-detectable rendered text | per-pixel layout, lipgloss border alignment under tight viewports, color tone (tmux capture-pane strips it) |
| `hack/pty-bridge/` (this) | full visual rendering through xterm.js: box-drawing alignment, overlay centering, multi-line wrapping, escape-code correctness | nothing the other two miss in their own niche; ~3s per scenario cost |

The bridge's value is **"what the operator actually sees on
screen"** — not "what state the Model is in." Scenarios that don't
have a visual signal beyond what teatest already asserts should NOT
be added here.

## Existing scenarios (5 — all passing)

| Test | What |
|------|------|
| `TestBridgeE2E_Bash` | Bridge plumbing round-trip (no stado dep). |
| `TestBridgeE2E_Stado` | Landing screen renders + Ctrl+P opens palette. |
| `TestBridgeE2E_Stado_F9bRegression` | landing + Ctrl+P + Esc (3 sub-scenarios in one test). |
| `TestBridgeE2E_Stado_RendersPanel` | Full F9b chain: build wasm → `stado plugin dev` → `/tool render_demo` → assert ASCII panel materialises. |
| `TestBridgeE2E_StadoDebug` | Diagnostic only (no assertions); dumps three timed snapshots so a human can eyeball state evolution. |

## Proposed new scenarios (ranked by signal-per-second)

Each scenario lists: **trigger** (what to send via
`window.bridge.sendKeys`), **assertion** (what to check via
`window.bridge.snapshot()`), **why bridge** (what makes it
bridge-UAT-only vs. teatest), and **cost** (rough seconds).

### P1 — high-value, cheap (~2-3s each)

#### 1. Help overlay (`?` from idle)

- **Trigger:** `window.bridge.sendKeys('?')`
- **Assertion:** snapshot contains "Slash commands" header + at least
  three canonical slash names (`/sidebar`, `/theme`, `/help`) +
  `╭` and `╯` rounded-border chars (the help overlay renders
  through `internal/tui/overlays.CenterOver` with a
  RoundedBorder lipgloss style).
- **Why bridge:** teatest can assert the overlay is open, but it
  can't see whether the overlay's box-drawing chars actually align
  at the rendered viewport size or whether content bleed-through
  from the conversation pane corrupts the frame. Real Chrome
  width matters: tmux capture-pane already covers the
  text-content side via `hack/tmux-uat.sh::cmd_help_overlay`.
- **Cost:** ~1.5s. No external state.

#### 2. Theme picker open + cursor move

- **Trigger:** `window.bridge.sendKeys('\x18t')` (Ctrl+X then T —
  the existing `ctrl+x t` chord, per the palette hint), then a
  `Down` arrow `'\x1b[B'`.
- **Assertion:** snapshot contains theme names (e.g. `dark`,
  `light`) + a cursor-style highlight indicator (the picker uses a
  reversed-video selection — looks for `▶` or matching style
  marker on the highlighted row).
- **Why bridge:** the picker is a bubbletea list with lipgloss
  styling; teatest sees the model state but not the cursor's
  visual movement between rows. Confirms the row-highlight redraw
  doesn't leave artifacts on the previous row.
- **Cost:** ~2s. No external state.

#### 3. Sidebar toggle visual width recompute

- **Trigger:** Submit a one-message turn (sidebar is suppressed on
  landing; reveals after first turn). Then send Ctrl+T twice to
  toggle off then on.
- **Assertion:** After first toggle: snapshot contains the
  conversation pane spanning the full width (no "Now" / "Agent"
  sidebar labels visible). After second toggle: sidebar labels
  return AND the conversation text wraps at the narrower width.
- **Why bridge:** sidebar toggling triggers a viewport-width
  recompute that affects how the conversation pane wraps text. A
  width-calculation regression would show as text overflow / clipping
  here in a way teatest can't see (teatest's virtual terminal is a
  fixed grid).
- **Cost:** ~3s. Needs a stub provider that produces deterministic
  output (use the existing `STADO_UAT_PROVIDER` machinery from
  `hack/tmux-uat.sh`).

#### 4. Quit-confirm popup centering at multiple widths

- **Trigger:** Set Chrome viewport to N×R, send Ctrl+D.
- **Assertion:** snapshot contains rounded-border popup
  (`╭ ─ ─ ╮ … ╰ ─ ─ ╯`), Y/N keycaps, and the popup row count
  fits within the viewport at all three widths (80×24, 120×40,
  160×50).
- **Why bridge:** centering math is layout-pinned to terminal
  dimensions; a regression in `internal/tui/quit_confirm.go`'s
  `lipgloss.Place` call would clip the popup at narrow widths.
  Teatest uses fixed dimensions and can't sweep widths cheaply.
- **Cost:** ~2s × 3 widths = 6s total. Requires
  `chromedp.EmulateViewport` calls between scenarios. No external
  state.

### P2 — medium cost (~3-8s)

#### 5. Approval drawer styling

- **Trigger:** `/tool approval_demo` (assuming
  `plugins/examples/approval-demo-go` installed via the same
  `stado plugin dev` pattern `TestBridgeE2E_Stado_RendersPanel`
  uses).
- **Assertion:** snapshot contains the warning indicator (⚠ or
  `[!]` marker per current implementation), border around the
  body block, "Allow" + "Deny" keycaps with selection contrast,
  and ↑/↓ navigation hints.
- **Why bridge:** the drawer is a layout-pinned widget that
  blends colours + box-drawing — teatest tests the
  `pluginApprovalRequestMsg` routing but doesn't see the drawer's
  rendered styling. Catches lipgloss padding regressions.
- **Cost:** ~6s (build approval-demo wasm + `plugin dev` install
  + drive). Could share build infrastructure with the panel test
  (extract a `installDemoPlugin` helper).

#### 6. Choice drawer multi-select with checkboxes

- **Trigger:** `/tool choose_demo {"multi":true,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Bravo"},{"id":"c","label":"Charlie"}]}`.
  Press `Down`, `Space`, `Down`, `Space`, `Enter`.
- **Assertion:** snapshot contains all three options, checkbox
  symbols (`[ ]` / `[x]`), the cursor mark `▶` on the current
  row, and "Space toggle / Enter confirm" hint.
- **Why bridge:** validates the F10 input-fields wiring rendered
  the multi-select layout correctly; teatest covers the response
  shape but not the visual checkbox column alignment.
- **Cost:** ~4s. Needs `choose-demo-go` installed (same shared
  helper as #5).

#### 7. Streaming + queued-prompt visual indicator

- **Trigger:** Submit message A (enters streaming). Type message B
  WITHOUT Enter — accumulates in input buffer. Type Enter while
  the stream is still going (queues B).
- **Assertion:** snapshot during streaming contains: status-bar
  "streaming" tone in the running area; input box shows the
  queued-prompt visual marker ("queued: <text>" tag in muted
  tone or similar).
- **Why bridge:** teatest's
  `TestQueuedPrompt_EnterWhileStreamingQueues` validates the
  state set; bridge proves the operator can actually see it
  on-screen as a visible tag (regression where the tag is
  computed but never reaches the renderer).
- **Cost:** ~5s. Needs deterministic stub provider; the
  `STADO_UAT_PROVIDER=stado-uat-none` env from
  `hack/tmux-uat.sh` already does this.

### P3 — lower priority (skip unless touching that surface)

- **Compaction-pending block visual.** Worth doing if compaction
  rendering changes; cost ~10s (needs to trigger compaction via
  budget config or `/memory compact`). Skipping unless we touch
  `model_render.go`'s compaction path.
- **Persona picker / Fleet picker.** Same shape as theme picker;
  one bridge test (#2 theme) suffices as the pattern.
- **Inline slash suggestions navigation.** Already drafted as
  scenario 4 in `TestBridgeE2E_Stado_F9bRegression`'s comment
  block — dropped because xterm.js redraw timing made the
  predicate flaky. Better covered by teatest
  (`TestUAT_SlashOpensInlineSuggestions`) which doesn't fight
  the same timing.

## Surfaces explicitly out of scope for bridge UAT

These are well-covered elsewhere; adding bridge tests for them
would burn time without catching anything new:

- **Block expansion / focus markers** — pure text-state assertions.
  teatest is the right tool.
- **Plan/Do mode toggle** — tmux-uat.sh::cmd_mode_toggle covers it;
  visual update is just `Do ·` ↔ `Plan ·` in the status bar.
- **Conversation block kinds** (user/assistant/tool/thinking) —
  rendering is straight text. The render_demo panel test
  exercises the block-rendering path enough.
- **Input-box editor primitives** (cursor movement, multi-line
  shift+Enter, paste) — `internal/tui/input/` package has its
  own test suite; bridge offers no new signal.
- **Status-bar token / cost display** — values come from session
  state; teatest covers the formatting.

## Shared infrastructure to add (not yet implemented)

When the new scenarios start landing, extract these helpers from
`bridge_uat_test.go` so per-scenario tests stay focused on assertions:

```go
// waitForSnapshot polls window.bridge.snapshot() until the predicate
// returns truthy or the deadline elapses. Returns the matched snapshot
// for further inspection.
func waitForSnapshot(ctx context.Context, t *testing.T, predicate string,
    timeout time.Duration) (snapshot string, err error)

// installDemoPlugin builds + signs + installs a plugins/examples/<name>
// plugin into the test process's XDG via `stado plugin dev`. Returns
// once `tool list` shows the registered tool. Used by render-demo,
// approval-demo, choose-demo, etc.
func installDemoPlugin(t *testing.T, stadoBin, demoName string)

// connectStado is the boilerplate that fills the bridge's cmd field
// and clicks connect. Not pulled out yet because each test slightly
// varies its initial wait predicate; revisit if the third caller
// arrives.
```

The `installDemoPlugin` helper is the highest-leverage one — three
of the proposed scenarios need it.

## Cost summary

| Phase | Scenarios | Total walltime |
|-------|-----------|----------------|
| P1 (cheap, high signal) | 1-4 | ~12s |
| P2 (medium, demo-plugin install) | 5-7 | ~15s |
| P3 (touch-only) | (skip default) | — |

Adding all P1+P2 to the existing 5 takes the bridge suite from
~19s to ~46s. Manageable as a `go test`-time gate (still under
60s); too long for every-PR CI without the existing
`STADO_PTY_BRIDGE_E2E=1` opt-in.

## Implementation order (when revived)

1. Extract `waitForSnapshot` from the existing `pollEval` so
   subsequent tests can match on the snapshot string itself, not
   a predicate-bool.
2. Extract `installDemoPlugin` from
   `TestBridgeE2E_Stado_RendersPanel`.
3. Land scenarios 1 (help overlay) + 2 (theme picker) — pure
   keypress drives, no plugin needed.
4. Land scenario 3 (sidebar toggle) — needs the
   `STADO_UAT_PROVIDER` stub provider env from `hack/tmux-uat.sh`.
5. Land scenario 4 (quit-confirm centering) — needs
   `chromedp.EmulateViewport`.
6. Land scenarios 5-7 — needs the demo-plugin helper from step 2.
