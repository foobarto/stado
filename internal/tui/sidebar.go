package tui

// Right-hand sidebar rendering + width management. The sidebar is a
// single column to the right of the conversation viewport that surfaces
// run-state at a glance: which session/agent/model is active, what the
// turn is doing right now, context/budget/sandbox risk lines, repo
// summary, plugin summary, and a todo digest. Built from a Go template
// (`sidebar`) executed by the renderer; the data preparation lives in
// the `sidebar*` helpers here.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/runtime"
)

const sidebarResizeStep = 4

type sidebarLine struct {
	Text string
	Tone string
}

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
