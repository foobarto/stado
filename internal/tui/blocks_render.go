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
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/foobarto/stado/internal/config"
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
	width := m.vp.Width() - 2
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
	oldBottomY := max(0, m.vp.TotalLineCount()-m.vp.Height())
	wasNearBottom := m.vp.YOffset() >= oldBottomY-2
	m.vp.SetContent(b.String())
	// Only auto-scroll to bottom when the user is already near the
	// bottom.  YOffset is the scroll position (0 = top).  The bottom
	// position is max(0, contentHeight - viewportHeight).  If the
	// user has scrolled up to read history, preserve their position.
	contentLines := m.vp.TotalLineCount()
	if wasNearBottom {
		m.vp.GotoBottom()
	} else if contentLines < m.vp.Height() {
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
func applyFocusMarker(rendered string, fg color.Color) string {
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

// toolOutputCollapsedHeight is the row budget for a collapsed tool
// block's output. Reads the configured value (clamped 3..20, default
// 8); falls back to the default when no config is attached (tests that
// build a bare Model).
func (m *Model) toolOutputCollapsedHeight() int {
	if m.cfg == nil {
		return config.TUI{}.EffectiveToolOutputCollapsedHeight()
	}
	return m.cfg.TUI.EffectiveToolOutputCollapsedHeight()
}

// clipToolOutput word-wraps a tool result to width, then keeps the
// first maxRows rendered rows. It returns the clipped body plus the
// count of rows that were dropped (0 when nothing was clipped). Wrapping
// before counting is deliberate: a body of few logical lines can still
// exceed the panel once long lines wrap, so the budget is measured in
// post-wrap rows (project_lipgloss_window_postwrap_rows).
func clipToolOutput(s string, width, maxRows int) (string, int) {
	wrapped := wrapToolOutput(s, width)
	lines := strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
	if maxRows < 1 {
		maxRows = 1
	}
	if len(lines) <= maxRows {
		return strings.Join(lines, "\n"), 0
	}
	more := len(lines) - maxRows
	return strings.Join(lines[:maxRows], "\n"), more
}

// wrapToolOutput mirrors the renderer's word-wrap (templates wrap tool
// args/result the same way) so the collapsed-panel row count matches
// what the template would emit when expanded. Kept local to the tui
// package because the renderer's wordWrap is unexported.
func wrapToolOutput(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		var line strings.Builder
		for _, w := range words {
			if line.Len() > 0 && line.Len()+1+len(w) > width {
				lines = append(lines, line.String())
				line.Reset()
			}
			if line.Len() > 0 {
				line.WriteByte(' ')
			}
			line.WriteString(w)
		}
		if line.Len() > 0 {
			lines = append(lines, line.String())
		}
	}
	return strings.Join(lines, "\n")
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
			"Body":  m.thinkingBlockBody(blk.body, blk.expanded),
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
		innerW := width - 4
		data := map[string]any{
			"Name":        blk.toolName,
			"ArgsPreview": truncate(blk.toolArgs, 40),
			"FullArgs":    prettyJSON(blk.toolArgs),
			"Result":      blk.toolResult,
			"Expanded":    blk.expanded,
			"Duration":    duration,
			"Width":       innerW,
		}
		// Collapsed tool blocks render their streaming output in a fixed-
		// height panel: the result is clipped to the configured row budget
		// with a "… N more lines (shift+tab)" footer. Expanding (shift+tab
		// or click) shows the full body. Wrapping happens here (not the
		// template) so the row count is measured post-wrap — clipping by
		// logical lines alone would undercount a body with long wrapping
		// lines. See memory project_lipgloss_window_postwrap_rows.
		if !blk.expanded && strings.TrimSpace(blk.toolResult) != "" {
			clipped, more := clipToolOutput(blk.toolResult, innerW, m.toolOutputCollapsedHeight())
			data["CollapsedResult"] = clipped
			data["MoreLines"] = more
		}
		return m.renderer.Exec("message_tool", data)
	case "system":
		tone := systemBlockTone(blk.body)
		return lipgloss.NewStyle().
			Background(m.theme.Bg("surface").GetBackground()).
			Border(lipgloss.Border{Left: "│"}, false, false, false, true).
			BorderForeground(m.theme.Fg(tone).GetForeground()).
			Foreground(m.theme.Fg("text").GetForeground()).
			Padding(0, 1).
			Width(width).
			Render(blk.body) + "\n", nil
	case "btw":
		return lipgloss.NewStyle().
			Background(m.theme.Bg("surface").GetBackground()).
			Border(lipgloss.Border{Left: "│"}, false, false, false, true).
			BorderForeground(m.theme.Fg("accent").GetForeground()).
			Foreground(m.theme.Fg("text_secondary").GetForeground()).
			Padding(0, 1).
			Width(width).
			Render("btw: "+blk.body) + "\n", nil
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
	convoW := m.vp.Width() - 2
	if convoW < 10 {
		convoW = 10
	}
	actW := m.activityVP.Width() - 2
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
