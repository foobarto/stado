package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/foobarto/stado/internal/config"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
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
		m.statusCredentialsRow(provider),
		{Key: "capabilities", Value: caps, Tone: "muted", Action: "/provider"},
	}
}

func (m *Model) statusCredentialsRow(provider string) statusRow {
	provider = strings.TrimSpace(provider)
	value, tone, action := providerCredentialHealth(provider)
	return statusRow{Key: "credentials", Value: value, Tone: tone, Action: action}
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
	plugins, pluginTone := m.statusPluginSummary()
	mcp, mcpTone := m.statusMCPSummary()
	otel := "disabled"
	if m.cfg != nil {
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
		{Key: "plugins", Value: plugins, Tone: pluginTone, Action: "/plugin"},
		{Key: "mcp", Value: mcp, Tone: mcpTone, Action: "config.toml"},
		{Key: "lsp", Value: lspStatusSummary(), Tone: "muted"},
		{Key: "otel", Value: otel, Tone: "muted", Action: "config.toml"},
	}
	if traceID := m.statusTraceID(); traceID != "" {
		rows = append(rows, statusRow{Key: "trace", Value: traceID, Tone: "muted"})
	}
	rows = append(rows, statusRow{Key: "instructions", Value: instructions, Tone: "muted", Action: "/context"})
	return rows
}

func (m *Model) statusPluginSummary() (string, string) {
	summary := m.sidebarPluginSummary()
	if summary == "" {
		summary = "none active"
	}
	state := "healthy"
	switch {
	case m.backgroundTickRunning && m.backgroundTickQueued:
		state = "ticking, queued"
	case m.backgroundTickRunning:
		state = "ticking"
	case m.backgroundTickQueued:
		state = "queued"
	}
	value := summary
	if len(m.backgroundPlugins) > 0 || state != "healthy" {
		value = fmt.Sprintf("%s (%s)", summary, state)
	}
	if len(m.backgroundPluginIssues) == 0 {
		return value, "text"
	}
	last := trimSeed(m.backgroundPluginIssues[len(m.backgroundPluginIssues)-1], 96)
	return fmt.Sprintf("%s; last issue: %s", value, last), "error"
}

func (m *Model) statusMCPSummary() (string, string) {
	var servers map[string]config.MCPServer
	if m.cfg != nil {
		servers = m.cfg.MCP.Servers
	}
	statuses := stadoruntime.MCPStatusSnapshot()
	if len(statuses) == 0 {
		return statusMCPSummary(servers), "text"
	}
	return statusMCPLiveSummary(servers, statuses)
}

func statusMCPSummary(servers map[string]config.MCPServer) string {
	if len(servers) == 0 {
		return "0 configured"
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	const maxNames = 3
	shown := names
	suffix := ""
	if len(names) > maxNames {
		shown = names[:maxNames]
		suffix = fmt.Sprintf(" +%d", len(names)-maxNames)
	}
	return fmt.Sprintf("%d configured: %s%s", len(names), strings.Join(shown, ", "), suffix)
}

func statusMCPLiveSummary(servers map[string]config.MCPServer, statuses []stadoruntime.MCPServerStatus) (string, string) {
	if len(servers) == 0 && len(statuses) == 0 {
		return "0 configured", "text"
	}
	configured := len(servers)
	if configured == 0 {
		configured = len(statuses)
	}
	allowed := map[string]struct{}{}
	for name := range servers {
		allowed[name] = struct{}{}
	}
	connected := 0
	tools := 0
	var firstErr string
	for _, st := range statuses {
		if len(allowed) > 0 {
			if _, ok := allowed[st.Name]; !ok {
				continue
			}
		}
		if st.Connected {
			connected++
			tools += st.ToolCount
		}
		if firstErr == "" && strings.TrimSpace(st.Error) != "" {
			firstErr = st.Error
		}
	}
	if firstErr != "" {
		return fmt.Sprintf("%d configured; %d connected; last error: %s", configured, connected, trimSeed(firstErr, 88)), "error"
	}
	if connected == 0 && configured > 0 {
		return fmt.Sprintf("%d configured; 0 connected", configured), "muted"
	}
	return fmt.Sprintf("%d configured; %d connected; %d tools", configured, connected, tools), "text"
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
