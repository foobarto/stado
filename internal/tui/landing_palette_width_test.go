package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestLandingInputWidensForPalette pins that the landing input card (and thus
// the inline / command palette nested in it) widens while the palette is open,
// so long command descriptions render untruncated. The compact 64-col landing
// card clipped them otherwise.
func TestLandingInputWidensForPalette(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	const W = 160

	narrow := m.landingInputW(W)
	if narrow != landingInputWidth(W) {
		t.Fatalf("with palette closed, landingInputW should equal the compact width %d, got %d",
			landingInputWidth(W), narrow)
	}

	// Open the inline palette with '/'.
	_, _ = m.Update(tea.KeyPressMsg{Text: "/"})
	if !m.slash.Visible || !m.slashInline {
		t.Fatal("inline slash palette should be visible after '/'")
	}

	wide := m.landingInputW(W)
	if wide <= narrow {
		t.Errorf("with palette open, landingInputW(%d)=%d should exceed the compact width %d", W, wide, narrow)
	}
	// Must still fit: renderInputBox(W) frames to W+2; needs <= screen width.
	if wide > W-2 {
		t.Errorf("landingInputW(%d)=%d would overflow the screen (frame=%d > %d)", W, wide, wide+2, W)
	}
}
