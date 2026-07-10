package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/changelog"
)

// #22: the landing screen shows a brief changelog summary anchored to the
// upper-left corner.
func TestLandingShowsWhatsNewUpperLeft(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// Use a height where the changelog and full branded banner both fit.
	// Shorter terminals intentionally prioritize the banner; that behavior is
	// pinned separately by TestLanding_WhatsNewYieldsThenReturns.
	m.width, m.height = 120, 60

	out := ansi.Strip(m.renderLanding(120, 60))
	rel := changelog.Latest()

	if !strings.Contains(out, "what's new") {
		t.Fatalf("landing should show the 'what's new' header:\n%s", out)
	}
	if rel.Version != "" && !strings.Contains(out, rel.Version) {
		t.Errorf("landing should show the latest version %q", rel.Version)
	}
	// Upper rows: the header should land near the top, left-aligned (column 0).
	lines := strings.Split(out, "\n")
	foundTop := false
	for i := 0; i < 3 && i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimRight(lines[i], " "), "what's new") {
			foundTop = true
		}
	}
	if !foundTop {
		top := lines
		if len(top) > 6 {
			top = top[:6]
		}
		t.Errorf("'what's new' should be in the upper-left rows, got:\n%s", strings.Join(top, "\n"))
	}
}
