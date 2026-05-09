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
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
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

// chromeUserDataDir returns a path under the user's Downloads folder
// for the Chrome --user-data-dir. This works around Chrome-via-
// Flatpak sandboxing that blocks /tmp; xdg-download is whitelisted
// in the Flatpak's filesystems= context. Outside Flatpak, the path
// is just an unused folder and Chrome happily uses it.
func chromeUserDataDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	dir := filepath.Join(home, "Downloads", "stado-pty-bridge-chrome")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir user-data-dir: %v", err)
	}
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

	ctx, timeoutCancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(timeoutCancel)

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

// TestBridgeE2E_Bash drives the bridge against /bin/bash to validate
// the round-trip plumbing without depending on stado being built.
// Sends `echo HELLO_FROM_TEST<Enter>` and asserts the output appears.
func TestBridgeE2E_Bash(t *testing.T) {
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
// NOT covered here (deliberately): a real `stado_ui_render` panel
// emit. Doing that needs the render-demo-go plugin built + signed +
// trusted + installed in a temp XDG dir per test run — six steps,
// each fragile. The ASCII output of `renderPanelASCII` is already
// exhaustively unit-tested in `internal/tui/render_panel_test.go`
// (14 cases covering all six body kinds + variants + table
// narrowing + width invariants); xterm.js doesn't change how those
// bytes render. Drive that path manually if a regression is
// suspected.
func TestBridgeE2E_Stado_F9bRegression(t *testing.T) {
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
//   - the render-demo-go source can't be located (test runs outside
//     the repo, e.g. installed copy of the bridge)
//   - the wasip1 toolchain isn't available (`go build` for wasm)
//
// Allow ~10s walltime: ~3s for the wasip1 wasm build, ~2s for
// plugin dev (sign + trust + install), ~3s for the bridge + stado
// startup, ~2s for the snapshot polling. Whichever is slowest sets
// the floor.
func TestBridgeE2E_Stado_RendersPanel(t *testing.T) {
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installDemoPlugin(t, stadoBinAbs, "render-demo-go", "render_demo")

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
			var hasBorder = s.indexOf('│') >= 0;
			var headings = ['Plain text', 'Key/value pairs', 'Numbered list',
				'Bullet list', 'Checklist', 'Code (language hint)', 'Table', 'Diff'];
			var sawHeading = false;
			for (var i = 0; i < headings.length; i++) {
				if (s.indexOf(headings[i]) >= 0) { sawHeading = true; break; }
			}
			return resultParts && hasBorder && sawHeading;
		})()`
		panelMatch := pollEval(ctx, t, panelPredicate, 20*time.Second, 200*time.Millisecond)
		if !panelMatch {
			var snap string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.snapshot()`, &snap))
			return fmt.Errorf("panel never appeared; snapshot:\n%s", snap)
		}
		t.Logf("✓ panel reached renderer: result line + border char + at least one section heading visible")
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
		// Predicate: at least one box-drawing corner from the overlay
		// border + at least three canonical slash-command names from
		// the help body. Three names rather than one because help is
		// a long enumeration; checking three reduces false-positives
		// from leftover landing-screen text. The name list is the
		// broader set actually visible in the help body's "View"
		// and "Tools" sections — the original 5-name list was too
		// narrow and missed because the popup truncates older
		// sections at viewport bottom.
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
		snap, err := waitForSnapshot(ctx, t, predicate, 10*time.Second)
		if err != nil {
			return fmt.Errorf("help overlay never showed corner+command-names: %w; snapshot:\n%s", err, snap)
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
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installDemoPlugin(t, stadoBinAbs, "approval-demo-go", "approval_demo")
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
			t.Logf("warning: Esc may not have dismissed the drawer cleanly (test still passed core assertions)")
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
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	installDemoPlugin(t, stadoBinAbs, "choose-demo-go", "choose_demo")
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
func TestBridgeE2E_Stado_SlashFilter(t *testing.T) {
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
			// Palette body shows several canonical names; require
			// /sidebar AND /theme together so we know we're seeing
			// the unfiltered palette (post-filter only one will
			// remain, so this captures the pre-filter baseline).
			return s.indexOf('/sidebar') >= 0 && s.indexOf('/theme') >= 0;
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

// TestBridgeE2E_StadoDebug is the diagnostic variant — connects,
// waits 5s, dumps whatever stado rendered. No assertions; useful
// when the rendering behaviour changes and you need to see what
// the new output looks like.
func TestBridgeE2E_StadoDebug(t *testing.T) {
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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// installDemoPlugin builds + signs + installs a plugin from
// `plugins/examples/<demoName>/` into the test process's XDG
// (which `isolateXDG` should have already pointed at scratch).
// Workflow:
//
//  1. Locate the demo source via runtime.Caller — the test file
//     lives at <repo>/hack/pty-bridge/, the demo at
//     <repo>/plugins/examples/<name>/. Skips when the source can't
//     be found (e.g. test running outside the repo).
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
// `TestBridgeE2E_Stado_RendersPanel` and the drawer scenarios
// (approval-demo-go, choose-demo-go).
func installDemoPlugin(t *testing.T, stadoBin, demoName, expectedToolName string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller failed; cannot locate demo plugin source")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	demoSrc := filepath.Join(repoRoot, "plugins", "examples", demoName)
	if _, err := os.Stat(filepath.Join(demoSrc, "main.go")); err != nil {
		t.Skipf("plugin source not found at %s: %v", demoSrc, err)
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
		t.Fatalf("stado plugin dev failed for %s: %v\n%s", demoName, err, out)
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
// timeout elapses, or the context cancels. Returns whether the
// predicate matched. Hand-rolled because chromedp.Poll's
// expression-wrapping semantics didn't reliably surface bool
// results in our test harness.
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
