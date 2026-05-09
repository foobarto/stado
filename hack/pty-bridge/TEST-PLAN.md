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

## Existing scenarios (15 — all passing as of 2026-05-09)

| # | Test | What | Time |
|---|------|------|------|
| 1 | `TestBridgeE2E_Bash` | Bridge plumbing round-trip (no stado dep). | ~3s |
| 2 | `TestBridgeE2E_Stado` | Landing screen renders + Ctrl+P opens palette. | ~3s |
| 3 | `TestBridgeE2E_Stado_F9bRegression` | landing + Ctrl+P + Esc (3 sub-scenarios in one test). | ~3s |
| 4 | `TestBridgeE2E_Stado_RendersPanel` | Full F9b chain: build wasm → `stado plugin dev` → `/tool render_demo` → assert ASCII panel materialises. | ~3s |
| 5 | `TestBridgeE2E_StadoDebug` | Diagnostic only; dumps three timed snapshots for visual review. | ~7s |
| 6 | `TestBridgeE2E_Stado_HelpOverlay` | `/help` opens overlay; assert rounded border + ≥3 canonical slash names. | ~3s |
| 7 | `TestBridgeE2E_Stado_ThemePicker` | `/theme` opens picker; Down arrow keeps it open. | ~3s |
| 8 | `TestBridgeE2E_Stado_QuitConfirmCentering` | Ctrl+D popup centered at 80×24 / 120×40 / 160×50 (3 sub-tests). | ~9s |
| 9 | `TestBridgeE2E_Stado_ApprovalDrawer` | `/tool approval_demo` drawer with title + body + Allow + Deny labels. | ~3s |
| 10 | `TestBridgeE2E_Stado_ChoiceDrawerMultiSelect` | `/tool choose_demo` multi-select: 3 labels + checkboxes; Space toggles `[ ]` ↔ `[x]`. | ~3s |
| 11 | `TestBridgeE2E_Stado_SlashFilter` | `/sid` narrows inline suggestions to /sidebar (excludes /theme). | ~4s |
| 12 | `TestBridgeE2E_Stado_PaletteFilter` | Ctrl+P then `the` narrows palette to /theme (excludes /sidebar). | ~3s |
| 13 | `TestBridgeE2E_Stado_LandingReflow` | Bare landing reflow at 80×24 / 120×40 / 160×50 (3 sub-tests). | ~9s |

**Suite total:** 15 tests including sub-tests. Walltime ~56s
end-to-end (33s headroom against the goal's 90s budget). Opt-in
via `STADO_PTY_BRIDGE_E2E=1`.

**Surfaces validated:** landing screen + reflow, Ctrl+P + Esc
palette path, /help overlay, /theme picker, /tool slash, plugin
dev install, render panel ASCII, approval drawer, choice drawer
multi-select + checkbox toggle, slash inline suggestions, palette
filter input, viewport-driven popup centering.

**Bugs caught + fixed during the bridge UAT work:**

- `cmd/stado/plugin_use_dev.go` signed the manifest TEMPLATE
  while install reads the canonical name → empty wasm hash →
  install failed. Fixed (`d4290ee`).
- `internal/tui/model_plugins.go::runPluginToolAsync` dropped
  PrintBridge / RenderBridge / ChoiceBridge — operator-driven
  `/tool` invocations of plugins emitting via the `ui:print` /
  `ui:render` / `ui:choice` capabilities all silently dropped
  the emit. Fixed in three commits (`c9592a4` for print/render,
  `8e1a87e` for choice).

## Status update (2026-05-09)

Most P1 + P2 scenarios from this plan are now shipped — see the
"Existing scenarios" table above. The proposed-scenario sections
below are kept in place as the original brainstorming + rationale
for what got built, not as outstanding work. Status per item:

- ✅ #1 Help overlay → `TestBridgeE2E_Stado_HelpOverlay` (entry 6)
- ✅ #2 Theme picker → `TestBridgeE2E_Stado_ThemePicker` (entry 7)
- ⏸ #3 Sidebar toggle → deferred. Requires either (a) submitting
  a real prompt via a stub LLM provider to leave the landing
  screen, OR (b) finding a sidebar-toggle path that works on the
  landing screen itself (none exists today — `/sidebar` toggles
  but on the landing the right pane is suppressed regardless).
  Not shipping unless the operator wants the stub-provider work.
- ✅ #4 Quit-confirm centering → `TestBridgeE2E_Stado_QuitConfirmCentering` (entry 8)
- ✅ #5 Approval drawer → `TestBridgeE2E_Stado_ApprovalDrawer` (entry 9)
- ✅ #6 Choice drawer multi-select → `TestBridgeE2E_Stado_ChoiceDrawerMultiSelect` (entry 10)
- ⏸ #7 Streaming + queued-prompt → deferred (stub-provider gap)

Also shipped beyond the original P1 + P2:

- ✅ Slash filter → `TestBridgeE2E_Stado_SlashFilter` (entry 11) —
  the canonical test the original `slash-opens-inline-suggestions`
  scenario in `TestBridgeE2E_Stado_F9bRegression` was dropped
  because of timing flake. Resolved by sending `/` alone first
  and using `waitForSnapshot` to settle before typing filter
  chars; root cause traced to handler_input.go's
  `len(msg.Runes) == 1` guard.
- ✅ Palette filter → `TestBridgeE2E_Stado_PaletteFilter` (entry 12)
- ✅ Landing reflow at 3 viewports → `TestBridgeE2E_Stado_LandingReflow`
  (entry 13)

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

## Deferred: surfaces that need a stub LLM provider

Three surfaces have genuine bridge-UAT value but can't be tested
without a deterministic LLM that emits responses (real or stub).
The goal's Non-goals declared a stub provider as "build a tiny
one if needed"; building it is itself substantial scope and was
deferred at AC4 to keep the suite shippable.

What would unblock them:

- Either a `STADO_DEFAULTS_PROVIDER=stub` value that emits a
  configurable fixed response (synchronous and / or streaming
  with text-delta timing).
- Or a localhost OAI-compat HTTP mock the bridge tests start
  in-process before launching stado (similar pattern to the
  bridge starting in-process — `httptest.Server` with a stub
  handler).

The deferred surfaces:

| Gap | Trigger | Why bridge | Why deferred |
|-----|---------|------------|--------------|
| Streaming text-delta visual | submit a prompt; bytes arrive over time and the assistant block ticks them in | Live tick rendering is bridge-only — teatest sees the final state, not the per-frame delta growth | Needs stub provider that emits chunked deltas with realistic timing |
| Markdown rendering in assistant blocks | submit a prompt that produces `# Heading` + ```code``` blocks | glamour renders to real terminal escape codes; bridge sees the styled output, teatest doesn't | Same — needs the assistant block to actually receive markdown content from a provider |
| Plan/Do mode border tint | Tab toggles mode | Border-color difference (yellow vs green) is the only visual signal; assertion needs colour decoding from snapshot or a sentinel test plugin | Mode toggle needs the input box to be in a specific state; visual-color assertion via xterm.js snapshot needs investigation (snapshot returns plain text by default) |
| Sidebar toggle width recompute | submit a prompt → reveal sidebar; Ctrl+T toggles | Width recompute affects conversation-pane wrap | Needs stub provider to leave landing |
| Queued-prompt visual marker | submit during streaming → second submit queues with visible "queued: …" tag | Tag visibility is bridge-only | Needs stub provider |

These are documented here rather than left as silent gaps so the
next operator who picks this up has the unblock path written
down. If the stub provider lands, all five become 1-2 hour tests.

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
