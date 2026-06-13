package main

import (
	"testing"
	"time"
)

// TestParseUsageTimeWindow_InvertedErrors: an inverted window (--until before
// --since, both set) should ERROR rather than silently print a reversed-window
// header with no rows. Reproduce-first for the 2026-06-13 deferred UAT item.
func TestParseUsageTimeWindow_InvertedErrors(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	// since=1d-ago, until=7d-ago → until is BEFORE since → inverted.
	if _, _, err := parseUsageTimeWindow("1d", "7d", now); err == nil {
		t.Errorf("inverted window (--since 1d --until 7d) did not error")
	}

	// Valid window: since=7d-ago, until=1d-ago (the day-1-to-7 window).
	if _, _, err := parseUsageTimeWindow("7d", "1d", now); err != nil {
		t.Errorf("valid window (--since 7d --until 1d) errored: %v", err)
	}

	// Single-bound (only --since) must still work (until unset = open).
	if _, _, err := parseUsageTimeWindow("7d", "", now); err != nil {
		t.Errorf("single-bound --since errored: %v", err)
	}
	// Equal bounds are not inverted.
	if _, _, err := parseUsageTimeWindow("1d", "1d", now); err != nil {
		t.Errorf("equal bounds errored: %v", err)
	}
}
