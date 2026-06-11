package treepicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		return tea.KeyPressMsg{Text: s}
	}
}

func sample() []Node {
	return []Node{
		{ID: "aaaaaaaa", Label: "root", Avail: AvailIdle, Depth: 0, TurnCount: 2,
			Turns: []Turn{
				{Number: 1, CommitHex: "a1", Text: "turn 1 · init"},
				{Number: 2, CommitHex: "a2", Text: "turn 2 · work"},
			}},
		{ID: "bbbbbbbb", Label: "child", Avail: AvailLive, Depth: 1, HasParent: true, ParentTurn: 2,
			IsCurrent: true, TurnCount: 1, Turns: []Turn{
				{Number: 1, CommitHex: "b1", Text: "turn 1 · forked"},
			}},
		{ID: "cccccccc", Label: "sibling", Avail: AvailDetached, Depth: 0},
	}
}

// TestCursorClampNoWrap: ↑ at the top and ↓ at the bottom stay put (no wrap).
func TestCursorClampNoWrap(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("initial selection = %q, want aaaaaaaa", got)
	}
	// ↑ at the top clamps.
	_, handled := p.Update(key("up"))
	if !handled {
		t.Fatal("up not handled")
	}
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after up-at-top selection = %q, want aaaaaaaa (clamp)", got)
	}
	// Walk to the bottom (3 session rows; current is collapsed so no turn rows).
	p.Update(key("down"))
	if got := p.SelectedID(); got != "bbbbbbbb" {
		t.Fatalf("after 1 down = %q, want bbbbbbbb", got)
	}
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after 2 down = %q, want cccccccc", got)
	}
	// ↓ at the bottom clamps.
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after down-at-bottom = %q, want cccccccc (clamp)", got)
	}
}

// TestExpandInsertsTurnRowsAndSkipsThem: → expands a session, rebuild inserts
// its (non-selectable) turn rows, and ↓ skips over them to the next session.
func TestExpandCollapseRebuildAndCursorRepin(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	// Expand the root (it has 2 turns).
	_, handled := p.Update(key("right"))
	if !handled {
		t.Fatal("right not handled")
	}
	// Cursor must re-pin to the same session after the rebuild.
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after expand selection = %q, want aaaaaaaa (re-pin)", got)
	}
	// rows = root + 2 turns + child + sibling = 5.
	if len(p.rows) != 5 {
		t.Fatalf("rows after expand = %d, want 5", len(p.rows))
	}
	// ↓ now lands ON the first turn row (turn rows are selectable). SelectedID
	// is the OWNING session, but SelectedIsTurn distinguishes it.
	p.Update(key("down"))
	if got := p.SelectedID(); got != "aaaaaaaa" || !p.SelectedIsTurn() {
		t.Fatalf("after 1 down = %q isTurn=%v, want aaaaaaaa turn row", got, p.SelectedIsTurn())
	}
	// Two more ↓ steps walk over the second turn then onto the child session.
	p.Update(key("down"))
	p.Update(key("down"))
	if got := p.SelectedID(); got != "bbbbbbbb" || p.SelectedIsTurn() {
		t.Fatalf("after 3 down = %q isTurn=%v, want bbbbbbbb session row", got, p.SelectedIsTurn())
	}
	// Move back up onto the root header and collapse it.
	p.Update(key("up"))
	p.Update(key("up"))
	p.Update(key("up"))
	if got := p.SelectedID(); got != "aaaaaaaa" || p.SelectedIsTurn() {
		t.Fatalf("after up to header = %q isTurn=%v, want aaaaaaaa header", got, p.SelectedIsTurn())
	}
	_, _ = p.Update(key("left"))
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after collapse selection = %q, want aaaaaaaa (re-pin)", got)
	}
	if len(p.rows) != 3 {
		t.Fatalf("rows after collapse = %d, want 3", len(p.rows))
	}
}

// TestExpandNoTurnsIsNoop: a session with no turns can't be expanded.
func TestExpandNoTurnsIsNoop(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	// Move to the sibling (cccccccc), which has TurnCount 0.
	p.Update(key("down"))
	p.Update(key("down"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("selection = %q, want cccccccc", got)
	}
	before := len(p.rows)
	p.Update(key("right"))
	if len(p.rows) != before {
		t.Fatalf("rows after expanding turn-less node = %d, want %d (no-op)", len(p.rows), before)
	}
}

// TestHomeEnd: g/G jump to the first/last session row.
func TestHomeEnd(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("G"))
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("after G = %q, want cccccccc", got)
	}
	p.Update(key("g"))
	if got := p.SelectedID(); got != "aaaaaaaa" {
		t.Fatalf("after g = %q, want aaaaaaaa", got)
	}
}

// TestOpenSwitchEmission: Open with a current id selects it, and Enter over a
// switchable row emits a CommandSwitch the host drains via TakeAction.
func TestOpenEnterSwitchEmission(t *testing.T) {
	p := New()
	p.Open(sample(), "bbbbbbbb")
	if got := p.SelectedID(); got != "bbbbbbbb" {
		t.Fatalf("Open current selection = %q, want bbbbbbbb", got)
	}
	_, handled := p.Update(key("enter"))
	if !handled {
		t.Fatal("enter not handled")
	}
	action := p.TakeAction()
	if action.Type != CommandSwitch || action.ID != "bbbbbbbb" {
		t.Fatalf("switch action = %+v, want CommandSwitch bbbbbbbb", action)
	}
	// Outbox drained — a second take is empty.
	if again := p.TakeAction(); again.Type != CommandNone {
		t.Fatalf("second TakeAction = %+v, want CommandNone", again)
	}
}

// TestEnterDetachedNoSwitch: Enter over a detached row emits nothing.
func TestEnterDetachedNoSwitch(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("G")) // cccccccc is detached
	if got := p.SelectedID(); got != "cccccccc" {
		t.Fatalf("selection = %q, want cccccccc", got)
	}
	p.Update(key("enter"))
	if action := p.TakeAction(); action.Type != CommandNone {
		t.Fatalf("detached enter action = %+v, want CommandNone", action)
	}
}

// TestBranchOnTurnRowEmitsForkAtTurn: expanding a session, landing on a turn
// row, and pressing `b` emits a CommandBranch addressing that turn's commit.
func TestBranchOnTurnRowEmitsForkAtTurn(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	// Expand root and step down onto its first turn row.
	p.Update(key("right"))
	p.Update(key("down"))
	if !p.SelectedIsTurn() {
		t.Fatalf("expected a turn row under the cursor, got session %q", p.SelectedID())
	}
	p.Update(key("b"))
	action := p.TakeAction()
	if action.Type != CommandBranch {
		t.Fatalf("branch action = %+v, want CommandBranch", action)
	}
	if action.ID != "aaaaaaaa" || action.TurnNumber != 1 || action.TurnCommit != "a1" {
		t.Fatalf("branch action = %+v, want owner aaaaaaaa turn 1 commit a1", action)
	}
}

// TestBranchOnSessionRowSetsNotice: `b` on a session header is a no-op action
// (no Command) and surfaces the "press b on a turn" notice instead.
func TestBranchOnSessionRowSetsNotice(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	if p.SelectedIsTurn() {
		t.Fatal("expected a session header at the top")
	}
	p.Update(key("b"))
	if action := p.TakeAction(); action.Type != CommandNone {
		t.Fatalf("session-row b action = %+v, want CommandNone", action)
	}
	out := p.View(120, 30)
	if !strings.Contains(out, "press b on a turn") {
		t.Fatalf("notice not surfaced in footer:\n%s", out)
	}
}

// TestEnterOnTurnRowEmitsPeek: Enter over a turn row emits a CommandPeek with
// the owner id, turn, commit, and the owner's tip turn (for the banner).
func TestEnterOnTurnRowEmitsPeek(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("right")) // expand root (2 turns)
	p.Update(key("down"))  // land on turn 1
	if !p.SelectedIsTurn() {
		t.Fatal("expected a turn row")
	}
	p.Update(key("enter"))
	action := p.TakeAction()
	if action.Type != CommandPeek {
		t.Fatalf("peek action = %+v, want CommandPeek", action)
	}
	if action.ID != "aaaaaaaa" || action.TurnNumber != 1 || action.TurnCommit != "a1" {
		t.Fatalf("peek action = %+v, want owner aaaaaaaa turn 1 commit a1", action)
	}
	// Owner has 2 turns; peeking turn 1 → tip is 2 (drives the "more turns" banner).
	if action.TurnTotal != 2 {
		t.Fatalf("peek TurnTotal = %d, want 2 (owner tip)", action.TurnTotal)
	}
}

// TestEnterOnDetachedTurnRowGated: a detached session can't be peeked — Enter
// over its turn row emits nothing and sets a notice. (Detached sessions have no
// turns in practice; we force-expand a synthetic one to prove the gate.)
func TestEnterOnDetachedTurnRowGated(t *testing.T) {
	p := New()
	nodes := []Node{
		{ID: "dddddddd", Label: "gone", Avail: AvailDetached, Depth: 0, TurnCount: 1,
			Turns: []Turn{{Number: 1, CommitHex: "d1", Text: "turn 1 · x"}}},
	}
	p.Open(nodes, "")
	p.Update(key("right")) // expand
	p.Update(key("down"))  // onto the turn row
	if !p.SelectedIsTurn() {
		t.Fatal("expected the detached session's turn row")
	}
	p.Update(key("enter"))
	if action := p.TakeAction(); action.Type != CommandNone {
		t.Fatalf("detached turn peek action = %+v, want CommandNone (gated)", action)
	}
}

// TestPeekLayeredEsc: with a peek open, the first Esc closes JUST the peek
// (tree stays visible); a second Esc closes the tree. Branch-here (`b`) inside
// the peek emits a CommandBranch for the peeked turn.
func TestPeekLayeredEsc(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.OpenPeek(NewPeek("aaaaaaaa", 1, "a1", "transcript label", "", []string{"hello"}))
	if !p.Peeking() {
		t.Fatal("OpenPeek should set Peeking")
	}
	// First Esc → peek closes, tree stays open.
	p.Update(key("esc"))
	if p.Peeking() {
		t.Fatal("first esc should close the peek")
	}
	if !p.Visible {
		t.Fatal("first esc must NOT close the tree (layered)")
	}
	// Second Esc → tree closes.
	p.Update(key("esc"))
	if p.Visible {
		t.Fatal("second esc should close the tree")
	}
}

// TestPeekBranchHere: `b` inside the peek emits a CommandBranch for the peeked
// turn (the operator decides to fork from the transcript they're reading).
func TestPeekBranchHere(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.OpenPeek(NewPeek("aaaaaaaa", 2, "a2", "transcript label", "", []string{"line"}))
	p.Update(key("b"))
	action := p.TakeAction()
	if action.Type != CommandBranch || action.ID != "aaaaaaaa" || action.TurnNumber != 2 || action.TurnCommit != "a2" {
		t.Fatalf("peek branch-here action = %+v, want CommandBranch aaaaaaaa turn 2 a2", action)
	}
}

// TestPeekViewHonestLabelAndBanner: the peek renders the honest read-only label
// and, when a banner is supplied, surfaces it.
func TestPeekViewHonestLabelAndBanner(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	label := "transcript — session aaaaaaaa @ turns/1 (read-only · full conversation, not a point-in-time snapshot)"
	banner := "⚠ showing the FULL transcript"
	p.OpenPeek(NewPeek("aaaaaaaa", 1, "a1", label, banner, []string{"a message line"}))
	out := p.View(140, 40)
	for _, want := range []string{
		"read-only",       // honest label
		"point-in-time",   // honest label caveat
		"FULL transcript", // banner
		"a message line",  // transcript content
		"branch here",     // peek footer hint
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("peek view missing %q:\n%s", want, out)
		}
	}
}

// TestFooterShowsCapWhenTruncated: SetStats(truncated) surfaces the cap warning.
func TestFooterShowsCapWhenTruncated(t *testing.T) {
	p := New()
	p.SetStats(5000, true)
	p.Open(sample(), "")
	out := p.View(120, 40)
	if !strings.Contains(out, "capped") {
		t.Fatalf("truncated footer missing cap warning:\n%s", out)
	}
	// Untruncated shows a plain count.
	q := New()
	q.SetStats(3, false)
	q.Open(sample(), "")
	out2 := q.View(120, 40)
	if !strings.Contains(out2, "3 sessions") {
		t.Fatalf("untruncated footer missing count:\n%s", out2)
	}
}

// TestEscCloses: Esc and Ctrl+C close the picker.
func TestEscCloses(t *testing.T) {
	p := New()
	p.Open(sample(), "")
	p.Update(key("esc"))
	if p.Visible {
		t.Fatal("esc should close the picker")
	}
	p.Open(sample(), "")
	p.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if p.Visible {
		t.Fatal("ctrl+c should close the picker")
	}
}

// --- reproduce-by-rendering: View width assertions (per the lipgloss memory) ---

// modalWidthFor mirrors View's modal-width formula so the test asserts the
// rendered frame matches the budget at a couple of screen widths.
func modalWidthFor(screenW int) int { return clampInt(screenW/2, 58, 98) }

func TestViewModalWidthAtScreenWidths(t *testing.T) {
	for _, sw := range []int{80, 120, 200} {
		p := New()
		p.Open(sample(), "bbbbbbbb")
		out := p.View(sw, 30)
		want := modalWidthFor(sw)
		// Every rendered line must fit within the modal frame width. lipgloss
		// .Width is the TOTAL box width in v2 (border + padding included), so
		// no rendered line should exceed `want`.
		for _, line := range strings.Split(out, "\n") {
			// lipgloss.Place centres the modal on the canvas, so each line
			// carries leading whitespace padding to the left of the frame.
			// Strip both sides so we measure the modal frame itself, not the
			// centering pad (which is what makes a line as wide as the screen).
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if w := lipgloss.Width(trimmed); w > want {
				t.Fatalf("screen=%d: line width %d exceeds modal width %d: %q", sw, w, want, trimmed)
			}
		}
		// The frame must actually be present (rounded border corners).
		if !strings.Contains(out, "╮") || !strings.Contains(out, "╰") {
			t.Fatalf("screen=%d: rounded border not rendered", sw)
		}
	}
}

// TestViewShowsGlyphsAndLabels: the rendered modal carries the design glyph
// language and the session labels.
func TestViewShowsGlyphsAndLabels(t *testing.T) {
	p := New()
	p.Open(sample(), "bbbbbbbb")
	// Expand root so a turn line renders too.
	p.Update(key("g"))
	p.Update(key("right"))
	out := p.View(120, 40)
	for _, want := range []string{
		"Session tree", // title
		glyphCurrent,   // ▸ you-are-here (bbbbbbbb is current)
		"child",        // current session label
		"root",         // root label
		glyphFork,      // ⑂ fork-origin marker on the child edge
		"turn 1",       // an expanded turn line
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered view missing %q\n%s", want, out)
		}
	}
}

// TestViewEmpty renders a placeholder when there are no sessions.
func TestViewEmpty(t *testing.T) {
	p := New()
	p.Open(nil, "")
	out := p.View(100, 30)
	if !strings.Contains(out, "no sessions") {
		t.Fatalf("empty view missing placeholder:\n%s", out)
	}
}
