package tui

import "strings"

// String returns the canonical config value for a display mode.
func (d displayMode) String() string {
	switch d {
	case displayAuto:
		return "auto"
	case displayCollapsed:
		return "collapsed"
	case displayExpanded:
		return "expanded"
	default:
		return "preview"
	}
}

// label is a short human-readable name for status lines.
func (d displayMode) label() string {
	switch d {
	case displayAuto:
		return "auto (full while streaming, then one line)"
	case displayCollapsed:
		return "collapsed (one line)"
	case displayExpanded:
		return "expanded (always full)"
	default:
		return "preview (clip to a few lines)"
	}
}

// parseDisplayMode maps a config string to a mode. It accepts the
// canonical values (preview/auto/collapsed/expanded) and is tolerant of
// the legacy thinking_display vocabulary so old configs keep loading:
// tail->preview, show/full->expanded, hide->collapsed. ok=false signals
// an unrecognized value so callers can warn / keep the current mode.
func parseDisplayMode(s string) (displayMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "preview":
		return displayPreview, true
	case "auto":
		return displayAuto, true
	case "collapsed":
		return displayCollapsed, true
	case "expanded":
		return displayExpanded, true
	// Legacy thinking_display values (clean break on canonical names, but
	// don't error an existing config — map them to the nearest mode).
	case "tail":
		return displayPreview, true
	case "show", "full", "on":
		return displayExpanded, true
	case "hide", "off":
		return displayCollapsed, true
	default:
		return displayPreview, false
	}
}

// effectiveRenderKind resolves how one thinking/tool block renders this
// frame from its type's mode, whether it is still streaming, and any
// per-block override. The override always wins.
func effectiveRenderKind(mode displayMode, streaming bool, ov blockOverride) renderKind {
	switch ov {
	case overrideExpanded:
		return renderFull
	case overrideCollapsed:
		return renderOneLine
	}
	switch mode {
	case displayExpanded:
		return renderFull
	case displayCollapsed:
		return renderOneLine
	case displayAuto:
		if streaming {
			return renderFull
		}
		return renderOneLine
	default: // displayPreview
		return renderClipped
	}
}

// toggleBlockExpansion flips a block between the display mode's default and
// an override. Assistant blocks toggle their details (the expanded bool).
//
// For thinking/tool blocks it is an on/off toggle of the per-block
// override: the first activation forces the opposite of what the mode
// currently shows (a clipped/one-line block expands to full; a full block
// collapses to one line), and the next activation clears the override so
// the block returns to its mode-governed default. This preserves the
// pre-display-modes behavior — e.g. in preview mode a click expands a
// block then returns it to the clipped tail, rather than oscillating
// full<->one-line and stranding the block away from the tail forever.
func (m *Model) toggleBlockExpansion(idx int) {
	if idx < 0 || idx >= len(m.blocks) {
		return
	}
	blk := &m.blocks[idx]
	switch blk.kind {
	case "tool", "thinking":
		if blk.override != overrideNone {
			// Already overridden — return to the mode-governed default.
			blk.override = overrideNone
		} else {
			mode := m.thinkingMode
			if blk.kind == "tool" {
				mode = m.toolMode
			}
			// Override to the opposite of what the mode shows by default.
			if effectiveRenderKind(mode, blk.streaming, overrideNone) == renderFull {
				blk.override = overrideCollapsed
			} else {
				blk.override = overrideExpanded
			}
		}
	default: // assistant (details)
		blk.expanded = !blk.expanded
	}
	m.invalidateBlockCache(idx)
}

// countLines reports the number of (non-trailing-empty) lines in s.
func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// finalizeStreamingThinking marks a trailing in-progress thinking block as
// done the moment a new block follows it, so auto/collapsed modes collapse
// it. A thinking block stops growing as soon as the model emits the next
// assistant text or a tool call.
func (m *Model) finalizeStreamingThinking() {
	if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == "thinking" && m.blocks[n-1].streaming {
		m.blocks[n-1].streaming = false
		m.invalidateBlockCache(n - 1)
	}
}

// finalizeStreamingBlocks marks every still-streaming block done. Backstop
// at turn completion so a trailing thinking/tool block still collapses
// under auto mode even when no later block followed it.
func (m *Model) finalizeStreamingBlocks() {
	for i := range m.blocks {
		if m.blocks[i].streaming {
			m.blocks[i].streaming = false
			m.invalidateBlockCache(i)
		}
	}
}
