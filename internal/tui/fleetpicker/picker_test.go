package fleetpicker

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/foobarto/stado/internal/runtime"
)

// sgrEscapeRE matches lipgloss's own SGR colour styling (ESC [ … m).
// v2 lipgloss emits SGR from Style.Render unconditionally, whereas v1
// produced plain text in non-TTY test runs. stripSGR removes only that
// legitimate styling so the leak checks still catch injected OSC/CSI/BEL
// sequences (which do not end in 'm').
var sgrEscapeRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripSGR(s string) string { return sgrEscapeRE.ReplaceAllString(s, "") }

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
	row := stripSGR(renderEntryRow(e, 120))
	for _, esc := range []string{"\x1b", "\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
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
	detail := stripSGR(renderEntryDetail(d, 120))
	for _, esc := range []string{"\x1b", "\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
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
	rdetail := stripSGR(renderEntryDetail(r, 120))
	for _, esc := range []string{"\x1b", "\x1b]52", "\x1b]8", "\x1b[", "\x07"} {
		if strings.Contains(rdetail, esc) {
			t.Errorf("renderEntryDetail(Result) leaks %q escape: %q", esc, rdetail)
		}
	}
}

// truncate is fed FleetEntry strings (prompts, last-tool/text, errors)
// that originate from headless agent runs and may contain wide runes
// (CJK, emoji) or multi-byte UTF-8. The byte-slicing version sliced
// mid-rune for any non-ASCII input longer than the budget, producing
// (a) invalid UTF-8 — raw continuation bytes leak into the terminal
// and corrupt the display, and (b) a display width that bears no
// relation to the requested column budget, so renderEntryRow's
// lipgloss.Width-based padding math drifts and the row overflows /
// the "last:" column misaligns. The budget n is a *column* budget
// (renderEntryRow pads against lipgloss.Width), so truncate must be
// display-width aware and always return valid UTF-8.
func TestTruncate_WideRunesValidUTF8AndWidthBound(t *testing.T) {
	// Each CJK rune is 3 bytes / 2 display columns.
	const cjk = "你好世界这是一个很长的中文提示需要被截断显示在窄面板里啊"
	for _, n := range []int{4, 10, 30, 50} {
		out := truncate(cjk, n)
		if !utf8.ValidString(out) {
			t.Errorf("truncate(cjk, %d) = %q: not valid UTF-8 (mid-rune byte slice leaks into terminal)", n, out)
		}
		if w := lipgloss.Width(out); w > n {
			t.Errorf("truncate(cjk, %d) width = %d, want <= %d (column budget overrun breaks row padding)", n, w, n)
		}
	}

	// ASCII passthrough must still hold: short input returned verbatim,
	// long input ends with the ellipsis and fits the budget.
	if got := truncate("hello", 50); got != "hello" {
		t.Errorf("truncate short ASCII = %q, want %q", got, "hello")
	}
	long := truncate(strings.Repeat("x", 80), 10)
	if w := lipgloss.Width(long); w > 10 {
		t.Errorf("truncate long ASCII width = %d, want <= 10", w)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("truncate long ASCII = %q, want trailing ellipsis", long)
	}
}

// A long prompt must never push the rendered row past the modal's
// inner width. Pre-fix the prompt was truncated to a fixed 50 columns
// regardless of innerW, so in the narrowest realistic modal (64-wide
// screen → modalW 64 → innerW 60) a long prompt produced an ~83-column
// row, shoving the "last:" column past the right border (mis-wrap /
// clip). renderEntryRow now sizes the prompt to the remaining width.
func TestRenderEntryRow_DoesNotOverflowInnerWidth(t *testing.T) {
	cases := []runtime.FleetEntry{
		{
			FleetID:  "fleet-1234567890",
			Status:   runtime.FleetStatusRunning,
			Prompt:   "investigate the failing integration test in the payment gateway module thoroughly please",
			LastTool: "bash",
		},
		{
			FleetID:  "fleet-1234567890",
			Status:   runtime.FleetStatusRunning,
			Prompt:   "你好世界这是一个很长的中文提示需要被截断显示在窄面板里啊调查问题排查故障",
			LastTool: "fs.read",
		},
	}
	// innerW=60 is the floor (modalW clamps to 64). 116 is the ceiling.
	for _, innerW := range []int{60, 72, 80, 116} {
		for _, e := range cases {
			row := stripSGR(renderEntryRow(e, innerW))
			if w := lipgloss.Width(row); w > innerW {
				t.Errorf("innerW=%d: row width=%d exceeds budget; row=%q", innerW, w, row)
			}
			if strings.Contains(row, "\n") {
				t.Errorf("innerW=%d: row wrapped to multiple lines; row=%q", innerW, row)
			}
		}
	}
}
