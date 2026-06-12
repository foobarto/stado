package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestStatusBar_LongErrorMessageStaysOneRow — a long provider/error
// message (~180 chars, e.g. an HTTP error body or "no provider
// configured: …" with a URL) must NOT overflow the one-row status bar.
//
// Repro: isolated XDG, no provider, launch at 120x40, type "hello" +
// Enter → the stream fails into stateError and m.errorMsg holds the
// full provider error. The status template rendered .ErrorMessage
// verbatim with no truncation; renderStatus then composed a "right"
// segment wider than the inner width, pad went negative, and it fell
// through to `return right + "\n"` — emitting an over-wide single line
// that the frame (lipgloss .Width(mainW)) wraps to ~4 rows spilling
// past the bottom border.
//
// The status bar is given the INNER content width (mainW); the frame
// border is added around it. So every rendered status line must be a
// single row whose display width ≤ width, or it wraps past the border.
func TestStatusBar_LongErrorMessageStaysOneRow(t *testing.T) {
	const width = 120
	m := queueModel(t)
	m.state = stateError
	// ~180 chars — a realistic provider error: HTTP status + body + URL.
	m.errorMsg = "no provider configured: POST https://api.anthropic.com/v1/messages: 401 unauthorized — authentication_error: invalid x-api-key; check ANTHROPIC_API_KEY or run `stado auth login anthropic` to re-authenticate"
	if len([]rune(m.errorMsg)) < 180 {
		t.Fatalf("test fixture error message too short (%d runes) — won't exercise overflow", len([]rune(m.errorMsg)))
	}

	got := m.renderStatus(width)
	got = strings.TrimRight(got, "\n")

	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Errorf("status bar rendered %d rows, want exactly 1:\n%s", len(lines), got)
	}
	for i, line := range lines {
		if lw := lipgloss.Width(line); lw > width {
			t.Errorf("status line %d display width %d exceeds inner width %d (overflows past border): %q",
				i, lw, width, line)
		}
	}
	// The actionable start of the message must survive the truncation —
	// truncating the *tail* keeps the part the user reads first.
	if !strings.Contains(ansi.Strip(got), "no provider configured") {
		t.Errorf("truncation dropped the actionable start of the error: %q", ansi.Strip(got))
	}
}

// TestStatusBar_ErrorMessageNeverOverflows_AcrossWidths — the one-row
// guarantee must hold across narrow-to-wide terminals and for
// content with wide (display-width-2) runes, where a rune-count cap
// would under-budget and still overflow. ansi.Truncate is the
// display-width-aware truncator that keeps the row within budget.
func TestStatusBar_ErrorMessageNeverOverflows_AcrossWidths(t *testing.T) {
	cases := map[string]string{
		"ascii": strings.Repeat("provider error: connection refused; ", 10),
		"wide":  strings.Repeat("接続が拒否されました ", 30), // CJK: 1 rune == 2 display cells
		"mixed": "POST https://api.example.com/v1: 503 — " + strings.Repeat("サービス利用不可 ", 20),
	}
	for _, width := range []int{40, 60, 80, 120, 200} {
		for name, msg := range cases {
			m := queueModel(t)
			m.state = stateError
			m.errorMsg = msg
			got := strings.TrimRight(m.renderStatus(width), "\n")
			lines := strings.Split(got, "\n")
			if len(lines) != 1 {
				t.Errorf("[%s w=%d] rendered %d rows, want 1:\n%s", name, width, len(lines), got)
			}
			for i, line := range lines {
				if lw := lipgloss.Width(line); lw > width {
					t.Errorf("[%s w=%d] line %d display width %d > inner width %d: %q",
						name, width, i, lw, width, line)
				}
			}
		}
	}
}
