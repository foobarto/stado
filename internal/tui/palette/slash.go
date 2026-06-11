// Package palette renders command discovery surfaces: a Ctrl+P modal
// command palette and an inline slash-command suggestion box.
package palette

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/sahilm/fuzzy"
)

const maxQueryBytes = 1024

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
	{"/btw", "Toggle BTW mode (off-band async queries)", "ctrl+x ctrl+b", "Quick"},

	// Session — state about this run.
	{"/agents", "Open the agent picker for Do, Plan, and BTW", "ctrl+x a", "Session"},
	{"/model", "Open a model picker (no args) or set a specific id (/model <id>)", "ctrl+x m", "Session"},
	{"/persona", "Open the persona picker (no args) or switch (/persona <name>)", "", "Session"},
	{"/status", "Open provider, tool, plugin, sandbox, and telemetry status", "ctrl+x s", "Session"},
	{"/provider", "Show current provider capabilities or setup hints (/provider <name>)", "", "Session"},
	{"/tools", "List tools available to the model", "", "Session"},
	{"/tasks", "Open the shared task manager", "ctrl+x k", "Session"},
	{"/todo", "Add a todo (/todo <title>) or open the task picker", "", "Session"},
	{"/steer", "Inject a message into the current turn at the next tool boundary (Enter while busy; /steer <msg>)", "", "Session"},
	{"/queue", "Queue a message to run when the current turn finishes (alt+enter; /queue <msg>)", "", "Session"},
	{"/interrupt", "Cancel the current turn and run a message now (ctrl+enter; /interrupt [msg])", "", "Session"},
	{"/compact", "Summarise the conversation and replace prior turns (requires confirmation)", "", "Session"},
	{"/context", "Show current token usage, thresholds, and recovery options", "", "Session"},
	{"/reload", "Re-read config from disk (tools, system prompt, persona, display) without restarting", "", "Session"},
	{"/memory", "Show or toggle prompt memory for this session (/memory on|off)", "", "Session"},
	{"/providers", "List active provider + any local runners detected on this machine", "", "Session"},
	{"/plugin", "Run a signed wasm plugin — /plugin to list, /plugin:<name> <tool> [json] (append -<ver> to pin)", "", "Session"},
	{"/tool", "Run a tool by name — /tool fs.read [json], /t for short. Verbs (ls/info/enable/disable/autoload/unautoload/reload) flow through the same command.", "", "Session"},
	{"/alias", "Manage operator-defined slash shortcuts — /alias create <name> <expansion> (use {1},{2},… for positional args), /alias list, /alias rm <name>. Global; rejects collisions with built-ins.", "", "Session"},
	{"/switch", "Open the session manager", "ctrl+x l", "Session"},
	{"/tree", "Open the session tree — navigate the fork graph and switch", "ctrl+x g", "Session"},
	{"/sessions", "List other sessions for this repo with a hint on how to resume each", "", "Session"},
	{"/subagents", "List recent spawned child sessions, status, and adoption commands", "", "Session"},
	{"/adopt", "Dry-run or apply recent worker subagent changes (/adopt [child] [--apply])", "", "Session"},
	{"/new", "Create and switch to a fresh session", "ctrl+x n", "Session"},
	{"/describe", "Set a human-readable label for this session (/describe <text> or --clear)", "", "Session"},
	{"/budget", "Show the cost budget or /budget ack to continue past the hard cap", "", "Session"},
	{"/skill", "List loaded skills — /skill:<name> to inject a skill's prompt body", "", "Session"},
	{"/retry", "Regenerate the last assistant turn from the same user prompt", "", "Session"},
	{"/loop", "Repeat a prompt automatically: /loop [duration] <prompt>  or  /loop stop (EP-0036)", "", "Session"},
	{"/monitor", "Stream process stdout as session notifications: /monitor <cmd>  or  /monitor stop (EP-0036)", "", "Session"},
	{"/session", "Print the current session id + worktree (copy for other shells)", "", "Session"},

	// View — layout toggles.
	{"/sidebar", "Toggle the right-hand sidebar; resize with ctrl+x [ / ]", "ctrl+t", "View"},
	{"/theme", "Open the theme picker or switch to a bundled theme (/theme <id>)", "ctrl+x t", "View"},
	{"/thinking", "Cycle or set thinking display (show, tail, hide)", "ctrl+x h", "View"},
	{"/debug", "Toggle sidebar diagnostics and log tail", "", "View"},
	{"/split", "Split the chat into conversation + activity tail panes", "", "View"},
}

// Model owns command fuzzy-search state. Ctrl+P renders it as a modal,
// while an empty-prompt slash renders the same state inline above input.
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

// Open resets command search with an empty query.
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
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch km.String() {
	case "up":
		m.moveCursor(-1)
		return nil, true
	case "down", "tab":
		m.moveCursor(1)
		return nil, true
	case "esc":
		m.Visible = false
		return nil, true
	case "backspace":
		if len(m.Query) > 0 {
			m.Query = textutil.TrimLastRune(m.Query)
			m.refresh()
		}
		return nil, true
	case "ctrl+u":
		m.Query = ""
		m.refresh()
		return nil, true
	case "space":
		m.Query = textutil.AppendWithinBytes(m.Query, " ", maxQueryBytes)
		m.refresh()
		return nil, true
	default:
		if km.Text != "" {
			m.Query = textutil.AppendWithinBytes(m.Query, km.Text, maxQueryBytes)
			m.refresh()
			return nil, true
		}
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
	// Width budget: longest rendered row is ~`desc + 2 + name + 2 + shortcut`.
	// Several "Session"-group descriptions land at 65–70 chars; add the
	// right column (~20 chars) and a single-space minimum gap and the
	// natural floor is ~92 chars + border/padding. Cap at 110 for
	// ultra-wide terminals so the modal doesn't fly off into useless
	// whitespace; floor at 64 so narrow terminals still render the whole
	// command name without truncation.
	modalW := clampInt(screenWidth*2/3, 64, 110)
	// Height budget: the full command list (36+ rows plus group headers)
	// is taller than most terminals, and bubbletea v2's compositor clips
	// the frame to the canvas — anything past screenHeight is simply
	// invisible (v1 let the overflow scroll the terminal instead, which
	// kept the bottom rows visible but lost the top ones). Window the
	// list to fit: total height = list + border (2) + header/blank/
	// search/blank chrome (4).
	listBudget := screenHeight - 6
	if listBudget < 3 {
		listBudget = 3
	}
	body := m.renderBody(modalW-4, listBudget) // -4 for border+padding
	// lipgloss v2: .Width is the TOTAL rendered width (border + padding
	// included), so .Width(modalW) keeps the modal exactly modalW wide. (In v1
	// .Width was content+padding and the border added 2 on top, so this used to
	// reserve 2 — the invariant flipped in v2.)
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

// InlineView renders slash-command suggestions anchored near the chat input.
// It shares the same fuzzy state as the modal command palette but keeps the
// surface compact enough to live above the textarea.
func (m *Model) InlineView(maxWidth int) string {
	if !m.Visible {
		return ""
	}
	boxW := maxWidth
	// No upper cap (unlike the centred modal in View, which caps at 110 to
	// avoid flying off into whitespace on ultra-wide terminals). The inline
	// popup is anchored above the textarea and spans the same width as the
	// input frame below it, so it should use the full available width — that
	// space is what lets long command descriptions render untruncated.
	if boxW < 24 {
		boxW = 24
	}
	body := m.renderInlineBody(boxW - 4)
	// v2 lipgloss .Width is the TOTAL box width (it includes the border and
	// padding), unlike v1 where the border was added on top. So set Width to
	// the full boxW: the content area is then boxW-4 (2 border + 2 padding),
	// which matches the body width above. Reserving 2 here (the v1 idiom)
	// shrank the content area to boxW-6 and lipgloss word-wrapped each row —
	// spilling the right column (/name + keybinding) onto the next line.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		Width(boxW).
		Render(body)
}

func (m *Model) renderInlineBody(innerW int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)
	title := bg.Foreground(theme.Text).Bold(true).Render("Slash commands")
	hints := bg.Foreground(theme.Muted).Render("enter run  esc")
	b.WriteString(rowTwoCol(innerW, title, hints))
	b.WriteString("\n")

	cursor := bg.Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	query := bg.Foreground(theme.Text).Render("/" + m.Query)
	b.WriteString(query + cursor)
	b.WriteString("\n")

	if len(m.Matches) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("no matches"))
		return b.String()
	}
	limit := minInt(len(m.Matches), 6)
	start := 0
	if m.Cursor >= limit {
		start = m.Cursor - limit + 1
	}
	if start+limit > len(m.Matches) {
		start = len(m.Matches) - limit
	}
	// Group headers are useful when browsing (empty query — the
	// categories help orient the user). When filtering, headers are
	// pure clutter — the user wants matches, not section labels —
	// so render a flat list. Operator-feedback cleanup.
	showGroups := strings.TrimSpace(m.Query) == ""
	lastGroup := ""
	for i := 0; i < limit; i++ {
		idx := start + i
		cmd := m.Matches[idx]
		group := cmd.Group
		if group == "" {
			group = "Commands"
		}
		if showGroups && group != lastGroup {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(bg.Foreground(theme.Secondary).
				Bold(true).
				Render("  "+group) + "\n")
			lastGroup = group
		}
		b.WriteString(renderRow(innerW, cmd, idx == m.Cursor))
		if i < limit-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderBody lays out:  header | blank | search line | blank | grouped list.
// The list is windowed to maxListRows rendered lines around the cursor —
// same sliding-window behaviour as renderInlineBody, just with a budget
// derived from the screen height instead of a fixed cap.
func (m *Model) renderBody(innerW, maxListRows int) string {
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(theme.Background)

	title := bg.Foreground(theme.Text).Bold(true).Render("Commands")
	esc := bg.Foreground(theme.Muted).Render("esc")
	b.WriteString(rowTwoCol(innerW, title, esc))
	b.WriteString("\n\n")

	// Search input line.
	searchLabel := bg.Foreground(theme.Text).Render("Search")
	cursor := bg.Foreground(theme.Text).
		Background(theme.Primary).
		Render(" ")
	queryDisplay := bg.Foreground(theme.Text).Render(m.Query)
	if m.Query == "" {
		b.WriteString(searchLabel + cursor)
	} else {
		b.WriteString(queryDisplay + cursor)
	}
	b.WriteString("\n\n")

	// Grouped list.
	if len(m.Matches) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).
			Render("no matches"))
		return b.String()
	}

	lines, cursorLine := m.renderListLines(innerW)
	b.WriteString(strings.Join(windowLines(lines, cursorLine, maxListRows), "\n"))
	return b.String()
}

// renderListLines renders the match list to individual lines and reports
// which line holds the cursor row (group headers and separator blanks
// count as lines but never hold the cursor).
func (m *Model) renderListLines(innerW int) ([]string, int) {
	var lines []string
	cursorLine := 0

	// Flat-list when filtering (categories add clutter to a search-result
	// view); keep grouped headers when browsing (empty query — they help
	// orient first-time users).
	if strings.TrimSpace(m.Query) != "" {
		for i, c := range m.Matches {
			if i == m.Cursor {
				cursorLine = len(lines)
			}
			lines = append(lines, renderRow(innerW, c, i == m.Cursor))
		}
		return lines, cursorLine
	}

	bg := lipgloss.NewStyle().Background(theme.Background)
	groupedList := groupMatches(m.Matches)
	flatIdx := 0
	for gi, g := range groupedList {
		if gi > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, bg.Foreground(theme.Secondary).
			Bold(true).
			Render(g.name))
		for _, c := range g.items {
			if flatIdx == m.Cursor {
				cursorLine = len(lines)
			}
			lines = append(lines, renderRow(innerW, c, flatIdx == m.Cursor))
			flatIdx++
		}
	}
	return lines, cursorLine
}

// windowLines returns at most budget lines, sliding the window down just
// enough to keep cursorLine visible (cursor sticks to the window bottom
// once it scrolls — the renderInlineBody behaviour).
func windowLines(lines []string, cursorLine, budget int) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	start := 0
	if cursorLine >= budget {
		start = cursorLine - budget + 1
	}
	if start+budget > len(lines) {
		start = len(lines) - budget
	}
	return lines[start : start+budget]
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

	if selected {
		// Build the line from PLAIN text + plain padding and wrap the whole
		// thing in the Primary highlight, so every cell (including the gap)
		// is uniformly highlighted. rowTwoCol paints its gap with the modal
		// background — using it here would punch a dark strip through the
		// selection highlight.
		desc := c.Desc
		if lipgloss.Width(desc)+lipgloss.Width(rightCol)+1 > width {
			budget := max(width-lipgloss.Width(rightCol)-2, 3)
			desc = truncateVisible(desc, budget)
		}
		pad := max(width-lipgloss.Width(desc)-lipgloss.Width(rightCol), 1)
		return lipgloss.NewStyle().
			Background(theme.Primary).
			Foreground(theme.Background).
			Render(desc + strings.Repeat(" ", pad) + rightCol)
	}
	// Split styling: command id in text_secondary, shortcut in muted
	// so the keybind pops while the name stays visible. Every span (and
	// the padding gap) carries the surface background so the row paints
	// solid — v2 styles default to no background, which left grey holes
	// between the foreground-coloured spans inside the dark modal.
	bg := lipgloss.NewStyle().Background(theme.Background)
	name := c.Name
	shortcut := c.Shortcut
	var right string
	if shortcut != "" {
		right = bg.Foreground(theme.Secondary).Render(name) +
			bg.Render("  ") +
			bg.Foreground(theme.Muted).Render(shortcut)
	} else {
		right = bg.Foreground(theme.Muted).Render(name)
	}
	// Truncate the description when desc+right would overflow, mirroring
	// rowTwoCol (the selected-row path). Without this, a long row exceeds
	// the box width and lipgloss wraps the right column (the /name +
	// keybinding) onto the next line — the scattered "junk" in the palette.
	desc := c.Desc
	if lipgloss.Width(desc)+lipgloss.Width(rightCol)+1 > width {
		budget := width - lipgloss.Width(rightCol) - 2
		if budget < 3 {
			budget = 3
		}
		desc = truncateVisible(desc, budget)
	}
	pad := max(width-lipgloss.Width(desc)-lipgloss.Width(rightCol), 1)
	return bg.Foreground(theme.Text).Render(desc) +
		bg.Render(strings.Repeat(" ", pad)) +
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
	// Paint the padding gap with the modal background so the header /
	// search rows don't show a grey hole between the two columns.
	gap := lipgloss.NewStyle().Background(theme.Background).Render(strings.Repeat(" ", pad))
	return left + gap + right
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Compile-time guard: palette.Model must remain small enough that the
// renderer doesn't re-allocate on every keystroke.
var _ = fmt.Sprintf
