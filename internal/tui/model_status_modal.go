package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	oteltrace "go.opentelemetry.io/otel/trace"
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
		if strings.TrimSpace(row.Action) != "" {
			value += m.theme.Fg("muted").Render(" " + row.Action)
		}
		b.WriteString("  " + statusTwoCol(width-2, key, value) + "\n")
	}
	b.WriteString("\n")
}

type statusRow struct {
	Key    string
	Value  string
	Tone   string
	Action string
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
		caps = fmt.Sprintf("ctx %s, cache %s, think %s, vision %s",
			humanize(c.MaxContextTokens), yesNo(c.SupportsPromptCache), yesNo(c.SupportsThinking), yesNo(c.SupportsVision))
	}
	return []statusRow{
		{Key: "agent", Value: m.mode.String(), Tone: "text", Action: "tab"},
		{Key: "model", Value: model, Tone: "text", Action: "/model"},
		{Key: "provider", Value: provider, Tone: "text", Action: "/model"},
		{Key: "capabilities", Value: caps, Tone: "muted", Action: "/provider"},
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
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
		{Key: "session", Value: session, Tone: "text", Action: "/sessions"},
		{Key: "worktree", Value: filepath.Base(worktree), Tone: "muted"},
		{Key: "sandbox", Value: sandbox, Tone: sandboxTone, Action: "/tools"},
		{Key: "tools", Value: tools, Tone: "text", Action: "/tools"},
	}
}

func (m *Model) statusContextRows() []statusRow {
	budget := "unbounded"
	if m.budgetWarnUSD > 0 || m.budgetHardUSD > 0 {
		budget = fmt.Sprintf("warn $%.2f, hard $%.2f", m.budgetWarnUSD, m.budgetHardUSD)
	}
	return []statusRow{
		{Key: "tokens", Value: fmt.Sprintf("%s in / %s out", humanize(m.usage.InputTokens), humanize(m.usage.OutputTokens)), Tone: "text", Action: "/context"},
		{Key: "cost", Value: fmt.Sprintf("$%.4f", m.usage.CostUSD), Tone: "text", Action: "/budget"},
		{Key: "budget", Value: budget, Tone: "muted", Action: "/budget"},
		{Key: "context", Value: fmt.Sprintf("soft %.0f%%, hard %.0f%%", m.ctxSoftThreshold*100, m.ctxHardThreshold*100), Tone: "muted", Action: "/context"},
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
	rows := []statusRow{
		{Key: "plugins", Value: plugins, Tone: "text", Action: "/plugin"},
		{Key: "mcp", Value: mcp, Tone: "text", Action: "config.toml"},
		{Key: "lsp", Value: lspStatusSummary(), Tone: "muted"},
		{Key: "otel", Value: otel, Tone: "muted", Action: "config.toml"},
	}
	if traceID := m.statusTraceID(); traceID != "" {
		rows = append(rows, statusRow{Key: "trace", Value: traceID, Tone: "muted"})
	}
	rows = append(rows, statusRow{Key: "instructions", Value: instructions, Tone: "muted", Action: "/context"})
	return rows
}

func (m *Model) statusTraceID() string {
	if m == nil || m.rootCtx == nil {
		return ""
	}
	sc := oteltrace.SpanContextFromContext(m.rootCtx)
	if !sc.IsValid() || !sc.TraceID().IsValid() {
		return ""
	}
	return sc.TraceID().String()
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
