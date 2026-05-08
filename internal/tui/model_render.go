package tui

import (
	"encoding/json"
	"fmt"
	"strings"

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
