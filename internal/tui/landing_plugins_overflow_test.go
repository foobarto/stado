package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	stadoruntime "github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tools"
)

func TestLandingPluginsHintTruncatesAtTerminalWidth(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.executor = &tools.Executor{Registry: stadoruntime.BuildDefaultRegistry(nil)}

	const width = 24
	got := m.landingPluginsHint(width)
	if visible := lipgloss.Width(got); visible > width {
		t.Fatalf("plugin hint width = %d, want <= %d: %q", visible, width, got)
	}
	if !strings.Contains(ansi.Strip(got), "…") {
		t.Fatalf("truncated plugin hint lacks an ellipsis: %q", ansi.Strip(got))
	}
}
