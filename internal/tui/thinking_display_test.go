package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/config"
)

func TestThinkingDisplayModesAffectRenderedBlocks(t *testing.T) {
	m := scenarioModel(t)
	m.vp.Width = 100
	m.vp.Height = 20

	var body strings.Builder
	for i := 1; i <= 12; i++ {
		body.WriteString(fmt.Sprintf("line %02d\n", i))
	}
	m.blocks = []block{
		{kind: "thinking", body: body.String()},
		{kind: "assistant", body: "final answer"},
	}

	m.renderBlocks()
	full := ansi.Strip(m.vp.View())
	if !strings.Contains(full, "line 01") || !strings.Contains(full, "line 12") {
		t.Fatalf("show mode should render full thinking: %q", full)
	}

	m.setThinkingDisplayMode(thinkingTail)
	m.renderBlocks()
	tail := ansi.Strip(m.vp.View())
	if strings.Contains(tail, "line 01") || !strings.Contains(tail, "line 12") {
		t.Fatalf("tail mode should render only recent thinking: %q", tail)
	}
	if !strings.Contains(tail, "...") {
		t.Fatalf("tail mode should mark truncation: %q", tail)
	}

	m.setThinkingDisplayMode(thinkingHide)
	m.renderBlocks()
	hidden := ansi.Strip(m.vp.View())
	if strings.Contains(hidden, "thinking") || strings.Contains(hidden, "line 12") {
		t.Fatalf("hide mode should suppress thinking blocks: %q", hidden)
	}
	if !strings.Contains(hidden, "final answer") {
		t.Fatalf("hide mode should keep assistant blocks: %q", hidden)
	}
}

func TestThinkingSlashSetsAndCyclesMode(t *testing.T) {
	m := scenarioModel(t)
	_ = m.handleSlash("/thinking hide")
	if m.thinkingMode != thinkingHide {
		t.Fatalf("mode = %s, want hide", m.thinkingMode)
	}

	_ = m.handleSlash("/thinking show")
	if m.thinkingMode != thinkingShow {
		t.Fatalf("mode = %s, want show", m.thinkingMode)
	}

	_ = m.handleSlash("/thinking")
	if m.thinkingMode != thinkingTail {
		t.Fatalf("mode = %s, want tail after cycle", m.thinkingMode)
	}
}

func TestThinkingDisplayLoadsFromConfig(t *testing.T) {
	m := scenarioModel(t)
	m.applyConfiguredThinkingDisplay(&config.Config{
		TUI: config.TUI{ThinkingDisplay: "tail"},
	})
	if m.thinkingMode != thinkingTail {
		t.Fatalf("mode = %s, want tail", m.thinkingMode)
	}
}

func TestThinkingDisplayPersistsToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nmodel = \"m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := scenarioModel(t)
	m.cfg = &config.Config{ConfigPath: path}

	m.setThinkingDisplayMode(thinkingHide)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`model = "m"`, `[tui]`, `thinking_display = "hide"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
}

func TestThinkingKeybindCyclesMode(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.thinkingMode != thinkingTail {
		t.Fatalf("mode = %s, want tail", m.thinkingMode)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.thinkingMode != thinkingHide {
		t.Fatalf("mode = %s, want hide", m.thinkingMode)
	}
}

func TestThinkingToggleDuringStreamingDoesNotAppendSystemBlock(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.blocks = []block{{kind: "thinking", body: "still reasoning"}}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})

	if len(m.blocks) != 1 {
		t.Fatalf("streaming display toggle should not append transcript blocks: %+v", m.blocks)
	}
	if m.thinkingMode != thinkingTail {
		t.Fatalf("mode = %s, want tail", m.thinkingMode)
	}
}
