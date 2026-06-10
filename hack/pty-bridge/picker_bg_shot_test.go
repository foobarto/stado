package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestBridgeShot_ModelPickerBackground opens the /model picker in the real
// xterm.js bridge and dumps the per-cell background colours of its rows, so
// the holey-bg fix can be confirmed at ground truth.
//
// Run: STADO_PTY_BRIDGE_E2E=1 STADO_BIN=/abs/stado go test -run TestBridgeShot_ModelPickerBackground -v .
func TestBridgeShot_ModelPickerBackground(t *testing.T) {
	requireBridgeE2E(t)
	stadoBinAbs := stadoBinForTest(t)
	baseURL, token := startBridgeInProcess(t)

	driveChrome(t, baseURL+"/?token="+token, func(ctx context.Context) error {
		if err := connectStado(ctx, t, stadoBinAbs); err != nil {
			return err
		}
		if !pollEval(ctx, t,
			`window.bridge && window.bridge.snapshot && window.bridge.snapshot().indexOf('Type a message') >= 0`,
			15*time.Second, 200*time.Millisecond) {
			return fmt.Errorf("TUI never rendered; snapshot:\n%s", snapshot(ctx, t))
		}
		// Open the model picker: type "/model" + Enter.
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.bridge.sendKeys('/model\r')`, nil)); err != nil {
			return fmt.Errorf("send /model: %w", err)
		}
		if !pollEval(ctx, t,
			`window.bridge && window.bridge.snapshot && window.bridge.snapshot().indexOf('Select a model') >= 0`,
			10*time.Second, 200*time.Millisecond) {
			return fmt.Errorf("model picker never opened; snapshot:\n%s", snapshot(ctx, t))
		}
		time.Sleep(400 * time.Millisecond)

		var png []byte
		if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
			return fmt.Errorf("screenshot: %w", err)
		}
		out := os.Getenv("STADO_SHOT_OUT")
		if out == "" {
			out = "/tmp/stado-modelpicker.png"
		}
		_ = os.WriteFile(out, png, 0o644)
		t.Logf("screenshot written to %s", out)

		// Dump per-row distinct bg counts for the picker rows (any row that
		// has a name or the 'Select a model' header / 'Search' label).
		js := `(function(){
			var buf = term.buffer.active, rows = [];
			for (var y = buf.viewportY; y < buf.viewportY + term.rows; y++) {
				var line = buf.getLine(y); if (!line) continue;
				var text = line.translateToString(true);
				if (text.replace(/\s/g,'') === '') continue;
				var def=0, rgb=0;
				for (var x=0; x<term.cols; x++){ var c=line.getCell(x); if(!c) continue;
					if (c.isBgDefault()) def++; else rgb++; }
				rows.push('def='+def+' painted='+rgb+' | '+text.slice(0,46));
			}
			return rows.join('\n');
		})()`
		var dump string
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &dump)); err != nil {
			return fmt.Errorf("cell dump: %w", err)
		}
		t.Logf("model-picker row backgrounds:\n%s", dump)
		return nil
	})
}
