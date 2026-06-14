package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestParseDisplayMode(t *testing.T) {
	cases := []struct {
		in   string
		want displayMode
		ok   bool
	}{
		{"preview", displayPreview, true},
		{"auto", displayAuto, true},
		{"collapsed", displayCollapsed, true},
		{"expanded", displayExpanded, true},
		{"  EXPANDED ", displayExpanded, true},
		// Legacy thinking_display vocabulary still loads.
		{"tail", displayPreview, true},
		{"show", displayExpanded, true},
		{"full", displayExpanded, true},
		{"hide", displayCollapsed, true},
		{"", displayPreview, false},
		{"bogus", displayPreview, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseDisplayMode(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parseDisplayMode(%q) = (%s,%v), want (%s,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDisplayModeRoundTripsThroughString(t *testing.T) {
	for _, mode := range []displayMode{displayPreview, displayAuto, displayCollapsed, displayExpanded} {
		got, ok := parseDisplayMode(mode.String())
		if !ok || got != mode {
			t.Fatalf("round-trip %s -> %q -> (%s,%v)", mode, mode.String(), got, ok)
		}
	}
}

func TestEffectiveRenderKind(t *testing.T) {
	cases := []struct {
		name      string
		mode      displayMode
		streaming bool
		override  blockOverride
		want      renderKind
	}{
		{"preview", displayPreview, false, overrideNone, renderClipped},
		{"preview streaming", displayPreview, true, overrideNone, renderClipped},
		{"expanded", displayExpanded, false, overrideNone, renderFull},
		{"collapsed", displayCollapsed, false, overrideNone, renderOneLine},
		{"auto streaming", displayAuto, true, overrideNone, renderFull},
		{"auto done", displayAuto, false, overrideNone, renderOneLine},
		// Overrides win over the mode in all cases.
		{"override expanded beats collapsed", displayCollapsed, false, overrideExpanded, renderFull},
		{"override collapsed beats expanded", displayExpanded, false, overrideCollapsed, renderOneLine},
		{"override expanded beats auto-done", displayAuto, false, overrideExpanded, renderFull},
		{"override collapsed beats preview", displayPreview, true, overrideCollapsed, renderOneLine},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveRenderKind(tc.mode, tc.streaming, tc.override); got != tc.want {
				t.Fatalf("effectiveRenderKind(%s,%v,%d) = %d, want %d", tc.mode, tc.streaming, tc.override, got, tc.want)
			}
		})
	}
}

// toolModeRenderMatrix exercises a finished tool block (30-line result)
// under each mode and asserts the visible result-row count + the
// one-line hint, measuring the real View() output.
func TestToolDisplayModesRenderedRowCounts(t *testing.T) {
	const budget = 8 // EffectiveToolOutputCollapsedHeight default
	cases := []struct {
		name      string
		mode      displayMode
		streaming bool
		override  blockOverride
		wantRows  int  // result lines visible
		wantHint  bool // "30 lines" one-line hint present
	}{
		{"preview", displayPreview, false, overrideNone, budget, false},
		{"expanded", displayExpanded, false, overrideNone, 30, false},
		{"collapsed", displayCollapsed, false, overrideNone, 0, true},
		{"auto while running", displayAuto, true, overrideNone, 30, false},
		{"auto once done", displayAuto, false, overrideNone, 0, true},
		{"override expanded in collapsed", displayCollapsed, false, overrideExpanded, 30, false},
		{"override collapsed in expanded", displayExpanded, false, overrideCollapsed, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := scenarioModel(t)
			m.vp.SetWidth(100)
			m.vp.SetHeight(40)
			m.setToolDisplayMode(tc.mode)
			m.blocks = []block{{
				kind:       "tool",
				toolName:   "bash",
				toolArgs:   `{"cmd":"echo"}`,
				toolResult: bigToolResult(30),
				streaming:  tc.streaming,
				override:   tc.override,
			}}
			m.renderBlocks()
			view := ansi.Strip(m.vp.View())
			if got := countResultRows(view); got != tc.wantRows {
				t.Fatalf("%s: result rows = %d, want %d\n%s", tc.name, got, tc.wantRows, view)
			}
			if !strings.Contains(view, "bash") {
				t.Fatalf("%s: tool header should always show the name:\n%s", tc.name, view)
			}
			if gotHint := strings.Contains(view, "30 lines"); gotHint != tc.wantHint {
				t.Fatalf("%s: one-line hint present=%v, want %v\n%s", tc.name, gotHint, tc.wantHint, view)
			}
		})
	}
}

// ctrl+x o cycles the tool-output display mode (sibling of ctrl+x h).
func TestToolDisplayKeybindCyclesMode(t *testing.T) {
	m := scenarioModel(t)

	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "o"})
	if m.toolMode != displayAuto {
		t.Fatalf("toolMode = %s, want auto after first cycle", m.toolMode)
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "o"})
	if m.toolMode != displayCollapsed {
		t.Fatalf("toolMode = %s, want collapsed after second cycle", m.toolMode)
	}
	// The thinking mode is untouched by the tool keybind.
	if m.thinkingMode != displayPreview {
		t.Fatalf("ctrl+x o must not change thinkingMode (got %s)", m.thinkingMode)
	}
}

func TestToolDisplaySlashSetsAndCycles(t *testing.T) {
	m := scenarioModel(t)
	_ = m.handleSlash("/tool-display expanded")
	if m.toolMode != displayExpanded {
		t.Fatalf("toolMode = %s, want expanded", m.toolMode)
	}
	_ = m.handleSlash("/tool-display")
	if m.toolMode != displayPreview {
		t.Fatalf("toolMode = %s, want preview after cycle from expanded", m.toolMode)
	}
}

// A running tool in auto mode renders full, then collapses to one line once
// its result arrives (the onToolResult transition clears streaming).
func TestToolAutoCollapsesWhenResultArrives(t *testing.T) {
	m := scenarioModel(t)
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)
	m.setToolDisplayMode(displayAuto)
	m.blocks = []block{{
		kind:      "tool",
		toolName:  "bash",
		streaming: true, // running, no result yet
	}}
	m.renderBlocks()
	// While running there is no result to show, but the block is in its
	// full (Expanded) form — confirm it is not yet showing a one-line hint.
	running := ansi.Strip(m.vp.View())
	if strings.Contains(running, "lines") {
		t.Fatalf("running tool in auto mode should not show a collapsed line hint:\n%s", running)
	}

	// Result arrives: streaming clears, auto collapses to one line.
	m.blocks[0].toolResult = bigToolResult(15)
	m.blocks[0].streaming = false
	m.invalidateBlockCache(0)
	m.renderBlocks()
	done := ansi.Strip(m.vp.View())
	if countResultRows(done) != 0 || !strings.Contains(done, "15 lines") {
		t.Fatalf("auto mode should collapse the tool to one line once the result arrives:\n%s", done)
	}
}

// The tool display mode and the thinking display mode are independent.
func TestToolAndThinkingModesAreIndependent(t *testing.T) {
	m := scenarioModel(t)
	m.vp.SetWidth(100)
	m.vp.SetHeight(40)
	m.setThinkingDisplayMode(displayExpanded)
	m.setToolDisplayMode(displayCollapsed)

	m.blocks = []block{
		{kind: "thinking", body: "alpha\nbeta\ngamma"},
		{kind: "tool", toolName: "grep", toolResult: bigToolResult(20)},
	}
	m.renderBlocks()
	view := ansi.Strip(m.vp.View())

	if !strings.Contains(view, "alpha") || !strings.Contains(view, "gamma") {
		t.Fatalf("thinking expanded should show full body:\n%s", view)
	}
	if countResultRows(view) != 0 || !strings.Contains(view, "20 lines") {
		t.Fatalf("tool collapsed should show only a one-line header:\n%s", view)
	}
}
