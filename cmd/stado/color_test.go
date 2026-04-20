package main

import (
	"strings"
	"testing"
)

// TestColorizeStatus_BranchByStatus: every known status gets its
// own ANSI colour; unknown falls through unchanged.
func TestColorizeStatus_BranchByStatus(t *testing.T) {
	cases := map[string]string{
		"live":     ansiGreen,
		"idle":     ansiGrey,
		"detached": ansiDim,
	}
	for status, wantPrefix := range cases {
		got := colorizeStatus(status, "padded-cell")
		if !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("status %q: got %q, expected prefix %q", status, got, wantPrefix)
		}
		if !strings.HasSuffix(got, ansiReset) {
			t.Errorf("status %q: missing reset suffix", status)
		}
	}

	// Unknown → no ANSI wrap.
	unknown := colorizeStatus("mystery", "padded-cell")
	if unknown != "padded-cell" {
		t.Errorf("unknown status should pass through unchanged, got %q", unknown)
	}
}

// TestUseColor_NoColorDisables: NO_COLOR set → useColor returns
// false regardless of tty.
func TestUseColor_NoColorDisables(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "")
	// os.Stdout is unlikely to be a TTY under `go test` anyway;
	// assert NO_COLOR wins even if it were.
	if useColor(nil) {
		// nil would panic if we got past the env check — which it
		// shouldn't, but guard anyway.
		t.Error("NO_COLOR should short-circuit to false")
	}
}

// TestUseColor_ForceColorOverrides: FORCE_COLOR makes useColor
// return true even when output isn't a TTY.
func TestUseColor_ForceColorOverrides(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	if !useColor(nil) {
		t.Error("FORCE_COLOR should force true")
	}
}
