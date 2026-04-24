package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) renderStatusModal(screenWidth, screenHeight int) string {
	modalW := screenWidth * 3 / 5
	if modalW < 58 {
		modalW = 58
	}
	if modalW > 96 {
		modalW = 96
	}
	if modalW > screenWidth-4 {
		modalW = screenWidth - 4
	}
	if modalW < 24 {
		modalW = 24
	}

	innerW := modalW - 4
	title := m.theme.Fg("text").Bold(true).Render("Status")
	esc := m.theme.Fg("muted").Render("esc")

	var b strings.Builder
	b.WriteString(statusTwoCol(innerW, title, esc))
	b.WriteString("\n\n")
	m.writeStatusSection(&b, innerW, "Agent", m.statusAgentRows())
	m.writeStatusSection(&b, innerW, "Runtime", m.statusRuntimeRows())
	m.writeStatusSection(&b, innerW, "Context", m.statusContextRows())
	m.writeStatusSection(&b, innerW, "Extensions", m.statusExtensionRows())

	box := lipgloss.NewStyle().
		Border(m.theme.Border()).
		BorderForeground(m.theme.Fg("border").GetForeground()).
		Background(m.theme.Bg("background").GetBackground()).
		Foreground(m.theme.Fg("text").GetForeground()).
		Padding(0, 1).
		Width(modalW).
		Render(strings.TrimRight(b.String(), "\n"))
	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) writeStatusSection(b *strings.Builder, width int, title string, rows []statusRow) {
	b.WriteString(m.theme.Fg("accent").Bold(true).Render(title))
	b.WriteString("\n")
	for _, row := range rows {
		key := m.theme.Fg("muted").Render(row.Key)
		value := m.theme.Fg(row.Tone).Render(row.Value)
		b.WriteString("  " + statusTwoCol(width-2, key, value) + "\n")
	}
	b.WriteString("\n")
}

type statusRow struct {
	Key   string
	Value string
	Tone  string
}

func (m *Model) statusAgentRows() []statusRow {
	provider := strings.TrimSpace(m.providerDisplayName())
	if provider == "" {
		provider = "not configured"
	}
	model := strings.TrimSpace(m.model)
	if model == "" {
		model = "not configured"
	}
	caps := "not initialized"
	if m.provider != nil {
		c := m.providerCaps()
		caps = fmt.Sprintf("ctx %d, cache %v, thinking %v, vision %v",
			c.MaxContextTokens, c.SupportsPromptCache, c.SupportsThinking, c.SupportsVision)
	}
	return []statusRow{
		{"agent", m.mode.String(), "text"},
		{"model", model, "text"},
		{"provider", provider, "text"},
		{"capabilities", caps, "muted"},
	}
}

func (m *Model) statusRuntimeRows() []statusRow {
	session := "none"
	worktree := m.cwd
	if m.session != nil {
		session = m.session.ID
		if strings.TrimSpace(m.session.WorktreePath) != "" {
			worktree = m.session.WorktreePath
		}
	}
	sandbox := "unavailable"
	sandboxTone := "muted"
	if m.executor != nil && m.executor.Runner != nil {
		sandbox = m.executor.Runner.Name()
		sandboxTone = "text"
		if sandbox == "none" {
			sandboxTone = "error"
		}
	}
	tools := "none"
	if m.executor != nil && m.executor.Registry != nil {
		tools = fmt.Sprintf("%d visible / %d registered", len(m.visibleTools()), len(m.executor.Registry.All()))
	}
	return []statusRow{
		{"session", session, "text"},
		{"worktree", filepath.Base(worktree), "muted"},
		{"sandbox", sandbox, sandboxTone},
		{"tools", tools, "text"},
	}
}

func (m *Model) statusContextRows() []statusRow {
	budget := "unbounded"
	if m.budgetWarnUSD > 0 || m.budgetHardUSD > 0 {
		budget = fmt.Sprintf("warn $%.2f, hard $%.2f", m.budgetWarnUSD, m.budgetHardUSD)
	}
	return []statusRow{
		{"tokens", fmt.Sprintf("%s in / %s out", humanize(m.usage.InputTokens), humanize(m.usage.OutputTokens)), "text"},
		{"cost", fmt.Sprintf("$%.4f", m.usage.CostUSD), "text"},
		{"budget", budget, "muted"},
		{"context", fmt.Sprintf("soft %.0f%%, hard %.0f%%", m.ctxSoftThreshold*100, m.ctxHardThreshold*100), "muted"},
	}
}

func (m *Model) statusExtensionRows() []statusRow {
	plugins := "none active"
	if summary := m.sidebarPluginSummary(); summary != "" {
		plugins = summary
	}
	mcp := "0 configured"
	otel := "disabled"
	if m.cfg != nil {
		mcp = fmt.Sprintf("%d configured", len(m.cfg.MCP.Servers))
		if m.cfg.OTel.Enabled {
			otel = strings.TrimSpace(m.cfg.OTel.Protocol + " " + m.cfg.OTel.Endpoint)
			if otel == "" {
				otel = "enabled"
			}
		}
	}
	instructions := "none"
	if strings.TrimSpace(m.systemPromptPath) != "" {
		instructions = filepath.Base(m.systemPromptPath)
	}
	return []statusRow{
		{"plugins", plugins, "text"},
		{"mcp", mcp, "text"},
		{"lsp", lspStatusSummary(), "muted"},
		{"otel", otel, "muted"},
		{"instructions", instructions, "muted"},
	}
}

func lspStatusSummary() string {
	servers := []struct {
		lang string
		bin  string
	}{
		{"go", "gopls"},
		{"rust", "rust-analyzer"},
		{"python", "pyright"},
		{"ts/js", "typescript-language-server"},
	}
	var available []string
	for _, srv := range servers {
		if _, err := exec.LookPath(srv.bin); err == nil {
			available = append(available, srv.lang)
		}
	}
	if len(available) == 0 {
		return "activates when supported files are read; no servers found"
	}
	return "activates when supported files are read; available: " + strings.Join(available, ", ")
}

func statusTwoCol(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		budget := width - rw - 2
		if budget < 8 {
			budget = max(1, width/2)
		}
		left = truncate(left, budget)
		lw = lipgloss.Width(left)
	}
	if lw+rw+1 > width {
		budget := width - lw - 2
		if budget < 8 {
			budget = max(1, width-lw-1)
		}
		right = truncate(right, budget)
		rw = lipgloss.Width(right)
	}
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}
