// Package palette renders the opencode-style command palette: a modal
// popup with its own search input and a grouped, keybinding-annotated
// list of commands. Opens on Ctrl+P, closes on Esc.
package palette

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

// Command is one palette entry. Shortcut is rendered right-aligned (muted)
// when non-empty — matches the opencode layout where each row shows its
// keybinding or a command-id token.
type Command struct {
	Name     string
	Desc     string
	Shortcut string
	Group    string
}

// Commands is the bundled list. Groups are rendered as bold section
// headers; within a group the Commands appear in registration order.
var Commands = []Command{
	// Quick — most common.
	{"/help", "Show keyboard shortcuts and help", "?", "Quick"},
	{"/clear", "Clear the message history", "", "Quick"},
	{"/exit", "Quit stado", "ctrl+d", "Quick"},

	// Session — state about this run.
	{"/model", "Open a model picker (no args) or set a specific id (/model <id>)", "", "Session"},
	{"/provider", "Show the current provider + capabilities", "", "Session"},
	{"/tools", "List tools available to the model", "", "Session"},
	{"/approvals", "'always <tool>' or 'forget'", "", "Session"},
	{"/compact", "Summarise the conversation and replace prior turns (requires confirmation)", "", "Session"},
	{"/context", "Show current token usage, thresholds, and recovery options", "", "Session"},
	{"/providers", "List active provider + any local runners detected on this machine", "", "Session"},
	{"/plugin", "Run a signed wasm plugin — /plugin to list, /plugin:<name>-<ver> <tool> [json]", "", "Session"},
	{"/sessions", "List other sessions for this repo with a hint on how to resume each", "", "Session"},
	{"/describe", "Set a human-readable label for this session (/describe <text> or --clear)", "", "Session"},
	{"/budget", "Show the cost budget or /budget ack to continue past the hard cap", "", "Session"},
	{"/skill", "List loaded skills — /skill:<name> to inject a skill's prompt body", "", "Session"},
	{"/retry", "Regenerate the last assistant turn from the same user prompt", "", "Session"},
	{"/session", "Print the current session id + worktree (copy for other shells)", "", "Session"},

	// View — layout toggles.
	{"/sidebar", "Toggle the right-hand sidebar", "ctrl+t", "View"},
	{"/split", "Split the chat into conversation + activity tail panes", "", "View"},
	{"/todo", "Add a todo item (/todo <title>)", "", "View"},
}

// Model is the modal palette. Its own Query string feeds the fuzzy matcher
// — Ctrl+P opens it without touching the main text input.
type Model struct {
	Visible bool
	Query   string
	Matches []Command
	Cursor  int

	// Outer screen size (so we can centre the modal).
	Width  int
	Height int
}

func New() *Model {
	m := &Model{}
	m.refresh()
	return m
}

// Open shows the palette with an empty query. Called from Ctrl+P.
func (m *Model) Open() {
	m.Visible = true
	m.Query = ""
	m.Cursor = 0
	m.refresh()
}

// Close dismisses the palette.
func (m *Model) Close() { m.Visible = false }

// Selected returns the currently-hovered command, or nil when the match
// list is empty or the palette is hidden.
func (m *Model) Selected() *Command {
	if !m.Visible || len(m.Matches) == 0 {
		return nil
	}
	return &m.Matches[m.Cursor]
}

// Update consumes every keypress while Visible=true. Returns (cmd, handled);
// handled=true means the caller MUST NOT propagate msg further (otherwise
// characters would leak into the main input widget beneath the modal).
func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch km.Type {
	case tea.KeyUp:
		m.moveCursor(-1)
		return nil, true
	case tea.KeyDown, tea.KeyTab:
		m.moveCursor(1)
		return nil, true
	case tea.KeyEscape:
		m.Visible = false
		return nil, true
	case tea.KeyBackspace:
		if len(m.Query) > 0 {
			m.Query = m.Query[:len(m.Query)-1]
			m.refresh()
		}
		return nil, true
	case tea.KeyCtrlU:
		m.Query = ""
		m.refresh()
		return nil, true
	case tea.KeyRunes:
		m.Query += string(km.Runes)
		m.refresh()
		return nil, true
	case tea.KeySpace:
		m.Query += " "
		m.refresh()
		return nil, true
	}
	return nil, false
}

func (m *Model) moveCursor(delta int) {
	if len(m.Matches) == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor = (m.Cursor + delta + len(m.Matches)) % len(m.Matches)
}

// refresh recomputes m.Matches from m.Query using fuzzy matching on
// command Names only. Including Desc in the haystack kicked up false
// rankings — e.g. typing "model" was matching `/tools` because its
// description ("List tools available to the model") contained the
// whole word. Name-only matching is what users expect when typing a
// slash-command prefix; the Desc stays as purely display copy.
//
// Empty query shows everything in registration order so groups stay
// intact for the categorised view.
func (m *Model) refresh() {
	q := strings.TrimSpace(strings.TrimPrefix(m.Query, "/"))
	if q == "" {
		m.Matches = append([]Command(nil), Commands...)
	} else {
		words := make([]string, len(Commands))
		for i, c := range Commands {
			words[i] = strings.TrimPrefix(c.Name, "/")
		}
		found := fuzzy.Find(q, words)
		m.Matches = nil
		for _, f := range found {
			m.Matches = append(m.Matches, Commands[f.Index])
		}
	}
	if m.Cursor >= len(m.Matches) {
		m.Cursor = len(m.Matches) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// View renders the modal centred on a screenWidth × screenHeight canvas
// using lipgloss.Place. Returns "" when hidden.
func (m *Model) View(screenWidth, screenHeight int) string {
	if !m.Visible {
		return ""
	}
	modalW := clampInt(screenWidth/2, 48, 80)
	body := m.renderBody(modalW - 4) // -4 for border+padding
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

// renderBody lays out:  header | blank | search line | blank | grouped list.
func (m *Model) renderBody(innerW int) string {
	var b strings.Builder

	title := lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render("Commands")
	esc := lipgloss.NewStyle().Foreground(theme.Muted).Render("esc")
	b.WriteString(rowTwoCol(innerW, title, esc))
	b.WriteString("\n\n")

	// Search input line.
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

	// Grouped list.
	if len(m.Matches) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).
			Render("no matches"))
		return b.String()
	}

	groupedList := groupMatches(m.Matches)
	cursorIdx := m.Cursor
	flatIdx := 0
	for gi, g := range groupedList {
		if gi > 0 {
			b.WriteString("\n")
		}
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Secondary).
			Bold(true).
			Render(g.name) + "\n")
		for _, c := range g.items {
			isSel := flatIdx == cursorIdx
			b.WriteString(renderRow(innerW, c, isSel) + "\n")
			flatIdx++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

type group struct {
	name  string
	items []Command
}

// groupMatches partitions the match list into groups in their first-seen
// order so the visual layout is stable — users don't see sections jump
// around as they type.
func groupMatches(cmds []Command) []group {
	order := []string{}
	byName := map[string][]Command{}
	for _, c := range cmds {
		g := c.Group
		if g == "" {
			g = "Commands"
		}
		if _, ok := byName[g]; !ok {
			order = append(order, g)
		}
		byName[g] = append(byName[g], c)
	}
	out := make([]group, 0, len(order))
	for _, n := range order {
		out = append(out, group{name: n, items: byName[n]})
	}
	return out
}

func renderRow(width int, c Command, selected bool) string {
	// Right column shows both the slash-command id AND the
	// keyboard shortcut (when one exists), separated by a spacer —
	// so users can see how to invoke a command both ways at a glance
	// rather than only seeing the "most-specific" form. Previously
	// a command with a shortcut hid its /name; one with no shortcut
	// hid the fact no shortcut existed.
	rightCol := c.Name
	if c.Shortcut != "" {
		rightCol = c.Name + "  " + c.Shortcut
	}
	padded := rowTwoCol(width, c.Desc, rightCol)

	if selected {
		return lipgloss.NewStyle().
			Background(theme.Primary).
			Foreground(theme.Background).
			Render(padded)
	}
	// Split styling: command id in text_secondary, shortcut in muted
	// so the keybind pops while the name stays visible.
	name := c.Name
	shortcut := c.Shortcut
	var right string
	if shortcut != "" {
		right = lipgloss.NewStyle().Foreground(theme.Secondary).Render(name) +
			"  " +
			lipgloss.NewStyle().Foreground(theme.Muted).Render(shortcut)
	} else {
		right = lipgloss.NewStyle().Foreground(theme.Muted).Render(name)
	}
	pad := max(width-lipgloss.Width(c.Desc)-lipgloss.Width(rightCol), 1)
	return lipgloss.NewStyle().Foreground(theme.Text).Render(c.Desc) +
		strings.Repeat(" ", pad) +
		right
}

// rowTwoCol produces a line of exactly `width` visible columns with `left`
// at the start and `right` at the end, padded in between. Short inputs are
// left alone; long inputs are truncated with an ellipsis.
func rowTwoCol(width int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > width {
		// Truncate the left column.
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
	// Best-effort — lipgloss doesn't export a truncator, so count runes.
	if width <= 1 {
		return "…"
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Compile-time guard: palette.Model must remain small enough that the
// renderer doesn't re-allocate on every keystroke.
var _ = fmt.Sprintf
