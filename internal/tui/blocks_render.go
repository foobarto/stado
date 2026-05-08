package tui

// Conversation block rendering — the heart of the TUI's chat view.
// renderBlocks runs every frame: it walks m.blocks (or splits them
// across the two split-view panes), renders each block via the
// glamour-backed templates (message_user / message_assistant /
// message_thinking / message_tool) or inline lipgloss styles
// (system / btw), and drives the cache that keeps streaming
// responsive on long histories.
//
// Render-only — no event handling lives here. Cache-invalidation
// hooks (invalidateBlockCache) are called from streamEvent /
// toolResult handlers.

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) renderBlocks() {
	// Split view: activity (tool + system) goes into activityVP (top
	// pane); conversation (user + assistant + thinking) stays in vp
	// (bottom pane). Single-view mode renders everything into vp in
	// timeline order, which is the default behaviour.
	if m.splitView {
		m.renderSplitPanes()
		return
	}
	var b strings.Builder
	width := m.vp.Width - 2
	if width < 10 {
		width = 10
	}
	first := true
	m.blockLineRanges = m.blockLineRanges[:0]
	curLine := 0
	for i := range m.blocks {
		if !m.shouldRenderBlock(m.blocks[i]) {
			continue
		}
		if !first {
			b.WriteString("\n")
			curLine++ // separator line
		}
		out := m.renderBlockCached(i, width)
		blockLines := strings.Count(out, "\n") + 1
		m.blockLineRanges = append(m.blockLineRanges, blockLineRange{
			start: curLine, end: curLine + blockLines, blockIdx: i,
		})
		curLine += blockLines
		b.WriteString(out)
		first = false
	}
	oldBottomY := max(0, m.vp.TotalLineCount()-m.vp.Height)
	wasNearBottom := m.vp.YOffset >= oldBottomY-2
	m.vp.SetContent(b.String())
	// Only auto-scroll to bottom when the user is already near the
	// bottom.  YOffset is the scroll position (0 = top).  The bottom
	// position is max(0, contentHeight - viewportHeight).  If the
	// user has scrolled up to read history, preserve their position.
	contentLines := m.vp.TotalLineCount()
	if wasNearBottom {
		m.vp.GotoBottom()
	} else if contentLines < m.vp.Height {
		m.vp.GotoTop()
	}
}

// renderBlockCached is the hot path: during streaming we call
// renderBlocks many times per second, so re-running glamour on every
// historical (unchanged) block is pure overhead. We cache the last
// rendered output on the block itself and reuse it whenever body /
// width / expand state / tool result are all unchanged. The live
// streaming assistant/thinking block keeps growing so its cache misses
// each tick, which is the intended behaviour — everything else is
// immutable the moment it scrolls past the current turn.
func (m *Model) renderBlockCached(i, width int) string {
	blk := &m.blocks[i]
	thinkingCacheOK := blk.kind != "thinking" || blk.cachedThinkingMode == m.thinkingMode
	if blk.cachedOut != "" &&
		blk.cachedWidth == width &&
		blk.cachedMeta == blk.meta &&
		blk.cachedDetails == blk.details &&
		blk.cachedExpand == blk.expanded &&
		blk.cachedFocused == blk.focused &&
		blk.cachedResult == blk.toolResult &&
		thinkingCacheOK {
		return blk.cachedOut
	}
	out, _ := m.renderBlock(*blk, width)
	if blk.focused {
		out = applyFocusMarker(out, m.theme.Fg("accent").GetForeground())
	}
	blk.cachedOut = out
	blk.cachedWidth = width
	blk.cachedMeta = blk.meta
	blk.cachedDetails = blk.details
	blk.cachedExpand = blk.expanded
	blk.cachedFocused = blk.focused
	blk.cachedResult = blk.toolResult
	if blk.kind == "thinking" {
		blk.cachedThinkingMode = m.thinkingMode
	}
	return out
}

// stripTrailingSpacesPerLine removes trailing spaces and tabs from
// every line of s. Used when no sidebar is shown so terminal click-
// drag-to-select copies the visible text without padding spaces.
// ANSI styling sequences live before the visible cells, so trimming
// SGR-bearing trailing whitespace is safe; we only strip ASCII
// space + tab to avoid touching unicode whitespace inside content.
func stripTrailingSpacesPerLine(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// strings.TrimRight on " \t" handles both common pad chars.
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// applyFocusMarker prepends a coloured left-border glyph to every line
// of the rendered block so the focused tool/assistant call stands out
// in the conversation pane. EP-N/A — older-tool expand UX.
func applyFocusMarker(rendered string, fg lipgloss.TerminalColor) string {
	marker := lipgloss.NewStyle().Foreground(fg).Render("▌ ")
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = marker + line
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderAssistantDetails(details string) string {
	lines := strings.Split(strings.TrimSpace(details), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return lipgloss.NewStyle().
		Foreground(m.theme.Fg("muted").GetForeground()).
		Render(strings.Join(lines, "\n")) + "\n"
}

// invalidateBlockCache forces a re-render of the given block next time
// renderBlocks runs. Call from handleStreamEvent after mutating a
// block's body so the cache doesn't serve stale content.
func (m *Model) invalidateBlockCache(i int) {
	if i >= 0 && i < len(m.blocks) {
		m.blocks[i].cachedOut = ""
	}
}

// renderBlock returns the rendered string for a single block at the
// given target column width. Used by both the single-view and
// split-view renderers. Width must already subtract padding for
// whatever pane is rendering.
func (m *Model) renderBlock(blk block, width int) (string, error) {
	switch blk.kind {
	case "user":
		// EP-0038 §E: multi-producer message metadata.
		// Operator-typed messages in an agent-driven session get [YOU].
		// Messages injected by other producers get [source].
		label := ""
		if m.fleet != nil && len(m.fleet.List()) > 0 {
			// We're in a session with running agents — add provenance label.
			if blk.source == "" || blk.source == "operator" {
				label = "[YOU] "
			} else {
				label = "[" + blk.source + "] "
			}
		}
		body := label + blk.body
		return m.renderer.Exec("message_user", map[string]any{
			"Body":   body,
			"Width":  width,
			"Queued": blk.queued,
		})
	case "assistant":
		out, err := m.renderer.Exec("message_assistant", map[string]any{
			"Body":  blk.body,
			"Width": width,
			"Model": m.model,
		})
		if err != nil || strings.TrimSpace(blk.meta) == "" {
			return out, err
		}
		footer := lipgloss.NewStyle().
			Foreground(m.theme.Fg("muted").GetForeground()).
			Render("  " + blk.meta)
		rendered := strings.TrimRight(out, "\n") + "\n" + footer + "\n"
		if blk.expanded && strings.TrimSpace(blk.details) != "" {
			rendered += m.renderAssistantDetails(blk.details)
		}
		return rendered, nil
	case "thinking":
		return m.renderer.Exec("message_thinking", map[string]any{
			"Body":  m.thinkingBlockBody(blk.body),
			"Width": width,
		})
	case "tool":
		duration := ""
		if !blk.startedAt.IsZero() {
			if !blk.endedAt.IsZero() {
				duration = blk.endedAt.Sub(blk.startedAt).Round(time.Millisecond).String()
			} else {
				// Tool is still running — show live elapsed counter.
				d := time.Since(blk.startedAt).Round(time.Second)
				if d < time.Second {
					d = time.Since(blk.startedAt).Round(100 * time.Millisecond)
				}
				duration = "running " + d.String()
			}
		}
		return m.renderer.Exec("message_tool", map[string]any{
			"Name":        blk.toolName,
			"ArgsPreview": truncate(blk.toolArgs, 40),
			"FullArgs":    prettyJSON(blk.toolArgs),
			"Result":      blk.toolResult,
			"Expanded":    blk.expanded,
			"Duration":    duration,
			"Width":       width - 4,
		})
	case "system":
		tone := systemBlockTone(blk.body)
		return lipgloss.NewStyle().
			Background(m.theme.Bg("surface").GetBackground()).
			Border(lipgloss.Border{Left: "│"}, false, false, false, true).
			BorderForeground(m.theme.Fg(tone).GetForeground()).
			Foreground(m.theme.Fg("text").GetForeground()).
			Padding(0, 1).
			Width(width).
			Render(truncate(blk.body, width*6)) + "\n", nil
	case "btw":
		return lipgloss.NewStyle().
			Background(m.theme.Bg("surface").GetBackground()).
			Border(lipgloss.Border{Left: "│"}, false, false, false, true).
			BorderForeground(m.theme.Fg("accent").GetForeground()).
			Foreground(m.theme.Fg("text_secondary").GetForeground()).
			Padding(0, 1).
			Width(width).
			Render("btw: "+truncate(blk.body, width*6)) + "\n", nil
	}
	return "", nil
}

func systemBlockTone(body string) string {
	body = strings.TrimSpace(strings.ToLower(body))
	switch {
	case strings.HasPrefix(body, "error:"),
		strings.Contains(body, " error:"),
		strings.Contains(body, ": error:"),
		strings.Contains(body, " failed:"),
		strings.Contains(body, ": load:"),
		strings.Contains(body, ": runtime:"),
		strings.Contains(body, "unavailable"):
		return "error"
	case strings.HasPrefix(body, "warning:"),
		strings.Contains(body, " warning:"),
		strings.Contains(body, "blocked"),
		strings.Contains(body, "crossed warn cap"):
		return "warning"
	default:
		return "accent"
	}
}

// renderSplitPanes paints m.blocks into two separate viewports:
// activity (tool + system) in the TOP pane (m.activityVP),
// conversation (user + assistant + thinking) in the BOTTOM pane
// (m.vp). Default ordering within each pane is chronological so the
// most recent output lands at the bottom of its pane (matching the
// chat-log metaphor).
func (m *Model) renderSplitPanes() {
	var convo, activity strings.Builder
	convoW := m.vp.Width - 2
	if convoW < 10 {
		convoW = 10
	}
	actW := m.activityVP.Width - 2
	if actW < 10 {
		actW = 10
	}
	for i := range m.blocks {
		blk := &m.blocks[i]
		if !m.shouldRenderBlock(*blk) {
			continue
		}
		isActivity := blk.kind == "tool" || blk.kind == "system"
		var target *strings.Builder
		var w int
		if isActivity {
			target = &activity
			w = actW
		} else {
			target = &convo
			w = convoW
		}
		target.WriteString(m.renderBlockCached(i, w))
		target.WriteString("\n")
	}
	m.activityVP.SetContent(activity.String())
	m.vp.SetContent(convo.String())
	// Pin each to its bottom so the most recent entry is always in
	// view when new events arrive.
	m.activityVP.GotoBottom()
	m.vp.GotoBottom()
}
