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

// TestFitLeftColumnNeverExceedsBudget (codex P2 on #134): fitLeftColumn must
// return a column whose display width never exceeds its budget. When the
// FIXED-cost prefix+suffix (deep indent + fork/provenance suffix at a narrow
// modal width) already exceed the column budget, the pre-fix code floored
// labelBudget to 1 and returned prefix+"…"+suffix — WIDER than budget. Because
// be347f1 removed the non-selected path's final truncation, that over-budget
// column let the row spill/wrap past the modal box. Assert the width invariant
// directly across the regimes (fits / label-overflow / prefix+suffix-overflow /
// prefix-alone-overflow).
func TestFitLeftColumnNeverExceedsBudget(t *testing.T) {
	forkSuffix := "  " + glyphFork + " turn 7"
	cases := []struct {
		name                         string
		prefix, label, suffix, right string
		innerW                       int
	}{
		{"fits", "  ● ", "short", forkSuffix, "● 1 turn", 80},
		{"label overflow", "  ● ", strings.Repeat("x", 200), forkSuffix, "● 1 turn", 60},
		{"deep prefix+suffix over budget", strings.Repeat(indentUnit, 16) + "● ", "deeplabel", forkSuffix, "● 3 turns · 2h", 54},
		{"prefix alone over budget", strings.Repeat(indentUnit, 30) + "● ", "x", forkSuffix, "● 9 turns", 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitLeftColumn(tc.prefix, tc.label, tc.suffix, tc.right, tc.innerW)
			budget := tc.innerW - lipgloss.Width(tc.right) - 1
			if budget < 1 {
				budget = 1
			}
			if w := lipgloss.Width(got); w > budget {
				t.Fatalf("fitLeftColumn width %d exceeds budget %d (innerW=%d): %q",
					w, budget, tc.innerW, got)
			}
		})
	}
}

// TestDeepForkRowDoesNotSpill (codex P2 on #134, render-level): a deeply branched
// session with a wide meta column at a narrow modal width must keep its meta on
// the SAME row as the node. Before the fix fitLeftColumn returned an over-budget
// left column (prefix+suffix already past budget) and — since be347f1 removed
// the non-selected path's final truncation — lipgloss hard-wrapped the meta onto
// its own row (the P2.1-style spill, just for a deep fork row instead of the
// header). Reproduce end-to-end through View(): assert the deep node's meta never
// appears as its own detached row.
func TestDeepForkRowDoesNotSpill(t *testing.T) {
	const deepMeta = "● 12 turns · 3h"
	var nodes []Node
	for i := 0; i < 16; i++ {
		n := Node{ID: "n" + strings.Repeat("x", 6) + string(rune('a'+i)), Label: "session-branch", Avail: AvailIdle, Depth: i}
		if i > 0 {
			n.HasParent = true
			n.ParentTurn = i
		}
		if i == 15 {
			n.Label = "a-very-long-descriptive-session-label-at-deep-fork-depth"
			n.Meta = deepMeta
			n.MutatedCount, n.DeniedCount = 2, 1
		}
		nodes = append(nodes, n)
	}
	p := New()
	p.Open(nodes, "")
	// Keep the cursor at the top so the deep wide-meta node renders via the
	// NON-selected path (the selected path's rowTwoCol clamps width and would
	// mask the spill).
	out := p.View(100, 40)
	for _, row := range frameInnerRows(out) {
		if strings.TrimSpace(row) == deepMeta {
			t.Fatalf("deep fork node's meta %q wrapped onto its own row "+
				"(left column overflowed the modal):\n%s", deepMeta, out)
		}
	}
}

// TestTruncateVisibleIsDisplayWidthAware (wide-char spill): truncateVisible
// truncated by RUNE COUNT, not display width, so a string of wide (CJK /
// fullwidth) graphemes whose rune count fits the budget still has a display
// width that overflows it. Every treepicker surface that bounds a line to the
// inner width via truncateVisible (peek transcript/banner/footer, the footer
// count, an unselected turn row) therefore handed lipgloss an over-wide string
// that hard-wrapped onto a second row inside the box. Assert the width
// invariant at the source: the returned display width never exceeds the budget.
func TestTruncateVisibleIsDisplayWidthAware(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
	}{
		{"ascii fits", "hello", 20},
		{"ascii overflow", strings.Repeat("x", 200), 30},
		{"cjk overflow", strings.Repeat("日本語", 60), 86},
		{"fullwidth overflow", strings.Repeat("Ａ", 120), 40},
		{"mixed overflow", "turn 1 · " + strings.Repeat("情報", 50), 50},
		{"narrow budget", strings.Repeat("日", 10), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateVisible(tc.s, tc.width)
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("truncateVisible(%q.., %d) display width %d exceeds budget: %q",
					tc.name, tc.width, w, got)
			}
		})
	}
}

// TestPeekWideLineDoesNotWrap (render-level sibling of the above): a peek
// transcript line full of wide CJK graphemes must occupy exactly ONE rendered
// row in the peek box. Before the fix truncateVisible kept (width-1) RUNES of a
// wide line — display width ~2x the budget — so lipgloss wrapped it onto a
// second interior row, throwing off the box's height accounting (which windows
// the transcript assuming one rendered row per line). Reproduce through the real
// box() render and assert no interior content row exceeds the inner width.
func TestPeekWideLineDoesNotWrap(t *testing.T) {
	wide := strings.Repeat("日本語", 60) // 180 runes, display width 360
	p := New()
	p.Open(sample(), "")
	p.OpenPeek(NewPeek("aaaaaaaa", 1, "a1", "label", "", []string{wide, "plain follow-up line"}))
	out := p.peek.box(120, 40)
	// lipgloss pads a wrapped row to the full frame width, so a width check
	// alone can't catch the wrap. The true signature is the EXTRA interior row:
	// the single wide transcript line must land on exactly one row carrying the
	// CJK content, not two.
	plain := ansi.Strip(out)
	rowsWithCJK := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "日") {
			rowsWithCJK++
		}
	}
	if rowsWithCJK != 1 {
		t.Fatalf("wide peek transcript line spans %d interior rows, want 1 (no wrap):\n%s", rowsWithCJK, out)
	}
	// The "plain follow-up line" must still appear (a wrapped wide line would
	// push it out of the windowed transcript or split the rows). It survives.
	if !strings.Contains(plain, "plain follow-up line") {
		t.Fatalf("follow-up line dropped — wide line consumed extra rows:\n%s", out)
	}
}

// TestTurnRowWideLineDoesNotWrap (render-level sibling): an unselected turn row
// whose summary carries wide CJK content must occupy exactly one rendered row in
// the tree body. Same root cause as the peek case — truncateVisible counted
// runes — so the over-wide row wrapped and shifted every following row down.
func TestTurnRowWideLineDoesNotWrap(t *testing.T) {
	wide := "turn 1 · " + strings.Repeat("日本語", 40)
	nodes := []Node{
		{ID: "aaaaaaaa", Label: "root", Avail: AvailIdle, Depth: 0, TurnCount: 1,
			Turns: []Turn{{Number: 1, CommitHex: "a1", Text: wide}}},
		{ID: "bbbbbbbb", Label: "next", Avail: AvailIdle, Depth: 0},
	}
	p := New()
	p.Open(nodes, "aaaaaaaa") // auto-expands aaaaaaaa so the turn row renders
	p.Update(key("G"))        // move cursor OFF the turn row → unselected path
	out := p.View(120, 40)
	// The wide turn summary must not appear on two interior rows.
	plain := ansi.Strip(out)
	rowsWithCJK := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "日") {
			rowsWithCJK++
		}
	}
	if rowsWithCJK != 1 {
		t.Fatalf("wide turn summary spans %d rows, want 1 (no wrap):\n%s", rowsWithCJK, out)
	}
}
