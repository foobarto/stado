package fleetpicker

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
)

// Codex G4/J-b P0 regression: FleetEntry fields (Prompt, LastTool,
// LastText, Error, Result) are populated by headless agent runs and
// are fully untrusted from the terminal-escape perspective. Pre-fix
// the picker's row + detail-pane renderers concatenated them into
// lipgloss output without any sanitize — an attacker-influenced
// prompt could plant OSC52 (clipboard hijack), OSC8 (clickable
// link), or CSI cursor moves and have them reach the operator's
// terminal whenever they popped the fleet picker.
//
// After fix every FleetEntry string passes through
// `textutil.StripControlChars` at the render boundary; both the
// single-line row and the detail-pane field rendering layouts stay
// intact, and no escape can survive a truncate boundary.
func TestRenderEntryRow_StripsEscapes(t *testing.T) {
	const osc52 = "\x1b]52;c;evil\x07"
	const osc8 = "\x1b]8;;https://evil\x1b\\link\x1b]8;;\x1b\\"
	const csi = "\x1b[2K\x1b[1;1H"

	e := runtime.FleetEntry{
		FleetID:  "fleet-1234567890",
		Status:   runtime.FleetStatusRunning,
		Prompt:   "investigate " + osc52,
		LastTool: "bash" + osc8,
	}
	row := renderEntryRow(e, 120)
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(row, esc) {
			t.Errorf("renderEntryRow leaks %q escape: %q", esc, row)
		}
	}
	// FleetID is treated as a known-clean short-id; just confirm it
	// renders fine alongside sanitized fields.
	if !strings.Contains(row, "fleet-12") {
		t.Errorf("row should contain fleet-id prefix; got %q", row)
	}
	// Tool name without escape should survive intact (basic
	// passthrough check).
	if !strings.Contains(row, "bash") {
		t.Errorf("row should still contain 'bash' tool name; got %q", row)
	}

	d := runtime.FleetEntry{
		FleetID:   "fleet-1234567890",
		Status:    runtime.FleetStatusError,
		Prompt:    "p " + csi,
		SessionID: "sess-" + osc52,
		LastText:  "last " + osc8,
		Error:     "err " + csi,
	}
	detail := renderEntryDetail(d, 120)
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(detail, esc) {
			t.Errorf("renderEntryDetail leaks %q escape: %q", esc, detail)
		}
	}

	r := runtime.FleetEntry{
		FleetID: "fleet-1234567890",
		Status:  runtime.FleetStatusCompleted,
		Prompt:  "p",
		Result:  "ok " + osc52,
	}
	rdetail := renderEntryDetail(r, 120)
	for _, esc := range []string{"\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(rdetail, esc) {
			t.Errorf("renderEntryDetail(Result) leaks %q escape: %q", esc, rdetail)
		}
	}
}
