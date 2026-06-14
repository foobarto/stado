package tui

import (
	"strings"

	"github.com/foobarto/stado/internal/config"
)

// cycleToolDisplayMode advances the tool-output display mode through the
// same 4-value cycle as thinking: preview -> auto -> collapsed -> expanded.
func (m *Model) cycleToolDisplayMode() {
	m.setToolDisplayMode(nextDisplayMode(m.toolMode))
}

func (m *Model) toolModeStatus() string {
	return "tool output: " + m.toolMode.label()
}

func (m *Model) setToolDisplayMode(mode displayMode) {
	m.toolMode = mode
	m.persistToolDisplayMode()
}

func (m *Model) applyConfiguredToolDisplay(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if mode, ok := parseDisplayMode(cfg.TUI.ToolDisplay); ok {
		m.toolMode = mode
	}
}

func (m *Model) persistToolDisplayMode() {
	if m.cfg == nil || strings.TrimSpace(m.cfg.ConfigPath) == "" {
		return
	}
	value := m.toolMode.String()
	if strings.EqualFold(strings.TrimSpace(m.cfg.TUI.ToolDisplay), value) {
		return
	}
	if err := config.WriteTUIToolDisplay(m.cfg.ConfigPath, value); err != nil {
		if m.state != stateStreaming && !m.compacting {
			m.appendBlock(block{kind: "system", body: "tool output: save display mode: " + err.Error()})
		}
		return
	}
	m.cfg.TUI.ToolDisplay = value
}

func (m *Model) announceToolDisplayMode() {
	if m.state == stateStreaming || m.compacting {
		return
	}
	m.appendBlock(block{kind: "system", body: m.toolModeStatus()})
}
