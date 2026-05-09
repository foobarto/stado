# stado-pty-bridge — `hack/pty-bridge/`

Dev-only tool: spawn a child process under a PTY, expose its terminal
I/O to a browser tab over WebSocket, render with xterm.js. Lets a
headless-Chrome-driving agent (chromedp / puppeteer / playwright /
Claude-in-Chrome) "see" a real terminal session for autonomous TUI
testing.

Lives under `hack/` (operator-facing dev tool, not part of the stado
binary surface) with its own go.mod so chromedp + gorilla/websocket
stay out of the main module's `go.sum`. Same isolation pattern as
`plugins/examples/*`.

Companion to the in-tree UAT options:

- **`hack/tmux-uat.sh`** — real PTY via tmux + grep. Fast, no browser
  required, but assertions are limited to whatever you can detect by
  rendered-text substring.
- **`internal/tui/uat_*_test.go`** — in-process `teatest` integration
  against the bubbletea `Update` loop. Fastest, runs in `go test
  ./...`, but can't see ANSI escape codes or terminal redraws.
- **This bridge** — full visual rendering through `xterm.js` in real
  Chrome via `chromedp`. Catches escape-code regressions, real-
  terminal-width layout breaks, and rendering timing issues the
  other two surfaces can't see. Costs a Chrome dependency and ~3-15s
  per scenario.

## Threat model

The bridge spawns arbitrary processes with the operator's UID. It
binds to loopback (`127.0.0.1`) by default and requires a bearer
token on every HTTP and WebSocket request. The token is generated
fresh at startup (32 bytes from `crypto/rand`, hex-encoded) and
printed to stdout in a one-line URL the operator pastes into the
browser.

Constant-time comparison protects against timing oracles. The
WebSocket upgrade additionally rejects cross-origin connections
(only `127.0.0.1` / `localhost` / `::1` origins accepted) so a
malicious page on another origin can't hijack the bridge via DNS
rebinding even if it learns the URL somehow.

**Not for production. No TLS, no audit log, no per-spawn
authorization.** Treat the auth token as cabin keys: if anyone
gets it they're inside.

## Build + run

```bash
cd hack/pty-bridge
go build -o pty-bridge .
./pty-bridge -addr 127.0.0.1:7878
# stdout prints:
#   open this URL in your browser:
#       http://127.0.0.1:7878/?token=<64 hex chars>
```

Open the printed URL in any browser. The page lets you pick a
command (default: `stado`), optional args, optional cwd, and
click "connect" to open a WebSocket and spawn the process under a
PTY.

For a fixed token (CI / scripted use): `-token <hex>`.

## Headless-Chrome E2E tests

`bridge_uat_test.go` drives a real headless Chrome via `chromedp`
to assert end-to-end. Quick start:

```bash
# Build stado first so the tests can drive it
go build -o /tmp/stado ./cmd/stado    # from repo root

# Run all bridge tests (~76s end-to-end)
cd hack/pty-bridge
STADO_PTY_BRIDGE_E2E=1 STADO_BIN=/tmp/stado go test -timeout 300s

# Run a single scenario
STADO_PTY_BRIDGE_E2E=1 STADO_BIN=/tmp/stado \
  go test -v -run TestBridgeE2E_Stado_RendersPanel
```

The tests:

1. Start the bridge in-process on an ephemeral loopback port.
2. Launch headless Chrome with `--user-data-dir` under `~/Downloads/`
   (xdg-download is whitelisted in the Chrome Flatpak's
   `filesystems=` context — `/tmp` isn't). On non-Flatpak Chrome
   the path is just unused scratch.
3. Navigate Chrome to the bridge URL with the token.
4. Click connect, send keystrokes, poll
   `window.bridge.snapshot()` against per-test predicates.
5. Assert the predicate matches within a timeout.

The catalogue of scenarios + their per-test cost lives in
[`TEST-PLAN.md`](TEST-PLAN.md). At time of writing: 14 top-level
test functions / 24 sub-scenarios, ~76s walltime end-to-end.

Skips when `STADO_PTY_BRIDGE_E2E` is unset OR no Chrome binary
is findable — the package's `go test` stays fast and offline
by default.

### Pre-requisites for running the tests

- A `STADO_BIN` env var pointing at a built `stado` binary
  (or `stado` on `$PATH`).
- A Chrome / Chromium binary at `~/.local/bin/chrome`, OR set
  `STADO_PTY_BRIDGE_CHROME=/path/to/chrome`.
- For the demo-plugin scenarios (render / approval / choice
  drawer): `GOOS=wasip1 GOARCH=wasm go build` toolchain (any
  recent Go on a host with the wasip1 target — bundled).
- For the streaming scenarios (text-delta / queued / sidebar /
  markdown / mode toggle): no extra setup — they use an
  in-process `httptest.Server` as a stub LLM provider.

## Browser-side automation API

`window.bridge` is exposed for CDP drivers:

| Method | Purpose |
|---|---|
| `bridge.connect()` | open WS + spawn the configured cmd |
| `bridge.disconnect()` | kill the child |
| `bridge.sendKeys(s)` | send `s` to the PTY (raw bytes; escape codes OK) |
| `bridge.snapshot()` | xterm.js buffer as one string — see scrollback caveat below |
| `bridge.visible()` | just the on-screen viewport rectangle |
| `bridge.cols()` / `bridge.rows()` | current size |
| `bridge.connected()` | bool |

Send keys use raw byte literals — `bridge.sendKeys("\x10")` for
Ctrl+P, `bridge.sendKeys("\x1b")` for Esc, `bridge.sendKeys("hello\r")`
to type and submit, `bridge.sendKeys("\x1b[A")` for an up arrow.

**Scrollback caveat.** `bridge.snapshot()` returns the xterm.js
buffer including whatever scrollback xterm.js has retained.
**However**, full-screen TUIs that switch to the **alternate
screen buffer** (bubbletea apps like stado, vim, htop, less)
have NO scrollback inside the alt-screen — when the application
scrolls a long panel up out of view, the older rows are gone for
good. Tests that assert content "must appear in the snapshot"
should pick markers that stay near the BOTTOM of the alt-screen
buffer at predicate-eval time (e.g. the most recent block, the
current input row, the status bar) rather than markers that are
likely to scroll off the top. The F9b panel-render test learned
this the hard way; see the comment in
`TestBridgeE2E_Stado_RendersPanel`.

### Sending printable characters from idle

stado's slash trigger
(`internal/tui/handler_input.go::245`) requires
`len(msg.Runes) == 1` for the `/` keypress to open inline
suggestions. Sending `'/foo'` as one `sendKeys` batch arrives
at bubbletea as a multi-rune paste event and fails this guard.

If you're testing slash-suggestion-style scenarios, send the
trigger key alone first (e.g. `'/'`), wait for the popup to
materialise via `waitForSnapshot`, THEN send the filter chars
as a batch (the trigger has already fired so the multi-rune
paste is fine for filtering).

Also: stado's auto-compact background plugin loads ~1s after
startup. Printable keypresses sent during that window can be
swallowed before the input handler is wired. Wait for both
"Type a message" + "ctrl+p commands" markers before sending,
plus a 1500ms settle. Control characters (Ctrl+P, Ctrl+D, etc.)
are unaffected — control-char dispatch is wired earlier in the
lifecycle.

See `TestBridgeE2E_Stado_SlashFilter` for the canonical pattern.

## Files

```
main.go              # bridge HTTP/WS server + bearer auth
index.html           # xterm.js + window.bridge JS API
bridge_uat_test.go   # headless Chrome E2E (chromedp)
TEST-PLAN.md         # scenario catalogue + cost table + future plan
go.mod / go.sum      # deps: creack/pty, gorilla/websocket, chromedp
```
