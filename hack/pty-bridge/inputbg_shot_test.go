package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestBridgeShot_InputBoxBackground drives stado in the real xterm.js
// bridge and dumps (1) a PNG screenshot and (2) the per-cell background
// colours of the input-box rows, so the chat-input background can be
// inspected at ground truth instead of through a flaky tmux capture.
//
// Run: STADO_PTY_BRIDGE_E2E=1 STADO_BIN=/abs/stado go test -run TestBridgeShot_InputBoxBackground -v .
func TestBridgeShot_InputBoxBackground(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	isolateXDG(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		// Wait for the landing input box to render.
		if !pollEval(ctx, t,
			`window.bridge && window.bridge.snapshot && window.bridge.snapshot().indexOf('Type a message') >= 0`,
			15*time.Second, 200*time.Millisecond) {
			return fmt.Errorf("input box never rendered; snapshot:\n%s", snapshot(ctx, t))
		}
		time.Sleep(500 * time.Millisecond) // let the frame settle

		// (1) PNG screenshot.
		var png []byte
		if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
			return fmt.Errorf("screenshot: %w", err)
		}
		out := os.Getenv("STADO_SHOT_OUT")
		if out == "" {
			out = "/tmp/stado-inputbg.png"
		}
		if err := os.WriteFile(out, png, 0o644); err != nil {
			return fmt.Errorf("write png: %w", err)
		}
		t.Logf("screenshot written to %s (%d bytes)", out, len(png))

		// (2) Per-cell bg colours of every row that the input box touches.
		// xterm cell API: getBgColor() + isBgDefault()/isBgRGB(). We dump
		// the distinct bg values per row so an unpainted (default-bg) cell
		// inside the box stands out.
		js := `(function(){
			var buf = term.buffer.active;
			var rows = [];
			var holes = 0;
			for (var y = buf.viewportY; y < buf.viewportY + term.rows; y++) {
				var line = buf.getLine(y); if (!line) continue;
				var text = line.translateToString(true);
				if (text.indexOf('Type a message') < 0 && text.indexOf('Do ') < 0 &&
				    text.indexOf('│') < 0) continue;
				var segs = {};
				var firstPainted = -1;
				var lastPainted = -1;
				for (var x = 0; x < term.cols; x++) {
					var c = line.getCell(x); if (!c) continue;
					var key;
					if (c.isBgDefault()) key = 'DEFAULT';
					else if (c.isBgRGB()) { var v=c.getBgColor(); key='rgb('+((v>>16)&255)+','+((v>>8)&255)+','+(v&255)+')'; }
					else key = 'palette('+c.getBgColor()+')';
					if (!c.isBgDefault()) {
						if (firstPainted < 0) firstPainted = x;
						lastPainted = x;
					}
					segs[key] = (segs[key]||0)+1;
				}
				for (var x = firstPainted; x >= 0 && x <= lastPainted; x++) {
					var c = line.getCell(x);
					if (c && c.isBgDefault()) holes++;
				}
				rows.push(y + ': ' + JSON.stringify(segs) + '  | ' + text.slice(0,40));
			}
			return {dump: rows.join('\n'), holes: holes};
		})()`
		var result struct {
			Dump  string `json:"dump"`
			Holes int    `json:"holes"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
			return fmt.Errorf("cell dump: %w", err)
		}
		t.Logf("input-box row backgrounds:\n%s", result.Dump)
		if result.Holes != 0 {
			return fmt.Errorf("input box has %d unpainted cells after its painted surface begins", result.Holes)
		}
		return nil
	})
}
