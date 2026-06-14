package tui

import (
	"strings"

	"github.com/foobarto/stado/internal/config"
)

const (
	thinkingTailLines = 8
	thinkingTailRunes = 1200
)

// cycleThinkingDisplayMode advances the thinking display mode through the
// 4-value cycle: preview -> auto -> collapsed -> expanded -> preview.
func (m *Model) cycleThinkingDisplayMode() {
	m.setThinkingDisplayMode(nextDisplayMode(m.thinkingMode))
}

// nextDisplayMode is the shared preview->auto->collapsed->expanded->preview
// cycle used by both the thinking and tool keybinds / slash commands.
func nextDisplayMode(d displayMode) displayMode {
	switch d {
	case displayPreview:
		return displayAuto
	case displayAuto:
		return displayCollapsed
	case displayCollapsed:
		return displayExpanded
	default:
		return displayPreview
	}
}

func (m *Model) thinkingModeStatus() string {
	return "thinking: " + m.thinkingMode.label()
}

func (m *Model) setThinkingDisplayMode(mode displayMode) {
	m.thinkingMode = mode
	m.persistThinkingDisplayMode()
}

func (m *Model) applyConfiguredThinkingDisplay(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if mode, ok := parseDisplayMode(cfg.TUI.ThinkingDisplay); ok {
		m.thinkingMode = mode
	}
}

func (m *Model) persistThinkingDisplayMode() {
	if m.cfg == nil || strings.TrimSpace(m.cfg.ConfigPath) == "" {
		return
	}
	value := m.thinkingMode.String()
	if strings.EqualFold(strings.TrimSpace(m.cfg.TUI.ThinkingDisplay), value) {
		return
	}
	if err := config.WriteTUIThinkingDisplay(m.cfg.ConfigPath, value); err != nil {
		if m.state != stateStreaming && !m.compacting {
			m.appendBlock(block{kind: "system", body: "thinking: save display mode: " + err.Error()})
		}
		return
	}
	m.cfg.TUI.ThinkingDisplay = value
}

func (m *Model) announceThinkingDisplayMode() {
	if m.state == stateStreaming || m.compacting {
		return
	}
	m.appendBlock(block{kind: "system", body: m.thinkingModeStatus()})
}

func tailThinkingText(s string, maxLines, maxRunes int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	truncated := false
	if maxLines > 0 {
		lines := strings.Split(s, "\n")
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
			s = strings.Join(lines, "\n")
			truncated = true
		}
	}
	if maxRunes > 0 {
		runes := []rune(s)
		if len(runes) > maxRunes {
			s = string(runes[len(runes)-maxRunes:])
			truncated = true
			if idx := strings.Index(s, "\n"); idx >= 0 && idx < len(s)-1 {
				s = s[idx+1:]
			}
		}
	}
	if truncated {
		return "...\n" + s
	}
	return s
}
