package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/config"
)

func TestThinkingDisplayModesAffectRenderedBlocks(t *testing.T) {
	m := scenarioModel(t)
	m.vp.SetWidth(100)
	m.vp.SetHeight(20)

	var body strings.Builder
	for i := 1; i <= 12; i++ {
		body.WriteString(fmt.Sprintf("line %02d\n", i))
	}
	// streaming defaults false — these are finished blocks.
	m.blocks = []block{
		{kind: "thinking", body: body.String()},
		{kind: "assistant", body: "final answer"},
	}

	// expanded: full body.
	m.setThinkingDisplayMode(displayExpanded)
	m.renderBlocks()
	full := ansi.Strip(m.vp.View())
	if !strings.Contains(full, "line 01") || !strings.Contains(full, "line 12") {
		t.Fatalf("expanded mode should render full thinking: %q", full)
	}

	// preview: tail only, with a truncation marker.
	m.setThinkingDisplayMode(displayPreview)
	m.renderBlocks()
	preview := ansi.Strip(m.vp.View())
	if strings.Contains(preview, "line 01") || !strings.Contains(preview, "line 12") {
		t.Fatalf("preview mode should render only recent thinking: %q", preview)
	}
	if !strings.Contains(preview, "...") {
		t.Fatalf("preview mode should mark truncation: %q", preview)
	}

	// collapsed: a single line — the header stays (unlike the old "hide"),
	// the body is gone, and a line-count hint appears.
	m.setThinkingDisplayMode(displayCollapsed)
	m.renderBlocks()
	collapsed := ansi.Strip(m.vp.View())
	if strings.Contains(collapsed, "line 12") {
		t.Fatalf("collapsed mode should hide the thinking body: %q", collapsed)
	}
	if !strings.Contains(collapsed, "thinking") || !strings.Contains(collapsed, "12 lines") {
		t.Fatalf("collapsed mode should keep a one-line header + count: %q", collapsed)
	}
	if !strings.Contains(collapsed, "final answer") {
		t.Fatalf("collapsed mode should keep assistant blocks: %q", collapsed)
	}
}

func TestThinkingAutoCollapsesWhenStreamingFinishes(t *testing.T) {
	m := scenarioModel(t)
	m.vp.SetWidth(100)
	m.vp.SetHeight(20)

	var body strings.Builder
	for i := 1; i <= 12; i++ {
		body.WriteString(fmt.Sprintf("line %02d\n", i))
	}
	m.setThinkingDisplayMode(displayAuto)
	m.blocks = []block{{kind: "thinking", body: body.String(), streaming: true}}

	m.renderBlocks()
	streaming := ansi.Strip(m.vp.View())
	if !strings.Contains(streaming, "line 01") || !strings.Contains(streaming, "line 12") {
		t.Fatalf("auto mode should render full while streaming: %q", streaming)
	}

	// Finishing the block collapses it to one line.
	m.finalizeStreamingBlocks()
	m.renderBlocks()
	done := ansi.Strip(m.vp.View())
	if strings.Contains(done, "line 12") {
		t.Fatalf("auto mode should collapse the body once streaming finishes: %q", done)
	}
	if !strings.Contains(done, "thinking") || !strings.Contains(done, "12 lines") {
		t.Fatalf("auto-collapsed block should show a one-line header + count: %q", done)
	}
}

func TestThinkingSlashSetsAndCyclesMode(t *testing.T) {
	m := scenarioModel(t)
	_ = m.handleSlash("/thinking collapsed")
	if m.thinkingMode != displayCollapsed {
		t.Fatalf("mode = %s, want collapsed", m.thinkingMode)
	}

	_ = m.handleSlash("/thinking expanded")
	if m.thinkingMode != displayExpanded {
		t.Fatalf("mode = %s, want expanded", m.thinkingMode)
	}

	// No-arg cycles: expanded -> preview (wraps).
	_ = m.handleSlash("/thinking")
	if m.thinkingMode != displayPreview {
		t.Fatalf("mode = %s, want preview after cycle from expanded", m.thinkingMode)
	}
}

func TestThinkingDisplayLoadsLegacyValueFromConfig(t *testing.T) {
	m := scenarioModel(t)
	// Legacy "tail" maps to preview so old configs keep working.
	m.applyConfiguredThinkingDisplay(&config.Config{
		TUI: config.TUI{ThinkingDisplay: "tail"},
	})
	if m.thinkingMode != displayPreview {
		t.Fatalf("legacy tail -> %s, want preview", m.thinkingMode)
	}

	m.applyConfiguredThinkingDisplay(&config.Config{
		TUI: config.TUI{ThinkingDisplay: "auto"},
	})
	if m.thinkingMode != displayAuto {
		t.Fatalf("mode = %s, want auto", m.thinkingMode)
	}
}

func TestThinkingDisplayPersistsToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nmodel = \"m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := scenarioModel(t)
	m.cfg = &config.Config{ConfigPath: path}

	m.setThinkingDisplayMode(displayCollapsed)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`model = "m"`, `[tui]`, `thinking_display = "collapsed"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestThinkingKeybindCyclesMode(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "h"})
	if m.thinkingMode != displayAuto {
		t.Fatalf("mode = %s, want auto", m.thinkingMode)
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "h"})
	if m.thinkingMode != displayCollapsed {
		t.Fatalf("mode = %s, want collapsed", m.thinkingMode)
	}
}

func TestThinkingToggleDuringStreamingDoesNotAppendSystemBlock(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.blocks = []block{{kind: "thinking", body: "still reasoning"}}

	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "h"})

	if len(m.blocks) != 1 {
		t.Fatalf("streaming display toggle should not append transcript blocks: %+v", m.blocks)
	}
	if m.thinkingMode != displayAuto {
		t.Fatalf("mode = %s, want auto", m.thinkingMode)
	}
}
