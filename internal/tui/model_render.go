package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/banner"
	"github.com/foobarto/stado/internal/tui/overlays"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) View() string {
	if m.showHelp {
		return overlays.RenderHelp(m.keys, m.width)
	}

	sidebarW := 0
	if m.sidebarOpen {
		sidebarW = m.theme.Layout.SidebarWidth
		if sidebarW > m.width/3 {
			sidebarW = m.width / 3
		}
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

	// Input height grows with newlines. Horizontal scroll handles
	// long single-line input (bubbles textarea doesn't soft-wrap),
	// but newlines from Shift+Enter produce extra rendered rows.
	value := m.input.Value()
	inputH := strings.Count(value, "\n") + 1
	if inputH < 1 {
		inputH = 1
	}
	if inputH > m.height/3 {
		inputH = m.height / 3
	}
	m.input.Model.SetHeight(inputH)
	if textW := mainW - 4; textW > 0 {
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
	if mainH < 4 {
		mainH = 4
	}

	m.vp.Width = mainW
	m.vp.Height = mainH

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
		m.vp.Height = convoH
	}

	// Left column: messages viewport + approval + input + status
	var left strings.Builder
	// Empty-state: draw the banner directly into the left column
	// (bypassing the viewport) so the top of the logo isn't eaten by
	// the viewport's scroll position when content is taller than the
	// pane. Leading newline compensates for the first-row eat in the
	// lipgloss layout pipeline; mainH-1 so the banner leaves one row
	// for the input box to dock at the bottom.
	// Narrow-terminal fallback: when the viewport is too narrow for the
	// ASCII banner, render a text hint so the user isn't staring at
	// empty whitespace.
	if len(m.blocks) == 0 {
		if banner := bannerFor(mainW); banner != "" {
			left.WriteString("\n" + renderBannerBlock(mainW, mainH-1))
		} else {
			left.WriteString(m.theme.Fg("muted").Render(
				"  Send a message to get started  —  /help for commands") + "\n")
		}
	} else if m.splitView {
		// Top pane: activity tail (tool + system blocks).
		left.WriteString(m.activityVP.View())
		// Separator between the two panes — a dim hr matching the
		// border colour so it reads as a structural divider rather
		// than just more chat content.
		left.WriteString("\n" + m.theme.Fg("border").Render(
			strings.Repeat("─", mainW)) + "\n")
		// Bottom pane: conversation.
		left.WriteString(m.vp.View())
	} else {
		left.WriteString(m.vp.View())
	}
	if m.approval != nil {
		left.WriteString(m.renderApprovalCard(mainW) + "\n")
	}
	left.WriteString(m.renderInputBox(mainW))
	left.WriteString(m.renderStatus(mainW))

	leftBlock := lipgloss.NewStyle().Width(mainW).Render(left.String())

	base := leftBlock
	if sidebarW > 0 {
		sidebar := m.renderSidebar(sidebarW)
		base = lipgloss.JoinHorizontal(lipgloss.Top,
			leftBlock,
			lipgloss.NewStyle().Foreground(m.theme.Fg("border").GetForeground()).Render(strings.Repeat("│\n", m.height-1)+"│"),
			sidebar,
		)
	}

	// Modal overlay: command palette centred on the whole screen.
	if m.slash.Visible {
		m.slash.Width = m.width
		m.slash.Height = m.height
		return m.slash.View(m.width, m.height)
	}
	// Model picker is the second modal — only one can be open at a
	// time since each path routes independently through Update.
	if m.modelPicker.Visible {
		m.modelPicker.Width = m.width
		m.modelPicker.Height = m.height
		return m.modelPicker.View(m.width, m.height)
	}

	// Quit confirmation popup — centred overlay with y/n options.
	if m.state == stateQuitConfirm {
		popupW := 40
		if popupW > m.width-4 {
			popupW = m.width - 4
		}
		lines := []string{
			m.theme.Fg("warning").Bold(true).Render("  Confirm exit?"),
			"",
			m.theme.Fg("muted").Render("  [y]es / [n]o"),
		}
		content := strings.Join(lines, "\n")
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Fg("warning").GetForeground()).
			Padding(1, 2).
			Width(popupW).
			Render(content)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}

	return base
}

// layout: recompute viewport size + wrap width; trigger a render.
func (m *Model) layout() {
	m.renderBlocks()
}

// renderInputBox produces the opencode-style bordered input: a textarea
// stacked on top of an inline status line (Mode · Model · Provider),
// all inside one rounded border whose LEFT edge is mode-coloured
// (yellow=Plan, green=Do) so the agent's stance is visible at a glance
// even when focus is elsewhere.
func (m *Model) renderInputBox(mainW int) string {
	inline, err := m.renderer.Exec("input_status", map[string]any{
		"Mode":         m.mode.String(),
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Hint":         "", // reserved — "xhigh" effort-style badge lands when reasoning-effort config does
	})
	if err != nil {
		inline = "[input status render error: " + err.Error() + "]"
	}

	// File-picker popover (triggered by `@` in the buffer). Rendered
	// INSIDE the bordered input frame, above the textarea, so the
	// suggestion column stays visually anchored to the input cursor
	// instead of floating at the top of the screen.
	var pickerPrefix string
	if m.filePicker.Visible && len(m.filePicker.Matches) > 0 {
		// Leave 2 cols of breathing room inside the border + padding.
		pickerPrefix = m.filePicker.View(mainW-4) + "\n"
	}

	body := pickerPrefix + m.input.View() + "\n" + strings.TrimRight(inline, "\n")

	modeColor := m.theme.Fg("success").GetForeground() // Do
	switch m.mode {
	case modePlan:
		modeColor = m.theme.Fg("warning").GetForeground()
	case modeBTW:
		modeColor = m.theme.Fg("accent").GetForeground()
	}
	style := lipgloss.NewStyle().
		Border(m.theme.Border()).
		BorderForeground(modeColor).
		Padding(0, 1).
		Width(mainW - 2)
	return style.Render(body) + "\n"
}

func (m *Model) approvalCardHeight(mainW int) int {
	card := m.renderApprovalCard(mainW)
	if card == "" {
		return 0
	}
	return lipgloss.Height(card) + 1
}

func (m *Model) renderApprovalCard(mainW int) string {
	if m.approval == nil {
		return ""
	}

	innerW := mainW - 8
	if innerW < 8 {
		innerW = 8
	}

	title := m.theme.Fg("warning").Bold(true).Render(strings.TrimSpace(m.approval.title))
	if strings.TrimSpace(m.approval.title) == "" {
		title = m.theme.Fg("warning").Bold(true).Render("Approval required")
	}
	body := m.theme.Fg("text").Render(truncate(m.approval.body, innerW*3))
	allow := m.renderApprovalButton("Allow [y]", m.approvalAllowSelected, "success")
	deny := m.renderApprovalButton("Deny [n]", !m.approvalAllowSelected, "error")
	hint := m.theme.Fg("muted").Render("Up to focus, Left/Right to choose, Enter to confirm")
	if m.approvalFocused {
		hint = m.theme.Fg("warning").Render("Left/Right to choose, Enter to confirm, Down to return")
	}

	cardBody := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		body,
		lipgloss.JoinHorizontal(lipgloss.Left, allow, " ", deny),
		hint,
	)

	border := m.theme.Fg("border").GetForeground()
	if m.approvalFocused {
		border = m.theme.Fg("warning").GetForeground()
	}
	style := lipgloss.NewStyle().
		Border(m.theme.Border()).
		BorderForeground(border).
		Padding(0, 1)
	if mainW > 2 {
		style = style.Width(mainW - 2)
	}
	return style.Render(cardBody)
}

func (m *Model) renderApprovalButton(label string, active bool, tone string) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Fg("border").GetForeground()).
		Padding(0, 1).
		Foreground(m.theme.Fg("muted").GetForeground())
	if active {
		style = style.
			BorderForeground(m.theme.Fg(tone).GetForeground()).
			Foreground(m.theme.Fg(tone).GetForeground()).
			Bold(true)
	}
	return style.Render(label)
}

func (m *Model) handleApprovalKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if approvalChoice(msg, 'y') {
		return m.resolveApproval(true), true
	}
	if approvalChoice(msg, 'n') {
		return m.resolveApproval(false), true
	}

	switch msg.Type {
	case tea.KeyUp:
		if m.filePicker.Visible {
			return nil, false
		}
		if !m.approvalFocused {
			m.approvalFocused = true
			m.renderBlocks()
		}
		return nil, true
	case tea.KeyDown:
		if m.approvalFocused {
			m.approvalFocused = false
			m.renderBlocks()
			return nil, true
		}
		if m.filePicker.Visible {
			return nil, false
		}
	case tea.KeyLeft:
		if m.approvalFocused && !m.approvalAllowSelected {
			m.approvalAllowSelected = true
			m.renderBlocks()
			return nil, true
		}
	case tea.KeyRight:
		if m.approvalFocused && m.approvalAllowSelected {
			m.approvalAllowSelected = false
			m.renderBlocks()
			return nil, true
		}
	case tea.KeyEnter:
		if m.approvalFocused {
			return m.resolveApproval(m.approvalAllowSelected), true
		}
		if m.filePicker.Visible {
			return nil, false
		}
		// Keep the editor live while approval is pending, but don't
		// submit a new turn until the pending tool decision resolves.
		return nil, true
	}

	return nil, false
}

func approvalChoice(msg tea.KeyMsg, want rune) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	r := msg.Runes[0]
	return r == want || r == want-'a'+'A'
}

// renderStatus runs the bottom status template (right-aligned muted
// tokens/cost + ctrl+p commands hint, plus an optional left-side
// state indicator when busy).
func (m *Model) renderStatus(width int) string {
	state := "idle"
	switch m.state {
	case stateStreaming:
		state = "streaming"
	case stateApproval:
		state = "approval"
	case stateError:
		state = "error"
	}
	tokens := fmt.Sprintf("%s (%s)", humanize(m.usage.InputTokens), tokenPctString(m))
	cost := fmt.Sprintf("$%.2f", m.usage.CostUSD)

	// Cache-hit ratio: fraction of input tokens served from prompt
	// cache. Only meaningful on providers that report it
	// (Anthropic + cache-aware OAI-compat); elsewhere zero. Render
	// only when the ratio is non-trivial so it doesn't clutter.
	cacheRatio := ""
	if r := cacheHitRatio(m.usage); r > 0 {
		cacheRatio = fmt.Sprintf("cache %.0f%%", r*100)
	}

	// Queued-message indicator (mid-stream Enter buffer). Empty when
	// nothing queued — template conditional-renders the pill.
	queued := ""
	if m.queuedPrompt != "" {
		queued = trimSeed(m.queuedPrompt, 40)
	}

	// Elapsed-time pill during streaming. Slow local reasoning models
	// (qwen3.6-35b with a large tool surface) can take tens of seconds
	// before the first EvDone; without an elapsed counter the "●
	// thinking" indicator looks indistinguishable from a freeze. The
	// counter ticks via tea.Tick in Update; here we just format the
	// current elapsed as "Ns" / "MmSs".
	elapsed := ""
	if m.state == stateStreaming && !m.turnStart.IsZero() {
		d := time.Since(m.turnStart).Round(time.Second)
		if d >= time.Minute {
			elapsed = fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
		} else {
			elapsed = fmt.Sprintf("%ds", int(d.Seconds()))
		}
	}

	body, err := m.renderer.Exec("status", map[string]any{
		"State":        state,
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Cwd":          m.cwd,
		"ErrorMessage": m.errorMsg,
		"Width":        width,
		"Tokens":       tokens,
		"Cost":         cost,
		"Cache":        cacheRatio,
		"Queued":       queued,
		"Budget":       m.budgetWarning(),
		"Elapsed":      elapsed,
	})
	if err != nil {
		return fmt.Sprintf("[status render error: %v]", err)
	}
	// Right-align the rendered status within the available width (+ 1 line
	// terminator). The template emits [left-state (optional) + right stats]
	// on a single line; padding the START of the line pushes the whole
	// thing to the right edge. Minus the ANSI noise — strip-free width
	// measurement comes from lipgloss.Width.
	visible := lipgloss.Width(strings.TrimRight(body, "\n"))
	if pad := width - visible; pad > 0 {
		body = strings.Repeat(" ", pad) + strings.TrimRight(body, "\n") + "\n"
	}
	return body
}

// tokenPctString renders the in-context-window fraction for the bottom
// status bar. Returns "0%" when we can't meaningfully compute the ratio.
// Soft/hard thresholds (DESIGN §"Token accounting") colour the number
// when crossed — warning at soft, error at hard — so users see the
// context approaching capacity without reading docs.
func tokenPctString(m *Model) string {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens
	if cap <= 0 || used == 0 {
		return "0%"
	}
	fraction := float64(used) / float64(cap)
	s := fmt.Sprintf("%d%%", int(100*fraction))
	switch {
	case fraction >= m.ctxHardThreshold:
		return lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render(s)
	case fraction >= m.ctxSoftThreshold:
		return lipgloss.NewStyle().Foreground(theme.Warning).Bold(true).Render(s)
	}
	return s
}

func (m *Model) renderSidebar(width int) string {
	tokPct := ""
	if cap := m.providerCaps().MaxContextTokens; cap > 0 && m.usage.InputTokens > 0 {
		pct := 100 * m.usage.InputTokens / cap
		tokPct = fmt.Sprintf("%d%% used", pct)
	}
	// Session description — shown below the stado title so the user
	// knows which session they're in. Empty when unset, template
	// conditionally renders.
	sessionLabel := ""
	if m.session != nil {
		sessionLabel = runtime.ReadDescription(m.session.WorktreePath)
	}
	// Show just the basename of the loaded AGENTS.md / CLAUDE.md so
	// the user knows which file informed the system prompt, without
	// eating sidebar width with a full path.
	instructionsName := ""
	if m.systemPromptPath != "" {
		instructionsName = filepath.Base(m.systemPromptPath)
	}
	// Skills count — surfaces the feature's existence. Empty string
	// (instead of "0") when no skills are loaded so the template
	// conditional hides the row entirely, not showing "Skills: 0"
	// which would look broken.
	skillsCount := ""
	if n := len(m.skills); n > 0 {
		verb := "skills"
		if n == 1 {
			verb = "skill"
		}
		skillsCount = fmt.Sprintf("%d %s — /skill", n, verb)
	}
	data := map[string]any{
		"Title":            "stado",
		"Version":          "0.0.0-dev",
		"SessionLabel":     sessionLabel,
		"Model":            modelOrPlaceholder(m.model),
		"ProviderName":     m.providerDisplayName(),
		"Cwd":              m.cwd,
		"TokensHuman":      fmt.Sprintf("%s tokens", humanize(m.usage.InputTokens+m.usage.OutputTokens)),
		"TokenPercent":     tokPct,
		"CostHuman":        fmt.Sprintf("$%.2f spent", m.usage.CostUSD),
		"Todos":            m.todos,
		"Width":            width - 4,
		"InstructionsName": instructionsName,
		"SkillsCount":      skillsCount,
	}
	body, err := m.renderer.Exec("sidebar", data)
	if err != nil {
		body = "[sidebar render error] " + err.Error()
	}
	// Height(m.height - 3): Pane adds 2 border rows, so passing
	// m.height-3 gives a total rendered height of m.height - 1 —
	// exactly matching leftBlock's (m.height - status_row) height
	// so JoinHorizontal doesn't pad leftBlock beyond pane height.
	// A taller sidebar here used to push the top row of the chat
	// off the visible area.
	return m.theme.Pane().Width(width - 2).Height(m.height - 3).Render(body)
}

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
	for i := range m.blocks {
		out := m.renderBlockCached(i, width)
		b.WriteString(out)
		if i < len(m.blocks)-1 {
			b.WriteString("\n")
		}
	}
	m.vp.SetContent(b.String())
	// Only auto-scroll to bottom when the user is already near the
	// bottom.  YOffset is the scroll position (0 = top).  The bottom
	// position is max(0, contentHeight - viewportHeight).  If the
	// user has scrolled up to read history, preserve their position.
	contentLines := strings.Count(b.String(), "\n")
	bottomY := 0
	if contentLines > m.vp.Height {
		bottomY = contentLines - m.vp.Height
	}
	if m.vp.YOffset >= bottomY-2 {
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
	if blk.cachedOut != "" &&
		blk.cachedWidth == width &&
		blk.cachedExpand == blk.expanded &&
		blk.cachedResult == blk.toolResult {
		return blk.cachedOut
	}
	out, _ := m.renderBlock(*blk, width)
	blk.cachedOut = out
	blk.cachedWidth = width
	blk.cachedExpand = blk.expanded
	blk.cachedResult = blk.toolResult
	return out
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
		return m.renderer.Exec("message_user", map[string]any{
			"Body":   blk.body,
			"Width":  width,
			"Queued": blk.queued,
		})
	case "assistant":
		return m.renderer.Exec("message_assistant", map[string]any{
			"Body":  blk.body,
			"Width": width,
			"Model": m.model,
		})
	case "thinking":
		return m.renderer.Exec("message_thinking", map[string]any{
			"Body":  blk.body,
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
		return m.theme.Fg("error").Render(blk.body) + "\n", nil
	case "btw":
		return m.theme.Fg("accent").Render("▸ btw: "+blk.body) + "\n", nil
	}
	return "", nil
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

// bannerFor returns the startup banner suitable for the given
// chat-column width. Width under 90 cols returns "" so the banner
// isn't truncated mid-line — narrow terminals just see the plain
// empty chat area.
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

// renderBannerBlock returns the banner trimmed to at most maxH rows
// so a short terminal gets a truncated banner rather than one that
// overflows and pushes the input box off-screen. No bottom padding:
// vp.View() on empty content returns an empty string (not maxH
// blanks), so the input box floats up naturally — we mirror that
// so the banner occupies just its own rows.
func renderBannerBlock(width, maxH int) string {
	raw := bannerFor(width)
	if raw == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	return strings.Join(lines, "\n")
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
