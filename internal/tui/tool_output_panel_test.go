package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/config"
)

// In preview mode tool outputs render in a fixed-height panel: clipped to
// ToolOutputCollapsedHeight rows with a "… N more line(s)" footer; a click
// or shift+tab expands a block to the full body. Expansion is driven by the
// per-block tri-state override (block.override = overrideExpanded /
// overrideCollapsed); block.expanded is reserved for assistant-detail
// toggles. See also display_modes_test.go for the full mode matrix.

// countResultRows returns how many of the rendered, ANSI-stripped lines
// carry a "result line NN" marker — the visible rows of the clipped
// panel. Header / footer rows are excluded.
func countResultRows(view string) int {
	n := 0
	for _, ln := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(ln, "result line ") {
			n++
		}
	}
	return n
}

func bigToolResult(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "result line %02d\n", i)
	}
	return b.String()
}

// A tall, collapsed tool block clips to the configured row budget and
// shows the truncation footer; expanding it reveals every line.
func TestToolOutputPanel_ClippedWhenCollapsed_FullWhenExpanded(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{} // default height (8)
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)

	const total = 30
	m.blocks = []block{{
		kind:       "tool",
		toolName:   "bash",
		toolResult: bigToolResult(total),
		// preview mode + override none → clipped (the default).
	}}

	m.renderBlocks()
	collapsed := m.vp.View()
	plain := ansi.Strip(collapsed)

	want := m.cfg.TUI.EffectiveToolOutputCollapsedHeight() // 8
	if got := countResultRows(collapsed); got != want {
		t.Fatalf("collapsed tool output: want %d result rows, got %d\n%s", want, got, plain)
	}
	// First lines visible, later lines hidden.
	if !strings.Contains(plain, "result line 01") {
		t.Fatalf("collapsed should show the first line:\n%s", plain)
	}
	if strings.Contains(plain, "result line 30") {
		t.Fatalf("collapsed should NOT show the last line:\n%s", plain)
	}
	// Truncation footer reports the hidden count (30 - 8 = 22).
	if !strings.Contains(plain, fmt.Sprintf("%d more line", total-want)) {
		t.Fatalf("collapsed should show a '… N more line(s)' footer (N=%d):\n%s", total-want, plain)
	}

	// Expand → full body, no truncation footer. Tool blocks use the
	// per-block override now (not expanded).
	m.blocks[0].override = overrideExpanded
	m.invalidateBlockCache(0)
	m.renderBlocks()
	full := ansi.Strip(m.vp.View())
	if !strings.Contains(full, "result line 01") || !strings.Contains(full, "result line 30") {
		t.Fatalf("expanded tool output should show the full body:\n%s", full)
	}
	if strings.Contains(full, "more line") {
		t.Fatalf("expanded tool output should not show a truncation footer:\n%s", full)
	}
}

// A short tool result (fewer rows than the budget) is not clipped and
// shows no truncation footer even when collapsed.
func TestToolOutputPanel_ShortResultNotClipped(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{}
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)

	m.blocks = []block{{
		kind:       "tool",
		toolName:   "bash",
		toolResult: bigToolResult(3), // < default 8
	}}
	m.renderBlocks()
	plain := ansi.Strip(m.vp.View())
	if got := countResultRows(m.vp.View()); got != 3 {
		t.Fatalf("short collapsed result: want 3 rows, got %d\n%s", got, plain)
	}
	if strings.Contains(plain, "more line") {
		t.Fatalf("short result should not show a truncation footer:\n%s", plain)
	}
}

// The configured height drives the clip budget.
func TestToolOutputPanel_HonoursConfiguredHeight(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{TUI: config.TUI{ToolOutputCollapsedHeight: 5}}
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)

	m.blocks = []block{{
		kind:       "tool",
		toolName:   "bash",
		toolResult: bigToolResult(20),
	}}
	m.renderBlocks()
	if got := countResultRows(m.vp.View()); got != 5 {
		t.Fatalf("configured height 5: want 5 rows, got %d\n%s", got, ansi.Strip(m.vp.View()))
	}
}

// clipToolOutput measures the budget in POST-WRAP rows: a single long
// logical line that wraps past the budget is clipped, not passed through
// whole. (project_lipgloss_window_postwrap_rows.)
func TestClipToolOutput_CountsPostWrapRows(t *testing.T) {
	// One logical line, 200 runes, wrapped at width 20 → ~10 rows.
	long := strings.Repeat("x ", 100)
	clipped, more := clipToolOutput(long, 20, 3)
	rows := strings.Count(clipped, "\n") + 1
	if rows != 3 {
		t.Fatalf("clip to 3 post-wrap rows: got %d rows\n%q", rows, clipped)
	}
	if more <= 0 {
		t.Fatalf("a long wrapping line should report dropped rows, got more=%d", more)
	}
}

// P2.2: a single long UNBROKEN token (base64 blob, minified JSON, long
// URL — no spaces) must still wrap mid-token at the width boundary and
// consume its real number of rows. strings.Fields-based wrapping only
// breaks between words, so a 400-char token used to count as ONE row,
// the panel passed it through whole, lipgloss hard-clipped it to content
// width, and the "… N more line(s)" footer reported NOTHING hidden.
func TestClipToolOutput_LongUnbrokenTokenWraps(t *testing.T) {
	// 400 chars, no spaces. At width 40 it must wrap to 10 rows; clipped
	// to a budget of 8 it shows 8 and reports 2 hidden.
	long := strings.Repeat("x", 400)
	clipped, more := clipToolOutput(long, 40, 8)
	rows := strings.Count(clipped, "\n") + 1
	if rows != 8 {
		t.Fatalf("400-char token at width 40, budget 8: want 8 rows, got %d\n%q", rows, clipped)
	}
	// 400/40 = 10 wrapped rows; 10 - 8 = 2 dropped.
	if more != 2 {
		t.Fatalf("400-char token: want more=2 dropped rows, got more=%d", more)
	}
	// No clipped row may exceed the width boundary (display-width aware).
	for _, ln := range strings.Split(clipped, "\n") {
		if w := ansi.StringWidth(ln); w > 40 {
			t.Fatalf("clipped row exceeds width 40 (got %d): %q", w, ln)
		}
	}
}

// A realistic base64 blob (no spaces) reports the hidden remainder
// instead of being silently truncated to one row.
func TestClipToolOutput_Base64BlobReportsHidden(t *testing.T) {
	// 432-char base64-ish blob, no whitespace.
	const blobLen = 432
	blob := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 9) // 48*9 = 432
	if len(blob) != blobLen {
		t.Fatalf("test setup: blob len %d, want %d", len(blob), blobLen)
	}
	clipped, more := clipToolOutput(blob, 40, 8)
	rows := strings.Count(clipped, "\n") + 1
	if rows != 8 {
		t.Fatalf("base64 blob at width 40, budget 8: want 8 rows, got %d\n%q", rows, clipped)
	}
	// 432/40 = 10.8 → 11 wrapped rows; 11 - 8 = 3 dropped.
	if more != 3 {
		t.Fatalf("base64 blob: want more=3 dropped rows, got more=%d", more)
	}
}

// End-to-end through the real tool template: a collapsed block whose
// result is one long unbroken token surfaces a "… N more line(s)"
// footer rather than hard-clipping to a single row with no disclosure.
func TestToolOutputPanel_LongUnbrokenTokenShowsFooter(t *testing.T) {
	m := scenarioModel(t)
	m.cfg = &config.Config{} // default height (8)
	m.vp.SetWidth(60)
	m.vp.SetHeight(40)

	// innerW = vp.Width-2-4 = 54; a 600-char unbroken token wraps to ~12
	// rows, well past the default budget of 8, so it must be clipped and
	// the footer must report the hidden remainder.
	m.blocks = []block{{
		kind:       "tool",
		toolName:   "bash",
		toolResult: strings.Repeat("Z", 600), // single unbroken token
	}}
	m.renderBlocks()
	plain := ansi.Strip(m.vp.View())
	if !strings.Contains(plain, "more line") {
		t.Fatalf("collapsed long-token result should show a '… N more line(s)' footer:\n%s", plain)
	}
}

func TestClipToolOutput_NoClipWhenUnderBudget(t *testing.T) {
	in := "a\nb\nc"
	out, more := clipToolOutput(in, 80, 8)
	if more != 0 {
		t.Fatalf("under-budget body should not be clipped, got more=%d", more)
	}
	if out != in {
		t.Fatalf("under-budget body should pass through unchanged: got %q", out)
	}
}

// Config default + clamp: unset → 8, below min → 3, above max → 20.
func TestEffectiveToolOutputCollapsedHeight(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want int
	}{
		{"unset defaults to 8", 0, 8},
		{"negative defaults to 8", -4, 8},
		{"below min clamps to 3", 1, 3},
		{"at min", 3, 3},
		{"in range", 12, 12},
		{"at max", 20, 20},
		{"above max clamps to 20", 99, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := config.TUI{ToolOutputCollapsedHeight: tc.set}.EffectiveToolOutputCollapsedHeight()
			if got != tc.want {
				t.Fatalf("EffectiveToolOutputCollapsedHeight(%d) = %d, want %d", tc.set, got, tc.want)
			}
		})
	}
}
