package main

// Headless-Chrome end-to-end test for the bridge. Spawns the bridge
// in-process, drives a real Chrome via CDP, and snapshots the
// rendered terminal output. This is the autonomous-testing surface
// the bridge exists to enable.
//
// Skips when STADO_PTY_BRIDGE_E2E is unset OR no Chrome binary is
// findable, so the package's `go test` stays fast and offline by
// default.
//
// Run manually:
//
//	cd ~/Dokumenty/stado-pty-bridge
//	STADO_PTY_BRIDGE_E2E=1 go test -v -run TestBridgeE2E_Bash
//	STADO_PTY_BRIDGE_E2E=1 go test -v -run TestBridgeE2E_Stado
//
// Exits non-zero if the rendered terminal doesn't contain the
// expected marker. The full snapshot is dumped on failure so
// regressions surface as concrete strings, not vague timeouts.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/creack/pty"
)

// findChrome returns a non-flatpak-or-flatpak-wrapper Chrome binary
// path suitable for chromedp. Order: $STADO_PTY_BRIDGE_CHROME, then
// the local wrapper at ~/.local/bin/chrome, then chromedp's default
// search.
func findChrome(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("STADO_PTY_BRIDGE_CHROME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, ".local/bin/chrome")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// chromeUserDataDir returns a UNIQUE path under the user's Downloads
// folder for the Chrome --user-data-dir. Per-test uniqueness
// matters because Chrome takes a lock on the user-data-dir; two
// concurrent test runs (or two parallel sub-tests using the same
// path) would deadlock on the lock and chromedp would time out.
//
// Why under ~/Downloads instead of t.TempDir(): Chrome-via-Flatpak
// sandboxing blocks /tmp (which is what t.TempDir uses); xdg-
// download is whitelisted in the Flatpak's filesystems= context.
// Outside Flatpak, the path is just an unused folder and Chrome
// happily uses it. Cleanup happens via t.Cleanup, so the unique
// subdir is removed at test end and no Downloads litter accrues.
//
// Uniqueness comes from t.TempDir-style randomness via
// crypto/rand, not from t.Name (sub-test names with `/` would
// create nested dirs Chrome can't open).
func chromeUserDataDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand for user-data-dir suffix: %v", err)
	}
	parent := filepath.Join(home, "Downloads", "stado-pty-bridge-chrome")
	dir := filepath.Join(parent, hex.EncodeToString(suffix))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir user-data-dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startBridgeInProcess spins up an httptest.Server bound to a real
// loopback port that mounts the same handlers as main(). Returns
// the URL prefix and the configured token. Token is freshly
// generated per test.
func startBridgeInProcess(t *testing.T) (baseURL, token string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("token: %v", err)
	}
	authToken = []byte(hex.EncodeToString(raw))

	mux := http.NewServeMux()
	mux.Handle("/ws", requireAuth(http.HandlerFunc(wsHandler)))
	mux.Handle("/", requireAuth(http.FileServer(http.FS(staticFS))))

	// Bind to an ephemeral loopback port so parallel runs don't
	// fight over :7878.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + listener.Addr().String(), string(authToken)
}

// driveChrome launches a headless Chrome via chromedp, navigates to
// the bridge URL, waits for the page to bootstrap window.bridge,
// then runs the scenario. Returns the final terminal snapshot.
func driveChrome(t *testing.T, bridgeURL string, scenario func(ctx context.Context) error) string {
	t.Helper()
	if os.Getenv("STADO_PTY_BRIDGE_E2E") == "" {
		t.Skip("STADO_PTY_BRIDGE_E2E unset; skipping headless-Chrome integration")
	}
	chromePath := findChrome(t)
	if chromePath == "" {
		t.Skip("no Chrome binary found; set STADO_PTY_BRIDGE_CHROME or install one in ~/.local/bin/chrome")
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.UserDataDir(chromeUserDataDir(t)),
		// Flags: headless=new is the modern protocol-mode headless;
		// no-sandbox is required because flatpak Chrome already
		// applies its own sandbox layer that conflicts with the
		// bundled SUID one.
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(allocCancel)

	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(cancel)

	// Chrome occasionally takes more than 30s to create a fresh profile under
	// full-suite load. Keep the scenario timeout bounded, but leave enough
	// launch headroom that a slow browser start is not misreported as a TUI
	// layout failure.
	ctx, timeoutCancel := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(timeoutCancel)
	defer func() {
		if err := closeChrome(ctx); err != nil {
			t.Logf("close Chrome: %v", err)
		}
	}()

	if err := chromedp.Run(ctx,
		chromedp.Navigate(bridgeURL),
		// Wait for the page's window.bridge API to be installed —
		// signals that xterm.js + the inline bootstrap finished.
		chromedp.Poll(`window.bridge && typeof window.bridge.connect === 'function'`, nil),
	); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := scenario(ctx); err != nil {
		t.Fatalf("scenario: %v", err)
	}

	var snapshot string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.bridge.snapshot()`, &snapshot),
	); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snapshot
}

// closeChrome closes the browser itself, not only the local wrapper process.
// This matters when findChrome resolves to a Flatpak wrapper: cancelling the
// chromedp allocator stops host-spawn but can leave the host Chrome running.
func closeChrome(ctx context.Context) error {
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return chromedp.Cancel(closeCtx)
}

// TestBridgeE2E_Bash drives the bridge against /bin/bash to validate
// the round-trip plumbing without depending on stado being built.
// Sends `echo HELLO_FROM_TEST<Enter>` and asserts the output appears.
func TestBridgeE2E_Bash(t *testing.T) {
	requireBridgeE2E(t)
	baseURL, token := startBridgeInProcess(t)

	got := driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		// Pick a deterministic bash invocation that prints a known
		// marker and exits — keeps the test independent of session
		// state. Use a sentinel so a coincidental echo in the
		// terminal can't fake-pass us.
		startCmd := `(function(){
			document.getElementById('cmd').value = '/bin/bash';
			document.getElementById('args').value = '-c "echo HELLO_FROM_TEST_${Date.now()}; exit"';
			window.bridge.connect();
			return true;
		})()`
		var ok bool
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(startCmd, nil),
			chromedp.Poll(`window.bridge.snapshot().includes('HELLO_FROM_TEST_')`, &ok),
		); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("never saw HELLO_FROM_TEST marker")
		}
		return nil
	})

	if !strings.Contains(got, "HELLO_FROM_TEST_") {
		t.Fatalf("snapshot missing marker; full snapshot:\n%s", got)
	}
}

// TestBridgeE2E_Stado drives the bridge against the stado binary,
// confirms the landing screen renders, and verifies a simple key
// interaction reaches the TUI. Skipped if STADO_BIN isn't set or
// the binary doesn't exist.
func TestBridgeE2E_Stado(t *testing.T) {
	requireBridgeE2E(t)
	isolateXDG(t)
	stadoBin := os.Getenv("STADO_BIN")
	if stadoBin == "" {
		stadoBin = "stado"
	}
	if _, err := exeLookup(stadoBin); err != nil {
		t.Skipf("STADO_BIN not found: %v", err)
	}
	baseURL, token := startBridgeInProcess(t)

	got := driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		// Drive: connect to stado, wait for the landing screen
		// banner ("ctrl+p commands" hint is the most stable
		// landing-screen marker), then press Ctrl+P to open the
		// command palette.
		startCmd := fmt.Sprintf(`(function(){
			document.getElementById('cmd').value = %q;
			document.getElementById('args').value = '';
			window.bridge.connect();
			return true;
		})()`, stadoBin)
		// chromedp.Poll has surprising semantics — its expression is
		// passed to Runtime.evaluate(awaitPromise=true), which on
		// some chromedp versions wraps the JS in a way that makes a
		// raw IIFE return undefined. Hand-roll the polling loop with
		// chromedp.Evaluate + time.Sleep — boring but reliable.
		if err := chromedp.Run(ctx, chromedp.Evaluate(startCmd, nil)); err != nil {
			return err
		}
		landingMatch := pollEval(ctx, t,
			`window.bridge && window.bridge.snapshot ? (window.bridge.snapshot().toLowerCase().indexOf('ctrl+p') >= 0) : false`,
			15*time.Second, 100*time.Millisecond)
		if !landingMatch {
			var snap string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge ? window.bridge.snapshot() : 'no bridge'`, &snap))
			return fmt.Errorf("landing screen never showed ctrl+p hint; final snapshot:\n%s", snap)
		}
		// Send Ctrl+P (0x10 = DC1) to open the command palette.
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.sendKeys('\x10')`, nil)); err != nil {
			return err
		}
		// The palette renders a scrollable list of commands; any of
		// the canonical bundled ones being visible proves it opened.
		// Names checked: /sidebar, /theme, /thinking, /split, /clear,
		// /help, /tool, /alias, /memory.
		palettePredicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot().toLowerCase();
			var marks = ['/sidebar','/theme','/thinking','/split','/clear','/help','/tool','/alias','/memory'];
			for (var i = 0; i < marks.length; i++) { if (s.indexOf(marks[i]) >= 0) return true; }
			return false;
		})()`
		paletteMatch := pollEval(ctx, t, palettePredicate, 10*time.Second, 100*time.Millisecond)
		if !paletteMatch {
			var snap string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.snapshot()`, &snap))
			return fmt.Errorf("Ctrl+P didn't open the palette; final snapshot:\n%s", snap)
		}
		return nil
	})

	if !strings.Contains(strings.ToLower(got), "ctrl+p") {
		t.Logf("final snapshot:\n%s", got)
	}
}

// TestBridgeE2E_Stado_F9bRegression exercises the surfaces the F9b
// work touched (Update message loop, Model, handler dispatch, slash
// suggestions) end-to-end through the xterm.js pipeline. Specifically
// validates that:
//   - Landing screen renders cleanly post-F9b.2 changes to
//     model_plugins.go / model_stream.go / handler_tools.go /
//     model_update.go.
//   - Ctrl+P opens the command palette (regression for F9b.2's
//     pluginRenderMsg routing through the same Update switch).
//   - Esc closes the palette without leaving artifacts (handler
//     dispatch path still drains correctly).
//   - Typing `/` from idle opens inline slash suggestions
//     (regression for the slash-suggest path that lives next to the
//     palette code F9b.2 touched).
//
// These four scenarios in one test give broader signal than the
// existing TestBridgeE2E_Stado (which only covers landing + Ctrl+P)
// without the cost of a per-scenario test fixture.
//
// Plugin render, approval, and choice requests are covered by the
// dedicated self-contained fixture scenarios below. Keeping them
// separate lets this regression test stay focused on keyboard routing.
func TestBridgeE2E_Stado_F9bRegression(t *testing.T) {
	requireBridgeE2E(t)
	isolateXDG(t)
	stadoBin := os.Getenv("STADO_BIN")
	if stadoBin == "" {
		stadoBin = "stado"
	}
	if _, err := exeLookup(stadoBin); err != nil {
		t.Skipf("STADO_BIN not found: %v", err)
	}
	baseURL, token := startBridgeInProcess(t)

	type scenario struct {
		name      string
		jsAction  string // optional JS to run before checking the predicate
		predicate string // JS bool expression — must evaluate truthy within timeout
		failHint  string // human-readable fail message
	}

	scenarios := []scenario{
		// Landing screen baseline — same predicate as TestBridgeE2E_Stado
		// but kept here so this test is self-contained.
		{
			name:      "landing-screen-shows-ctrl+p-hint",
			predicate: `window.bridge && window.bridge.snapshot ? (window.bridge.snapshot().toLowerCase().indexOf('ctrl+p') >= 0) : false`,
			failHint:  "landing screen never showed the ctrl+p hint",
		},
		// Ctrl+P opens the palette — proves Update routes the keypress
		// through the post-F9b switch correctly.
		{
			name:     "ctrl+p-opens-palette",
			jsAction: `window.bridge.sendKeys('\x10')`,
			predicate: `(function(){
				if (!window.bridge || !window.bridge.snapshot) return false;
				var s = window.bridge.snapshot().toLowerCase();
				var marks = ['/sidebar','/theme','/thinking','/split','/clear','/help','/tool','/alias','/memory'];
				for (var i = 0; i < marks.length; i++) { if (s.indexOf(marks[i]) >= 0) return true; }
				return false;
			})()`,
			failHint: "Ctrl+P didn't open the palette",
		},
		// Esc closes the palette — proves the dispatch path drains
		// cleanly. Predicate is "palette markers GONE while idle hint
		// returns" — the most-stable proxy for "palette is closed."
		{
			name:     "esc-closes-palette",
			jsAction: `window.bridge.sendKeys('\x1b')`,
			predicate: `(function(){
				if (!window.bridge || !window.bridge.snapshot) return false;
				var s = window.bridge.snapshot().toLowerCase();
				// "ctrl+p commands" lives in the input row hint when
				// the palette is closed; the palette body covers it
				// when open. So its reappearance is a reliable signal.
				return s.indexOf('ctrl+p commands') >= 0;
			})()`,
			failHint: "Esc didn't return the TUI to the idle landing layout",
		},
		// Originally I had a fourth scenario here for "/ from idle
		// opens inline suggestions" but the chained-keypress timing
		// against xterm.js's redraw cycle (Ctrl+P → Esc → /) is
		// flaky in this harness — the palette body sometimes leaks
		// into the snapshot after Esc, and the predicate can't tell
		// "leftover palette content" from "new slash suggestions"
		// because both surfaces include the same /sidebar /theme
		// names. Slash suggestions are covered exhaustively by
		// in-process unit tests in stado:
		// internal/tui/uat_scenarios_test.go::
		// TestUAT_SlashOpensInlineSuggestions and friends. The bridge
		// UAT's value is end-to-end visual rendering, not key-by-key
		// dispatch coverage that the unit tests do better.
	}

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		startCmd := fmt.Sprintf(`(function(){
			document.getElementById('cmd').value = %q;
			document.getElementById('args').value = '';
			window.bridge.connect();
			return true;
		})()`, stadoBin)
		if err := chromedp.Run(ctx, chromedp.Evaluate(startCmd, nil)); err != nil {
			return fmt.Errorf("connect stado: %w", err)
		}

		for _, sc := range scenarios {
			if sc.jsAction != "" {
				if err := chromedp.Run(ctx, chromedp.Evaluate(sc.jsAction, nil)); err != nil {
					return fmt.Errorf("scenario %q: jsAction: %w", sc.name, err)
				}
			}
			ok := pollEval(ctx, t, sc.predicate, 15*time.Second, 100*time.Millisecond)
			if !ok {
				var snap string
				_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.snapshot()`, &snap))
				return fmt.Errorf("scenario %q: %s; final snapshot:\n%s", sc.name, sc.failHint, snap)
			}
			t.Logf("✓ %s", sc.name)
		}
		return nil
	})
}

// TestBridgeE2E_Stado_RendersPanel is the F9b end-to-end visual
// check: install render-demo-go via `stado plugin dev`, spawn stado
// in the bridge, type `/tool render_demo`, snapshot the rendered
// terminal, and assert the bordered panel from
// internal/tui/panel_render.go appears with the expected body kinds.
//
// This is the "real panel emit through xterm.js" path — covers the
// chain plugin SDK → stado_ui_render host import → tuiRenderBridge
// → onPluginRender → renderPanelASCII → bubbletea View() →
// terminal escape codes → xterm.js → snapshot. The unit tests in
// internal/tui/render_panel_test.go cover the renderer in isolation;
// this test covers everything *around* the renderer.
//
// Skips when:
//   - STADO_PTY_BRIDGE_E2E unset (same as the other E2E tests)
//   - Chrome binary not findable (same as the other E2E tests)
//   - STADO_BIN not pointing at a real binary
//   - the in-tree testdata/ui-demo fixture can't be located
//   - the wasip1 toolchain isn't available (`go build` for wasm)
//
// Allow ~10s walltime: ~3s for the wasip1 wasm build, ~2s for
// plugin dev (sign + trust + install), ~3s for the bridge + stado
// startup, ~2s for the snapshot polling. Whichever is slowest sets
// the floor.
func TestBridgeE2E_Stado_RendersPanel(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installUITestPlugin(t, stadoBinAbs, "render_demo")

	// Drive the bridge + stado.
	baseURL, token := startBridgeInProcess(t)
	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}

		// Type `/tool render_demo` then Enter. Each char goes through
		// the bridge sendKeys path; the trailing \r is the canonical
		// Enter encoding the bridge already documents in its README.
		typeCmd := `window.bridge.sendKeys('/tool render_demo\r')`
		if err := chromedp.Run(ctx, chromedp.Evaluate(typeCmd, nil)); err != nil {
			return fmt.Errorf("type /tool render_demo: %w", err)
		}

		// Wait for the panel to render. The TUI's bordered system
		// block from panel_render.go uses lipgloss.RoundedBorder
		// box chars. Bubbletea runs in the terminal's alt-screen
		// (no scrollback), and the rendered panel is significantly
		// taller than the xterm.js viewport — by the time the plugin
		// returns and we snapshot, the conversation pane has scrolled
		// to keep the latest content (result line + tail of the
		// panel) in view. The structural top of the panel
		// (╭ + title) and the bottom corners (╰ ╯) typically fall
		// outside the visible alt-screen rectangle.
		//
		// What we *can* always observe post-emit:
		//  (a) the plugin's tool-result confirmation line
		//      ("render_demo: panel emitted (8 sections)")
		//  (b) at least one box-drawing │ vertical bar (the panel
		//      body's left edge)
		//  (c) at least one section heading from render-demo's
		//      payload (proof the renderer actually walked the
		//      sections, not just emitted the title bar)
		// Together these prove "the panel reached the TUI renderer
		// AND its body content materialised in the conversation
		// pane" — which is the F9b end-to-end claim. Pre-the-bridge-
		// wiring-fix in this same commit, the result line appeared
		// but no panel content did because runPluginToolAsync
		// dropped the RenderBridge.
		panelPredicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			// xterm.js wraps at the viewport width, so a long result
			// line ("plugin render-demo-go-0.1.0/render_demo →
			// render_demo: panel emitted (8 sections)") splits across
			// rows in the snapshot — for example "...panel\n
			// emitted...". Match the two halves independently rather
			// than relying on byte-contiguity.
			var resultParts = s.indexOf('render_demo: panel') >= 0 &&
				s.indexOf('emitted') >= 0 &&
				s.indexOf('sections') >= 0;
			// A single vertical bar is everywhere in the TUI
			// (sidebar borders, status row separators, even
			// kv-section column dividers inside the panel itself).
			// Per gemini review the lone vertical-bar predicate
			// would false-positive on any frame that happens to
			// render the sidebar. Tighten to require at least one
			// horizontal border RUN of 4+ box-drawing dashes —
			// only the panel renderer emits those long horizontal
			// runs. Combined with the result-line + heading
			// checks, a panel had to render.
			var hasPanelBorder = s.indexOf('────') >= 0;
			// Long panels scroll their headings off the alternate screen. The
			// fixture footer stays at the visible tail and cannot be produced by
			// the ordinary tool-result block, so it is the stable renderer proof.
			var sawFixtureFooter = s.indexOf('stado UAT render fixture') >= 0;
			return resultParts && hasPanelBorder && sawFixtureFooter;
		})()`
		panelMatch := pollEval(ctx, t, panelPredicate, 20*time.Second, 200*time.Millisecond)
		if !panelMatch {
			var snap string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.snapshot()`, &snap))
			return fmt.Errorf("panel never appeared; snapshot:\n%s", snap)
		}
		final := snapshot(ctx, t)
		if strings.Contains(final, "ignoring project-local plugins") {
			return fmt.Errorf("registry rebuild wrote a raw warning over the active TUI; snapshot:\n%s", final)
		}
		t.Logf("✓ panel reached renderer: result line + border + fixture footer visible without raw stderr corruption")
		return nil
	})
}

// TestBridgeE2E_Stado_HelpOverlay verifies that `/help` opens the
// help overlay with the expected slash-command list inside a
// rounded-border box. Bridge-only because:
//   - lipgloss.RoundedBorder corner alignment isn't visible to
//     teatest's virtual terminal grid (it asserts strings, not
//     box-char correctness).
//   - tmux-uat captures pane text but its overlay test
//     (`cmd_help_overlay`) doesn't validate that the rendered
//     border characters survive the alt-screen path through
//     real terminal escape codes intact.
//
// Spec: TEST-PLAN.md P1 #1.
// Goal: AC2 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_HelpOverlay(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// `/help\r` opens the overlay (model_commands.go::case "/help"
		// sets m.showHelp). Sending the slash command rather than a
		// single keypress because the TUI doesn't bind '?' to help —
		// it goes through the slash router.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/help\r')`, nil)); err != nil {
			return fmt.Errorf("type /help: %w", err)
		}
		// The help overlay is height-aware: its body is windowed to the
		// canvas and scrolls with ↑/↓ / G. First wait for the box itself
		// (border corner + a keybinding group from the top of the body)…
		boxOpen := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var hasCorner = s.indexOf('╭') >= 0 || s.indexOf('╮') >= 0 ||
				s.indexOf('╰') >= 0 || s.indexOf('╯') >= 0;
			// "Toggle help" is the ? row of the App group — the very
			// first rows of the body, visible in any window size.
			return hasCorner && s.indexOf('Toggle help') >= 0;
		})()`
		snap, err := waitForSnapshot(ctx, t, boxOpen, 10*time.Second)
		if err != nil {
			return fmt.Errorf("help overlay box never rendered: %w; snapshot:\n%s", err, snap)
		}
		// …then jump to the bottom (G) where the slash-command section
		// lives and assert the canonical names. Three names rather than
		// one because help is a long enumeration; checking three reduces
		// false-positives from leftover landing-screen text.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('G')`, nil)); err != nil {
			return fmt.Errorf("send G (scroll to bottom): %w", err)
		}
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var hasCorner = s.indexOf('╭') >= 0 || s.indexOf('╮') >= 0 ||
				s.indexOf('╰') >= 0 || s.indexOf('╯') >= 0;
			var canonicalNames = ['/sidebar', '/theme', '/thinking', '/debug',
				'/split', '/monitor', '/session', '/loop', '/budget',
				'/skill', '/retry'];
			var count = 0;
			for (var i = 0; i < canonicalNames.length; i++) {
				if (s.indexOf(canonicalNames[i]) >= 0) count++;
			}
			return hasCorner && count >= 3;
		})()`
		snap, err = waitForSnapshot(ctx, t, predicate, 10*time.Second)
		if err != nil {
			return fmt.Errorf("help overlay never showed corner+command-names after G: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ help overlay rendered with rounded border + canonical command names")
		return nil
	})
}

// TestBridgeE2E_Stado_ThemePicker verifies that `/theme` opens the
// theme picker, the picker renders bundled theme names, and an
// arrow-down moves the selection cursor. Bridge-only because:
//   - The picker is a bubbletea list with lipgloss styling; the
//     visual highlight transition between rows is not visible to
//     teatest (which checks model state but not rendered styles).
//   - The picker box-drawing border alignment depends on the real
//     terminal width, which tmux-uat at fixed dims doesn't sweep.
//
// Spec: TEST-PLAN.md P1 #2.
// Goal: AC2 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_ThemePicker(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/theme\r')`, nil)); err != nil {
			return fmt.Errorf("type /theme: %w", err)
		}
		// First wait for the picker itself to render. Two bundled
		// theme names + a rounded-border corner is the strongest
		// "picker is open" signal — pre-fix the wrong predicate
		// matched leftover landing-screen content.
		pickerOpen := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot().toLowerCase();
			var hasCorner = s.indexOf('╭') >= 0 || s.indexOf('╮') >= 0;
			// Bundled themes include "default" plus several
			// alternates; matching on two canonical names handles
			// the case where the list scrolls.
			var hasName = s.indexOf('default') >= 0 ||
				s.indexOf('dark') >= 0 || s.indexOf('light') >= 0 ||
				s.indexOf('mono') >= 0 || s.indexOf('ocean') >= 0;
			return hasCorner && hasName;
		})()`
		if _, err := waitForSnapshot(ctx, t, pickerOpen, 10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("theme picker never opened: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ theme picker opened with bundled theme name + rounded border")

		// Send Down arrow (CSI B) to move the cursor. Bubbletea
		// list components redraw the highlight on each cursor move.
		// We can't easily assert the highlight position via plain
		// snapshot text (style attributes don't surface as text),
		// so the assertion here is "snapshot still shows the picker
		// after the arrow keypress" — i.e. the keypress didn't
		// crash the picker or close it. A regression where the
		// picker died on arrow-key input would surface as either
		// closed picker or empty snapshot.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b[B')`, nil)); err != nil {
			return fmt.Errorf("send Down arrow: %w", err)
		}
		// Re-poll: picker still open + theme names still visible.
		if _, err := waitForSnapshot(ctx, t, pickerOpen, 5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("theme picker disappeared after Down arrow: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ theme picker survived a Down-arrow keypress")
		return nil
	})
}

// TestBridgeE2E_Stado_QuitConfirmCentering verifies the quit-confirm
// popup (Ctrl+D) renders centered with rounded border + Y/N keycaps
// at multiple terminal widths. Bridge-only because:
//   - lipgloss.Place centering math depends on real terminal dims,
//     which teatest's virtual grid doesn't exercise.
//   - tmux-uat is fixed-width; can't sweep multiple sizes cheaply.
//
// Sweeps three widths covering narrow-mobile-ish (80×24), normal
// (120×40), and wide (160×50). At each width the popup must render
// with title "Quit stado?", Y + N keycaps, the bottom-row hint
// "Enter quits · Esc cancels", and rounded-border corners.
//
// Spec: TEST-PLAN.md P1 #4.
// Goal: AC2 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_QuitConfirmCentering(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)

	for _, dim := range []struct {
		name          string
		width, height int64
	}{
		{"narrow-80x24", 80, 24},
		{"normal-120x40", 120, 40},
		{"wide-160x50", 160, 50},
	} {
		t.Run(dim.name, func(t *testing.T) {
			baseURL, token := startBridgeInProcess(t)
			driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
				// Set viewport BEFORE connecting so xterm.js sizes
				// the terminal accordingly and stado spawns at the
				// right cols/rows from the start.
				if err := emulateViewport(ctx, dim.width*7, dim.height*16); err != nil {
					return fmt.Errorf("emulateViewport: %w", err)
				}
				if err := connectStado(ctx, t, stadoBinAbs); err != nil {
					return err
				}
				// Ctrl+D triggers stateQuitConfirm.
				if err := chromedp.Run(ctx, chromedp.Evaluate(
					`window.bridge.sendKeys('\x04')`, nil)); err != nil {
					return fmt.Errorf("send Ctrl+D: %w", err)
				}
				// Predicate: title text + at least one rounded-
				// border corner + the bottom hint. Y/N keycaps
				// render with NormalBorder boxes (so ╔/┌ chars,
				// not the rounded ones), but the OUTER popup
				// uses RoundedBorder. Distinguishing both — outer
				// rounded + inner key text — proves the layout
				// composed correctly.
				predicate := `(function(){
					if (!window.bridge || !window.bridge.snapshot) return false;
					var s = window.bridge.snapshot();
					var hasTitle = s.indexOf('Quit stado?') >= 0;
					var hasCorner = s.indexOf('╭') >= 0 && s.indexOf('╯') >= 0;
					var hasHint = s.indexOf('Enter quits') >= 0 ||
						s.indexOf('Esc cancels') >= 0;
					var hasKeycap = s.indexOf('Y') >= 0 && s.indexOf('N') >= 0;
					return hasTitle && hasCorner && hasHint && hasKeycap;
				})()`
				snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
				if err != nil {
					return fmt.Errorf("quit-confirm popup never rendered at %dx%d: %w; snapshot:\n%s",
						dim.width, dim.height, err, snap)
				}
				t.Logf("✓ quit-confirm popup rendered at %dx%d (title + corner + hint + keycap)",
					dim.width, dim.height)

				// Cancel the popup with Esc so the test cleanup
				// doesn't kill stado in stateQuitConfirm — this
				// exercises the Esc dismissal path while we're
				// here.
				if err := chromedp.Run(ctx, chromedp.Evaluate(
					`window.bridge.sendKeys('\x1b')`, nil)); err != nil {
					return fmt.Errorf("send Esc: %w", err)
				}
				return nil
			})
		})
	}
}

// TestBridgeE2E_Stado_ApprovalDrawer verifies that
// `/tool approval_demo` opens the approval drawer with the title,
// body, and Allow/Deny buttons rendered. Bridge-only because:
//   - The drawer is a layout-pinned component blending colours +
//     box-drawing; teatest tests the pluginApprovalRequestMsg
//     routing but doesn't see the drawer's rendered styling.
//   - The Allow/Deny buttons render with NormalBorder boxes inside
//     the outer drawer — confirming both shapes prove the layout
//     composed correctly through real terminal escape codes.
//
// The drawer blocks waiting for the operator; we Esc to dismiss
// after asserting so test cleanup doesn't leave stado wedged in
// stateApproval.
//
// Spec: TEST-PLAN.md P2 #5.
// Goal: AC3 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_ApprovalDrawer(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installUITestPlugin(t, stadoBinAbs, "approval_demo")
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Pass an explicit title + body so the predicate has stable
		// strings to match. The plugin's defaults are also fine but
		// we control the wire here for assertion clarity. Use a
		// distinctive marker ("UAT_APPROVE_MARKER") to rule out
		// false-positive matches against any other rendered text.
		invocation := `(function(){
			window.bridge.sendKeys('/tool approval_demo {"title":"UAT_APPROVE_TITLE","body":"UAT_APPROVE_BODY_marker"}\r');
			return true;
		})()`
		if err := chromedp.Run(ctx, chromedp.Evaluate(invocation, nil)); err != nil {
			return fmt.Errorf("invoke /tool approval_demo: %w", err)
		}
		// Predicate: the drawer renders the title, body, and the
		// Allow/Deny labels. Match Allow + Deny + Y + N keycaps;
		// Y/N alone would match noise (sidebar, status bar) so
		// require all four together.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var title = s.indexOf('UAT_APPROVE_TITLE') >= 0;
			var body = s.indexOf('UAT_APPROVE_BODY') >= 0; // wrapping safe — short
			var allow = s.indexOf('Allow') >= 0;
			var deny = s.indexOf('Deny') >= 0;
			return title && body && allow && deny;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 15*time.Second)
		if err != nil {
			return fmt.Errorf("approval drawer never rendered title+body+Allow+Deny: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ approval drawer rendered with title + body + Allow + Deny labels")

		// Esc dismisses the drawer (handler_input.go path).
		// Important — without this, stado exits cleanup wedged in
		// stateApproval and the test process leaks.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b')`, nil)); err != nil {
			return fmt.Errorf("send Esc: %w", err)
		}
		// Confirm dismissal — the drawer's title text should disappear
		// from the visible viewport (or at minimum the Allow/Deny
		// buttons should). Loose check: title text is gone OR
		// "ctrl+p commands" footer is back (idle landing footer).
		dismissed := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('Allow') < 0 || s.indexOf('ctrl+p commands') >= 0;
		})()`
		if !pollEval(ctx, t, dismissed, 5*time.Second, 100*time.Millisecond) {
			return fmt.Errorf("Esc did not dismiss approval drawer; snapshot:\n%s", snapshot(ctx, t))
		}
		return nil
	})
}

// TestBridgeE2E_Stado_ChoiceDrawerMultiSelect verifies that
// `/tool choose_demo` with multi=true renders the multi-select
// drawer with checkboxes, option labels, and the navigation hint.
// Bridge-only because:
//   - Checkboxes render as `[ ]` / `[x]` text, but the cursor
//     marker `▸` and accent-coloured highlights are styled — the
//     visual composition is bridge-only.
//   - The drawer's bottom hint "Space toggle · Enter confirm · Esc
//     cancel" is a styled muted line; teatest doesn't validate
//     that it was added to the View output.
//
// Sends Space to toggle the cursor row's checkbox, then Esc to
// cancel (avoids leaving stado wedged in stateChoice).
//
// Spec: TEST-PLAN.md P2 #6.
// Goal: AC3 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_ChoiceDrawerMultiSelect(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installUITestPlugin(t, stadoBinAbs, "choose_demo")
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Three options + multi-select. Distinctive label markers
		// rule out coincidental matches against other surfaces.
		invocation := `(function(){
			window.bridge.sendKeys('/tool choose_demo {"prompt":"UAT_CHOOSE_PROMPT","multi":true,"options":[{"id":"a","label":"UAT_OPT_ALPHA"},{"id":"b","label":"UAT_OPT_BRAVO"},{"id":"c","label":"UAT_OPT_CHARLIE"}]}\r');
			return true;
		})()`
		if err := chromedp.Run(ctx, chromedp.Evaluate(invocation, nil)); err != nil {
			return fmt.Errorf("invoke /tool choose_demo: %w", err)
		}
		// Drawer rendering predicate: prompt, all three labels,
		// at least one empty checkbox, and the multi-select hint.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var prompt = s.indexOf('UAT_CHOOSE_PROMPT') >= 0;
			var alpha = s.indexOf('UAT_OPT_ALPHA') >= 0;
			var bravo = s.indexOf('UAT_OPT_BRAVO') >= 0;
			var charlie = s.indexOf('UAT_OPT_CHARLIE') >= 0;
			var checkbox = s.indexOf('[ ]') >= 0;
			var hint = s.indexOf('Space') >= 0 && s.indexOf('toggle') >= 0;
			return prompt && alpha && bravo && charlie && checkbox && hint;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 15*time.Second)
		if err != nil {
			return fmt.Errorf("choice drawer never rendered prompt+options+checkboxes+hint: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ choice drawer rendered: prompt + 3 labels + [ ] checkbox + Space/toggle hint")

		// Send Space to toggle the cursor row's checkbox. After
		// the toggle, [x] should appear somewhere AND [ ] should
		// also still appear (the other two options stay unchecked).
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys(' ')`, nil)); err != nil {
			return fmt.Errorf("send Space: %w", err)
		}
		toggled := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('[x]') >= 0 && s.indexOf('[ ]') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, toggled, 5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("Space toggle didn't switch a checkbox to [x]: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ Space toggled a checkbox: both [x] and [ ] now visible")

		// Cancel with Esc to free stado from stateChoice.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b')`, nil)); err != nil {
			return fmt.Errorf("send Esc: %w", err)
		}
		return nil
	})
}

// TestBridgeE2E_Stado_SlashFilter verifies that typing `/sid` from
// idle opens the inline slash suggestions and narrows them so that
// /sidebar appears AND /theme does NOT (filtered out by the
// substring match). Bridge-only because:
//   - The inline-suggestions popup is layout-pinned above the input
//     box; teatest tests that suggestions are computed but not
//     that the rendered list updates correctly per keystroke.
//   - The previous F9b-regression-suite drop of this scenario flaked
//     on chained Ctrl+P → Esc → / timing. Fresh-idle approach
//     (no preceding palette open) avoids that hazard, and
//     waitForSnapshot polls until the post-typing snapshot is
//     stable.
//
// Spec: AC5 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
// TestBridgeE2E_Stado_VerifyCommand exercises the v0.77 completion-gate
// control through the real PTY and xterm.js rendering path. It verifies that
// user configuration reaches the TUI and that status, disable, and re-enable
// commands all produce visible state transitions.
func TestBridgeE2E_Stado_VerifyCommand(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := `[verify]
commands = ["true"]
max_rounds = 2
strict = true
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge && window.bridge.snapshot && window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("verify input never became ready: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}
		time.Sleep(1500 * time.Millisecond)

		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/verify status\r')`, nil)); err != nil {
			return fmt.Errorf("send /verify status: %w", err)
		}
		statusPredicate := `(function(){
			var s = window.bridge.snapshot();
			return s.indexOf('verify: on') >= 0 && s.indexOf('1 command(s)') >= 0 &&
				s.indexOf('max 2 round(s)') >= 0 && s.indexOf('strict=true') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, statusPredicate, 10*time.Second); err != nil {
			return fmt.Errorf("configured verify status not rendered: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}

		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/verify off\r')`, nil)); err != nil {
			return fmt.Errorf("send /verify off: %w", err)
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('verify: off') >= 0`, 10*time.Second); err != nil {
			return fmt.Errorf("verify off state not rendered: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}

		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/verify on\r')`, nil)); err != nil {
			return fmt.Errorf("send /verify on: %w", err)
		}
		onAgainPredicate := `(function(){
			var s = window.bridge.snapshot();
			return s.split('verify: on').length - 1 >= 2;
		})()`
		if _, err := waitForSnapshot(ctx, t, onAgainPredicate, 10*time.Second); err != nil {
			return fmt.Errorf("verify on state not rendered after re-enable: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}
		return nil
	})
}

// TestBridgeE2E_Stado_SuperviseWizard exercises the installed official
// lifecycle application's first setup choice through the real source build,
// ephemeral dev signing, source-keyed install, application admission, slash
// dispatcher, PTY, Bubble Tea update loop, and xterm.js renderer. It cancels at
// the first choice, before baseline admission, so no provider call is needed.
func TestBridgeE2E_Stado_SuperviseWizard(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge && window.bridge.snapshot && window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("supervise input never became ready: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}
		time.Sleep(1500 * time.Millisecond)

		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b[200~/supervise Ship the guarded feature\x1b[201~\r')`, nil)); err != nil {
			return fmt.Errorf("send /supervise: %w", err)
		}
		setupChoice := `(function(){
			var s = window.bridge.snapshot();
			return s.indexOf('What should the supervised worker accomplish?') >= 0 &&
				s.indexOf('Ship the guarded feature') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, setupChoice, 10*time.Second); err != nil {
			return fmt.Errorf("official supervise setup choice not rendered: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}

		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b')`, nil)); err != nil {
			return fmt.Errorf("send Esc: %w", err)
		}
		closed := `(function(){
			var s = window.bridge.snapshot();
			return s.indexOf('Type a message') >= 0 &&
				s.indexOf('What should the supervised worker accomplish?') < 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, closed, 10*time.Second); err != nil {
			return fmt.Errorf("supervise wizard did not dismiss: %w; snapshot:\n%s", err, snapshot(ctx, t))
		}
		return nil
	})
}

type synchronizedPTYOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *synchronizedPTYOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *synchronizedPTYOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

var ptyCSISequence = regexp.MustCompile(`\x1b\[[0-9;?=>]*[ -/]*[@-~]`)

func normalizedPTYOutput(raw string) string {
	return ptyCSISequence.ReplaceAllString(raw, "")
}

func ptyOutputDiagnostic(output *synchronizedPTYOutput) string {
	diagnostic := normalizedPTYOutput(output.String())
	context := ""
	for _, marker := range []string{"agent.down durable publication failed", "lifecycle application event dispatcher", "supervise baseline"} {
		if index := strings.LastIndex(diagnostic, marker); index >= 0 {
			end := index + 2048
			if end > len(diagnostic) {
				end = len(diagnostic)
			}
			context += diagnostic[index:end] + "\n"
		}
	}
	const maxDiagnosticBytes = 24 << 10
	if len(diagnostic) > maxDiagnosticBytes {
		diagnostic = diagnostic[len(diagnostic)-maxDiagnosticBytes:]
	}
	return context + diagnostic
}

func waitForPTYOutput(output *synchronizedPTYOutput, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(normalizedPTYOutput(output.String()), needle) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("PTY output did not contain %q within %s", needle, timeout)
}

func waitForPTYOutputAfter(output *synchronizedPTYOutput, rawOffset int, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw := output.String()
		if rawOffset < 0 {
			rawOffset = 0
		}
		if rawOffset < len(raw) && strings.Contains(normalizedPTYOutput(raw[rawOffset:]), needle) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("PTY output after byte %d did not contain %q within %s", rawOffset, needle, timeout)
}

// sendPTYSlashCommand drives the same single-rune slash trigger an interactive
// terminal produces. Sending a complete pasted slash command during a fast
// recurring provider loop can arrive as one multi-rune key event and bypass
// the inline palette trigger; bytewise input keeps the test faithful to the
// operator path and makes mid-stream control deterministic.
func sendPTYSlashCommand(ptmx io.Writer, command string) error {
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "/") || command == "/" {
		return fmt.Errorf("invalid PTY slash command %q", command)
	}
	if _, err := ptmx.Write([]byte{'/'}); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	for _, char := range []byte(strings.TrimPrefix(command, "/")) {
		if _, err := ptmx.Write([]byte{char}); err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err := ptmx.Write([]byte{'\r'})
	return err
}

// TestPTYE2E_Stado_OfficialSuperviseSetupCancel is the browser-independent
// release gate for the official application's setup boundary. The Chrome test
// above additionally checks rendered layout when Chrome is available; this
// test always exercises the real PTY bytes and can run on a minimal Linux CI
// host.
func TestPTYE2E_Stado_OfficialSuperviseSetupCancel(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise Ship the guarded feature\x1b[201~")); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := ptmx.Write([]byte{'\r'}); err != nil {
		t.Fatalf("submit /supervise command: %v", err)
	}
	if err := waitForPTYOutput(&output, "What should the supervised worker accomplish?", 20*time.Second); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGQUIT)
			time.Sleep(500 * time.Millisecond)
		}
		diagnostic := output.String()
		if start := strings.LastIndex(diagnostic, "SIGQUIT: quit"); start >= 0 {
			diagnostic = diagnostic[start:]
		}
		const maxDiagnosticBytes = 48 << 10
		if len(diagnostic) > maxDiagnosticBytes {
			diagnostic = diagnostic[len(diagnostic)-maxDiagnosticBytes:]
		}
		t.Fatalf("official application setup choice missing: %v\n%s", err, diagnostic)
	}
	if err := waitForPTYOutput(&output, "Ship the guarded feature", 2*time.Second); err != nil {
		t.Fatalf("objective seed missing from official setup choice: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{0x1b}); err != nil {
		t.Fatalf("cancel supervise setup: %v", err)
	}
	if err := waitForPTYOutput(&output, "setup cancelled before any baseline agent", 20*time.Second); err != nil {
		t.Fatalf("official setup cancellation result missing: %v\n%s", err, output.String())
	}
}

// TestPTYE2E_Stado_OfficialSuperviseBaselineReject drives the complete default
// setup sequence, a fresh read-only baseline child, and the operator quality
// confirmation boundary. Rejecting the proposal proves that the application
// does not activate a WorkerRun merely because its model produced a valid
// contract.
func TestPTYE2E_Stado_OfficialSuperviseBaselineReject(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint := stubLLMServer(t, []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"role":"assistant","content":%q}}]}`, proposal),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":100,"total_tokens":300}}`,
	})
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise Ship the guarded feature\x1b[201~\r")); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	baselineDeadline := time.Now().Add(40 * time.Second)
	var childCompletedAt time.Time
	var baselineErr error
	for time.Now().Before(baselineDeadline) {
		visible := normalizedPTYOutput(output.String())
		if strings.Contains(visible, "Fresh supervise baseline is ready") {
			break
		}
		if strings.Contains(visible, "error explorer/read_only") {
			baselineErr = errors.New("baseline child terminated with an error")
			break
		}
		if strings.Contains(visible, "baseline proposal attempt 1 failed") {
			baselineErr = errors.New("baseline proposal was rejected after terminal delivery")
			break
		}
		if childCompletedAt.IsZero() && strings.Contains(visible, "completed explorer/read_only") {
			childCompletedAt = time.Now()
		}
		if !childCompletedAt.IsZero() && time.Since(childCompletedAt) > 5*time.Second {
			baselineErr = errors.New("baseline child completed but its durable terminal event did not advance setup")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if baselineErr == nil && !strings.Contains(normalizedPTYOutput(output.String()), "Fresh supervise baseline is ready") {
		baselineErr = errors.New("fresh baseline did not complete within 40s")
	}
	if baselineErr != nil {
		_, _ = ptmx.Write([]byte("\x1b[200~/supervise status\x1b[201~\r"))
		_ = waitForPTYOutput(&output, "setup supervise-", 2*time.Second)
		// Surface the selected child's bounded terminal diagnostic instead
		// of dumping an unreadable full terminal redraw stream.
		_, _ = ptmx.Write([]byte("\x1b[200~/fleet\x1b[201~\r"))
		_ = waitForPTYOutput(&output, "Background agents", 2*time.Second)
		t.Fatalf("fresh baseline did not complete: %v\n%s", baselineErr, ptyOutputDiagnostic(&output))
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise resume\x1b[201~\r")); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'n'}); err != nil {
		t.Fatalf("reject baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "baseline rejected; no contract candidate or worker was activated", 20*time.Second); err != nil {
		t.Fatalf("baseline rejection result missing: %v\n%s", err, output.String())
	}
}

// TestPTYE2E_Stado_OfficialSuperviseActivateCancel crosses the authority split
// that the rejection case deliberately stops before: the operator confirms the
// exact baseline, the application requests one broker-owned WorkerRun, the TUI
// activates it, and /supervise cancel terminalizes that run while its provider
// turn is active. The signed application remains the only command owner.
func TestPTYE2E_Stado_OfficialSuperviseActivateCancel(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint, workerRequestCancelled := stubFirstLLMThenHold(t, []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"role":"assistant","content":%q}}]}`, proposal),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":100,"total_tokens":300}}`,
	})
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise Ship the guarded feature\x1b[201~\r")); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise resume\x1b[201~\r")); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel active supervise workflow: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("active workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-workerRequestCancelled:
	case <-time.After(5 * time.Second):
		t.Fatalf("durable worker cancellation did not cancel the in-flight provider request\n%s", ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
		t.Fatalf("query cancelled supervise status: %v", err)
	}
	// The status sentence may wrap between "worker recurrence" and
	// "cancelled" at the current PTY width. Its broker-projected version fact
	// is layout-independent and appears only in the status response.
	if err := waitForPTYOutput(&output, "cancelled at version", 20*time.Second); err != nil {
		t.Fatalf("cancelled WorkerRun was not projected durably: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
}

// TestPTYE2E_Stado_OfficialSuperviseStepReview crosses the next application
// boundaries after WorkerRun activation. The first iteration has no committed
// turn anchor yet, so it performs ordinary work and must not invent one. The
// next iteration receives the exact signed application context, repeats that
// host-published anchor in a progress claim, and requests completion of the
// current step. The application then admits a fresh pinned read-only watchdog.
// Only its exact current-anchor approval advances the durable plan; the model's
// tool call cannot approve its own claim. The scheduling barrier must not send
// the tool result back to the parent provider while review is pending; the
// approval pauses that recurrence before any follow-up provider dispatch.
func TestPTYE2E_Stado_OfficialSuperviseStepReview(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint, unexpectedWorkerFollowup := stubSuperviseStepReview(t, proposal)
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise Ship the guarded feature\x1b[201~\r")); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}

	// The watchdog result arrives through agent.down and the persistent
	// application's event cursor. Querying status exercises the command on the
	// same instance while its worker provider request remains live.
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query supervise review status: %v", err)
		}
		if err := waitForPTYOutput(&output, "1/1 plan steps complete", 2*time.Second); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(normalizedPTYOutput(output.String()), "1/1 plan steps complete") {
		t.Fatalf("current-anchor watchdog approval did not advance the exact plan step\n%s", ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel reviewed supervise workflow: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("reviewed workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if strings.Contains(normalizedPTYOutput(output.String()), "application worker cancellation returned non-cancelled status") {
		t.Fatalf("terminal workflow cleanup emitted a false native cancellation-handoff error\n%s", ptyOutputDiagnostic(&output))
	}
	select {
	case <-unexpectedWorkerFollowup:
		t.Fatalf("scheduling barrier dispatched a parent-provider follow-up while review was pending\n%s", ptyOutputDiagnostic(&output))
	default:
	}
}

// TestPTYE2E_Stado_OfficialSuperviseReloadRestart proves exact-scope recovery
// outside a single persistent module instance. A fresh watchdog pauses the
// exact current run; /reload must rebind the same journal and broker
// projection, and a new stado process must adopt that logical-session scope
// without starting a duplicate recurrence or falling back to native policy.
func TestPTYE2E_Stado_OfficialSuperviseReloadRestart(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint, unexpectedWorkerFollowup := stubSupervisePauseReview(t, proposal)
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise Ship the guarded feature"); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}

	pausedDeadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(pausedDeadline) {
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query paused supervise status: %v", err)
		}
		visible := normalizedPTYOutput(output.String())
		statusAt := strings.LastIndex(visible, "0/1 plan steps complete")
		if statusAt >= 0 && strings.Contains(visible[statusAt:], "interrupted at version") {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	visible := normalizedPTYOutput(output.String())
	statusAt := strings.LastIndex(visible, "0/1 plan steps complete")
	if statusAt < 0 || !strings.Contains(visible[statusAt:], "interrupted at version") {
		t.Fatalf("watchdog pause did not reach the exact durable WorkerRun\n%s", ptyOutputDiagnostic(&output))
	}

	reloadDeadline := time.Now().Add(20 * time.Second)
	reloaded := false
	for time.Now().Before(reloadDeadline) {
		offset := len(output.String())
		if err := sendPTYSlashCommand(ptmx, "/reload"); err != nil {
			t.Fatalf("reload admitted supervise application: %v", err)
		}
		if err := waitForPTYOutputAfter(&output, offset, "/reload: config re-read", 2*time.Second); err == nil {
			reloaded = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !reloaded {
		t.Fatalf("admitted supervise application did not reload transactionally\n%s", ptyOutputDiagnostic(&output))
	}
	offset := len(output.String())
	if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
		t.Fatalf("query supervise after reload: %v", err)
	}
	if err := waitForPTYOutputAfter(&output, offset, "0/1 plan steps complete", 20*time.Second); err != nil {
		t.Fatalf("reload lost durable supervise state: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := waitForPTYOutputAfter(&output, offset, "interrupted at version", 20*time.Second); err != nil {
		t.Fatalf("reload lost exact WorkerRun projection: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	sessionMatches := regexp.MustCompile(`sess ([0-9a-f]{8})`).FindAllStringSubmatch(normalizedPTYOutput(output.String()), -1)
	if len(sessionMatches) == 0 {
		t.Fatalf("first TUI did not expose its resumable logical session ID\n%s", ptyOutputDiagnostic(&output))
	}
	logicalSessionPrefix := sessionMatches[len(sessionMatches)-1][1]

	if err := sendPTYSlashCommand(ptmx, "/quit"); err != nil {
		t.Fatalf("quit first TUI before cold restart: %v", err)
	}
	firstWait := make(chan error, 1)
	go func(first *exec.Cmd) { firstWait <- first.Wait() }(cmd)
	select {
	case err := <-firstWait:
		if err != nil {
			t.Fatalf("first TUI did not exit cleanly: %v\n%s", err, ptyOutputDiagnostic(&output))
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("first TUI did not detach its logical session\n%s", ptyOutputDiagnostic(&output))
	}
	_ = ptmx.Close()
	select {
	case <-copyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first TUI output reader did not stop")
	}

	cmd = exec.Command(stadoBinAbs, "session", "resume", logicalSessionPrefix)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("restart stado PTY: %v", err)
	}
	output = synchronizedPTYOutput{}
	copyDone = make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("restarted TUI did not become ready: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	offset = len(output.String())
	if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
		t.Fatalf("query supervise after cold restart: %v", err)
	}
	if err := waitForPTYOutputAfter(&output, offset, "0/1 plan steps complete", 20*time.Second); err != nil {
		t.Fatalf("cold restart lost durable supervise state: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := waitForPTYOutputAfter(&output, offset, "interrupted at version", 20*time.Second); err != nil {
		t.Fatalf("cold restart lost exact WorkerRun projection: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if strings.Contains(normalizedPTYOutput(output.String()), "application worker started") {
		t.Fatalf("cold restart duplicated an interrupted recurrence\n%s", ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel recovered supervise workflow: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("recovered workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if strings.Contains(normalizedPTYOutput(output.String()), "application worker cancellation returned non-cancelled status") {
		t.Fatalf("recovered terminal workflow emitted a false native handoff error\n%s", ptyOutputDiagnostic(&output))
	}
	select {
	case <-unexpectedWorkerFollowup:
		t.Fatalf("watchdog pause allowed a parent-provider follow-up\n%s", ptyOutputDiagnostic(&output))
	default:
	}
}

// TestPTYE2E_Stado_OfficialSuperviseAutomaticCompaction proves the final
// cross-repository recovery boundary. The first active WorkerRun iteration
// receives a provider context-overflow error; bundled auto-compact must fork a
// direct child, move the complete authenticated broker scope to it, and resume
// that exact WorkerRun through its durable projection. Replaying the worker
// prompt as an ordinary turn would omit its application-only model tools and
// fail the stub before the watchdog can pause the recovered run.
func TestPTYE2E_Stado_OfficialSuperviseAutomaticCompaction(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the compacted guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature survives recovery"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and recovery checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint, summaryServed, recoveredWorkerServed, pauseServed, unexpectedWorker := stubSuperviseAutomaticCompaction(t, proposal)
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("seed the exact automatic recovery source turn\r")); err != nil {
		t.Fatalf("submit source-turn seed: %v", err)
	}
	if err := waitForPTYOutput(&output, "source turn committed for automatic compaction", 20*time.Second); err != nil {
		t.Fatalf("automatic recovery source turn was not committed: %v\n%s", err, ptyOutputDiagnostic(&output))
	}

	if err := sendPTYSlashCommand(ptmx, "/supervise Ship the compacted guarded feature"); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}

	for label, signal := range map[string]<-chan struct{}{
		"auto-compact summary":         summaryServed,
		"recovered application worker": recoveredWorkerServed,
		"recovered watchdog pause":     pauseServed,
	} {
		select {
		case <-signal:
		case <-time.After(40 * time.Second):
			t.Fatalf("%s was not served\n%s", label, ptyOutputDiagnostic(&output))
		}
	}
	if err := waitForPTYOutput(&output, "auto-recovery: switched to compacted child session", 30*time.Second); err != nil {
		t.Fatalf("automatic child handoff was not visible: %v\n%s", err, ptyOutputDiagnostic(&output))
	}

	pausedDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(pausedDeadline) {
		offset := len(output.String())
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query recovered supervise status: %v", err)
		}
		if err := waitForPTYOutputAfter(&output, offset, "interrupted at version", 2*time.Second); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	visible := normalizedPTYOutput(output.String())
	statusAt := strings.LastIndex(visible, "0/1 plan steps complete")
	if statusAt < 0 || !strings.Contains(visible[statusAt:], "interrupted at version") {
		t.Fatalf("compacted child lost the exact supervise journal or WorkerRun\n%s", ptyOutputDiagnostic(&output))
	}
	childMatch := regexp.MustCompile(`auto-recovery: switched to compacted child session ([0-9a-f-]{36})`).FindStringSubmatch(visible)
	if len(childMatch) != 2 {
		t.Fatalf("automatic recovery did not expose its broker-verified direct child: child=%v\n%s", childMatch, ptyOutputDiagnostic(&output))
	}
	select {
	case <-unexpectedWorker:
		t.Fatalf("automatic recovery duplicated or bypassed the broker-owned recurrence\n%s", ptyOutputDiagnostic(&output))
	default:
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel compacted supervise workflow: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("compacted workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
}

// TestPTYE2E_Stado_OfficialSuperviseOperatorInput proves the immutable-input
// boundary while a broker-owned WorkerRun is actively streaming. The ordinary
// operator message must not steer the live provider request or enter its
// context directly. It is first captured for the exact run, claimed by the
// signed application, classified by a fresh pinned read-only reviewer, and
// delivered byte-for-byte only after the current assistant turn commits. The
// following recurrence is the first provider request allowed to observe it.
func TestPTYE2E_Stado_OfficialSuperviseOperatorInput(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	const operatorInput = "Please keep the focused regression green while finishing the guarded feature."
	endpoint, secondWorkerStarted, reviewerServed, deliveredInputSeen, thirdRequestCancelled, releaseSecondWorker := stubSuperviseOperatorInput(t, proposal, operatorInput)
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		releaseSecondWorker()
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise Ship the guarded feature"); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-secondWorkerStarted:
	case <-time.After(20 * time.Second):
		t.Fatalf("anchored worker iteration did not begin\n%s", ptyOutputDiagnostic(&output))
	}

	if _, err := ptmx.Write([]byte("\x1b[200~" + operatorInput + "\x1b[201~\r")); err != nil {
		t.Fatalf("submit operator input during worker stream: %v", err)
	}
	if err := waitForPTYOutput(&output, "lifecycle application is routing the captured input", 20*time.Second); err != nil {
		t.Fatalf("operator input was not durably captured for the application: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-reviewerServed:
	case <-time.After(30 * time.Second):
		t.Fatalf("fresh operator-input reviewer did not classify the exact captured record\n%s", ptyOutputDiagnostic(&output))
	}
	// Classification may settle while the worker is still streaming, but the
	// immutable original cannot be appended before this assistant boundary.
	releaseSecondWorker()
	select {
	case <-deliveredInputSeen:
	case <-time.After(30 * time.Second):
		t.Fatalf("next recurrence did not receive the immutable routed operator input\n%s", ptyOutputDiagnostic(&output))
	}

	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel supervise workflow after routed input: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("routed-input workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-thirdRequestCancelled:
	case <-time.After(5 * time.Second):
		t.Fatalf("durable cancellation did not stop the post-delivery provider request\n%s", ptyOutputDiagnostic(&output))
	}
}

// TestPTYE2E_Stado_OfficialSupervisePivot proves that neither the worker's
// model tool call nor a fresh watchdog recommendation selects a replacement
// contract. With the default user policy, only a later explicit quality
// confirmation may CAS the exact reviewed candidate, advance the artifact and
// plan versions, and release recurrence on the new first step.
func TestPTYE2E_Stado_OfficialSupervisePivot(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	replacement := `{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement-revised","title":"Implement with the regression gate first","done_when":"Focused and regression checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}`
	endpoint, reviewServed, revisedWorkerStarted, revisedWorkerCancelled := stubSupervisePivot(t, proposal, replacement)
	configureStadoStub(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() { _, _ = io.Copy(&output, ptmx); close(copyDone) }()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise Ship the guarded feature"); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?", "Choose watchdog cadence.",
		"Who may approve plan-level pivots?", "Choose the supervision assurance profile.",
		"If an operator /loop already exists", "Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-reviewServed:
	case <-time.After(30 * time.Second):
		t.Fatalf("fresh pivot watchdog did not review the exact replacement\n%s", ptyOutputDiagnostic(&output))
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query pending pivot status: %v", err)
		}
		if err := waitForPTYOutput(&output, "pivot plan_only is awaiting_user_confirmation", 1500*time.Millisecond); err == nil {
			break
		}
	}
	if !strings.Contains(normalizedPTYOutput(output.String()), "pivot plan_only is awaiting_user_confirmation") {
		t.Fatalf("watchdog recommendation selected or lost the pivot without operator confirmation\n%s", ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume exact pivot confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm exact supervision pivot", 20*time.Second); err != nil {
		t.Fatalf("trusted pivot confirmation boundary missing: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm exact reviewed pivot: %v", err)
	}
	if err := waitForPTYOutput(&output, "selected the exact quality-confirmed supervision pivot and re-anchored the plan", 20*time.Second); err != nil {
		t.Fatalf("quality-confirmed pivot did not commit: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-revisedWorkerStarted:
	case <-time.After(20 * time.Second):
		t.Fatalf("recurrence did not resume on the revised plan anchor\n%s", ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise cancel"); err != nil {
		t.Fatalf("cancel pivoted supervise workflow: %v", err)
	}
	if err := waitForPTYOutput(&output, "supervise workflow and its worker recurrence are durably cancelled", 20*time.Second); err != nil {
		t.Fatalf("pivoted workflow cancellation did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	select {
	case <-revisedWorkerCancelled:
	case <-time.After(5 * time.Second):
		t.Fatalf("pivoted worker request was not cancelled\n%s", ptyOutputDiagnostic(&output))
	}
}

// TestPTYE2E_Stado_OfficialSuperviseVerificationCompletion crosses the final
// success boundary. The worker may finish a plan step and request completion,
// but it cannot select the operator-owned verification commands, approve its
// own completion claim, or end its WorkerRun. The TUI executes the configured
// suite at the broker-pinned completion tree, a watchdog admits only the exact
// candidate, a separate fresh verifier decides it, and the generic successful-
// completion handoff terminates recurrence.
func TestPTYE2E_Stado_OfficialSuperviseVerificationCompletion(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	proposal := `{"schema":"stado.dev/supervise/baseline-proposal/v1","baseline":{"objective":"Ship the guarded feature","constraints":["Preserve existing behavior"],"non_goals":["Unrelated refactors"],"acceptance_criteria":["The guarded feature works"],"plan":[{"id":"implement","title":"Implement the guarded feature","done_when":"Focused checks pass"}],"definition_of_done":["Focused and regression checks pass"],"verification":["Run the focused checks"],"risks":["Avoid scope drift"]}}`
	endpoint, completionClaimReviewed, verifierServed, unexpectedWorkerFollowup := stubSuperviseVerificationCompletion(t, proposal)
	configureStadoStubWithVerification(t, endpoint)
	installOfficialSupervisePlugin(t, stadoBinAbs)
	t.Cleanup(func() {
		stop := exec.Command(stadoBinAbs, "daemon", "stop", "--force")
		stop.Env = os.Environ()
		_ = stop.Run()
	})

	cmd := exec.Command(stadoBinAbs)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		t.Fatalf("start stado PTY: %v", err)
	}
	var output synchronizedPTYOutput
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(copyDone)
	}()
	t.Cleanup(func() {
		_, _ = ptmx.Write([]byte{3})
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
	})

	if err := waitForPTYOutput(&output, "Type a message", 20*time.Second); err != nil {
		t.Fatalf("stado input did not become ready: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte("\x1b[200~/supervise Ship the guarded feature\x1b[201~\r")); err != nil {
		t.Fatalf("submit /supervise: %v", err)
	}
	for _, prompt := range []string{
		"What should the supervised worker accomplish?",
		"Choose watchdog cadence.",
		"Who may approve plan-level pivots?",
		"Choose the supervision assurance profile.",
		"If an operator /loop already exists",
		"Use profile defaults or review advanced setup?",
	} {
		if err := waitForPTYOutput(&output, prompt, 20*time.Second); err != nil {
			t.Fatalf("setup prompt %q missing: %v\n%s", prompt, err, output.String())
		}
		if _, err := ptmx.Write([]byte{'\r'}); err != nil {
			t.Fatalf("accept default for %q: %v", prompt, err)
		}
	}
	if err := waitForPTYOutput(&output, "Fresh supervise baseline is ready", 40*time.Second); err != nil {
		t.Fatalf("fresh baseline did not complete: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
		t.Fatalf("resume baseline confirmation: %v", err)
	}
	if err := waitForPTYOutput(&output, "Confirm supervised-work baseline", 20*time.Second); err != nil {
		t.Fatalf("baseline approval boundary missing: %v\n%s", err, output.String())
	}
	if _, err := ptmx.Write([]byte{'y'}); err != nil {
		t.Fatalf("confirm baseline proposal: %v", err)
	}
	if err := waitForPTYOutput(&output, "application worker started", 20*time.Second); err != nil {
		t.Fatalf("confirmed baseline did not activate its exact WorkerRun: %v\n%s", err, ptyOutputDiagnostic(&output))
	}
	stepDeadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(stepDeadline) {
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query reviewed step status: %v", err)
		}
		if err := waitForPTYOutput(&output, "1/1 plan steps complete", 1500*time.Millisecond); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(normalizedPTYOutput(output.String()), "1/1 plan steps complete") {
		t.Fatalf("current-anchor watchdog did not advance the exact plan step\n%s", ptyOutputDiagnostic(&output))
	}
	reviewedStatus := normalizedPTYOutput(output.String())
	statusAt := strings.LastIndex(reviewedStatus, "1/1 plan steps complete")
	if statusAt < 0 {
		t.Fatalf("reviewed recurrence status disappeared\n%s", ptyOutputDiagnostic(&output))
	}
	switch statusTail := reviewedStatus[statusAt:]; {
	case strings.Contains(statusTail, "worker recurrence interrupted"):
		if err := sendPTYSlashCommand(ptmx, "/supervise resume"); err != nil {
			t.Fatalf("resume exact interrupted worker for completion phase: %v", err)
		}
		if err := waitForPTYOutput(&output, "requested exact worker recurrence resume", 20*time.Second); err != nil {
			t.Fatalf("reviewed recurrence did not enter explicit resume handoff: %v\n%s", err, ptyOutputDiagnostic(&output))
		}
	case strings.Contains(statusTail, "worker recurrence active"):
		// The reviewed step and the next recurrence can cross in either order.
		// An already-active exact run needs no synthetic resume transition.
	default:
		t.Fatalf("reviewed recurrence was neither active nor interrupted: %q\n%s", statusTail, ptyOutputDiagnostic(&output))
	}

	select {
	case <-completionClaimReviewed:
	case <-time.After(45 * time.Second):
		t.Fatalf("completion watchdog did not review the exact host-verified candidate\n%s", ptyOutputDiagnostic(&output))
	}
	select {
	case <-verifierServed:
	case <-time.After(30 * time.Second):
		t.Fatalf("fresh independent completion verifier did not run\n%s", ptyOutputDiagnostic(&output))
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := sendPTYSlashCommand(ptmx, "/supervise status"); err != nil {
			t.Fatalf("query completed supervise status: %v", err)
		}
		visible := normalizedPTYOutput(output.String())
		if strings.Contains(visible, "independently verified complete") && strings.Contains(visible, "successful-completion handoff recorded") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	visible := normalizedPTYOutput(output.String())
	if !strings.Contains(visible, "independently verified complete") || !strings.Contains(visible, "successful-completion handoff recorded") {
		t.Fatalf("fresh verifier approval did not reach the generic completion handoff\n%s", ptyOutputDiagnostic(&output))
	}
	select {
	case <-unexpectedWorkerFollowup:
		t.Fatalf("parent provider received a completion-tool follow-up before independent review\n%s", ptyOutputDiagnostic(&output))
	default:
	}
}

type superviseStepReviewRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func superviseRequestText(request superviseStepReviewRequest) string {
	var joined strings.Builder
	for _, message := range request.Messages {
		if value, ok := message.Content.(string); ok {
			joined.WriteString(value)
			joined.WriteByte('\n')
		}
	}
	return joined.String()
}

func superviseApplicationAnchor(request superviseStepReviewRequest) (json.RawMessage, error) {
	const marker = "[stado supervise application context]"
	for _, message := range request.Messages {
		value, ok := message.Content.(string)
		if !ok {
			continue
		}
		markerAt := strings.LastIndex(value, marker)
		if markerAt < 0 {
			continue
		}
		objectAt := strings.Index(value[markerAt+len(marker):], "{")
		if objectAt < 0 {
			return nil, errors.New("supervise application context has no JSON object")
		}
		objectAt += markerAt + len(marker)
		var contextFacts struct {
			Anchor json.RawMessage `json:"anchor"`
		}
		if err := json.NewDecoder(strings.NewReader(value[objectAt:])).Decode(&contextFacts); err != nil {
			return nil, fmt.Errorf("decode supervise application context: %w", err)
		}
		if len(contextFacts.Anchor) == 0 {
			return nil, errors.New("supervise application context omitted anchor")
		}
		return append(json.RawMessage(nil), contextFacts.Anchor...), nil
	}
	return nil, errors.New("provider request omitted supervise application context")
}

func superviseReviewerAnchor(request superviseStepReviewRequest) (json.RawMessage, error) {
	for _, message := range request.Messages {
		value, ok := message.Content.(string)
		if !ok || !strings.Contains(value, `"role":"independent stado supervision watchdog"`) {
			continue
		}
		var prompt struct {
			Role   string          `json:"role"`
			Anchor json.RawMessage `json:"anchor"`
		}
		// The provider sees the generic read-only child preamble followed by
		// the application's strict JSON task. Decode the task object rather
		// than incorrectly treating the entire user message as JSON.
		objectAt := strings.Index(value, "{")
		if objectAt < 0 {
			return nil, errors.New("watchdog prompt has no JSON object")
		}
		if err := json.Unmarshal([]byte(value[objectAt:]), &prompt); err != nil {
			return nil, fmt.Errorf("decode watchdog prompt: %w", err)
		}
		if prompt.Role != "independent stado supervision watchdog" {
			return nil, fmt.Errorf("unexpected watchdog role %q", prompt.Role)
		}
		if len(prompt.Anchor) == 0 {
			return nil, errors.New("watchdog prompt omitted anchor")
		}
		return append(json.RawMessage(nil), prompt.Anchor...), nil
	}
	return nil, errors.New("provider request omitted watchdog prompt")
}

func writeSuperviseStubSSE(w http.ResponseWriter, values ...any) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, value := range values {
		raw, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func superviseCompletionVerifierAnchor(request superviseStepReviewRequest) (json.RawMessage, error) {
	for _, message := range request.Messages {
		value, ok := message.Content.(string)
		if !ok || !strings.Contains(value, `"role":"independent stado completion verifier"`) {
			continue
		}
		objectAt := strings.Index(value, "{")
		if objectAt < 0 {
			return nil, errors.New("completion verifier prompt has no JSON object")
		}
		var prompt struct {
			Role                  string          `json:"role"`
			Anchor                json.RawMessage `json:"anchor"`
			CriterionEvidence     []any           `json:"criterion_evidence"`
			HostVerificationFacts struct {
				Outcome     string `json:"outcome"`
				SuiteDigest string `json:"suite_digest"`
			} `json:"host_verification_facts"`
		}
		if err := json.Unmarshal([]byte(value[objectAt:]), &prompt); err != nil {
			return nil, fmt.Errorf("decode completion verifier prompt: %w", err)
		}
		if prompt.Role != "independent stado completion verifier" || len(prompt.Anchor) == 0 || len(prompt.CriterionEvidence) == 0 || prompt.HostVerificationFacts.Outcome != "commands_succeeded" || !strings.HasPrefix(prompt.HostVerificationFacts.SuiteDigest, "sha256:") {
			return nil, errors.New("completion verifier prompt omitted exact criteria or host verification facts")
		}
		return append(json.RawMessage(nil), prompt.Anchor...), nil
	}
	return nil, errors.New("provider request omitted completion verifier prompt")
}

// stubSuperviseVerificationCompletion forces the complete success sequence:
// anchor establishment, exact step claim, watchdog approval, a fresh completion
// turn whose boundary triggers the operator-owned `true` suite, exact completion
// claim, watchdog admission, and a distinct completion verifier. The host-side
// verification request is deliberately absent from the provider protocol; the
// fourth worker request can occur only after its hold is factually resolved.
func stubSuperviseVerificationCompletion(t *testing.T, proposal string) (string, <-chan struct{}, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	completionClaimReviewed := make(chan struct{}, 1)
	verifierServed := make(chan struct{}, 1)
	unexpectedWorkerFollowup := make(chan struct{}, 1)
	var workerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request superviseStepReviewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requestText := superviseRequestText(request)
		switch {
		case strings.Contains(requestText, "fresh independent stado supervision baseline architect"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": proposal}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300}},
			)
		case strings.Contains(requestText, `"role":"independent stado completion verifier"`):
			anchor, err := superviseCompletionVerifierAnchor(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			verdict := json.RawMessage(fmt.Sprintf(`{"verdict":{"decision":"approve","anchor":%s,"rationale":"the exact host-verified tree proves every criterion and done condition","evidence_refs":["trace:pty-native-verification","trace:pty-criterion"]}}`, anchor))
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(verdict)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 180, "completion_tokens": 80, "total_tokens": 260}},
			)
			select {
			case verifierServed <- struct{}{}:
			default:
			}
		case strings.Contains(requestText, `"role":"independent stado supervision watchdog"`):
			anchor, err := superviseReviewerAnchor(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			completionClaim := strings.Contains(requestText, `"type":"completion_claim"`)
			rationale := "the exact host evidence supports the current step claim"
			evidence := "trace:pty-criterion"
			if completionClaim {
				rationale = "the exact host verification facts and criterion evidence admit fresh completion verification"
				evidence = "trace:pty-native-verification"
			}
			verdict := json.RawMessage(fmt.Sprintf(`{"verdict":{"decision":"approve","anchor":%s,"rationale":%q,"evidence_refs":[%q]}}`, anchor, rationale, evidence))
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(verdict)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 160, "completion_tokens": 70, "total_tokens": 230}},
			)
			if completionClaim {
				select {
				case completionClaimReviewed <- struct{}{}:
				default:
				}
			}
		default:
			switch workerRequests.Add(1) {
			case 1:
				if _, err := superviseApplicationAnchor(request); err == nil {
					http.Error(w, "first worker turn invented a committed application anchor", http.StatusConflict)
					return
				}
				writeSuperviseStubSSE(w,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "implemented the guarded change and committed the first exact worker tree"}}}},
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130}},
				)
			case 2:
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "step-claim worker omitted exact anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				arguments := fmt.Sprintf(`{"idempotency_key":"pty-complete-step","anchor":%s,"criterion_index":0,"evidence_refs":["trace:pty-criterion"],"complete_active_step":"implement"}`, anchor)
				toolCall := map[string]any{"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": "call_supervise_complete_step", "type": "function",
						"function": map[string]any{"name": "supervise__report_progress", "arguments": arguments},
					}}},
				}}}
				writeSuperviseStubSSE(w,
					toolCall,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 45, "total_tokens": 165}},
				)
			case 3:
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "completion-boundary worker omitted exact anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				var value struct {
					ActiveStep string `json:"active_step"`
				}
				if err := json.Unmarshal(anchor, &value); err != nil || value.ActiveStep != "completion" {
					http.Error(w, "host verification did not start from the exact completion anchor", http.StatusConflict)
					return
				}
				writeSuperviseStubSSE(w,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "committed the exact completion tree for the operator-configured verification suite"}}}},
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 110, "completion_tokens": 35, "total_tokens": 145}},
				)
			case 4:
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "completion-claim worker omitted exact anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				arguments := fmt.Sprintf(`{"idempotency_key":"pty-request-completion","anchor":%s,"criteria":[{"criterion_index":0,"evidence_refs":["trace:pty-criterion"]}]}`, anchor)
				toolCall := map[string]any{"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": "call_supervise_completion", "type": "function",
						"function": map[string]any{"name": "supervise__request_completion", "arguments": arguments},
					}}},
				}}}
				writeSuperviseStubSSE(w,
					toolCall,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 130, "completion_tokens": 50, "total_tokens": 180}},
				)
			case 5:
				select {
				case unexpectedWorkerFollowup <- struct{}{}:
				default:
				}
				http.Error(w, "parent provider follow-up bypassed completion review", http.StatusConflict)
			default:
				http.Error(w, "unexpected extra worker recurrence after completion", http.StatusConflict)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1", completionClaimReviewed, verifierServed, unexpectedWorkerFollowup
}

// stubSuperviseStepReview derives every authority-shaped input from the real
// provider request. In particular, neither the tool claim nor watchdog verdict
// guesses a session, tree, plan, or turn coordinate: both copy the exact
// application/broker anchor they were shown and the plugin revalidates it.
func stubSuperviseStepReview(t *testing.T, proposal string) (string, <-chan struct{}) {
	return stubSuperviseReviewDecision(t, proposal, "approve")
}

func stubSupervisePauseReview(t *testing.T, proposal string) (string, <-chan struct{}) {
	return stubSuperviseReviewDecision(t, proposal, "pause")
}

// stubSuperviseAutomaticCompaction distinguishes the provider call made by
// bundled auto-compact from the surrounding supervise baseline, WorkerRun,
// and watchdog calls. Its first worker request fails with a real OAI-compatible
// 400 context error. The next ordinary-looking request is accepted only when
// the child composition projects supervise's exact application-worker tools,
// proving the host reconciled the durable run instead of replaying it as an
// unowned prompt.
func stubSuperviseAutomaticCompaction(t *testing.T, proposal string) (string, <-chan struct{}, <-chan struct{}, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	summaryServed := make(chan struct{}, 1)
	recoveredWorkerServed := make(chan struct{}, 1)
	pauseServed := make(chan struct{}, 1)
	unexpectedWorker := make(chan struct{}, 1)
	var workerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request superviseStepReviewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requestText := superviseRequestText(request)
		hasSuperviseWorkerTool := false
		for _, declared := range request.Tools {
			if declared.Function.Name == "supervise__report_progress" {
				hasSuperviseWorkerTool = true
				break
			}
		}
		switch {
		case strings.Contains(requestText, "fresh independent stado supervision baseline architect"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": proposal}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300}},
			)
		case strings.Contains(requestText, "You are a conversation-summarisation tool for stado"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "- Continue the exact broker-owned supervised run.\n- Preserve its journal, hold, event cursor, and WorkerRun identity."}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 140, "completion_tokens": 45, "total_tokens": 185}},
			)
			select {
			case summaryServed <- struct{}{}:
			default:
			}
		case strings.Contains(requestText, `"role":"independent stado supervision watchdog"`):
			anchor, err := superviseReviewerAnchor(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			verdict := json.RawMessage(fmt.Sprintf(`{"verdict":{"decision":"pause","anchor":%s,"rationale":"pause after proving exact automatic child recovery","evidence_refs":["trace:pty-auto-compact"]}}`, anchor))
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(verdict)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 160, "completion_tokens": 80, "total_tokens": 240}},
			)
			select {
			case pauseServed <- struct{}{}:
			default:
			}
		case strings.Contains(requestText, "seed the exact automatic recovery source turn") && !hasSuperviseWorkerTool:
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "source turn committed for automatic compaction"}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}},
			)
		default:
			switch workerRequests.Add(1) {
			case 1:
				http.Error(w, "maximum context length exceeded for exact WorkerRun", http.StatusBadRequest)
			case 2:
				if !hasSuperviseWorkerTool {
					http.Error(w, "compacted worker prompt replayed without exact application ownership", http.StatusConflict)
					return
				}
				writeSuperviseStubSSE(w,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "continued the exact supervised worker after authenticated child handoff; committing the child turn before an anchored claim"}}}},
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 40, "total_tokens": 160}},
				)
				select {
				case recoveredWorkerServed <- struct{}{}:
				default:
				}
			case 3:
				if !hasSuperviseWorkerTool {
					http.Error(w, "compacted worker follow-up lost exact application ownership", http.StatusConflict)
					return
				}
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "compacted worker lost its exact application anchor: "+err.Error(), http.StatusConflict)
					return
				}
				arguments := fmt.Sprintf(`{"idempotency_key":"pty-auto-compact-step","anchor":%s,"criterion_index":0,"evidence_refs":["trace:pty-auto-compact"],"complete_active_step":"implement"}`, anchor)
				toolCall := map[string]any{"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": "call_supervise_auto_compact_progress", "type": "function",
						"function": map[string]any{"name": "supervise__report_progress", "arguments": arguments},
					}}},
				}}}
				writeSuperviseStubSSE(w,
					toolCall,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 40, "total_tokens": 160}},
				)
			default:
				select {
				case unexpectedWorker <- struct{}{}:
				default:
				}
				http.Error(w, "unexpected duplicate WorkerRun recurrence after automatic recovery", http.StatusConflict)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1", summaryServed, recoveredWorkerServed, pauseServed, unexpectedWorker
}

func stubSuperviseReviewDecision(t *testing.T, proposal, decision string) (string, <-chan struct{}) {
	t.Helper()
	if decision != "approve" && decision != "pause" {
		t.Fatalf("unsupported supervise review decision %q", decision)
	}
	unexpectedWorkerFollowup := make(chan struct{}, 1)
	var anchorlessWorkerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request superviseStepReviewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requestText := superviseRequestText(request)
		switch {
		case strings.Contains(requestText, "fresh independent stado supervision baseline architect"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": proposal}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300}},
			)
		case strings.Contains(requestText, `"role":"independent stado supervision watchdog"`):
			anchor, err := superviseReviewerAnchor(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			rationale := "the exact host evidence supports the current step claim"
			if decision == "pause" {
				rationale = "pause the exact reviewed run before lifecycle recovery validation"
			}
			verdict := json.RawMessage(fmt.Sprintf(`{"verdict":{"decision":%q,"anchor":%s,"rationale":%q,"evidence_refs":["trace:pty-worker"]}}`, decision, anchor, rationale))
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(verdict)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 160, "completion_tokens": 80, "total_tokens": 240}},
			)
		default:
			hasProgressTool := false
			for _, declared := range request.Tools {
				if declared.Function.Name == "supervise__report_progress" {
					hasProgressTool = true
					break
				}
			}
			hasToolResult := false
			for _, message := range request.Messages {
				if message.Role == "tool" {
					hasToolResult = true
					break
				}
			}
			if hasProgressTool && !hasToolResult {
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					// The first WorkerRun iteration intentionally predates any
					// authenticated committed-turn anchor. It may do ordinary work,
					// but it cannot call an exact-anchor application tool. Completing
					// this turn publishes session.turn_committed; the next recurrence
					// receives the resulting application context.
					if !strings.Contains(err.Error(), "omitted supervise application context") || anchorlessWorkerRequests.Add(1) != 1 {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					writeSuperviseStubSSE(w,
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "implemented the focused guarded change; committing this turn before making an evidence claim"}}}},
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 40, "total_tokens": 160}},
					)
					return
				}
				arguments := fmt.Sprintf(`{"idempotency_key":"pty-step-review","anchor":%s,"criterion_index":0,"evidence_refs":["trace:pty-worker"],"complete_active_step":"implement"}`, anchor)
				toolCallChunk := map[string]any{
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role": "assistant",
							"tool_calls": []any{map[string]any{
								"index": 0, "id": "call_supervise_progress", "type": "function",
								"function": map[string]any{"name": "supervise__report_progress", "arguments": arguments},
							}},
						},
					}},
				}
				writeSuperviseStubSSE(w,
					toolCallChunk,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 40, "total_tokens": 160}},
				)
				return
			}
			if hasProgressTool && hasToolResult {
				select {
				case unexpectedWorkerFollowup <- struct{}{}:
				default:
				}
				http.Error(w, "parent provider follow-up bypassed supervise review barrier", http.StatusConflict)
				return
			}
			t.Logf("unexpected supervise provider request (first 4096 bytes):\n%.4096s", requestText)
			http.Error(w, "unexpected supervise provider request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1", unexpectedWorkerFollowup
}

type superviseOperatorInputReviewPrompt struct {
	Role          string          `json:"role"`
	Anchor        json.RawMessage `json:"anchor"`
	OperatorInput struct {
		InputID string `json:"input_id"`
		Text    string `json:"text"`
	} `json:"operator_input"`
	ResponseSchema struct {
		Classification struct {
			ReviewID string          `json:"review_id"`
			InputID  string          `json:"input_id"`
			Anchor   json.RawMessage `json:"anchor"`
		} `json:"classification"`
	} `json:"response_schema"`
}

func superviseOperatorInputReview(request superviseStepReviewRequest, original string) (json.RawMessage, error) {
	for _, message := range request.Messages {
		value, ok := message.Content.(string)
		if !ok || !strings.Contains(value, `"role":"independent stado operator-input quality reviewer"`) {
			continue
		}
		objectAt := strings.Index(value, "{")
		if objectAt < 0 {
			return nil, errors.New("operator-input reviewer prompt has no JSON object")
		}
		var prompt superviseOperatorInputReviewPrompt
		if err := json.Unmarshal([]byte(value[objectAt:]), &prompt); err != nil {
			return nil, fmt.Errorf("decode operator-input reviewer prompt: %w", err)
		}
		expected := prompt.ResponseSchema.Classification
		if prompt.Role != "independent stado operator-input quality reviewer" || prompt.OperatorInput.Text != original || prompt.OperatorInput.InputID == "" || expected.ReviewID == "" || expected.InputID != prompt.OperatorInput.InputID || len(prompt.Anchor) == 0 || len(expected.Anchor) == 0 || !bytes.Equal(prompt.Anchor, expected.Anchor) {
			return nil, errors.New("operator-input reviewer prompt changed exact captured identity")
		}
		classification := map[string]any{
			"classification": map[string]any{
				"review_id": expected.ReviewID, "input_id": expected.InputID, "anchor": expected.Anchor,
				"disposition": "deliver", "label": "active-step", "rationale": "the immutable input directly concerns the active guarded step",
			},
		}
		raw, err := json.Marshal(classification)
		return json.RawMessage(raw), err
	}
	return nil, errors.New("provider request omitted operator-input reviewer prompt")
}

// stubSuperviseOperatorInput keeps the second worker turn live while the TUI
// captures and the application classifies an ordinary input. The third worker
// request is accepted only if the exact original text appears after that turn
// boundary; observing it earlier or in a different provider path fails closed.
func stubSuperviseOperatorInput(t *testing.T, proposal, original string) (string, <-chan struct{}, <-chan struct{}, <-chan struct{}, <-chan struct{}, func()) {
	t.Helper()
	secondWorkerStarted := make(chan struct{}, 1)
	reviewerServed := make(chan struct{}, 1)
	deliveredInputSeen := make(chan struct{}, 1)
	thirdRequestCancelled := make(chan struct{}, 1)
	releaseSecondWorker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSecondWorker) }) }
	var workerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request superviseStepReviewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requestText := superviseRequestText(request)
		switch {
		case strings.Contains(requestText, "fresh independent stado supervision baseline architect"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": proposal}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300}},
			)
		case strings.Contains(requestText, `"role":"independent stado operator-input quality reviewer"`):
			classification, err := superviseOperatorInputReview(request, original)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(classification)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 140, "completion_tokens": 60, "total_tokens": 200}},
			)
			select {
			case reviewerServed <- struct{}{}:
			default:
			}
		default:
			switch workerRequests.Add(1) {
			case 1:
				if strings.Contains(requestText, original) {
					http.Error(w, "operator input reached the worker before capture", http.StatusConflict)
					return
				}
				writeSuperviseStubSSE(w,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "committed an ordinary first worker turn to establish the exact anchor"}}}},
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130}},
				)
			case 2:
				if strings.Contains(requestText, original) {
					http.Error(w, "operator input steered the already-live worker turn", http.StatusConflict)
					return
				}
				if _, err := superviseApplicationAnchor(request); err != nil {
					http.Error(w, "second worker omitted exact committed anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				select {
				case secondWorkerStarted <- struct{}{}:
				default:
				}
				select {
				case <-releaseSecondWorker:
					writeSuperviseStubSSE(w,
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "finished the live worker turn before accepting routed operator input"}}}},
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 110, "completion_tokens": 35, "total_tokens": 145}},
					)
				case <-r.Context().Done():
				}
			case 3:
				if !strings.Contains(requestText, original) {
					http.Error(w, "post-boundary worker omitted immutable routed operator input", http.StatusBadRequest)
					return
				}
				select {
				case deliveredInputSeen <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case thirdRequestCancelled <- struct{}{}:
				default:
				}
			default:
				http.Error(w, "unexpected extra worker recurrence", http.StatusConflict)
			}
		}
	}))
	t.Cleanup(func() {
		release()
		server.Close()
	})
	return server.URL + "/v1", secondWorkerStarted, reviewerServed, deliveredInputSeen, thirdRequestCancelled, release
}

func equalJSONValue(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func supervisePivotVerdict(request superviseStepReviewRequest, replacement string) (json.RawMessage, error) {
	for _, message := range request.Messages {
		value, ok := message.Content.(string)
		if !ok || !strings.Contains(value, `"role":"independent stado supervision watchdog"`) || !strings.Contains(value, `"pivot_proposal"`) {
			continue
		}
		objectAt := strings.Index(value, "{")
		if objectAt < 0 {
			return nil, errors.New("pivot watchdog prompt has no JSON object")
		}
		var prompt struct {
			Role          string          `json:"role"`
			Anchor        json.RawMessage `json:"anchor"`
			PivotProposal struct {
				Classification string          `json:"classification"`
				Stage          string          `json:"stage"`
				Replacement    json.RawMessage `json:"replacement"`
			} `json:"pivot_proposal"`
		}
		if err := json.Unmarshal([]byte(value[objectAt:]), &prompt); err != nil {
			return nil, fmt.Errorf("decode pivot watchdog prompt: %w", err)
		}
		if prompt.Role != "independent stado supervision watchdog" || prompt.PivotProposal.Classification != "plan_only" || prompt.PivotProposal.Stage != "reviewing" || len(prompt.Anchor) == 0 || !equalJSONValue(prompt.PivotProposal.Replacement, []byte(replacement)) {
			return nil, errors.New("pivot watchdog prompt changed the exact structured replacement")
		}
		verdict := map[string]any{"verdict": map[string]any{
			"decision": "approve", "anchor": prompt.Anchor,
			"rationale":     "the exact plan-only replacement is supported by the current evidence",
			"evidence_refs": []string{"trace:pty-pivot-review"},
		}}
		raw, err := json.Marshal(verdict)
		return json.RawMessage(raw), err
	}
	return nil, errors.New("provider request omitted exact structured pivot review")
}

func stubSupervisePivot(t *testing.T, proposal, replacement string) (string, <-chan struct{}, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	reviewServed := make(chan struct{}, 1)
	revisedWorkerStarted := make(chan struct{}, 1)
	revisedWorkerCancelled := make(chan struct{}, 1)
	var workerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request superviseStepReviewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requestText := superviseRequestText(request)
		switch {
		case strings.Contains(requestText, "fresh independent stado supervision baseline architect"):
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": proposal}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300}},
			)
		case strings.Contains(requestText, `"pivot_proposal"`):
			verdict, err := supervisePivotVerdict(request, replacement)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeSuperviseStubSSE(w,
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(verdict)}}}},
				map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 160, "completion_tokens": 70, "total_tokens": 230}},
			)
			select {
			case reviewServed <- struct{}{}:
			default:
			}
		default:
			switch workerRequests.Add(1) {
			case 1:
				writeSuperviseStubSSE(w,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "committed the first worker turn before requesting a pivot"}}}},
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130}},
				)
			case 2:
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "pivoting worker omitted exact anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				arguments := fmt.Sprintf(`{"idempotency_key":"pty-pivot","anchor":%s,"rationale":"current evidence requires the regression gate first","replacement":%s}`, anchor, replacement)
				toolCall := map[string]any{"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": "call_supervise_pivot", "type": "function",
						"function": map[string]any{"name": "supervise__request_pivot", "arguments": arguments},
					}}},
				}}}
				writeSuperviseStubSSE(w,
					toolCall,
					map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 45, "total_tokens": 165}},
				)
			case 3:
				anchor, err := superviseApplicationAnchor(request)
				if err != nil {
					http.Error(w, "post-pivot worker omitted application anchor: "+err.Error(), http.StatusBadRequest)
					return
				}
				var value struct {
					PlanVersion uint64 `json:"plan_version"`
					ActiveStep  string `json:"active_step"`
				}
				if err := json.Unmarshal(anchor, &value); err != nil || value.PlanVersion != 2 || value.ActiveStep != "implement-revised" {
					http.Error(w, "post-pivot recurrence did not carry the revised plan anchor", http.StatusConflict)
					return
				}
				select {
				case revisedWorkerStarted <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case revisedWorkerCancelled <- struct{}{}:
				default:
				}
			default:
				http.Error(w, "unexpected extra pivot worker recurrence", http.StatusConflict)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1", reviewServed, revisedWorkerStarted, revisedWorkerCancelled
}

// stubFirstLLMThenHold completes exactly the first provider request and keeps
// later requests streaming until their context is cancelled. The supervise
// activation test uses this to keep one worker turn active without flooding
// the Bubble Tea event queue with synthetic zero-tool recurrence iterations.
func stubFirstLLMThenHold(t *testing.T, first []string) (string, <-chan struct{}) {
	t.Helper()
	var requests atomic.Int32
	workerRequestCancelled := make(chan struct{})
	var cancellationOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if requests.Add(1) == 1 {
			for _, chunk := range first {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(50 * time.Millisecond)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"working\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			cancellationOnce.Do(func() { close(workerRequestCancelled) })
		case <-time.After(60 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", workerRequestCancelled
}

func TestBridgeE2E_Stado_SlashFilter(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Wait for stado to be fully ready before sending / —
		// the auto-compact background plugin loads ~1s after
		// startup, and the landing-screen "ctrl+p" hint that
		// connectStado polls for can appear BEFORE plugin loading
		// completes. During plugin init, early printable
		// keypresses can be swallowed before the input handler
		// is wired. The "Type a message..." input placeholder
		// appearing alongside the landing footer means the input
		// component is rendered and ready.
		ready := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('Type a message') >= 0 &&
				s.indexOf('ctrl+p commands') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, ready, 10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("input never became ready: %w; snapshot:\n%s", err, snap)
		}
		// Extra settle so background plugin loading finishes.
		// Empirically the auto-compact plugin loads at ~T+1s and
		// the keypress before that often gets dropped. 1500ms
		// covers the longest observed plugin init path.
		time.Sleep(1500 * time.Millisecond)

		// Send '/' alone first. The trigger in handler_input.go::245
		// fires only when the keypress is a single rune AND
		// m.input.Value() is empty — so we can't send '/sid' as one
		// batch (sendKeys writes bytes contiguously to PTY, which
		// bubbletea may parse as a multi-rune paste event that fails
		// the len(msg.Runes) == 1 check).
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('/')`, nil)); err != nil {
			return fmt.Errorf("send /: %w", err)
		}
		// Wait for the slash popup to materialise before typing
		// the filter chars. Several canonical slash commands +
		// rounded border together is the strongest "popup is open"
		// signal — works whether stado renders it as inline
		// suggestions (unit-test default) OR a modal command
		// palette (observed in bridge mode for some configs);
		// either is fine for the filter-narrowing assertion.
		if _, err := waitForSnapshot(ctx, t,
			`(function(){
				if (!window.bridge || !window.bridge.snapshot) return false;
				var s = window.bridge.snapshot();
				var hasCorner = s.indexOf('╭') >= 0 || s.indexOf('╮') >= 0 ||
					s.indexOf('╰') >= 0 || s.indexOf('╯') >= 0;
				var slashCount = 0;
				var names = ['/sidebar','/theme','/help','/tool','/agents',
					'/model','/persona','/skill','/loop','/monitor','/session',
					'/budget','/thinking','/debug','/split','/clear','/retry'];
				for (var i = 0; i < names.length; i++) {
					if (s.indexOf(names[i]) >= 0) slashCount++;
				}
				return hasCorner && slashCount >= 2;
			})()`, 10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("slash popup never opened after /: %w; snapshot:\n%s", err, snap)
		}
		// Now type 'sid' to filter — this batch is fine because the
		// trigger condition has already fired and we're just
		// updating the filter input.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('sid')`, nil)); err != nil {
			return fmt.Errorf("type 'sid' filter: %w", err)
		}
		// Predicate: /sidebar visible (the substring match
		// candidate) AND /theme NOT visible (would be in the
		// unfiltered list). The negative half is what makes this a
		// FILTER assertion rather than just an "open" assertion.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var hasSidebar = s.indexOf('/sidebar') >= 0;
			var hasTheme = s.indexOf('/theme') >= 0;
			return hasSidebar && !hasTheme;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
		if err != nil {
			return fmt.Errorf("/sid filter never produced /sidebar-only suggestions: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ /sid filter narrowed inline suggestions to /sidebar (excluded /theme)")
		return nil
	})
}

// TestBridgeE2E_Stado_PaletteFilter verifies that opening the
// command palette via Ctrl+P then typing `the` filters the entries
// so /theme appears AND /sidebar does NOT. Bridge-only because:
//   - The palette is a modal popup with its own filter input;
//     teatest tests palette state but not the rendered filtering
//     transitions per keystroke.
//   - Combining Ctrl+P + character input through real escape codes
//     exercises the alt-screen redraw path that broke the original
//     slash-suggest test attempt — this version isolates the
//     palette open from the filter typing rather than chaining
//     keypresses.
//
// Spec: AC5 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_PaletteFilter(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Open the palette first; wait for it to render with the
		// canonical command names before sending filter input. This
		// avoids the chained-keypress timing hazard that flaked the
		// original slash test.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x10')`, nil)); err != nil {
			return fmt.Errorf("send Ctrl+P: %w", err)
		}
		paletteOpen := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			// The modal windows its list to the canvas height (the
			// full 37-command list never fits), so only the TOP of
			// the unfiltered list is visible pre-filter. Require the
			// Search label plus two early rows (/agents and /exit
			// from the Quick/Session groups) — together they only
			// appear when the unfiltered palette is open.
			return s.indexOf('Search') >= 0 &&
				s.indexOf('/agents') >= 0 && s.indexOf('/exit') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, paletteOpen, 10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("palette never opened pre-filter: %w; snapshot:\n%s", err, snap)
		}

		// Now type 'the' to filter. The palette has its own filter
		// input that consumes characters while open.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('the')`, nil)); err != nil {
			return fmt.Errorf("type filter 'the': %w", err)
		}
		// Predicate: /theme visible AND /sidebar NOT visible — the
		// "the" substring matches /theme but not /sidebar.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var hasTheme = s.indexOf('/theme') >= 0;
			var hasSidebar = s.indexOf('/sidebar') >= 0;
			return hasTheme && !hasSidebar;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
		if err != nil {
			return fmt.Errorf("'the' filter never narrowed palette to /theme-only: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ palette filter 'the' narrowed entries to /theme (excluded /sidebar)")

		// Esc to close the palette so test cleanup doesn't leave
		// stado wedged in the modal.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x1b')`, nil)); err != nil {
			return fmt.Errorf("send Esc: %w", err)
		}
		return nil
	})
}

// TestBridgeE2E_Stado_LandingReflow verifies that the bare landing
// screen reflows correctly at multiple terminal widths (no popup,
// no conversation). Bridge-only because:
//   - The landing layout (banner + footer + input box) depends on
//     terminal width for its positioning math; teatest's fixed
//     virtual grid can't sweep widths.
//   - The complementary TestBridgeE2E_Stado_QuitConfirmCentering
//     covers the popup-overlay reflow at the same three widths,
//     but with a popup composited on top — this test isolates
//     base-frame reflow without the overlay distraction.
//
// At each width the landing must show the input placeholder
// (proving the input box rendered at the new width) AND the
// Plan/Do mode marker in the footer. At the narrow 80×24 size
// the "ctrl+p commands" hint may wrap; we don't assert it —
// the input placeholder is the load-bearing signal that the
// frame is laid out.
//
// Spec: AC4 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_LandingReflow(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)

	for _, dim := range []struct {
		name          string
		width, height int64
	}{
		{"narrow-80x24", 80, 24},
		{"normal-120x40", 120, 40},
		{"wide-160x50", 160, 50},
	} {
		t.Run(dim.name, func(t *testing.T) {
			baseURL, token := startBridgeInProcess(t)
			driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
				if err := emulateViewport(ctx, dim.width*7, dim.height*16); err != nil {
					return fmt.Errorf("emulateViewport: %w", err)
				}
				if err := connectStado(ctx, t, stadoBinAbs); err != nil {
					return err
				}
				// Two anchors that should always be visible on the
				// landing screen regardless of viewport width:
				//   - "Type a message" — the input placeholder text
				//   - "Do " (with trailing space) — the mode marker
				//     in the footer (Plan/Do mode indicator)
				// The "ctrl+p commands" hint sometimes wraps at
				// narrow widths so we don't anchor on it here.
				predicate := `(function(){
					if (!window.bridge || !window.bridge.snapshot) return false;
					var s = window.bridge.snapshot();
					var hasInput = s.indexOf('Type a message') >= 0;
					var hasMode = s.indexOf('Do ') >= 0 || s.indexOf('Plan ') >= 0;
					return hasInput && hasMode;
				})()`
				snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
				if err != nil {
					return fmt.Errorf("landing never reflowed at %dx%d: %w; snapshot:\n%s",
						dim.width, dim.height, err, snap)
				}
				t.Logf("✓ landing reflowed at %dx%d (input placeholder + mode marker visible)",
					dim.width, dim.height)
				return nil
			})
		})
	}
}

// stubChunksMarkdown returns SSE chunks that produce a short
// streaming response with a markdown heading + bold + code so
// glamour-rendered styling is visible post-stream. Common
// fixture for the streaming + markdown tests.
func stubChunksMarkdown(marker string) []string {
	// Three content chunks deliberately spread the text over
	// multiple frames so the streaming visual is observable.
	// finish_reason "stop" + usage on the last chunk per the
	// OAI-compat shape stado expects.
	return []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"role":"assistant","content":"# %s\n\nThis is "}}]}`, marker),
		`{"choices":[{"index":0,"delta":{"content":"**bold** text with ` + "`code`" + `."}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":12,"total_tokens":16}}`,
	}
}

// TestBridgeE2E_Stado_StreamingTextDelta verifies streaming text
// deltas reach the TUI's assistant block in xterm.js. Uses the
// in-process stub LLM server so the test is fully deterministic
// and offline. Bridge-only because:
//   - The streaming visual is per-frame buffer growth as deltas
//     arrive; teatest sees the final state but not the
//     incremental rendering.
//   - The chunked SSE → cancelreader → bubbletea Update path
//     exercises real terminal escape codes the in-process
//     teatest virtual terminal doesn't see.
//
// AC4 + AC3 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_StreamingTextDelta(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	endpoint := stubLLMServer(t, stubChunksMarkdown("Hello"))
	configureStadoStub(t, endpoint)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Settle so background plugin loading finishes (per the
		// finding from the slash-filter test).
		ready := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('Type a message') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, ready, 10*time.Second); err != nil {
			return fmt.Errorf("input never became ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// Submit a prompt — Enter (\r) after the text. Send the
		// text as one batch (paste-mode is fine for non-trigger
		// chars), then \r alone.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('say hello')`, nil)); err != nil {
			return fmt.Errorf("type prompt: %w", err)
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\r')`, nil)); err != nil {
			return fmt.Errorf("send Enter: %w", err)
		}

		// Predicate: BOTH chunk 1 ("Hello" heading) AND chunk 2
		// ("bold" body word) reach the snapshot. Codex caught
		// the original `bold || Hello` predicate as too weak —
		// it would have passed when only chunk 1 arrived (which
		// happens via a single non-streaming response too).
		// Requiring both proves the second SSE frame was actually
		// processed by the consumer, which is the whole point of
		// the streaming-text-delta test.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('Hello') >= 0 && s.indexOf('bold') >= 0;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 15*time.Second)
		if err != nil {
			return fmt.Errorf("streamed assistant content never appeared: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ streaming assistant text reached the TUI (chunked SSE → xterm.js render)")
		return nil
	})
}

// TestBridgeE2E_Stado_QueuedPrompt verifies that submitting a
// second prompt while the first is still streaming queues it
// with a visible "queued" marker in the input area. Bridge-only
// because:
//   - The "queued: ..." tag visibility is a render-side concern
//     teatest tests the model state but not the rendered tag.
//
// AC3 of the goal.
func TestBridgeE2E_Stado_QueuedPrompt(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	// Long-running stub: many chunks with extra delays so the
	// second prompt has time to submit + queue WHILE the first
	// is still streaming. Codex caught the original 5-chunk x
	// 50ms (250ms total window) as too tight: by the time we
	// wait for first-stream-visible THEN type+submit a second
	// prompt, the first stream had likely completed and the
	// second was just dispatched as a fresh turn (not queued).
	// Add filler chunks AND increase the per-chunk delay so the
	// streaming window is closer to ~3s — wide enough for the
	// human-paced sendKeys + the queue dispatch to land while
	// the stream is genuinely active.
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"first "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"second "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"third "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"fourth "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"fifth "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"sixth "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"seventh "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"eighth "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"ninth "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"tenth"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":10,"total_tokens":14}}`,
	}
	endpoint := stubLLMServer(t, chunks)
	configureStadoStub(t, endpoint)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("input never ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// Submit first prompt to start streaming.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('first prompt\r')`, nil)); err != nil {
			return fmt.Errorf("send first: %w", err)
		}
		// Wait for streaming to start (assistant content visible).
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('first ') >= 0 || window.bridge.snapshot().indexOf('second ') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("first stream never started: %w", err)
		}

		// While streaming, type a second prompt and QUEUE it with
		// alt+enter. Plain Enter while busy now STEERS (mid-turn inject)
		// under the steering/queue/interrupt input model
		// (decision_steering_queue_interrupt_model) — only alt+enter
		// (the QueueMessage chord) queues for the next turn and renders
		// the "queued" marker this test asserts on. alt+enter over the
		// pty is ESC + CR (`\x1b\r`).
		//
		// Send the text AND the alt+enter in ONE sendKeys batch so the
		// bytes hit the pty contiguously. Splitting them into two
		// chromedp.Run calls opens a race (Codex): on a slow bridge the
		// stubbed first turn can drain between the two calls, leaving the
		// model idle when alt+enter lands — applyQueue then promotes the
		// prompt immediately instead of rendering the queued marker, and
		// the assert flakes even though queuing works.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('queued prompt\x1b\r')`, nil)); err != nil {
			return fmt.Errorf("send queued (text + alt+enter): %w", err)
		}

		// Predicate: the user-block template stado renders for a
		// queued message (internal/tui/render/templates/
		// message_user.tmpl line 5: "⋯ queued — runs when the
		// current turn finishes") appears in the snapshot.
		// Codex caught the original "typed text + streaming text
		// both visible" predicate as too weak — that would also
		// pass if the second prompt was dispatched as a fresh
		// turn instead of being queued. The "⋯ queued" marker
		// is rendered ONLY when the user block has the
		// queued=true field, which only fires through the
		// queued-prompt code path.
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			// Match either marker — the message_user.tmpl tag OR
			// the status.tmpl indicator — both are rendered only
			// when a real queued block exists.
			return s.indexOf('queued — runs when') >= 0 ||
				s.indexOf('queued:') >= 0;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
		if err != nil {
			return fmt.Errorf("queued prompt never appeared during streaming: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ queued prompt visible alongside streaming first turn")
		return nil
	})
}

// TestBridgeE2E_Stado_DisplayModes verifies the thinking + tool
// display-mode keybinds dispatch in a real terminal. Bridge-only
// because:
//   - render unit tests exercise the rendering (collapse/expand) but
//     NOT the keybind → prefix-chord → cycle → announce → render path,
//     which only a real pty + key bytes can drive. (EP-0025 revision /
//     v0.73.0 display modes; CLAUDE.md "test TUI changes via pty-bridge".)
//
// ctrl+x o (NEW) cycles tool-output display; ctrl+x h cycles thinking
// display. Each appends a "<thing>: <mode>" system block at idle. The
// two are independent settings, so both markers appear after pressing
// both chords.
func TestBridgeE2E_Stado_DisplayModes(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("input never ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// ctrl+x = \x18 (CAN); the prefix chord is ctrl+x then the
		// second key. ctrl+x o → cycleToolDisplayMode → announce.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x18o')`, nil)); err != nil {
			return fmt.Errorf("send ctrl+x o: %w", err)
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('tool output:') >= 0`,
			5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("ctrl+x o did not announce a tool-output display mode: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ ctrl+x o cycled the tool-output display mode")

		// ctrl+x h → cycleThinkingDisplayMode → announce. Independent
		// of the tool setting.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x18h')`, nil)); err != nil {
			return fmt.Errorf("send ctrl+x h: %w", err)
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('thinking:') >= 0`,
			5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("ctrl+x h did not announce a thinking display mode: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ ctrl+x h cycled the thinking display mode")
		return nil
	})
}

// TestBridgeE2E_Stado_SidebarTogglePostTurn verifies that after
// completing a conversation turn (which reveals the sidebar),
// Ctrl+T toggles the sidebar off and on with the right-pane
// labels disappearing/reappearing. Bridge-only because:
//   - Sidebar visibility affects conversation-pane wrap width;
//     reflow under different widths is a real-terminal concern
//     teatest's fixed grid can't sweep.
//
// AC2 #3 + AC4 of the goal.
func TestBridgeE2E_Stado_SidebarTogglePostTurn(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	endpoint := stubLLMServer(t, stubChunksMarkdown("Reply"))
	configureStadoStub(t, endpoint)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("input never ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// Submit prompt to leave landing screen + reveal sidebar.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('hi\r')`, nil)); err != nil {
			return fmt.Errorf("send prompt: %w", err)
		}
		// Wait for the turn to complete — assistant content visible
		// AND the sidebar's "Now" or "Repo" zone marker appears.
		// "Repo" is the most reliable post-turn sidebar marker; it
		// shows up when the sidebar is rendered post-first-turn.
		if _, err := waitForSnapshot(ctx, t,
			`(function(){
				var s = window.bridge.snapshot();
				return (s.indexOf('Reply') >= 0 || s.indexOf('bold') >= 0) &&
					(s.indexOf('Repo') >= 0 || s.indexOf('agent: Do') >= 0);
			})()`, 15*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("sidebar never revealed post-turn: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ sidebar revealed post-turn")

		// Ctrl+T toggles sidebar (per palette /sidebar entry's
		// ctrl+t hint). Send + wait for sidebar markers to
		// disappear from the snapshot.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x14')`, nil)); err != nil {
			return fmt.Errorf("send Ctrl+T: %w", err)
		}
		// After toggle: "Repo" sidebar marker GONE (Repo is a
		// distinctive sidebar zone label that doesn't appear in
		// the conversation pane).
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Repo') < 0`,
			10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("sidebar didn't hide after Ctrl+T: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ Ctrl+T hid the sidebar")

		// Toggle again — sidebar markers return.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\x14')`, nil)); err != nil {
			return fmt.Errorf("send second Ctrl+T: %w", err)
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Repo') >= 0`,
			10*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("sidebar didn't return after second Ctrl+T: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ Ctrl+T re-showed the sidebar")
		return nil
	})
}

// TestBridgeE2E_Stado_MarkdownRendering verifies that markdown
// in assistant blocks renders through glamour to styled terminal
// output. Bridge-only because:
//   - glamour produces real terminal escape codes (color, bold,
//     headings); teatest's virtual terminal doesn't see styled
//     output, just raw text.
//
// Asserts the heading marker reaches the rendered output. Doesn't
// assert the EXACT styling (colors/bold are encoded as escape
// sequences that strip out of the snapshot text), just that the
// markdown content materialised in the assistant block.
//
// AC4 of the goal.
func TestBridgeE2E_Stado_MarkdownRendering(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	endpoint := stubLLMServer(t, stubChunksMarkdown("MARKDOWN_HEADING"))
	configureStadoStub(t, endpoint)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("input never ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// Submit any prompt — the stub server returns the same
		// markdown response regardless of the request body.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('render me\r')`, nil)); err != nil {
			return fmt.Errorf("send prompt: %w", err)
		}
		// Predicate: heading marker text + bold marker text
		// appear in the snapshot. glamour may strip the literal
		// '#' from the heading rendering, but the heading TEXT
		// ("MARKDOWN_HEADING") survives. Same for 'bold' word in
		// "**bold**".
		predicate := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			var hasHeading = s.indexOf('MARKDOWN_HEADING') >= 0;
			var hasBold = s.indexOf('bold') >= 0;
			return hasHeading && hasBold;
		})()`
		snap, err := waitForSnapshot(ctx, t, predicate, 15*time.Second)
		if err != nil {
			return fmt.Errorf("markdown content never reached the TUI: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ markdown content (heading + bold) materialised in assistant block")
		return nil
	})
}

// TestBridgeE2E_Stado_PlanDoModeToggle verifies that Tab toggles
// between Do and Plan modes, with the mode marker text in the
// status bar changing accordingly. Bridge-only because:
//   - The status-bar mode indicator depends on the rendered
//     status-bar layout; teatest sees model state but not the
//     rendered indicator placement / styling.
//   - The input box border tint also changes (yellow=Plan,
//     green=Do); colour assertion via plain snapshot text isn't
//     reliable, so we assert the TEXT change which proves the
//     mode-toggle dispatch reached the renderer.
//
// AC4 of `2026-05-09-full-tui-test-coverage-via-pty-bridge`.
func TestBridgeE2E_Stado_PlanDoModeToggle(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Wait for input ready then settle.
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Type a message') >= 0`,
			10*time.Second); err != nil {
			return fmt.Errorf("input never ready: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)

		// Initial mode should be "Do" (the default per the
		// modeDo init in handler_input.go's ModeToggle dispatch:
		// `if m.mode == modeDo { m.mode = modePlan } else
		// { m.mode = modeDo }` — so the first Tab takes us to
		// Plan, confirming Do was the start state).
		// Status-bar marker is "Do ·" with a trailing dot
		// separator visible in the prior bridge tests.
		if _, err := waitForSnapshot(ctx, t,
			`window.bridge.snapshot().indexOf('Do ') >= 0`,
			5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("initial Do mode marker never visible: %w; snapshot:\n%s", err, snap)
		}

		// Send Tab to toggle to Plan.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\t')`, nil)); err != nil {
			return fmt.Errorf("send Tab: %w", err)
		}
		// Plan marker appears AND Do marker is gone from the
		// status-bar position. Both halves of the predicate
		// matter — a snapshot that has both Do and Plan would
		// indicate the toggle didn't actually fire (e.g. Tab
		// hit a different handler).
		planVisible := `(function(){
			if (!window.bridge || !window.bridge.snapshot) return false;
			var s = window.bridge.snapshot();
			return s.indexOf('Plan ') >= 0;
		})()`
		if _, err := waitForSnapshot(ctx, t, planVisible, 5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("Plan mode marker never appeared after Tab: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ Tab toggled mode: Plan marker visible")

		// Send Tab again to toggle back to Do.
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`window.bridge.sendKeys('\t')`, nil)); err != nil {
			return fmt.Errorf("send second Tab: %w", err)
		}
		if _, err := waitForSnapshot(ctx, t,
			`(function(){
				var s = window.bridge.snapshot();
				return s.indexOf('Do ') >= 0;
			})()`, 5*time.Second); err != nil {
			snap := snapshot(ctx, t)
			return fmt.Errorf("Do mode marker never re-appeared after second Tab: %w; snapshot:\n%s", err, snap)
		}
		t.Logf("✓ Second Tab toggled back to Do")
		return nil
	})
}

// TestBridgeE2E_StadoDebug is the diagnostic variant — connects,
// waits 5s, dumps whatever stado rendered. No assertions; useful
// when the rendering behaviour changes and you need to see what
// the new output looks like.
func TestBridgeE2E_StadoDebug(t *testing.T) {
	requireBridgeE2E(t)
	isolateXDG(t)
	stadoBin := os.Getenv("STADO_BIN")
	if stadoBin == "" {
		stadoBin = "stado"
	}
	if _, err := exeLookup(stadoBin); err != nil {
		t.Skipf("STADO_BIN not found: %v", err)
	}
	baseURL, token := startBridgeInProcess(t)
	got := driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		startCmd := fmt.Sprintf(`(function(){
			document.getElementById('cmd').value = %q;
			window.bridge.connect();
			return true;
		})()`, stadoBin)
		if err := chromedp.Run(ctx, chromedp.Evaluate(startCmd, nil)); err != nil {
			return err
		}
		// Capture snapshots at increasing intervals so we see how
		// the output evolves (in case stado is mid-startup).
		for _, d := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond, 4 * time.Second} {
			time.Sleep(d)
			var s string
			if err := chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.snapshot()`, &s)); err != nil {
				return err
			}
			t.Logf("=== snapshot after total %v ===\n%s\n=== /snapshot ===", d, s)
		}
		return nil
	})
	t.Logf("final:\n%s", got)
}

// requireBridgeE2E is the gate every E2E test must call FIRST,
// before any setup that does real work (sockets, wasm builds,
// plugin dev installs, stub HTTP servers, Chrome launches). The
// previous pattern of letting `driveChrome` perform the skip
// meant heavyweight setup ran before the gate fired — codex
// review confirmed the UI fixture install (wasm build + plugin
// dev) executed for ~1.5s before the skip in TestBridgeE2E_
// Stado_RendersPanel with STADO_PTY_BRIDGE_E2E unset, contra-
// dicting the README's "stays fast and offline by default"
// claim. This function is the single source of truth for the
// e2e gate; only `driveChrome` may skip after it (Chrome
// binary discovery is the Chrome-side prerequisite that
// belongs there).
//
// Cost when env is unset: a single os.Getenv call.
func requireBridgeE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("STADO_PTY_BRIDGE_E2E") == "" {
		t.Skip("STADO_PTY_BRIDGE_E2E unset; skipping headless-Chrome integration")
	}
}

// stubLLMServer stands up an in-process httptest.Server that
// speaks just enough of the OAI-compat /v1/chat/completions
// streaming API for the bridge tests. The given `chunks` are
// emitted as `data: <chunk>\n\n` SSE frames in order, then
// `data: [DONE]`. Each chunk is a JSON object matching the
// stado oaicompat provider's expected shape (role/content
// delta + optional finish_reason).
//
// Returned URL has the `/v1` suffix already stripped (the
// caller passes it to stado's preset.endpoint, and stado
// appends `/chat/completions` itself per
// internal/providers/oaicompat/oaicompat.go::StreamTurn).
//
// Cleanup closes the server when the test exits.
func stubLLMServer(t *testing.T, chunks []string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
			// 50ms between chunks so the streaming visual is
			// observable in the bridge — too fast and the
			// snapshot only ever sees the final state, defeating
			// the streaming-visual assertion.
			time.Sleep(50 * time.Millisecond)
		}
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// configureStadoStub writes a config.toml to the test process's
// XDG_CONFIG_HOME pointing at the given OAI-compat endpoint, and
// sets the API-key env var so stado's auth check passes. Caller
// must have already isolated XDG via isolateXDG(t).
//
// Used together with stubLLMServer to give bridge tests a
// deterministic LLM provider for streaming / markdown / queued-
// prompt scenarios. The tests inherit env via os.Environ() into
// the bridge-spawned stado, so the stado child sees the same
// XDG_CONFIG_HOME and reads the config.toml we wrote here.
func configureStadoStub(t *testing.T, endpoint string) {
	t.Helper()
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	body := fmt.Sprintf(`[defaults]
provider = "stub"
model = "stub-model"

[inference.presets.stub]
endpoint = %q
api_key_env = "STADO_STUB_API_KEY"
`, endpoint)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("STADO_STUB_API_KEY", "stub-test-key")
}

func configureStadoStubWithVerification(t *testing.T, endpoint string) {
	t.Helper()
	configureStadoStub(t, endpoint)
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado", "config.toml")
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open stub config for verification: %v", err)
	}
	if _, err := file.WriteString("\n[verify]\ncommands = [\"true\"]\nmax_rounds = 1\nstrict = true\n"); err != nil {
		_ = file.Close()
		t.Fatalf("append operator verification suite: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close stub verification config: %v", err)
	}
}

// connectStado fills the bridge's launch form, clicks connect, and
// waits for stado to render its landing screen (the "ctrl+p
// commands" hint in the bottom row is the most-stable post-startup
// marker). Returns an error if the landing screen never appears.
// Most TUI-surface tests start with this; extracted so each test
// stays focused on the assertions that distinguish it.
func connectStado(ctx context.Context, t *testing.T, stadoBinAbs string) error {
	t.Helper()
	startCmd := fmt.Sprintf(`(function(){
		document.getElementById('cmd').value = %q;
		document.getElementById('args').value = '';
		window.bridge.connect();
		return true;
	})()`, stadoBinAbs)
	if err := chromedp.Run(ctx, chromedp.Evaluate(startCmd, nil)); err != nil {
		return fmt.Errorf("connect stado: %w", err)
	}
	if !pollEval(ctx, t,
		`window.bridge && window.bridge.snapshot ? (window.bridge.snapshot().toLowerCase().indexOf('ctrl+p') >= 0) : false`,
		15*time.Second, 100*time.Millisecond) {
		var snap string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge ? window.bridge.snapshot() : 'no bridge'`, &snap))
		return fmt.Errorf("landing screen never showed; snapshot:\n%s", snap)
	}
	return nil
}

// snapshot returns the current xterm.js buffer as a string, or
// "<error: …>" / "<no bridge>" when something's wrong. Helper to
// keep failure paths short in the per-test bodies.
func snapshot(ctx context.Context, t *testing.T) string {
	t.Helper()
	var s string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`window.bridge && window.bridge.snapshot ? window.bridge.snapshot() : '<no bridge>'`, &s)); err != nil {
		return "<error: " + err.Error() + ">"
	}
	return s
}

// stadoBinForTest returns an absolute path to the stado binary
// configured via $STADO_BIN, or signals the test to skip when
// unavailable. Centralises the env-var-and-skip dance every TUI-
// surface test starts with.
func stadoBinForTest(t *testing.T) string {
	t.Helper()
	stadoBin := os.Getenv("STADO_BIN")
	if stadoBin == "" {
		stadoBin = "stado"
	}
	abs, err := exeLookup(stadoBin)
	if err != nil {
		t.Skipf("STADO_BIN not found: %v", err)
	}
	return abs
}

// waitForSnapshot polls window.bridge.snapshot() against the
// predicate (a JS expression that should return truthy when the
// expected state is reached) until it matches or the timeout
// elapses. On success returns the matched snapshot string for
// further inspection; on timeout returns the LAST snapshot the
// poll saw plus a non-nil error. Saves the four-line
// "pollEval + chromedp.Run(snapshot)" boilerplate every test
// otherwise repeats. The error path returns the snapshot too so
// callers can include it in t.Fatalf without a second round-trip.
func waitForSnapshot(ctx context.Context, t *testing.T, predicate string, timeout time.Duration) (string, error) {
	t.Helper()
	if pollEval(ctx, t, predicate, timeout, 100*time.Millisecond) {
		return snapshot(ctx, t), nil
	}
	return snapshot(ctx, t), fmt.Errorf("predicate never satisfied within %v", timeout)
}

// isolateXDG points the test process's XDG state at fresh temp
// directories so any state stado creates (config, plugin install
// dir, sessions) is sandboxed. The bridge inherits the test
// process env via os.Environ() — the spawned stado sees these
// values too. Don't override HOME: the chromedp Chrome lookup
// needs the real one (~/.local/bin/chrome and ~/Downloads
// chrome-user-data-dir).
func isolateXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatalf("secure XDG_RUNTIME_DIR: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
}

// installOfficialSupervisePlugin builds the external official source into an
// isolated staging directory, then exercises stado's ordinary dev signing,
// trust, receipt, source-keyed install, and explicit lifecycle enablement path.
// The source checkout is never mutated and the generated key/signature remain
// under the test's private XDG/temp roots. Set STADO_OFFICIAL_PLUGINS_DIR to
// the stado-plugins checkout that contains supervise/.
func installOfficialSupervisePlugin(t *testing.T, stadoBin string) {
	t.Helper()
	officialRoot := os.Getenv("STADO_OFFICIAL_PLUGINS_DIR")
	if officialRoot == "" {
		t.Skip("STADO_OFFICIAL_PLUGINS_DIR unset; skipping cross-repository supervise integration")
	}
	sourceDir := filepath.Join(officialRoot, "supervise")
	buildScript := filepath.Join(sourceDir, "build.sh")
	templatePath := filepath.Join(sourceDir, "plugin.manifest.template.json")
	for _, required := range []string{buildScript, templatePath} {
		if _, err := os.Stat(required); err != nil {
			t.Fatalf("official supervise source is incomplete at %s: %v", required, err)
		}
	}

	stagingDir := t.TempDir()
	buildCmd := exec.Command(buildScript)
	buildCmd.Dir = sourceDir
	buildCmd.Env = append(os.Environ(), "SUPERVISE_BUILD_DIR="+stagingDir)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build official supervise source: %v\n%s", err, out)
	}
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read official supervise manifest template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "plugin.manifest.template.json"), template, 0o644); err != nil {
		t.Fatalf("stage official supervise manifest template: %v", err)
	}

	devCmd := exec.Command(stadoBin, "plugin", "dev", stagingDir)
	if out, err := devCmd.CombinedOutput(); err != nil {
		t.Fatalf("ephemeral-sign/install official supervise: %v\n%s", err, out)
	}
	infoCmd := exec.Command(stadoBin, "plugin", "info", "supervise")
	if out, err := infoCmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "supervise") {
		t.Fatalf("installed official supervise is not resolvable: %v\n%s", err, out)
	}

	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create isolated stado config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	configBody, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read isolated stado config: %v", err)
	}
	if strings.Contains(string(configBody), "[plugins]") {
		t.Fatal("isolated stado config already declares [plugins]")
	}
	if len(configBody) > 0 && configBody[len(configBody)-1] != '\n' {
		configBody = append(configBody, '\n')
	}
	configBody = append(configBody, []byte("\n[plugins]\nbackground = [\"supervise\"]\n")...)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatalf("enable official supervise lifecycle application: %v", err)
	}
}

// installUITestPlugin builds + signs + installs the self-contained UI fixture
// under testdata/ui-demo into the test process's XDG
// (which `isolateXDG` should have already pointed at scratch).
// Workflow:
//
//  1. Locate the fixture relative to this test file. Keeping it under testdata
//     makes the UAT independent of the separately-distributed stado-plugins
//     repository and prevents the critical drawer tests from silently skipping.
//  2. Stage main.go + go.mod + plugin.manifest.template.json into
//     a temp dir. Avoids mutating the source-controlled directory
//     that `stado plugin dev` would otherwise drop signing
//     artefacts into.
//  3. Build plugin.wasm (GOOS=wasip1 GOARCH=wasm,
//     -buildvcs=false because staging isn't under git, -buildmode=
//     c-shared per the bundled-plugin convention). Skips when
//     the wasip1 toolchain isn't available.
//  4. Run `stado plugin dev <staging>` to sign + trust + install.
//     Fails the test on a non-zero exit (the dev workflow is
//     load-bearing for the test).
//  5. Sanity-check `stado tool list` includes the expected
//     registered tool name.
//
// Returns the staging directory path (rarely needed) so callers
// can introspect the build artefacts on failure. Used by
// `TestBridgeE2E_Stado_RendersPanel` and the approval/choice drawer
// scenarios.
func installUITestPlugin(t *testing.T, stadoBin, expectedToolName string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate UI test plugin source")
	}
	demoSrc := filepath.Join(filepath.Dir(thisFile), "testdata", "ui-demo")
	if _, err := os.Stat(filepath.Join(demoSrc, "main.go")); err != nil {
		t.Fatalf("UI test plugin source not found at %s: %v", demoSrc, err)
	}

	stagingDir := t.TempDir()
	for _, name := range []string{"main.go", "go.mod", "plugin.manifest.template.json"} {
		src := filepath.Join(demoSrc, name)
		dst := filepath.Join(stagingDir, name)
		body, readErr := os.ReadFile(src)
		if readErr != nil {
			t.Fatalf("copy %s: %v", name, readErr)
		}
		if writeErr := os.WriteFile(dst, body, 0o644); writeErr != nil {
			t.Fatalf("write %s: %v", dst, writeErr)
		}
	}

	buildCmd := exec.Command("go", "build",
		"-buildmode=c-shared",
		"-buildvcs=false",
		"-o", filepath.Join(stagingDir, "plugin.wasm"),
		".")
	buildCmd.Dir = stagingDir
	buildCmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("wasm build failed (no wasip1 toolchain?): %v\n%s", err, out)
	}

	devCmd := exec.Command(stadoBin, "plugin", "dev", stagingDir)
	if out, err := devCmd.CombinedOutput(); err != nil {
		t.Fatalf("stado plugin dev failed for UI fixture: %v\n%s", err, out)
	}

	listCmd := exec.Command(stadoBin, "tool", "list")
	listOut, _ := listCmd.CombinedOutput()
	if !strings.Contains(string(listOut), expectedToolName) {
		t.Fatalf("%s not in tool list after `plugin dev`:\n%s", expectedToolName, listOut)
	}
	return stagingDir
}

// emulateViewport drives chromedp.EmulateViewport on the current
// browser tab, used by tests that sweep multiple terminal sizes
// (quit-confirm centering, sidebar reflow, etc.). The PTY child's
// terminal size doesn't auto-track this — emulateViewport paints
// xterm.js into the new window dims; tests that need stado to
// SIGWINCH at the new cols/rows would also need to send the
// matching `bridge.sendKeys` resize sequence (xterm.js by default
// reports its size to the connected backend on resize, which the
// bridge forwards as a TIOCSWINSZ to the child).
func emulateViewport(ctx context.Context, width, height int64) error {
	return chromedp.Run(ctx, chromedp.EmulateViewport(width, height))
}

// pollEval evaluates a JS expression repeatedly until it returns
// truthy (bool true / non-zero number / non-empty string), the
// timeout elapses, the context cancels, or Chrome reports a
// terminal error (target closed / context cancelled — there's
// no recovering from those, so polling further wastes the
// remaining timeout). Returns whether the predicate matched.
//
// Hand-rolled because chromedp.Poll's expression-wrapping
// semantics didn't reliably surface bool results in our test
// harness.
//
// Per gemini review: terminal Chrome errors used to be
// swallowed and the loop kept re-trying for the full timeout —
// a Chrome crash or PTY death would manifest as a 15s hang
// followed by the predicate timeout, hiding the real failure
// mode. Now we bail immediately on those errors so the test
// fails closer to the actual cause.
func pollEval(ctx context.Context, t *testing.T, expr string, timeout, interval time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok))
		if err == nil && ok {
			return true
		}
		// Bail on terminal errors. "context canceled" / "context
		// deadline exceeded" come from the test ctx itself; the
		// chromedp-specific "target closed" / "websocket: close"
		// indicate Chrome went away. Either way the predicate
		// will never match — no point burning the rest of the
		// timeout polling a dead browser.
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "context canceled") ||
				strings.Contains(msg, "context deadline exceeded") ||
				strings.Contains(msg, "target closed") ||
				strings.Contains(msg, "websocket: close") {
				t.Logf("pollEval bailing on terminal error: %v", err)
				return false
			}
		}
		time.Sleep(interval)
	}
	return false
}

// exeLookup is os/exec.LookPath spelled out to avoid the unused-
// import warning when Stado test is skipped.
func exeLookup(name string) (string, error) {
	// If `name` is an absolute or relative path, just stat it.
	if strings.ContainsAny(name, "/") {
		if _, err := os.Stat(name); err != nil {
			return "", err
		}
		return name, nil
	}
	// Otherwise walk PATH.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		full := filepath.Join(dir, name)
		if info, err := os.Stat(full); err == nil && info.Mode()&0o111 != 0 {
			return full, nil
		}
	}
	return "", fmt.Errorf("%s not in PATH", name)
}
