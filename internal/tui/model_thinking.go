package tui

import "strings"

const (
	thinkingTailLines = 8
	thinkingTailRunes = 1200
)

func (m thinkingDisplayMode) String() string {
	switch m {
	case thinkingTail:
		return "tail"
	case thinkingHide:
		return "hide"
	default:
		return "show"
	}
}

func parseThinkingDisplayMode(s string) (thinkingDisplayMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "show", "full", "on":
		return thinkingShow, true
	case "tail":
		return thinkingTail, true
	case "hide", "off":
		return thinkingHide, true
	default:
		return thinkingShow, false
	}
}

func (m *Model) cycleThinkingDisplayMode() {
	switch m.thinkingMode {
	case thinkingShow:
		m.thinkingMode = thinkingTail
	case thinkingTail:
		m.thinkingMode = thinkingHide
	default:
		m.thinkingMode = thinkingShow
	}
}

func (m *Model) thinkingModeStatus() string {
	switch m.thinkingMode {
	case thinkingTail:
		return "thinking: tail (showing recent thinking only)"
	case thinkingHide:
		return "thinking: hide"
	default:
		return "thinking: show"
	}
}

func (m *Model) setThinkingDisplayMode(mode thinkingDisplayMode) {
	m.thinkingMode = mode
}

func (m *Model) announceThinkingDisplayMode() {
	if m.state == stateStreaming || m.compacting {
		return
	}
	m.appendBlock(block{kind: "system", body: m.thinkingModeStatus()})
}

func (m *Model) shouldRenderBlock(blk block) bool {
	return blk.kind != "thinking" || m.thinkingMode != thinkingHide
}

func (m *Model) thinkingBlockBody(body string) string {
	if m.thinkingMode != thinkingTail {
		return body
	}
	return tailThinkingText(body, thinkingTailLines, thinkingTailRunes)
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
