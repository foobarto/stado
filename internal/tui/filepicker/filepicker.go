// Package filepicker renders an inline @-completion popover triggered by
// typing `@` in the main TUI input. Mirrors the patterns other
// coding-agent TUIs (opencode, pi) use for @-mentions: fuzzy match
// agents first, then sessions, then skills, then repo files. Tab/Enter
// accepts the selected item.
package filepicker

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui/theme"
)

// maxVisibleMatches caps the rendered list so the popover never takes
// more than ~half a terminal height.
const maxVisibleMatches = 10

const (
	KindAgent   = "agent"
	KindSession = "session"
	KindSkill   = "skill"
	KindFile    = "file"
)

type Item struct {
	Kind    string
	ID      string
	Display string
	Meta    string
	Insert  string
}

// Model is the file-picker popover. Constructed once per TUI session;
// paths are scanned at Open() time so the picker reflects the repo as
// it exists when the user starts typing, not at boot.
type Model struct {
	Visible bool
	Query   string
	Matches []string
	Cursor  int

	// Anchor is the byte offset in the *input buffer* where the `@`
	// that triggered the popover sits. The main Update() flow uses
	// this to know what range to replace when the user accepts a
	// match. Zero when the picker is hidden.
	Anchor int

	// cwd is the root the picker scans. Set on Open(). Paths returned
	// via Matches are relative to this root.
	cwd string

	// allPaths is the full candidate list collected on Open(). Cached
	// for the life of the visible popover so typing doesn't re-walk
	// the filesystem; re-collected on next Open().
	allPaths []string

	allItems     []Item
	matchedItems []Item
}

// New returns an empty, hidden picker. The caller must call Open(cwd)
// before the picker is useful.
func New() *Model { return &Model{} }

// Open shows the picker rooted at cwd and resets Query/Cursor.
func (m *Model) Open(cwd string, anchor int) {
	m.OpenWithItems(cwd, anchor, nil)
}

// OpenWithItems shows the picker with non-file candidates before repo
// files. Scans the directory tree synchronously; callers concerned
// about responsiveness on huge repos should cache Open in a tea.Cmd.
func (m *Model) OpenWithItems(cwd string, anchor int, items []Item) {
	m.Visible = true
	m.Query = ""
	m.Cursor = 0
	m.Anchor = anchor
	m.cwd = cwd
	m.allPaths = scanPaths(cwd)
	m.allItems = append([]Item(nil), items...)
	for _, path := range m.allPaths {
		m.allItems = append(m.allItems, Item{
			Kind:    KindFile,
			ID:      path,
			Display: path,
			Insert:  path,
		})
	}
	m.refresh()
}

// Close hides the picker without changing the input buffer.
func (m *Model) Close() {
	m.Visible = false
	m.Query = ""
	m.Cursor = 0
	m.Anchor = 0
	m.allPaths = nil
	m.allItems = nil
	m.matchedItems = nil
	m.Matches = nil
}

// SetQuery updates the fuzzy filter and re-ranks matches. Called by
// the host every time the user types/deletes after the `@` trigger.
func (m *Model) SetQuery(q string) {
	m.Query = q
	m.refresh()
	if m.Cursor >= len(m.Matches) {
		m.Cursor = 0
	}
}

// Selected returns the currently-highlighted match, or "" when the
// list is empty or the picker is hidden.
func (m *Model) Selected() string {
	if sel, ok := m.SelectedItem(); ok {
		return sel.Display
	}
	return ""
}

func (m *Model) SelectedItem() (Item, bool) {
	if !m.Visible || len(m.matchedItems) == 0 {
		return Item{}, false
	}
	return m.matchedItems[m.Cursor], true
}

// Update consumes navigation keys (Up/Down/Tab) while the picker is
// visible. Returns (cmd, handled); handled=true means the caller MUST
// NOT propagate the key further — crucial so Up/Down don't leak into
// the textarea and move the cursor instead of the match list.
//
// Tab and Enter are NOT handled here — the caller decides the semantic
// (Tab-to-accept in opencode/pi style; Enter in nested palettes). This
// keeps the picker policy-free.
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
	case tea.KeyDown:
		m.moveCursor(1)
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

// refresh recomputes Matches from Query using fuzzy matching on the
// cached path list. Empty query lists all paths in directory-walk order.
// Limited to maxVisibleMatches so the popover stays compact.
func (m *Model) refresh() {
	items := m.allItems
	if q := strings.TrimSpace(m.Query); q == "" {
		if len(items) > maxVisibleMatches {
			m.matchedItems = items[:maxVisibleMatches]
		} else {
			m.matchedItems = items
		}
		m.refreshMatchStrings()
		return
	}
	words := make([]string, len(items))
	for i, item := range items {
		words[i] = item.ID + " " + item.Display + " " + item.Meta + " " + item.Kind
	}
	found := fuzzy.Find(m.Query, words)
	out := make([]Item, 0, maxVisibleMatches)
	for i, f := range found {
		if i >= maxVisibleMatches {
			break
		}
		out = append(out, items[f.Index])
	}
	m.matchedItems = out
	m.refreshMatchStrings()
}

func (m *Model) refreshMatchStrings() {
	m.Matches = m.Matches[:0]
	for _, item := range m.matchedItems {
		m.Matches = append(m.Matches, item.Display)
	}
}

// View renders the popover as a bordered box of matches. Returns "" when
// hidden. The popover is positioned by the caller (lipgloss.Place or
// equivalent) — this function only produces the block string.
func (m *Model) View(maxWidth int) string {
	if !m.Visible || len(m.Matches) == 0 {
		return ""
	}
	var b strings.Builder
	header := lipgloss.NewStyle().Foreground(theme.Muted).
		Render("@ → agents + sessions + skills + files · ↑/↓ navigate · tab/enter accept · esc cancel")
	b.WriteString(header)
	b.WriteString("\n")
	lastKind := ""
	for i, item := range m.matchedItems {
		if item.Kind != lastKind {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Secondary).Bold(true).Render("  " + groupLabel(item.Kind)))
			b.WriteString("\n")
			lastKind = item.Kind
		}
		display := textutil.StripControlChars(item.Display)
		meta := textutil.StripControlChars(item.Meta)
		if meta != "" {
			display += lipgloss.NewStyle().Foreground(theme.Muted).Render("  " + meta)
		}
		var row string
		if i == m.Cursor {
			row = lipgloss.NewStyle().
				Background(theme.Primary).
				Foreground(theme.Background).
				Render(" " + display + " ")
		} else {
			row = lipgloss.NewStyle().Foreground(theme.Text).Render("  " + display)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		MaxWidth(maxWidth).
		Render(strings.TrimRight(b.String(), "\n"))
	return box
}

func groupLabel(kind string) string {
	switch kind {
	case KindAgent:
		return "Agents"
	case KindSession:
		return "Sessions"
	case KindSkill:
		return "Skills"
	case KindFile:
		return "Files"
	default:
		return "Other"
	}
}

// scanPaths walks cwd and returns relative paths of regular files,
// ignoring hidden directories (anything starting with '.') and common
// vendor/build directories. No .gitignore parsing — that's an upgrade
// for later; the coarse filters here cover 90% of real-world noise.
//
// Capped at 5000 entries so absurdly large repos don't stall the TUI
// on the first @-press. Users hitting the cap will see fewer choices,
// not a broken picker.
func scanPaths(cwd string) []string {
	const cap = 5000
	var out []string
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		name := d.Name()
		if d.IsDir() {
			if path == cwd {
				return nil
			}
			if strings.HasPrefix(name, ".") ||
				name == "node_modules" ||
				name == "vendor" ||
				name == "dist" ||
				name == "build" ||
				name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		if textutil.HasControlChars(rel) {
			return nil
		}
		out = append(out, rel)
		if len(out) >= cap {
			return filepath.SkipAll
		}
		return nil
	}
	_ = filepath.WalkDir(cwd, walk)
	return out
}
