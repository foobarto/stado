package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/limitedio"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/banner"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/overlays"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/version"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/agent"
)

const (
	maxGitHeadFileBytes         int64 = 4 << 10
	maxGitStatusProbeBytes            = 1
	maxGitStatusProbeErrorBytes       = 4 << 10
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
		left.WriteString(m.renderInputBox(mainW))
		left.WriteString(m.renderStatus(mainW))

		leftBlock := lipgloss.NewStyle().Width(mainW).Render(left.String())

		base = leftBlock
		if sidebarW > 0 {
			sidebar := m.renderSidebar(sidebarW)
			sepH := max(1, m.height)
			base = lipgloss.JoinHorizontal(lipgloss.Top,
				leftBlock,
				lipgloss.NewStyle().Foreground(m.theme.Fg("border").GetForeground()).Render(strings.Repeat("│\n", sepH-1)+"│"),
				sidebar,
			)
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

const sidebarResizeStep = 4

func (m *Model) sidebarMinWidth() int {
	if m.theme != nil && m.theme.Layout.SidebarMinWidth > 0 {
		return m.theme.Layout.SidebarMinWidth
	}
	return 24
}

func (m *Model) sidebarPreferredWidth() int {
	if m.sidebarWidth > 0 {
		return m.sidebarWidth
	}
	if m.theme != nil && m.theme.Layout.SidebarWidth > 0 {
		return m.theme.Layout.SidebarWidth
	}
	return 32
}

func (m *Model) sidebarMaxPreferredWidth() int {
	minW := m.sidebarMinWidth()
	maxW := max(minW, 56)
	if m.width > 0 {
		frameMax := m.width / 2
		if frameMax > 0 && frameMax < maxW {
			maxW = frameMax
		}
	}
	if maxW < minW {
		maxW = minW
	}
	return maxW
}

func (m *Model) sidebarRenderWidth() int {
	width := m.sidebarPreferredWidth()
	if m.width > 0 {
		frameMax := m.width / 2
		if frameMax > 0 && width > frameMax {
			width = frameMax
		}
	}
	return width
}

func (m *Model) resizeSidebar(delta int) {
	if delta == 0 {
		return
	}
	width := m.sidebarPreferredWidth() + delta
	minW := m.sidebarMinWidth()
	maxW := m.sidebarMaxPreferredWidth()
	if width < minW {
		width = minW
	}
	if width > maxW {
		width = maxW
	}
	m.sidebarWidth = width
	m.sidebarOpen = true
}

func landingInputWidth(width int) int {
	if width < 1 {
		return 1
	}
	target := 64
	if width < 90 {
		target = width - 8
	}
	if target > width-8 {
		target = width - 8
	}
	if target < 40 {
		target = width - 4
	}
	if target < 20 {
		target = width
	}
	if target < 1 {
		target = 1
	}
	return target
}

func (m *Model) renderLanding(width, height int) string {
	if width < 1 {
		return ""
	}
	input := strings.TrimRight(m.renderInputBox(landingInputWidth(width)), "\n")
	hint := landingHint(m.theme)
	bodyH := height - 1
	if bodyH < 1 {
		bodyH = 1
	}
	logoMaxH := bodyH - lipgloss.Height(input) - lipgloss.Height(hint) - 3
	logo := renderLandingLogo(width, logoMaxH)

	parts := make([]string, 0, 3)
	if logo != "" {
		parts = append(parts, logo)
	}
	parts = append(parts, centerLines(input, width), centerLines(hint, width))
	stack := strings.Join(parts, "\n\n")
	body := lipgloss.Place(width, bodyH, lipgloss.Center, lipgloss.Center, stack)
	return body + "\n" + m.renderLandingFooter(width)
}

const (
	landingBannerMinHeight = 6
	landingBannerMaxHeight = 8
)

func renderLandingLogo(width, maxH int) string {
	if maxH < landingBannerMinHeight {
		return compactLandingLogo(width)
	}
	raw := bannerFor(width)
	if raw == "" {
		return compactLandingLogo(width)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	targetH := maxH
	if targetH > landingBannerMaxHeight {
		targetH = landingBannerMaxHeight
	}
	lines = sampleLandingLogoLines(lines, targetH)
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func compactLandingLogo(width int) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, "stado")
}

func sampleLandingLogoLines(lines []string, target int) []string {
	if target <= 0 || len(lines) <= target {
		return lines
	}
	if target == 1 {
		return lines[:1]
	}
	out := make([]string, 0, target)
	last := len(lines) - 1
	denom := target - 1
	for i := 0; i < target; i++ {
		idx := (i*last + denom/2) / denom
		out = append(out, lines[idx])
	}
	return out
}

func landingHint(th *theme.Theme) string {
	if th == nil {
		return "ctrl+p commands"
	}
	return th.Fg("text_secondary").Bold(true).Render("ctrl+p") + " " +
		th.Fg("muted").Render("commands")
}

func centerLines(s string, width int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderLandingFooter(width int) string {
	if width < 1 {
		return ""
	}
	left := m.compactLandingCwd(width)
	right := version.Version
	if right == "" {
		right = "0.0.0-dev"
	}
	left = m.theme.Fg("muted").Render(left)
	right = m.theme.Fg("muted").Render(right)
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func (m *Model) compactLandingCwd(width int) string {
	cwd := filepath.Clean(m.cwd)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(cwd, home); ok {
			cwd = "~" + rel
		}
	}
	maxW := width - len(version.Version) - 4
	if maxW < 12 {
		maxW = 12
	}
	return trimSeed(cwd, maxW)
}

// renderInputBox produces the bordered input area: a textarea stacked on
// top of an inline status line (Mode · Model · Provider), all inside one
// surfaced rounded frame.
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
	if m.slash.Visible && m.slashInline {
		pickerPrefix = m.slash.InlineView(mainW-4) + "\n"
	} else if m.filePicker.Visible && len(m.filePicker.Matches) > 0 {
		// Leave 2 cols of breathing room inside the border + padding.
		pickerPrefix = m.filePicker.View(mainW-4) + "\n"
	}

	body := pickerPrefix + m.input.View() + "\n" + strings.TrimRight(inline, "\n")

	style := m.theme.Bg("surface").
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(m.theme.Fg(m.inputBorderTone()).GetForeground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1).
		Width(mainW - 1)
	return style.Render(body) + "\n"
}

func (m *Model) inputBorderTone() string {
	switch m.mode {
	case modePlan:
		return "role_thinking"
	case modeBTW:
		return "accent"
	default:
		return "role_user"
	}
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
	style := m.theme.Bg("surface").
		Border(m.theme.Border()).
		BorderForeground(border).
		Foreground(m.theme.Fg("text").GetForeground()).
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
	right := strings.TrimRight(body, "\n")
	rightW := lipgloss.Width(right)
	if leftRaw := m.compactStatusLeft(width - rightW - 2); leftRaw != "" {
		left := m.theme.Fg("muted").Render(leftRaw)
		pad := width - lipgloss.Width(left) - rightW
		if pad > 0 {
			return left + strings.Repeat(" ", pad) + right + "\n"
		}
	}
	// Fallback: right-align the busy/usage side when the terminal is too
	// narrow for cwd/branch/version.
	if pad := width - rightW; pad > 0 {
		return strings.Repeat(" ", pad) + right + "\n"
	}
	return right + "\n"
}

func (m *Model) compactStatusLeft(maxW int) string {
	if maxW < 24 {
		return ""
	}
	parts := []string{m.compactStatusCwd(maxW)}
	if git := m.compactStatusGit(); git != "" {
		parts = append(parts, git)
	}
	if session := m.compactStatusSession(); session != "" {
		parts = append(parts, session)
	}
	if version.Version != "" {
		parts = append(parts, version.Version)
	}
	return trimSeed(strings.Join(parts, " · "), maxW)
}

const statusGitCacheTTL = 5 * time.Second

func (m *Model) compactStatusGit() string {
	if m.statusGitCwd == m.cwd && !m.statusGitCheckedAt.IsZero() && time.Since(m.statusGitCheckedAt) < statusGitCacheTTL {
		return m.statusGitSummary
	}
	summary := currentGitBranch(m.cwd)
	if summary != "" && gitWorktreeDirty(m.cwd) {
		summary += "*"
	}
	m.statusGitCwd = m.cwd
	m.statusGitSummary = summary
	m.statusGitCheckedAt = time.Now()
	return summary
}

func (m *Model) compactStatusCwd(width int) string {
	if repoCwd := compactRepoCwd(m.cwd); repoCwd != "" {
		return trimSeed(repoCwd, max(12, width))
	}
	cwd := filepath.Clean(m.cwd)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(cwd, home); ok {
			cwd = "~" + rel
		}
	}
	return trimSeed(cwd, max(12, width))
}

func compactRepoCwd(cwd string) string {
	clean := filepath.Clean(cwd)
	repo := runtime.FindRepoRoot(clean)
	if repo == "" {
		return ""
	}
	name := filepath.Base(repo)
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	rel, err := filepath.Rel(repo, clean)
	if err != nil || rel == "." {
		return name
	}
	if strings.HasPrefix(rel, "..") {
		return name
	}
	return filepath.ToSlash(filepath.Join(name, rel))
}

func (m *Model) compactStatusSession() string {
	if m.session == nil || m.session.ID == "" {
		return ""
	}
	if label := runtime.ReadDescription(m.session.WorktreePath); label != "" {
		return "sess " + label
	}
	return "sess " + shortSessionID(m.session.ID)
}

func currentGitBranch(cwd string) string {
	repo := runtime.FindRepoRoot(cwd)
	if repo == "" {
		return ""
	}

	root, err := workdirpath.OpenRootNoSymlink(repo)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Stat(".git"); err != nil || !info.IsDir() {
		return ""
	}
	head, err := workdirpath.ReadRootRegularFileLimited(root, filepath.Join(".git", "HEAD"), maxGitHeadFileBytes)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(value, "ref: refs/heads/"); ok {
		return ref
	}
	if len(value) >= 7 {
		return value[:7]
	}
	return value
}

func gitWorktreeDirty(cwd string) bool {
	repo := runtime.FindRepoRoot(cwd)
	if repo == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--untracked-files=normal") // #nosec G204 -- fixed git status probe rooted at detected repository.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	out := limitedio.NewBuffer(maxGitStatusProbeBytes)
	errBuf := limitedio.NewBuffer(maxGitStatusProbeErrorBytes)
	cmd.Stdout = out
	cmd.Stderr = errBuf
	err := cmd.Run()
	if err != nil || ctx.Err() != nil {
		return false
	}
	return out.Len() > 0 || out.Truncated()
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

type sidebarLine struct {
	Text string
	Tone string
}

func (m *Model) renderSidebar(width int) string {
	// Session description — shown below the stado title so the user
	// knows which session they're in. Empty when unset, template
	// conditionally renders.
	sessionLabel := ""
	if m.session != nil {
		sessionLabel = runtime.ReadDescription(m.session.WorktreePath)
	}
	data := map[string]any{
		"Title":        "stado",
		"Version":      "0.0.0-dev",
		"SessionLabel": sessionLabel,
		"SessionMeta":  m.sidebarSessionMeta(),
		"NowLines":     m.sidebarNowLines(),
		"Subagents":    m.sidebarSubagentLines(),
		"RiskLines":    m.sidebarRiskLines(),
		"AgentLines":   m.sidebarAgentLines(),
		"RepoLines":    m.sidebarRepoLines(),
		"LogLines":     m.sidebarLogLines(),
		"TodoSummary":  m.sidebarTodoSummary(),
		"Todos":        m.sidebarTodos(),
		"Width":        width - 4,
	}
	body, err := m.renderer.Exec("sidebar", data)
	if err != nil {
		body = "[sidebar render error] " + err.Error()
	}
	return lipgloss.NewStyle().
		Background(m.theme.Bg("surface").GetBackground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(1, 1).
		Width(width - 2).
		Height(m.height).
		Render(body)
}

func (m *Model) sidebarSessionMeta() string {
	parts := []string{}
	if m.session != nil && m.session.ID != "" {
		parts = append(parts, "sess "+shortSessionID(m.session.ID))
		parts = append(parts, fmt.Sprintf("turn %d", m.session.Turn()))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) sidebarNowLines() []sidebarLine {
	lines := []sidebarLine{m.sidebarStateLine()}
	if tool, _, ok := m.sidebarRunningTool(); ok {
		lines = append(lines, sidebarLine{Text: "tool: " + tool, Tone: "role_tool"})
	}
	if elapsed := m.sidebarElapsed(); elapsed != "" {
		lines = append(lines, sidebarLine{Text: "elapsed: " + elapsed, Tone: "muted"})
	}
	if m.queuedPrompt != "" {
		lines = append(lines, sidebarLine{
			Text: "queued: " + trimSeed(m.queuedPrompt, 32),
			Tone: "accent",
		})
	}
	if m.recoveryPluginActive {
		name := m.recoveryPluginName
		if name == "" {
			name = "plugin"
		}
		lines = append(lines, sidebarLine{
			Text: "recovery: " + name + " will replay the blocked prompt",
			Tone: "warning",
		})
	}
	if m.providerProbePending {
		lines = append(lines, sidebarLine{
			Text: "probing local providers before the first turn",
			Tone: "muted",
		})
	} else if m.provider == nil && m.buildProvider != nil {
		lines = append(lines, sidebarLine{
			Text: "no provider configured",
			Tone: "muted",
		})
	}
	return lines
}

func (m *Model) sidebarStateLine() sidebarLine {
	if m.recoveryPluginActive {
		name := m.recoveryPluginName
		if name == "" {
			name = "plugin"
		}
		return sidebarLine{Text: "waiting for " + name + " recovery", Tone: "warning"}
	}
	switch m.state {
	case stateStreaming:
		if m.compacting {
			return sidebarLine{Text: "compacting conversation", Tone: "warning"}
		}
		return sidebarLine{Text: "streaming turn", Tone: "warning"}
	case stateApproval:
		return sidebarLine{Text: "awaiting approval", Tone: "warning"}
	case stateCompactionPending:
		return sidebarLine{Text: "review compaction summary", Tone: "accent"}
	case stateCompactionEditing:
		return sidebarLine{Text: "editing compaction summary", Tone: "accent"}
	case stateError:
		text := "last turn failed"
		if strings.TrimSpace(m.errorMsg) != "" {
			text = "error: " + trimSeed(m.errorMsg, 32)
		}
		return sidebarLine{Text: text, Tone: "error"}
	case stateQuitConfirm:
		return sidebarLine{Text: "confirm exit", Tone: "warning"}
	default:
		return sidebarLine{Text: "idle", Tone: "text"}
	}
}

func (m *Model) sidebarRunningTool() (string, time.Time, bool) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		blk := m.blocks[i]
		if blk.kind == "tool" && blk.toolName != "" && blk.endedAt.IsZero() {
			return blk.toolName, blk.startedAt, true
		}
	}
	return "", time.Time{}, false
}

func (m *Model) sidebarElapsed() string {
	if _, startedAt, ok := m.sidebarRunningTool(); ok && !startedAt.IsZero() {
		return sidebarDurationString(time.Since(startedAt))
	}
	if m.state == stateStreaming && !m.turnStart.IsZero() {
		return sidebarDurationString(time.Since(m.turnStart))
	}
	return ""
}

func sidebarDurationString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d >= time.Minute {
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d >= 10*time.Second {
		d = d.Round(time.Second)
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	d = d.Round(100 * time.Millisecond)
	return d.String()
}

func (m *Model) sidebarRiskLines() []sidebarLine {
	lines := []sidebarLine{m.sidebarContextLine(), m.sidebarBudgetLine(), m.sidebarSandboxLine()}
	out := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line.Text) != "" {
			out = append(out, line)
		}
	}
	return out
}

func (m *Model) sidebarContextLine() sidebarLine {
	cap := m.providerCaps().MaxContextTokens
	if cap <= 0 {
		if !m.sidebarDebug {
			return sidebarLine{}
		}
		return sidebarLine{Text: "ctx unknown until the provider reports a limit", Tone: "muted"}
	}
	if m.usage.InputTokens <= 0 && !m.sidebarDebug {
		return sidebarLine{}
	}
	if m.usage.InputTokens <= 0 {
		return sidebarLine{Text: fmt.Sprintf("ctx 0%% / hard %d%%", int(100*m.ctxHardThreshold)), Tone: "muted"}
	}
	fraction := m.contextFraction()
	tone := "text"
	switch {
	case fraction >= m.ctxHardThreshold:
		tone = "error"
	case fraction >= m.ctxSoftThreshold:
		tone = "warning"
	}
	return sidebarLine{
		Text: fmt.Sprintf("ctx %d%% / hard %d%%", int(100*fraction), int(100*m.ctxHardThreshold)),
		Tone: tone,
	}
}

func (m *Model) sidebarBudgetLine() sidebarLine {
	if m.budgetWarnUSD <= 0 && m.budgetHardUSD <= 0 {
		if !m.sidebarDebug {
			return sidebarLine{}
		}
		return sidebarLine{Text: "budget unbounded", Tone: "muted"}
	}
	limit := m.budgetHardUSD
	label := fmt.Sprintf("$%.2f", limit)
	if limit <= 0 {
		limit = m.budgetWarnUSD
		label = "warn $" + fmt.Sprintf("%.2f", limit)
	}
	tone := "text"
	switch {
	case m.budgetHardUSD > 0 && m.usage.CostUSD >= m.budgetHardUSD && !m.budgetAcked:
		tone = "error"
	case m.budgetWarnUSD > 0 && m.usage.CostUSD >= m.budgetWarnUSD:
		tone = "warning"
	}
	text := fmt.Sprintf("budget $%.2f / %s", m.usage.CostUSD, label)
	if m.budgetAcked {
		text += " (acked)"
	}
	return sidebarLine{Text: text, Tone: tone}
}

func (m *Model) sidebarSandboxLine() sidebarLine {
	if m.executor == nil || m.executor.Runner == nil {
		if !m.sidebarDebug {
			return sidebarLine{}
		}
		return sidebarLine{Text: "sandbox unavailable", Tone: "muted"}
	}
	name := m.executor.Runner.Name()
	if strings.TrimSpace(name) == "" {
		name = "unknown"
	}
	tone := "text"
	if name == "none" {
		tone = "error"
	}
	if !m.sidebarDebug && tone != "error" {
		return sidebarLine{}
	}
	return sidebarLine{Text: "sandbox: " + name, Tone: tone}
}

func (m *Model) sidebarAgentLines() []sidebarLine {
	modelLine := modelOrPlaceholder(m.model)
	if provider := strings.TrimSpace(m.providerDisplayName()); provider != "" {
		modelLine += " via " + provider
	}
	lines := []sidebarLine{{
		Text: "agent: " + m.mode.String(),
		Tone: "accent",
	}, {
		Text: modelLine,
		Tone: "text",
	}}
	if m.systemPromptPath != "" {
		lines = append(lines, sidebarLine{
			Text: "instructions: " + filepath.Base(m.systemPromptPath),
			Tone: "muted",
		})
	}
	if n := len(m.skills); n > 0 {
		verb := "skills"
		if n == 1 {
			verb = "skill"
		}
		lines = append(lines, sidebarLine{
			Text: fmt.Sprintf("%d %s loaded · /skill", n, verb),
			Tone: "accent",
		})
	}
	if pluginSummary := m.sidebarPluginSummary(); pluginSummary != "" {
		lines = append(lines, sidebarLine{
			Text: "plugins: " + pluginSummary,
			Tone: "muted",
		})
	}
	return lines
}

func (m *Model) sidebarPluginSummary() string {
	if len(m.backgroundPlugins) == 0 {
		return ""
	}
	names := make([]string, 0, len(m.backgroundPlugins))
	for _, bp := range m.backgroundPlugins {
		if bp == nil {
			continue
		}
		name := strings.TrimSpace(bp.Name())
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	switch len(names) {
	case 0:
		return fmt.Sprintf("%d active", len(m.backgroundPlugins))
	case 1:
		return names[0]
	case 2:
		return names[0] + ", " + names[1]
	default:
		return fmt.Sprintf("%s +%d", names[0], len(names)-1)
	}
}

func (m *Model) sidebarRepoLines() []sidebarLine {
	repoRoot := m.sidebarRepoRoot()
	if repoRoot == "" {
		return nil
	}
	lines := []sidebarLine{{
		Text: "repo: " + filepath.Base(repoRoot),
		Tone: "text",
	}}
	if rel := sidebarRepoPath(repoRoot, m.cwd); rel != "" {
		lines = append(lines, sidebarLine{
			Text: "path: " + rel,
			Tone: "muted",
		})
	} else if m.session != nil && m.session.ID != "" {
		lines = append(lines, sidebarLine{
			Text: "worktree: " + shortSessionID(m.session.ID),
			Tone: "muted",
		})
	}
	return lines
}

func (m *Model) sidebarRepoRoot() string {
	if m.session != nil && m.session.WorktreePath != "" {
		if pinned := runtime.ReadUserRepoPin(m.session.WorktreePath); pinned != "" {
			return pinned
		}
	}
	if m.cwd == "" {
		return ""
	}
	return runtime.FindRepoRoot(m.cwd)
}

func sidebarRepoPath(repoRoot, cwd string) string {
	if repoRoot == "" || cwd == "" {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, cwd)
	if err != nil || rel == "" || rel == "." {
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

func (m *Model) sidebarTodoSummary() string {
	if len(m.todos) == 0 {
		return ""
	}
	open := 0
	done := 0
	for _, td := range m.todos {
		if td.Status == "done" {
			done++
			continue
		}
		open++
	}
	return fmt.Sprintf("%d open / %d done", open, done)
}

func (m *Model) sidebarTodos() []todo {
	if len(m.todos) == 0 {
		return nil
	}
	out := make([]todo, 0, 3)
	for _, td := range m.todos {
		if td.Status == "done" {
			continue
		}
		out = append(out, td)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
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
	first := true
	for i := range m.blocks {
		if !m.shouldRenderBlock(m.blocks[i]) {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		out := m.renderBlockCached(i, width)
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
		blk.cachedResult == blk.toolResult &&
		thinkingCacheOK {
		return blk.cachedOut
	}
	out, _ := m.renderBlock(*blk, width)
	blk.cachedOut = out
	blk.cachedWidth = width
	blk.cachedMeta = blk.meta
	blk.cachedDetails = blk.details
	blk.cachedExpand = blk.expanded
	blk.cachedResult = blk.toolResult
	if blk.kind == "thinking" {
		blk.cachedThinkingMode = m.thinkingMode
	}
	return out
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
		return m.renderer.Exec("message_user", map[string]any{
			"Body":   blk.body,
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
