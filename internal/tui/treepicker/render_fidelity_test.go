package treepicker

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// nonBlankBodyLines returns the rendered, centre-trimmed lines of the modal,
// dropping blanks. Each trimmed line is the modal frame's own content (the
// lipgloss.Place centring pad is stripped).
func nonBlankBodyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines
}

// TestHeaderHintFitsOneRow (P2.1): the modal header (title + hint) must fit on a
// SINGLE row at the common terminal widths (100/120 cols). Before the fix the
// 62-col "title + hints" overflows the ~54-56 inner width and lipgloss
// hard-wraps the hint to a second line that pokes past the box, pushing every
// row down. Reproduce by rendering and asserting (a) the header occupies one
// row and (b) no rendered body line exceeds the modal frame width.
func TestHeaderHintFitsOneRow(t *testing.T) {
	for _, sw := range []int{100, 120} {
		p := New()
		p.Open(sample(), "bbbbbbbb")
		out := p.View(sw, 40)
		modalW := clampInt(sw/2, 58, 98)

		// (a) No rendered line may exceed the modal frame width. A wrapped
		// header produces a line wider than the frame (it spills past it).
		for _, line := range nonBlankBodyLines(out) {
			if w := lipgloss.Width(line); w > modalW {
				t.Fatalf("screen=%d: line width %d exceeds modal width %d (header wrapped?): %q",
					sw, w, modalW, line)
			}
		}

		// (b) The header (title + hint) must occupy exactly ONE rendered row.
		// renderBody emits  header \n\n  before the row list, so the row
		// directly after the header must be the blank separator. A wrapped
		// hint inserts a SECOND header row, so the line below the header still
		// carries hint text ("g/G", "esc", "expand", ...) instead of being
		// blank — that's the defect signature. Test it on the modal body
		// directly (strip the frame so we count content rows, not the border).
		bodyRows := frameInnerRows(out)
		hdrIdx := -1
		for i, row := range bodyRows {
			if strings.Contains(row, "Session tree") {
				hdrIdx = i
				break
			}
		}
		if hdrIdx < 0 {
			t.Fatalf("screen=%d: no header row with the title:\n%s", sw, out)
		}
		if hdrIdx+1 >= len(bodyRows) {
			t.Fatalf("screen=%d: header is the last row (no body):\n%s", sw, out)
		}
		if next := strings.TrimSpace(bodyRows[hdrIdx+1]); next != "" {
			t.Fatalf("screen=%d: header hint wrapped to a 2nd row %q "+
				"(should be the blank separator):\n%s", sw, next, out)
		}
	}
}

// frameInnerRows extracts the modal's inner content rows from a fully rendered
// View: it drops the centring pad and the rounded border, returning each row's
// interior text. Used to assert on the body's row STRUCTURE (e.g. that the
// header occupies a single row) rather than just visible widths.
func frameInnerRows(out string) []string {
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		// Drop ANSI so the border glyphs match literally.
		plain := strings.TrimSpace(ansi.Strip(line))
		if plain == "" {
			continue
		}
		// Skip the top/bottom border rows (rounded corners).
		if strings.HasPrefix(plain, "╭") || strings.HasPrefix(plain, "╰") {
			continue
		}
		// Strip the left/right border bars (│) to expose the interior.
		inner := strings.TrimPrefix(plain, "│")
		inner = strings.TrimSuffix(inner, "│")
		rows = append(rows, inner)
	}
	return rows
}

// TestForkTagSurvivesDeepLabelTruncation (P3.1): at fork depth (Depth>=2) a long
// session Label must not crowd out the "⑂ turn N" fork tag. Before the fix the
// whole left column (label + appended tag) is truncated as a unit, so a long
// label eats the budget and the tag is sliced off. Reproduce by rendering a deep
// fork node with a long label at a width where the column overflows, and assert
// the fork glyph survives.
func TestForkTagSurvivesDeepLabelTruncation(t *testing.T) {
	nodes := []Node{
		{ID: "rrrrrrrr", Label: "root", Avail: AvailIdle, Depth: 0},
		{ID: "mmmmmmmm", Label: "mid", Avail: AvailIdle, Depth: 1, HasParent: true, ParentTurn: 1},
		{
			ID:    "ffffffff",
			Label: "a-very-long-descriptive-session-label-that-eats-the-whole-budget",
			Avail: AvailIdle, Depth: 2, HasParent: true, ParentTurn: 7,
			Meta: "● 3 turns · 2h",
		},
	}
	p := New()
	p.Open(nodes, "")
	// Land on the deep fork node so it's the (truncated) non-selected path
	// rendered with a real meta column.
	p.Update(key("G"))
	if got := p.SelectedID(); got != "ffffffff" {
		t.Fatalf("selection = %q, want ffffffff", got)
	}
	// Move the cursor OFF it so we exercise the non-selected truncation path
	// (the selected path paints one highlight span over the whole row).
	p.Update(key("up"))
	out := p.View(100, 40)

	if !strings.Contains(out, glyphFork) {
		t.Fatalf("P3.1: fork tag %q sliced off the deep long-label row:\n%s", glyphFork, out)
	}
	// The "turn 7" tag text should survive too, not just the glyph.
	if !strings.Contains(out, "turn 7") {
		t.Fatalf("P3.1: fork-origin 'turn 7' truncated away on deep long-label row:\n%s", out)
	}

	// The SELECTED path renders the whole row in one highlight span via
	// rowTwoCol (a different code path). The fork tag must survive there too —
	// land back on the deep node and assert.
	p.Update(key("G"))
	if got := p.SelectedID(); got != "ffffffff" {
		t.Fatalf("selection = %q, want ffffffff (selected path)", got)
	}
	out = p.View(100, 40)
	if !strings.Contains(out, glyphFork) || !strings.Contains(out, "turn 7") {
		t.Fatalf("P3.1 (selected): fork-origin 'turn 7' sliced off the highlighted deep row:\n%s", out)
	}
}

// TestPeekClarifierSurvivesAt120 (P3.2): the peek label's honesty clarifier
// ("not a point-in-time snapshot") must survive at the common 120-col width.
// At 120 cols the peek inner width is 86 but the single-line label is 101 chars,
// so a straight truncate drops the clarifier. Reproduce by rendering the peek
// box and asserting the clarifier is present AND no rendered line overflows the
// inner width.
func TestPeekClarifierSurvivesAt120(t *testing.T) {
	label := "transcript — session aaaaaaaa @ turns/1 (read-only · full conversation, not a point-in-time snapshot)"
	p := New()
	p.Open(sample(), "")
	p.OpenPeek(NewPeek("aaaaaaaa", 1, "a1", label, "", []string{"a message line"}))
	for _, sw := range []int{120, 100} {
		// Render the peek box in isolation (not composed over the tree) so the
		// label rows aren't interleaved with the tree frame's border bars. The
		// peek's interior rows reconstruct the label faithfully.
		out := p.peek.box(sw, 40)
		modalW := clampInt(sw*3/4, 64, 120)
		innerW := modalW - 4
		// The label is wrapped across two rows to fit the inner width, so the
		// clarifier breaks at a word/hyphen boundary ("…point-" / "in-time
		// snapshot)"). Join the peek's inner rows and collapse whitespace so the
		// assertion is indifferent to WHERE the wrap landed — the clarifier text
		// itself must survive intact (the wrap only adds/removes whitespace).
		joined := stripWhitespace(strings.Join(frameInnerRows(out), ""))
		if !strings.Contains(joined, stripWhitespace("not a point-in-time snapshot")) {
			t.Fatalf("P3.2 @%d: honesty clarifier truncated off the peek label:\n%s", sw, out)
		}
		// Belt-and-suspenders: every rendered row stays inside the frame.
		for _, line := range nonBlankBodyLines(out) {
			if w := lipgloss.Width(line); w > modalW {
				t.Fatalf("P3.2 @%d: line width %d exceeds peek modal width %d (innerW %d): %q",
					sw, w, modalW, innerW, line)
			}
		}
	}
}

// stripWhitespace removes every space/tab/newline so a substring check is
// indifferent to wrapping (a label split across rows still matches).
func stripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
}
