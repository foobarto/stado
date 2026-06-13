package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestPanelWideCJKRowWidth: structured-panel bodies that carry wide-CJK /
// emoji content (plugin output is arbitrary) must still render every row at
// exactly the box width — the rune/byte-count wrap+pad math under-budgeted
// wide graphemes, shoving the right border out by ~2x and splitting runes
// mid-byte (invalid UTF-8 leaking into the terminal). All body kinds that
// flow through wrapWords / writeRow / table column-width detection are
// exercised.
func TestPanelWideCJKRowWidth(t *testing.T) {
	panel := pluginRuntime.Panel{
		Title:   "宽字符测试 " + strings.Repeat("标题", 40),
		Variant: "ok",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Text: strings.Repeat("你好世界", 30)},
			{Kind: "kv", KV: []pluginRuntime.KVPair{
				{Label: "名称", Value: strings.Repeat("値", 60)},
				{Label: "ascii", Value: strings.Repeat("emoji😀", 20)},
			}},
			{Kind: "list", List: pluginRuntime.ListBody{Items: []string{strings.Repeat("項目", 50)}}},
			{Kind: "code", Code: pluginRuntime.CodeBody{Content: strings.Repeat("コード", 40)}},
			{Kind: "table", Table: pluginRuntime.TableBody{
				Columns: []string{"列一", "列二"},
				Rows:    [][]string{{strings.Repeat("セル", 30), strings.Repeat("值", 30)}},
			}},
		},
		Footer: strings.Repeat("脚注", 50),
	}
	out := renderPanelASCII(panel)
	want := panelRenderWidth + 2 // two │ borders + w inner columns
	for i, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if !utf8.ValidString(line) {
			t.Errorf("row %d is not valid UTF-8 (rune split mid-grapheme): %q", i, line)
		}
		if w := ansi.StringWidth(line); w != want {
			t.Errorf("row %d display width = %d, want %d: %q", i, w, want, line)
		}
	}
}

// TestWrapWordsWideOversizedWord: a single word wider than the budget is
// broken on display-column boundaries, never mid-rune, and no fragment
// exceeds the width.
func TestWrapWordsWideOversizedWord(t *testing.T) {
	word := strings.Repeat("漢", 20) // 40 display columns, one "word"
	width := 10
	for _, frag := range wrapWords(word, width) {
		if !utf8.ValidString(frag) {
			t.Fatalf("fragment is invalid UTF-8: %q", frag)
		}
		if w := ansi.StringWidth(frag); w > width {
			t.Fatalf("fragment display width = %d > budget %d: %q", w, width, frag)
		}
	}
}

// TestWrapWordsWidthOneWideGrapheme: a grapheme wider than the budget must
// still make progress (one grapheme per row) rather than loop forever. If
// the zero-progress guard regresses, this hangs and the test timeout fires.
func TestWrapWordsWidthOneWideGrapheme(t *testing.T) {
	rows := wrapWords("漢漢漢", 1)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (one grapheme each), got %d: %q", len(rows), rows)
	}
}
