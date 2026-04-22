package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/textutil"
)

// sessionTreeCmd is the standalone interactive half of Phase 11.5. See
// DESIGN §"Fork-from-point ergonomics": this is a top-level cobra
// subcommand with its own tea.Program — deliberately NOT a slash
// command inside the main TUI — so post-session recovery works from
// any shell independent of whether the main TUI is running.
//
// Keybindings:
//   ↑ / k     move cursor up
//   ↓ / j     move cursor down
//   f / enter fork a new session rooted at the highlighted turn
//   q / esc   quit
var sessionTreeCmd = &cobra.Command{
	Use:   "tree <id>",
	Short: "Browse a session's turn history and fork from a chosen turn",
	Long: "Opens an interactive view of the session's turn boundaries\n" +
		"(turns/<N>) in chronological order. Selecting a turn and pressing\n" +
		"'f' creates a fresh session rooted at that turn — the equivalent of\n" +
		"`stado session fork <id> --at turns/<N>` from the same shell.\n\n" +
		"Sub-turn commits are not rendered by default; obtain the SHA via\n" +
		"`git log refs/sessions/<id>/tree` and pass it to `session fork --at`\n" +
		"for sub-turn precision.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		id := args[0]
		turns, err := sc.ListTurnRefs(id)
		if err != nil {
			return fmt.Errorf("session tree: %w", err)
		}
		if len(turns) == 0 {
			fmt.Fprintln(os.Stderr, "session has no turn tags yet — nothing to browse")
			return nil
		}
		m := &treeModel{turns: turns, sessionID: id, cfg: cfg}
		p := tea.NewProgram(m, tea.WithAltScreen())
		final, err := p.Run()
		if err != nil {
			return err
		}
		if fm, ok := final.(*treeModel); ok {
			if fm.err != nil {
				return fm.err
			}
			printForkOutcome(fm)
		}
		return nil
	},
}

// treeModel is the bubbletea state for `session tree`. Kept intentionally
// terse — the whole point is a navigable turn list with a single fork
// keybinding, not a second TUI.
type treeModel struct {
	turns     []stadogit.TurnEntry
	sessionID string
	cfg       *config.Config

	cursor int
	width  int
	height int

	// Set on successful fork so the quit-handler can print the new
	// session's id + worktree to stdout/stderr.
	forked *forkOutcome
	err    error
}

type forkOutcome struct {
	childID       string
	worktreePath  string
	atCommit      string
	fromTurn      int
}

func (m *treeModel) Init() tea.Cmd { return nil }

func (m *treeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.turns)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.turns) - 1
		case "f", "enter":
			return m, m.forkAtCursor()
		}
	}
	return m, nil
}

// forkAtCursor runs the fork synchronously and quits the program. The
// bubbletea pattern is usually async, but a one-shot action that exits
// the TUI is cleaner handled inline — we're quitting anyway.
func (m *treeModel) forkAtCursor() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.turns) {
		return nil
	}
	t := m.turns[m.cursor]
	child, err := createSessionAt(m.cfg, m.sessionID, t.Commit)
	if err != nil {
		m.err = err
		return tea.Quit
	}
	m.forked = &forkOutcome{
		childID:      child.ID,
		worktreePath: child.WorktreePath,
		atCommit:     t.Commit.String(),
		fromTurn:     t.Turn,
	}
	return tea.Quit
}

func (m *treeModel) View() string {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).
		Render(fmt.Sprintf("session %s — %d turn(s)", m.sessionID, len(m.turns)))
	b.WriteString(title)
	b.WriteString("\n\n")

	hint := lipgloss.NewStyle().Faint(true).
		Render("↑/↓: navigate   f/enter: fork here   q: quit")
	b.WriteString(hint)
	b.WriteString("\n\n")

	for i, t := range m.turns {
		marker := "  "
		if i == m.cursor {
			marker = "▶ "
		}
		row := fmt.Sprintf("%sturns/%-4d  %s  %s  %s",
			marker,
			t.Turn,
			t.Commit.String()[:12],
			t.When.Format("2006-01-02 15:04"),
			firstN(textutil.StripControlChars(t.Summary), 64),
		)
		if i == m.cursor {
			row = lipgloss.NewStyle().Bold(true).Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("error: "))
		b.WriteString(m.err.Error())
	}
	return b.String()
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// printForkOutcome is called after the tea.Program returns so the
// post-quit stderr/stdout lines match what `session fork --at` prints.
func printForkOutcome(m *treeModel) {
	if m.forked == nil {
		return
	}
	fmt.Println(m.forked.childID)
	fmt.Fprintf(os.Stderr, "worktree: %s\n", m.forked.worktreePath)
	fmt.Fprintf(os.Stderr, "rooted at: turns/%d (%s)\n",
		m.forked.fromTurn, m.forked.atCommit[:12])
}
