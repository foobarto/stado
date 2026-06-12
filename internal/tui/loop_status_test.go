package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestStatusBar_LoopIndicator (EP-0036): the bottom status bar must show a
// "↻ loop" indicator while a /loop is active — "↻ loop (Nm)" for a timed loop,
// plain "↻ loop" for immediate-repeat — and nothing when no loop is running.
// EP-0036 promised this affordance but it was never implemented (only the
// per-iteration separator existed), so a running loop had no persistent signal.
func TestStatusBar_LoopIndicator(t *testing.T) {
	const width = 120

	// No active loop: no indicator.
	m := queueModel(t)
	if got := ansi.Strip(m.renderStatus(width)); strings.Contains(got, "↻ loop") {
		t.Fatalf("status bar shows a loop indicator with no active loop: %q", got)
	}

	// Immediate-repeat loop: "↻ loop" with no interval suffix.
	m.loop = &loopState{prompt: "do x", interval: 0}
	got := ansi.Strip(m.renderStatus(width))
	if !strings.Contains(got, "↻ loop") {
		t.Fatalf("status bar missing loop indicator for an active immediate loop: %q", got)
	}
	if strings.Contains(got, "↻ loop (") {
		t.Fatalf("immediate-repeat loop must not show an interval suffix: %q", got)
	}

	// Timed loop: "↻ loop (5m)".
	m.loop = &loopState{prompt: "do x", interval: 5 * time.Minute}
	got = ansi.Strip(m.renderStatus(width))
	if !strings.Contains(got, "↻ loop (5m)") {
		t.Fatalf("status bar missing timed-loop indicator '↻ loop (5m)': %q", got)
	}
}

// TestCompactDuration checks the loop-interval label formatting used by the
// status indicator.
func TestCompactDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Minute, "5m"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{2 * time.Hour, "2h"},
	}
	for _, tc := range cases {
		if got := compactDuration(tc.d); got != tc.want {
			t.Errorf("compactDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
