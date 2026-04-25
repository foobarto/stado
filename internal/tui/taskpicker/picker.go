package taskpicker

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

type CommandType int

const (
	CommandNone CommandType = iota
	CommandCreate
	CommandUpdate
	CommandDelete
)

type Command struct {
	Type   CommandType
	ID     string
	Title  string
	Body   string
	Status tasks.Status
}

type mode int

const (
	modeList mode = iota
	modeDetail
	modeForm
	modeDelete
)

type Model struct {
	Visible bool
	Query   string
	Items   []tasks.Task
	Matches []tasks.Task
	Cursor  int
	Notice  string

	mode       mode
	target     tasks.Task
	formNew    bool
	formTitle  string
	formBody   string
	formStatus tasks.Status
	formField  int

	Width  int
	Height int
}

func New() *Model { return &Model{} }

func (m *Model) Open(items []tasks.Task, selectedID string) {
	m.Visible = true
	m.Query = ""
	m.mode = modeList
	m.target = tasks.Task{}
	m.Notice = ""
	m.Items = append([]tasks.Task(nil), items...)
	m.refresh()
	m.Cursor = 0
	if selectedID != "" {
		m.selectID(selectedID)
	}
}

func (m *Model) Close() { m.Visible = false }

func (m *Model) SetNotice(text string) {
	m.Notice = strings.TrimSpace(text)
}

func (m *Model) Selected() *tasks.Task {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

func (m *Model) ShowDetail(id string) bool {
	if !m.selectID(id) {
		return false
	}
	if sel := m.Selected(); sel != nil {
		m.target = *sel
		m.mode = modeDetail
		return true
	}
	return false
}

func (m *Model) Update(msg tea.Msg) (Command, bool) {
	if !m.Visible {
		return Command{}, false
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return Command{}, false
	}
	m.Notice = ""
	switch m.mode {
	case modeDetail:
		return m.updateDetail(km), true
	case modeForm:
		return m.updateForm(km), true
	case modeDelete:
		return m.updateDelete(km), true
	default:
		return m.updateList(km), true
	}
}

func (m *Model) updateList(km tea.KeyMsg) Command {
	switch km.Type {
	case tea.KeyUp:
		m.moveCursor(-1)
	case tea.KeyDown, tea.KeyTab:
		m.moveCursor(1)
	case tea.KeyEnter:
		if sel := m.Selected(); sel != nil {
			m.target = *sel
			m.mode = modeDetail
		}
	case tea.KeyEsc:
		m.Close()
	case tea.KeyCtrlN:
		m.beginNew()
	case tea.KeyCtrlE:
		m.beginEdit()
	case tea.KeyCtrlD:
		m.beginDelete()
	case tea.KeyBackspace:
		if len(m.Query) > 0 {
			m.Query = trimLastRune(m.Query)
			m.refresh()
		}
	case tea.KeyCtrlU:
		m.Query = ""
		m.refresh()
	case tea.KeyRunes:
		m.Query += string(km.Runes)
		m.refresh()
	case tea.KeySpace:
		m.Query += " "
		m.refresh()
	}
	return Command{}
}

func (m *Model) updateDetail(km tea.KeyMsg) Command {
	switch km.Type {
	case tea.KeyEsc, tea.KeyBackspace:
		m.mode = modeList
	case tea.KeyCtrlN:
		m.beginNew()
	case tea.KeyCtrlE:
		m.beginEditTarget(m.target)
	case tea.KeyCtrlD:
		m.beginDeleteTarget(m.target)
	case tea.KeyUp:
		m.moveCursor(-1)
		if sel := m.Selected(); sel != nil {
			m.target = *sel
		}
	case tea.KeyDown, tea.KeyTab:
		m.moveCursor(1)
		if sel := m.Selected(); sel != nil {
			m.target = *sel
		}
	}
	return Command{}
}

func (m *Model) updateForm(km tea.KeyMsg) Command {
	switch km.Type {
	case tea.KeyEsc:
		m.mode = modeList
	case tea.KeyEnter:
		cmdType := CommandUpdate
		if m.formNew {
			cmdType = CommandCreate
		}
		return Command{
			Type:   cmdType,
			ID:     m.target.ID,
			Title:  strings.TrimSpace(m.formTitle),
			Body:   strings.TrimSpace(m.formBody),
			Status: m.formStatus,
		}
	case tea.KeyUp:
		m.formField = (m.formField + 2) % 3
	case tea.KeyDown, tea.KeyTab:
		m.formField = (m.formField + 1) % 3
	case tea.KeyLeft:
		if m.formField == 1 {
			m.formStatus = previousStatus(m.formStatus)
		}
	case tea.KeyRight:
		if m.formField == 1 {
			m.formStatus = nextStatus(m.formStatus)
		}
	case tea.KeyBackspace:
		switch m.formField {
		case 0:
			m.formTitle = trimLastRune(m.formTitle)
		case 2:
			m.formBody = trimLastRune(m.formBody)
		}
	case tea.KeyCtrlU:
		switch m.formField {
		case 0:
			m.formTitle = ""
		case 2:
			m.formBody = ""
		}
	case tea.KeyRunes:
		if m.formField == 1 {
			m.applyStatusRune(km.Runes)
			break
		}
		m.appendToForm(string(km.Runes))
	case tea.KeySpace:
		m.appendToForm(" ")
	}
	return Command{}
}

func (m *Model) updateDelete(km tea.KeyMsg) Command {
	switch km.Type {
	case tea.KeyEnter:
		return Command{Type: CommandDelete, ID: m.target.ID}
	case tea.KeyEsc:
		m.mode = modeList
	case tea.KeyRunes:
		if len(km.Runes) == 1 {
			switch km.Runes[0] {
			case 'y', 'Y':
				return Command{Type: CommandDelete, ID: m.target.ID}
			case 'n', 'N':
				m.mode = modeList
			}
		}
	}
	return Command{}
}

func (m *Model) beginNew() {
	m.mode = modeForm
	m.formNew = true
	m.target = tasks.Task{}
	m.formTitle = ""
	m.formBody = ""
	m.formStatus = tasks.StatusOpen
	m.formField = 0
}

func (m *Model) beginEdit() {
	if sel := m.Selected(); sel != nil {
		m.beginEditTarget(*sel)
	}
}

func (m *Model) beginEditTarget(task tasks.Task) {
	if task.ID == "" {
		return
	}
	m.mode = modeForm
	m.formNew = false
	m.target = task
	m.formTitle = task.Title
	m.formBody = task.Body
	m.formStatus = task.Status
	if m.formStatus == "" {
		m.formStatus = tasks.StatusOpen
	}
	m.formField = 0
}

func (m *Model) beginDelete() {
	if sel := m.Selected(); sel != nil {
		m.beginDeleteTarget(*sel)
	}
}

func (m *Model) beginDeleteTarget(task tasks.Task) {
	if task.ID == "" {
		return
	}
	m.mode = modeDelete
	m.target = task
}

func (m *Model) appendToForm(s string) {
	switch m.formField {
	case 0:
		m.formTitle += s
	case 2:
		m.formBody += s
	}
}

func (m *Model) applyStatusRune(runes []rune) {
	if len(runes) == 0 {
		return
	}
	switch strings.ToLower(string(runes[0])) {
	case "o":
		m.formStatus = tasks.StatusOpen
	case "p", "i":
		m.formStatus = tasks.StatusInProgress
	case "d":
		m.formStatus = tasks.StatusDone
	}
}

func (m *Model) moveCursor(delta int) {
	if len(m.Matches) == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor = (m.Cursor + delta + len(m.Matches)) % len(m.Matches)
}

func (m *Model) refresh() {
	q := strings.TrimSpace(m.Query)
	if q == "" {
		m.Matches = append([]tasks.Task(nil), m.Items...)
	} else {
		words := make([]string, len(m.Items))
		for i, task := range m.Items {
			words[i] = task.ID + " " + task.Title + " " + task.Body + " " + string(task.Status)
		}
		found := fuzzy.Find(q, words)
		m.Matches = nil
		for _, f := range found {
			m.Matches = append(m.Matches, m.Items[f.Index])
		}
	}
	if m.Cursor >= len(m.Matches) {
		m.Cursor = len(m.Matches) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

func (m *Model) selectID(id string) bool {
	for i, task := range m.Matches {
		if task.ID == id {
			m.Cursor = i
			return true
		}
	}
	for i, task := range m.Items {
		if task.ID == id {
			m.Query = ""
			m.refresh()
			for j, match := range m.Matches {
				if match.ID == m.Items[i].ID {
					m.Cursor = j
					return true
				}
			}
		}
	}
	return false
}

func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 58, 98)
	body := m.renderBody(modalW - 4)
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		Width(modalW).
		Render(body)
	return lipgloss.Place(screenWidth, screenHeight,
		lipgloss.Center, lipgloss.Center,
		modal)
}

func (m *Model) renderBody(innerW int) string {
	var b strings.Builder
	titleText := "Tasks"
	hints := "enter detail  ctrl+n new  ctrl+e edit  ctrl+d delete  esc"
	switch m.mode {
	case modeDetail:
		titleText = "Task detail"
		hints = "ctrl+e edit  ctrl+d delete  up/down browse  esc back"
	case modeForm:
		if m.formNew {
			titleText = "New task"
		} else {
			titleText = "Edit task"
		}
		hints = "enter save  tab field  status: o/i/d  esc cancel"
	case modeDelete:
		titleText = "Delete task"
		hints = "enter/y delete  esc/n cancel"
	}
	hintText := hints
	if lipgloss.Width(titleText)+lipgloss.Width(hintText)+1 > innerW {
		hintText = truncateVisible(hintText, maxInt(innerW-lipgloss.Width(titleText)-2, 8))
	}
	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render(titleText)
	b.WriteString(rowTwoCol(innerW, title, lipgloss.NewStyle().Foreground(theme.Muted).Render(hintText)))
	b.WriteString("\n\n")
	if m.Notice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Error).Render(truncateVisible(m.Notice, innerW)))
		b.WriteString("\n\n")
	}

	switch m.mode {
	case modeDetail:
		m.renderDetail(&b, innerW)
	case modeForm:
		m.renderForm(&b, innerW)
	case modeDelete:
		m.renderDelete(&b)
	default:
		m.renderList(&b, innerW)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderList(b *strings.Builder, innerW int) {
	searchLabel := lipgloss.NewStyle().Foreground(theme.Text).Render("Search")
	cursor := lipgloss.NewStyle().
		Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	queryDisplay := lipgloss.NewStyle().Foreground(theme.Text).Render(m.Query)
	if m.Query == "" {
		b.WriteString(searchLabel + cursor)
	} else {
		b.WriteString(queryDisplay + cursor)
	}
	b.WriteString("\n\n")

	if len(m.Matches) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("no tasks"))
		return
	}
	start := 0
	limit := 10
	if len(m.Matches) < limit {
		limit = len(m.Matches)
	}
	if m.Cursor >= limit {
		start = m.Cursor - limit + 1
	}
	for i, task := range m.Matches[start : start+limit] {
		idx := start + i
		isSel := idx == m.Cursor
		left := "[" + string(task.Status) + "] " + task.Title
		right := relativeTime(task.UpdatedAt)
		padded := rowTwoCol(innerW, left, right)
		if isSel {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(padded))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(truncateVisible(left, innerW-lipgloss.Width(right)-1)) +
				strings.Repeat(" ", maxInt(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)) +
				lipgloss.NewStyle().Foreground(theme.Muted).Render(right))
		}
		b.WriteString("\n")
	}
	if hidden := len(m.Matches) - limit; hidden > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render("+" + strconv.Itoa(hidden) + " more; keep typing to narrow"))
	}
}

func (m *Model) renderDetail(b *strings.Builder, innerW int) {
	task := m.target
	if task.ID == "" {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("no task selected"))
		return
	}
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render(task.Title))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
		Render(rowTwoCol(innerW, "status: "+string(task.Status), "id: "+shortID(task.ID))))
	b.WriteString("\n\n")
	body := strings.TrimSpace(task.Body)
	if body == "" {
		body = "(no details)"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(wrapPlain(body, innerW)))
}

func (m *Model) renderForm(b *strings.Builder, innerW int) {
	rows := []struct {
		label string
		value string
	}{
		{"Title", m.formTitle},
		{"Status", string(m.formStatus)},
		{"Body", m.formBody},
	}
	for i, row := range rows {
		value := row.value
		if value == "" {
			value = " "
		}
		line := rowTwoCol(innerW, row.label, truncateVisible(value, maxInt(innerW-lipgloss.Width(row.label)-2, 8)))
		if i == m.formField {
			b.WriteString(lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(line))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Text).Render(line))
		}
		b.WriteString("\n")
	}
}

func (m *Model) renderDelete(b *strings.Builder) {
	title := m.target.Title
	if title == "" {
		title = m.target.ID
	}
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Error).Bold(true).
		Render("Delete " + title + "?"))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
		Render("This removes the task from the shared task store."))
}

func previousStatus(status tasks.Status) tasks.Status {
	switch status {
	case tasks.StatusOpen:
		return tasks.StatusDone
	case tasks.StatusDone:
		return tasks.StatusInProgress
	default:
		return tasks.StatusOpen
	}
}

func nextStatus(status tasks.Status) tasks.Status {
	switch status {
	case tasks.StatusOpen:
		return tasks.StatusInProgress
	case tasks.StatusInProgress:
		return tasks.StatusDone
	default:
		return tasks.StatusOpen
	}
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	if d < 24*time.Hour {
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func wrapPlain(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := ""
	for _, word := range words {
		if line == "" {
			line = word
			continue
		}
		if lipgloss.Width(line)+1+lipgloss.Width(word) > width {
			b.WriteString(line)
			b.WriteString("\n")
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		b.WriteString(line)
	}
	return b.String()
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func rowTwoCol(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		budget := width - rw - 2
		if budget < 3 {
			budget = 3
		}
		left = truncateVisible(left, budget)
		lw = lipgloss.Width(left)
	}
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func truncateVisible(s string, width int) string {
	if width <= 1 {
		return "."
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "."
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
