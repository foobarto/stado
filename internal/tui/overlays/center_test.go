package overlays

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestCenterOver_BaseRowsPreserved: rows above and below the popup
// area come through unchanged from base, so chat context surrounding
// the modal stays visible (the whole point vs lipgloss.Place).
func TestCenterOver_BaseRowsPreserved(t *testing.T) {
	base := strings.Join([]string{
		"row-0----------",
		"row-1----------",
		"row-2----------",
		"row-3----------",
		"row-4----------",
		"row-5----------",
		"row-6----------",
		"row-7----------",
		"row-8----------",
		"row-9----------",
	}, "\n")
	popup := strings.Join([]string{
		"+-----+",
		"|popup|",
		"+-----+",
	}, "\n")
	out := ansi.Strip(CenterOver(base, popup, 15, 10))
	rows := strings.Split(out, "\n")
	if len(rows) != 10 {
		t.Fatalf("rows = %d, want 10", len(rows))
	}
	if rows[0] != "row-0----------" || rows[1] != "row-1----------" {
		t.Errorf("top rows clobbered: %q / %q", rows[0], rows[1])
	}
	if rows[9] != "row-9----------" || rows[8] != "row-8----------" {
		t.Errorf("bottom rows clobbered: %q / %q", rows[8], rows[9])
	}
	// Popup occupies 3 rows centred — at heights 10 / popup-3 the
	// top pad is (10-3)/2 = 3, so popup goes on rows 3, 4, 5.
	if !strings.Contains(rows[4], "popup") {
		t.Errorf("popup body missing from row 4: %q", rows[4])
	}
}

// TestCenterOver_PopupTooTallBails: when popup ≥ canvas height there's
// no room to overlay, so we return the popup as-is rather than
// clipping it.
func TestCenterOver_PopupTooTallBails(t *testing.T) {
	base := "ignored"
	popup := strings.Join([]string{"a", "b", "c", "d"}, "\n")
	out := CenterOver(base, popup, 5, 3)
	if out != popup {
		t.Errorf("oversized popup should pass through; got %q", out)
	}
}

// TestCenterOver_ZeroDims: defensive — width/height = 0 returns base
// unchanged so callers don't need to guard the call site.
func TestCenterOver_ZeroDims(t *testing.T) {
	if got := CenterOver("base", "popup", 0, 10); got != "base" {
		t.Errorf("zero width: got %q", got)
	}
	if got := CenterOver("base", "popup", 10, 0); got != "base" {
		t.Errorf("zero height: got %q", got)
	}
}
