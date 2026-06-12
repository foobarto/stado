package treepicker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/tui/theme"
)

// Peek is the read-only transcript overlay layered over the tree (design
// stage 6). It is pure presentation: the host loads the target session's
// conversation with a deterministic, read-only runtime.LoadConversation (NO
// OpenSessionByID, no ref write, m.session untouched), renders it to plain
// lines, and hands those lines plus an honest label + banner here. The overlay
// owns only scroll state.
//
// The label is deliberately honest about what peek shows: the WHOLE
// conversation on disk, not a point-in-time snapshot at turn N. Turns are git
// tags and conversation.jsonl carries no per-message turn field, so slicing to
// "messages up to turn N" needs a data-model change that's out of v1 scope.
// The banner makes that explicit whenever the session has more turns than N.
type Peek struct {
	// SessionID / Turn / Commit identify what is being peeked. Carried so a
	// branch-here from inside the peek (`b`) addresses the same commit.
	SessionID string
	Turn      int
	Commit    string

	// Label is the honest header line, e.g.
	// "transcript — session abcd1234 @ turns/3 (read-only · full conversation,
	// not a point-in-time snapshot)".
	Label string
	// Banner is a muted caveat shown under the label when the session has more
	// turns than the peeked one (so the full transcript shows content AFTER
	// turn N). Empty when the peeked turn is the session tip.
	Banner string
	// Lines are the pre-rendered transcript lines (msgsToBlocks → rendered).
	Lines []string

	// offset is the top visible line (scroll position; 0 == top).
	offset int
}

// NewPeek builds a peek overlay. lines are the host-rendered transcript lines
// (may be empty — a fresh session shows the placeholder).
func NewPeek(sessionID string, turn int, commit, label, banner string, lines []string) *Peek {
	return &Peek{
		SessionID: sessionID,
		Turn:      turn,
		Commit:    commit,
		Label:     label,
		Banner:    banner,
		Lines:     lines,
	}
}

// Update consumes a scroll key. The host routes Esc/Ctrl+C/`b` before us
// (layered close + branch-here), so we only handle movement. The visible page
// height is recomputed in View, so scroll keys move by a fixed conservative
// page here and View clamps the offset to the content.
func (p *Peek) Update(km tea.KeyPressMsg) {
	switch km.String() {
	case "up", "k":
		p.scroll(-1)
	case "down", "j":
		p.scroll(1)
	case "pgup":
		p.scroll(-10)
	case "pgdown", " ":
		p.scroll(10)
	case "g", "home":
		p.offset = 0
	case "G", "end":
		p.offset = len(p.Lines) // clamped in View
	}
}

func (p *Peek) scroll(delta int) {
	p.offset += delta
	if p.offset < 0 {
		p.offset = 0
	}
}

// box renders the peek's bordered modal (no canvas placement) so the caller
// can compose it over the tree via overlays.CenterOver. The transcript is
// windowed to the available height; the offset is clamped here so it always
// shows a full page of content at the bottom.
func (p *Peek) box(screenWidth, screenHeight int) string {
	modalW := clampInt(screenWidth*3/4, 64, 120)
	innerW := modalW - 4 // border(2) + padding(2)
	if innerW < 10 {
		innerW = 10
	}
	// Wrap the honest label across up to two rows so the "not a point-in-time
	// snapshot" clarifier survives at common widths (at 120 cols innerW is 86
	// but the single-line label is ~101 chars, so a straight truncate dropped
	// the clarifier — P3.2). ansi.Wrap is display-width + grapheme aware.
	labelRows := wrapLabel(p.Label, innerW)

	// Height budget: border(2) + label(len(labelRows)) + optional banner(1) +
	// blank(1) + footer(1). Window the transcript to what's left.
	chrome := 4 + len(labelRows)
	if p.Banner != "" {
		chrome++
	}
	listBudget := screenHeight - chrome
	if listBudget < 3 {
		listBudget = 3
	}

	bg := lipgloss.NewStyle().Background(theme.Background)
	var b strings.Builder

	for _, lr := range labelRows {
		b.WriteString(bg.Foreground(theme.Text).Bold(true).Render(lr))
		b.WriteString("\n")
	}
	if p.Banner != "" {
		b.WriteString(bg.Foreground(theme.Warning).Render(truncateVisible(p.Banner, innerW)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(p.Lines) == 0 {
		b.WriteString(bg.Foreground(theme.Muted).Render("(no messages on disk)"))
	} else {
		p.clampOffset(listBudget)
		end := p.offset + listBudget
		if end > len(p.Lines) {
			end = len(p.Lines)
		}
		window := p.Lines[p.offset:end]
		rendered := make([]string, len(window))
		for i, ln := range window {
			rendered[i] = bg.Render(truncateVisible(ln, innerW))
		}
		b.WriteString(strings.Join(rendered, "\n"))
	}
	b.WriteString("\n")
	b.WriteString(bg.Foreground(theme.Muted).Render(truncateVisible(p.footer(), innerW)))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Background(theme.Background).
		Padding(0, 1).
		Width(modalW).
		Render(b.String())
}

// View renders the peek standalone (centred on an empty canvas). Used by tests
// and as a fallback; the live picker composes box() over the tree base.
func (p *Peek) View(screenWidth, screenHeight int) string {
	return lipgloss.Place(screenWidth, screenHeight,
		lipgloss.Center, lipgloss.Center,
		p.box(screenWidth, screenHeight))
}

// clampOffset pulls the scroll offset into range so the window never runs past
// the end of the transcript (keeps a full page visible at the bottom).
func (p *Peek) clampOffset(pageRows int) {
	maxOffset := len(p.Lines) - pageRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

func (p *Peek) footer() string {
	return "↑↓/jk scroll  g/G  b branch here  esc back to tree"
}

// wrapLabel breaks the honest peek label across at most TWO rows at the given
// inner width so the "not a point-in-time snapshot" clarifier survives where a
// single-line truncate would drop it (P3.2). ansi.Wrap breaks on word
// boundaries and is display-width + grapheme aware. If the label still needs a
// third row (a pathologically narrow box), the second row is ellipsis-truncated
// so the box height stays bounded.
func wrapLabel(label string, width int) []string {
	if width < 1 {
		width = 1
	}
	if ansi.StringWidth(label) <= width {
		return []string{label}
	}
	// Break on spaces (and the always-on hyphen) so words stay intact; fall
	// back to Hardwrap only if a single token is wider than the box.
	wrapped := strings.Split(ansi.Wrap(label, width, " "), "\n")
	if len(wrapped) <= 2 {
		return wrapped
	}
	// More than two rows: keep row 1, fold the rest into row 2, ellipsized.
	rest := strings.TrimSpace(strings.Join(wrapped[1:], " "))
	return []string{wrapped[0], ansi.Truncate(rest, width, "…")}
}
