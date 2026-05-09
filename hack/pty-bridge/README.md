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
cd ~/Dokumenty/stado-pty-bridge
go build -o stado-pty-bridge .
./stado-pty-bridge -addr 127.0.0.1:7878
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
to assert end-to-end:

```bash
STADO_PTY_BRIDGE_E2E=1 go test -v -run TestBridgeE2E_Bash
STADO_PTY_BRIDGE_E2E=1 STADO_BIN=/path/to/stado go test -v -run TestBridgeE2E_Stado
```

The tests:

1. Start the bridge in-process on an ephemeral loopback port.
2. Launch headless Chrome with `--user-data-dir` under `~/Downloads/`
   (xdg-download is whitelisted in the Chrome Flatpak's
   `filesystems=` context — `/tmp` isn't). On non-Flatpak Chrome
   the path is just unused scratch.
3. Navigate Chrome to the bridge URL with the token.
4. Click connect, send keystrokes, snapshot the rendered terminal
   via `window.bridge.snapshot()`.
5. Assert the expected text appears.

`TestBridgeE2E_Bash` validates the round-trip plumbing without
depending on stado being built. `TestBridgeE2E_Stado` confirms
stado renders its landing screen and that Ctrl+P opens the
command palette.

Skips when `STADO_PTY_BRIDGE_E2E` is unset — the package's `go test`
stays fast and offline by default.

## Browser-side automation API

`window.bridge` is exposed for CDP drivers:

| Method | Purpose |
|---|---|
| `bridge.connect()` | open WS + spawn the configured cmd |
| `bridge.disconnect()` | kill the child |
| `bridge.sendKeys(s)` | send `s` to the PTY (raw bytes; escape codes OK) |
| `bridge.snapshot()` | full xterm buffer (incl. scrollback) as one string |
| `bridge.visible()` | just the on-screen viewport rectangle |
| `bridge.cols()` / `bridge.rows()` | current size |
| `bridge.connected()` | bool |

Send keys use raw byte literals — `bridge.sendKeys("\x10")` for
Ctrl+P, `bridge.sendKeys("\x1b")` for Esc, `bridge.sendKeys("hello\r")`
to type and submit, `bridge.sendKeys("\x1b[A")` for an up arrow.

## Files

```
main.go              # bridge HTTP/WS server + bearer auth
index.html           # xterm.js + window.bridge JS API
bridge_uat_test.go   # headless Chrome E2E (chromedp)
go.mod / go.sum      # deps: creack/pty, gorilla/websocket, chromedp
```
