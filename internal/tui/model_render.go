package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/tui/banner"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/overlays"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) View() string {
	if m.showHelp {
		return overlays.RenderHelp(m.keys, m.width)
	}
	if m.showStatus {
		return m.renderStatusModal(m.width, m.height)
	}

	landing := len(m.blocks) == 0 && m.approval == nil
	sidebarW := 0
	if m.sidebarOpen && !landing {
		sidebarW = m.sidebarRenderWidth()
		// Too-narrow terminal: don't render a sidebar this frame, but
		// keep m.sidebarOpen so a later WindowSizeMsg with a wider
		// terminal brings it back. Previously we flipped the flag here,
		// which meant the first View() call (pre-WindowSizeMsg, width=0)
		// permanently closed the sidebar for the session.
		if sidebarW < m.theme.Layout.SidebarMinWidth {
			sidebarW = 0
		}
	}
	mainW := m.width - sidebarW
	if sidebarW > 0 {
		mainW -= 1 // gap
	}

	// Input height grows with newlines, plus a few spare rows so the
	// editor is comfortable before users start writing multi-line prompts.
	value := m.input.Value()
	inputH := strings.Count(value, "\n") + 1 + input.ExtraVisibleRows
	if inputH < input.DefaultVisibleRows {
		inputH = input.DefaultVisibleRows
	}
	maxInputH := m.height / 3
	if maxInputH < 1 {
		maxInputH = 1
	}
	if inputH > maxInputH {
		inputH = maxInputH
	}
	m.input.Model.SetHeight(inputH)
	inputFrameW := mainW
	if landing {
		inputFrameW = landingInputWidth(mainW)
	}
	if textW := inputFrameW - 4; textW > 0 {
		m.input.Model.SetWidth(textW)
	}
	// Reserve: textarea (inputH) + top border (1) + inline status
	// row inside the bordered box (1) + bottom border (1) + trailing
	// newline after the input box (1) + outer status row (1) =
	// inputH + 5. Earlier constants (3, then 4) left the left
	// column taller than the pane by 1-2 rows, so the top of the
	// chat area (first user-card row) got clipped off.
	mainH := m.height - inputH - 5
	if m.approval != nil {
		mainH -= m.approvalCardHeight(mainW)
	}
	if m.choice != nil {
		mainH -= m.choiceDrawerHeight(mainW)
	}
	if mainH < 4 {
		mainH = 4
	}

	vpWidth, vpHeight := mainW, mainH

	// Split-view: top = activity (tool + system), bottom = conversation.
	// Divide the chat area roughly 40/60 between them. Both panes get
	// the same width but half the height (minus 1 for the separator).
	if m.splitView {
		actH := mainH*2/5 - 1
		if actH < 3 {
			actH = 3
		}
		convoH := mainH - actH - 1 // -1 for separator row
		if convoH < 3 {
			convoH = 3
		}
		m.activityVP.Width = mainW
		m.activityVP.Height = actH
		vpHeight = convoH
	}
	vpChanged := m.vp.Width != vpWidth || m.vp.Height != vpHeight
	m.vp.Width = vpWidth
	m.vp.Height = vpHeight
	if vpChanged && len(m.blocks) > 0 {
		m.renderBlocks()
	}

	base := ""
	if landing {
		base = m.renderLanding(mainW, m.height)
	} else {
		// Left column: messages viewport + approval + input + status
		var left strings.Builder
		if m.splitView {
			// Top pane: activity tail (tool + system blocks).
			left.WriteString(m.activityVP.View())
			// Separator between the two panes — a dim hr matching the
			// border colour so it reads as a structural divider rather
			// than just more chat content.
			left.WriteString("\n" + m.theme.Fg("border").Render(
				strings.Repeat("─", max(0, mainW))) + "\n")
			// Bottom pane: conversation.
			left.WriteString(m.vp.View())
		} else {
			left.WriteString(m.vp.View())
		}
		if m.approval != nil {
			left.WriteString(m.renderApprovalCard(mainW) + "\n")
		}
		if m.choice != nil {
			left.WriteString(m.renderChoiceDrawer(mainW) + "\n")
		}
		left.WriteString(m.renderInputBox(mainW))
		left.WriteString(m.renderStatus(mainW))

		if sidebarW > 0 {
			leftBlock := lipgloss.NewStyle().Width(mainW).Render(left.String())
			sidebar := m.renderSidebar(sidebarW)
			sepH := max(1, m.height)
			base = lipgloss.JoinHorizontal(lipgloss.Top,
				leftBlock,
				lipgloss.NewStyle().Foreground(m.theme.Fg("border").GetForeground()).Render(strings.Repeat("│\n", sepH-1)+"│"),
				sidebar,
			)
		} else {
			// No sidebar → don't pad each line to mainW. Trailing
			// spaces would otherwise be included in terminal click-
			// drag-to-select copy. Strip per-line so the user's
			// clipboard gets the visible text only.
			base = stripTrailingSpacesPerLine(left.String())
		}
	}

	// Modal overlay: command palette centred on the whole screen.
	if m.slash.Visible && !m.slashInline {
		m.slash.Width = m.width
		m.slash.Height = m.height
		return m.slash.View(m.width, m.height)
	}
	if m.agentPick.Visible {
		m.agentPick.Width = m.width
		m.agentPick.Height = m.height
		return m.agentPick.View(m.width, m.height)
	}
	// Pickers are modal too — only one can be open at a time since
	// each path routes independently through Update.
	if m.modelPicker.Visible {
		m.modelPicker.Width = m.width
		m.modelPicker.Height = m.height
		return m.modelPicker.View(m.width, m.height)
	}
	if m.personaPicker.Visible {
		m.personaPicker.Width = m.width
		m.personaPicker.Height = m.height
		return m.personaPicker.View(m.width, m.height)
	}
	if m.fleetPicker != nil && m.fleetPicker.Visible {
		// Refresh on each render so status pills + last-tool snapshots
		// reflect what the goroutines have written. Cheap (Fleet.List
		// is a copy under a mutex) and avoids a separate refresh
		// timer.
		m.fleetPicker.Refresh(m.fleet.List())
		return m.fleetPicker.View(m.width, m.height)
	}
	if m.sessionPick.Visible {
		m.sessionPick.Width = m.width
		m.sessionPick.Height = m.height
		return m.sessionPick.View(m.width, m.height)
	}
	if m.taskPick.Visible {
		m.taskPick.Width = m.width
		m.taskPick.Height = m.height
		return m.taskPick.View(m.width, m.height)
	}
	if m.themePick.Visible {
		m.themePick.Width = m.width
		m.themePick.Height = m.height
		return m.themePick.View(m.width, m.height)
	}

	// Quit confirmation popup — centred overlay with y/n options.
	if m.state == stateQuitConfirm {
		return m.renderQuitConfirm(base)
	}

	return base
}

// layout: recompute viewport size + wrap width; trigger a render.
func (m *Model) layout() {
	m.renderBlocks()
}

// Sidebar rendering + width management moved to sidebar.go.




// renderStatus runs the bottom status template (right-aligned muted
// tokens/cost + ctrl+p commands hint, plus an optional left-side
// state indicator when busy).
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

// modelOrPlaceholder returns the model name, or a helpful placeholder
// when no model is configured so the sidebar never shows a blank line.
func modelOrPlaceholder(s string) string {
	if s == "" {
		return "no model set  —  /model"
	}
	return s
}

// bannerFor returns the startup banner suitable for the given chat
// column width. Width under 90 cols returns "" so the banner isn't
// truncated mid-line; narrow terminals use the compact wordmark.
//
// We now write the banner directly into the left column (bypassing
// bubbletea's viewport) so the 256-colour ANSI variant renders
// correctly: lipgloss's layout passes the escape bytes through
// untouched and the terminal paints them as colours. NO_COLOR is
// honoured inside banner.String() — sets the plain variant there.
func bannerFor(vpWidth int) string {
	if vpWidth < 90 {
		return ""
	}
	return banner.String()
}

func humanize(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// cacheHitRatio returns fraction of prompt tokens served from prompt
// cache on the last turn. Formula: CacheReadTokens /
// (CacheReadTokens + InputTokens) — the numerator is what the cache
// saved the user; the denominator is everything the model had to
// "look at" whether from cache or from the fresh prompt body. Returns
// 0 when either the provider doesn't report cache tokens or there
// were no prompts yet.
func cacheHitRatio(u agent.Usage) float64 {
	total := u.CacheReadTokens + u.InputTokens
	if total == 0 || u.CacheReadTokens == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(total)
}

func truncate(s string, max int) string {
	if max <= 1 || len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func trimSeed(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func prettyJSON(raw string) string {
	if raw == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(out)
}
