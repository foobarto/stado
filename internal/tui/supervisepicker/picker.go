// Package supervisepicker implements the trusted /supervise setup wizard.
// It collects operator choices only; it neither starts models nor approves the
// watchdog's later baseline proposal.
package supervisepicker

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/supervise"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
)

const maxFieldBytes = 4096

type Draft struct {
	Objective         string
	AcceptanceHints   string
	DefinitionHints   string
	VerificationHints string
	Config            supervise.Config
}

type Action int

const (
	ActionNone Action = iota
	ActionStart
	ActionCancel
)

type Result struct {
	Action Action
	Draft  Draft
}

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldChoice
	fieldToggle
	fieldAction
)

type field struct {
	label  string
	kind   fieldKind
	get    func() string
	set    func(string)
	values []string
	action string
}

type Model struct {
	Visible  bool
	Advanced bool
	Width    int
	Height   int
	Cursor   int
	Notice   string
	Draft    Draft
	Out      Result
}

func New() *Model { return &Model{} }

func (m *Model) Open(objective, provider, model string) {
	cfg := supervise.DefaultConfig()
	cfg.Watchdog.Provider, cfg.Watchdog.Model = provider, model
	cfg.Verifier.Provider, cfg.Verifier.Model = provider, model
	m.Visible, m.Advanced, m.Cursor, m.Notice = true, false, 0, ""
	m.Draft = Draft{Objective: strings.TrimSpace(objective), Config: cfg}
	m.Out = Result{}
}

func (m *Model) Close() { m.Visible = false }

func (m *Model) TakeResult() Result { out := m.Out; m.Out = Result{}; return out }

func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	fields := m.fields()
	if m.Cursor >= len(fields) {
		m.Cursor = len(fields) - 1
	}
	switch key.String() {
	case "esc", "ctrl+g":
		m.Out = Result{Action: ActionCancel}
		m.Close()
		return nil, true
	case "ctrl+a":
		m.Advanced = !m.Advanced
		m.Cursor, m.Notice = 0, ""
		return nil, true
	case "up", "shift+tab":
		m.move(-1, len(fields))
		return nil, true
	case "down", "tab":
		m.move(1, len(fields))
		return nil, true
	case "left":
		m.cycle(fields[m.Cursor], -1)
		return nil, true
	case "right", "space":
		if fields[m.Cursor].kind != fieldText {
			m.cycle(fields[m.Cursor], 1)
			return nil, true
		}
	case "backspace":
		if fields[m.Cursor].kind == fieldText {
			fields[m.Cursor].set(textutil.TrimLastRune(fields[m.Cursor].get()))
			m.Notice = ""
		}
		return nil, true
	case "ctrl+u":
		if fields[m.Cursor].kind == fieldText {
			fields[m.Cursor].set("")
			m.Notice = ""
		}
		return nil, true
	case "enter":
		if fields[m.Cursor].kind == fieldAction {
			switch fields[m.Cursor].action {
			case "advanced":
				m.Advanced, m.Cursor = true, 0
			case "basic":
				m.Advanced, m.Cursor = false, 0
			case "start":
				m.start()
			}
		} else if fields[m.Cursor].kind != fieldText {
			m.cycle(fields[m.Cursor], 1)
		}
		return nil, true
	}
	if fields[m.Cursor].kind == fieldText && key.Text != "" {
		fields[m.Cursor].set(textutil.AppendWithinBytes(fields[m.Cursor].get(), key.Text, maxFieldBytes))
		m.Notice = ""
		return nil, true
	}
	return nil, true
}

func (m *Model) start() {
	m.Draft.Objective = strings.TrimSpace(m.Draft.Objective)
	if m.Draft.Objective == "" {
		m.Notice = "Objective is required."
		return
	}
	cfg, err := supervise.NormalizeConfig(m.Draft.Config)
	if err != nil {
		m.Notice = err.Error()
		return
	}
	m.Draft.Config = cfg
	m.Out = Result{Action: ActionStart, Draft: m.Draft}
	m.Close()
}

func (m *Model) move(delta, total int) {
	if total > 0 {
		m.Cursor = (m.Cursor + delta + total) % total
	}
	m.Notice = ""
}

func (m *Model) cycle(f field, delta int) {
	if f.kind == fieldToggle {
		if f.get() == "on" {
			f.set("off")
		} else {
			f.set("on")
		}
		return
	}
	if len(f.values) == 0 {
		return
	}
	cur := 0
	for i, v := range f.values {
		if v == f.get() {
			cur = i
			break
		}
	}
	f.set(f.values[(cur+delta+len(f.values))%len(f.values)])
}

func (m *Model) fields() []field {
	if !m.Advanced {
		return []field{
			m.text("Objective", func() string { return m.Draft.Objective }, func(v string) { m.Draft.Objective = v }),
			m.choice("Watchdog mode", func() string { return string(m.Draft.Config.Mode) }, func(v string) { m.Draft.Config.Mode = supervise.Mode(v) }, "event", "live"),
			m.choice("Plan pivots", func() string { return string(m.Draft.Config.PivotApproval) }, func(v string) { m.Draft.Config.PivotApproval = supervise.PivotApproval(v) }, "user", "watchdog"),
			m.choice("Assurance profile", func() string { return string(m.Draft.Config.Profile) }, func(v string) { m.Draft.Config.Profile = supervise.Profile(v) }, "standard", "high_assurance", "custom"),
			{label: "Advanced settings", kind: fieldAction, action: "advanced"},
			{label: "Start requirements review", kind: fieldAction, action: "start"},
		}
	}
	return []field{
		m.text("Acceptance/gate hints", func() string { return m.Draft.AcceptanceHints }, func(v string) { m.Draft.AcceptanceHints = v }),
		m.text("Definition-of-done hints", func() string { return m.Draft.DefinitionHints }, func(v string) { m.Draft.DefinitionHints = v }),
		m.text("Verification hints", func() string { return m.Draft.VerificationHints }, func(v string) { m.Draft.VerificationHints = v }),
		m.text("Watchdog provider", func() string { return m.Draft.Config.Watchdog.Provider }, func(v string) { m.Draft.Config.Watchdog.Provider = strings.TrimSpace(v) }),
		m.text("Watchdog model", func() string { return m.Draft.Config.Watchdog.Model }, func(v string) { m.Draft.Config.Watchdog.Model = strings.TrimSpace(v) }),
		m.choice("Watchdog thinking", func() string { return string(m.Draft.Config.Watchdog.Thinking) }, func(v string) { m.Draft.Config.Watchdog.Thinking = supervise.Thinking(v) }, "auto", "on", "off"),
		m.integer("Watchdog thinking budget", &m.Draft.Config.Watchdog.ThinkingBudgetTokens),
		m.choice("Watchdog effort", func() string { return string(m.Draft.Config.Watchdog.Effort) }, func(v string) { m.Draft.Config.Watchdog.Effort = supervise.Effort(v) }, "low", "medium", "high", "xhigh", "max"),
		m.integer("Watchdog token cap", &m.Draft.Config.WatchdogBudget.TokenCap),
		m.decimal("Watchdog cost cap USD", &m.Draft.Config.WatchdogBudget.CostCapUSD),
		m.integer("Watchdog timeout seconds", &m.Draft.Config.WatchdogBudget.TimeoutSeconds),
		m.text("Verifier provider", func() string { return m.Draft.Config.Verifier.Provider }, func(v string) { m.Draft.Config.Verifier.Provider = strings.TrimSpace(v) }),
		m.text("Verifier model", func() string { return m.Draft.Config.Verifier.Model }, func(v string) { m.Draft.Config.Verifier.Model = strings.TrimSpace(v) }),
		m.choice("Verifier thinking", func() string { return string(m.Draft.Config.Verifier.Thinking) }, func(v string) { m.Draft.Config.Verifier.Thinking = supervise.Thinking(v) }, "auto", "on", "off"),
		m.integer("Verifier thinking budget", &m.Draft.Config.Verifier.ThinkingBudgetTokens),
		m.choice("Verifier effort", func() string { return string(m.Draft.Config.Verifier.Effort) }, func(v string) { m.Draft.Config.Verifier.Effort = supervise.Effort(v) }, "low", "medium", "high", "xhigh", "max"),
		m.integer("Verifier token cap", &m.Draft.Config.VerifierBudget.TokenCap),
		m.decimal("Verifier cost cap USD", &m.Draft.Config.VerifierBudget.CostCapUSD),
		m.integer("Verifier timeout seconds", &m.Draft.Config.VerifierBudget.TimeoutSeconds),
		m.integer("Event review retries", &m.Draft.Config.EventReviewRetries),
		m.integer("Failed-event pause limit", &m.Draft.Config.FailedEventLimit),
		m.integer("Correction attempts", &m.Draft.Config.CorrectionLimit),
		m.integer("Live retry base milliseconds", &m.Draft.Config.LiveRetryBaseMillis),
		m.integer("Live retry max milliseconds", &m.Draft.Config.LiveRetryMaxMillis),
		m.toggle("Watchdog required", &m.Draft.Config.WatchdogRequired),
		m.toggle("Verifier required", &m.Draft.Config.VerifierRequired),
		m.toggle("Advisory fallback", &m.Draft.Config.AllowAdvisoryFallback),
		{label: "Back to basic settings", kind: fieldAction, action: "basic"},
		{label: "Start requirements review", kind: fieldAction, action: "start"},
	}
}

func (m *Model) text(label string, get func() string, set func(string)) field {
	return field{label: label, kind: fieldText, get: get, set: set}
}
func (m *Model) choice(label string, get func() string, set func(string), values ...string) field {
	return field{label: label, kind: fieldChoice, get: get, set: set, values: values}
}
func (m *Model) integer(label string, value *int) field {
	return m.text(label, func() string { return strconv.Itoa(*value) }, func(v string) {
		if strings.TrimSpace(v) == "" {
			*value = 0
			return
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*value = n
		}
	})
}
func (m *Model) decimal(label string, value *float64) field {
	return m.text(label, func() string { return strconv.FormatFloat(*value, 'f', -1, 64) }, func(v string) {
		if strings.TrimSpace(v) == "" {
			*value = 0
			return
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			*value = n
		}
	})
}
func (m *Model) toggle(label string, value *bool) field {
	return field{label: label, kind: fieldToggle, get: func() string {
		if *value {
			return "on"
		}
		return "off"
	}, set: func(v string) { *value = v == "on" }}
}

func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := min(max(screenWidth*3/4, 64), 110)
	modalW = min(modalW, max(screenWidth-4, 1))
	innerW := max(modalW-6, 12)
	fields := m.fields()
	maxRows := max(screenHeight-10, 5)
	first, last := window(m.Cursor, len(fields), maxRows)
	var rows []string
	for i := first; i < last; i++ {
		rows = append(rows, m.renderField(fields[i], i == m.Cursor, innerW))
	}
	title := "Supervise setup"
	if m.Advanced {
		title += " · advanced"
	}
	parts := []string{lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render(title), ""}
	if first > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Muted).Render(fmt.Sprintf("  ↑ %d more", first)))
	}
	parts = append(parts, rows...)
	if last < len(fields) {
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Muted).Render(fmt.Sprintf("  ↓ %d more", len(fields)-last)))
	}
	if m.Notice != "" {
		parts = append(parts, "", lipgloss.NewStyle().Foreground(theme.Error).Render("✗ "+ansi.Truncate(m.Notice, innerW, "…")))
	}
	parts = append(parts, "", lipgloss.NewStyle().Foreground(theme.Muted).Render(ansi.Truncate("Reviewer providers receive filtered session, tool, and diff evidence.", innerW, "…")))
	parts = append(parts, lipgloss.NewStyle().Foreground(theme.Muted).Render("↑/↓ fields · ←/→ choices · Ctrl+A advanced · Enter action · Esc cancel"))
	modal := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Border).Background(theme.Background).Padding(0, 1).Width(modalW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, modal)
}

func (m *Model) renderField(f field, active bool, width int) string {
	prefix := "  "
	if active {
		prefix = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("▸ ")
	}
	label := f.label
	value := ""
	if f.kind == fieldAction {
		value = "Enter"
	} else {
		value = f.get()
	}
	if f.kind == fieldChoice {
		value = "‹ " + value + " ›"
	}
	if f.kind == fieldToggle {
		value = "[" + value + "]"
	}
	// Advanced labels carry authority-significant distinctions (for example,
	// definition-of-done vs verification). Give them enough room to remain
	// legible in the standard PTY viewport while still preserving a value cell.
	labelW := min(max(width*2/5, 18), max(width-10, 8))
	label = ansi.Truncate(label, labelW, "…")
	value = ansi.Truncate(value, max(width-labelW-5, 8), "…")
	if active {
		label = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render(label)
		value = lipgloss.NewStyle().Foreground(theme.Text).Render(value + map[bool]string{true: "▏"}[f.kind == fieldText])
	} else {
		label = lipgloss.NewStyle().Foreground(theme.Text).Render(label)
		value = lipgloss.NewStyle().Foreground(theme.Muted).Render(value)
	}
	return prefix + label + strings.Repeat(" ", max(1, labelW-ansi.StringWidth(ansi.Strip(label))+2)) + value
}

func window(cursor, total, maxRows int) (int, int) {
	if total <= maxRows {
		return 0, total
	}
	first := cursor - maxRows/2
	if first < 0 {
		first = 0
	}
	if first+maxRows > total {
		first = total - maxRows
	}
	return first, first + maxRows
}
