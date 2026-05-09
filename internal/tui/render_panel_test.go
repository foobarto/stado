package tui

import (
	"strings"
	"testing"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// F9b.2 unit tests for the structured-panel TUI bridge:
//   - Update path: pluginRenderMsg lands as a system block
//   - Bridge: nil-program drops on the floor
//   - Renderer: each body kind produces visually distinguishable
//     output without panicking on edge inputs

// TestPluginRender_AppendsSystemBlock: a stado_ui_render emit lands
// as a system-style block whose body contains the rendered panel.
// Mirrors TestPluginPrint_AppendsSystemBlock. Fire-and-forget — no
// response channel, no state changes beyond the appended block. F9b.2.
func TestPluginRender_AppendsSystemBlock(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	before := len(m.blocks)
	panel := pluginRuntime.Panel{
		Title:   "Scan results",
		Variant: "ok",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Text: "3 hosts up, 2 down."},
		},
	}
	_, _ = m.Update(pluginRenderMsg{panel: panel})
	if len(m.blocks) != before+1 {
		t.Fatalf("expected one new block; before=%d after=%d", before, len(m.blocks))
	}
	added := m.blocks[len(m.blocks)-1]
	if added.kind != "system" {
		t.Errorf("kind = %q, want %q", added.kind, "system")
	}
	// The rendered body must include the title, the variant tag in
	// the title bar, and the body text.
	for _, want := range []string{"Scan results", "(ok)", "3 hosts up, 2 down."} {
		if !strings.Contains(added.body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, added.body)
		}
	}
	// Border characters must surround the content (top + bottom).
	if !strings.Contains(added.body, "╭") || !strings.Contains(added.body, "╮") {
		t.Errorf("body missing rounded top border\n--- body ---\n%s", added.body)
	}
	if !strings.Contains(added.body, "╰") || !strings.Contains(added.body, "╯") {
		t.Errorf("body missing rounded bottom border\n--- body ---\n%s", added.body)
	}
}

// TestPluginRender_NilProgramDropsOnFloor: the render bridge must
// not block or error when the model has no live tea.Program — same
// fire-and-forget contract as print. F9b.2.
func TestPluginRender_NilProgramDropsOnFloor(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.program = nil

	bridge := tuiRenderBridge{model: m}
	err := bridge.Render(t.Context(), pluginRuntime.Panel{
		Title:    "x",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "y"}},
	})
	if err != nil {
		t.Errorf("nil-program render should drop silently, got: %v", err)
	}
}

// TestRenderPanelASCII_AllBodyKinds: every body kind produces output
// containing the expected payload — the canary for "the renderer
// hasn't silently dropped a kind." Snapshot-style assertions on
// substrings, not full byte equality, so cosmetic spacing changes
// don't churn the test on every layout tweak. F9b.2.
func TestRenderPanelASCII_AllBodyKinds(t *testing.T) {
	panel := pluginRuntime.Panel{
		Title:  "All-kind sweep",
		Footer: "footer text here",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Heading: "Plain", Text: "narrative goes here"},
			{Kind: "kv", Heading: "Pairs", KV: []pluginRuntime.KVPair{
				{Label: "host", Value: "10.0.0.1"},
				{Label: "port", Value: "443"},
			}},
			{Kind: "list", Heading: "Steps", List: pluginRuntime.ListBody{
				Marker: "numbered",
				Items:  []string{"first", "second", "third"},
			}},
			{Kind: "code", Heading: "Snippet", Code: pluginRuntime.CodeBody{
				Language: "go",
				Content:  "fmt.Println(\"hi\")",
			}},
			{Kind: "table", Heading: "Hosts", Table: pluginRuntime.TableBody{
				Columns: []string{"host", "port"},
				Rows:    [][]string{{"a.example", "22"}, {"b.example", "80"}},
			}},
			{Kind: "diff", Heading: "Change", Diff: pluginRuntime.DiffBody{
				Before: "old line",
				After:  "new line",
			}},
		},
	}
	out := renderPanelASCII(panel)
	mustContain := []string{
		"All-kind sweep",
		"narrative goes here",
		"Plain", "Pairs", "Steps", "Snippet", "Hosts", "Change",
		"host: 10.0.0.1",
		"port: 443",
		"1. first",
		"2. second",
		"3. third",
		"[go]",
		"fmt.Println",
		"a.example",
		"b.example",
		"- old line",
		"+ new line",
		"footer text here",
	}
	for _, w := range mustContain {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n--- output ---\n%s", w, out)
		}
	}
}

// TestRenderPanelASCII_ListMarkers: bullet / numbered / check all
// produce visibly distinct prefixes. F9b.2.
func TestRenderPanelASCII_ListMarkers(t *testing.T) {
	cases := []struct {
		marker string
		want   string
	}{
		{"", "•"},        // default
		{"bullet", "•"},
		{"numbered", "1."},
		{"check", "[ ]"},
	}
	for _, tc := range cases {
		t.Run(tc.marker, func(t *testing.T) {
			out := renderPanelASCII(pluginRuntime.Panel{
				Title: "t",
				Sections: []pluginRuntime.Section{
					{Kind: "list", List: pluginRuntime.ListBody{
						Marker: tc.marker,
						Items:  []string{"item"},
					}},
				},
			})
			if !strings.Contains(out, tc.want) {
				t.Errorf("marker=%q: missing %q in output:\n%s", tc.marker, tc.want, out)
			}
		})
	}
}

// TestRenderPanelASCII_VariantInTitle: every variant value surfaces
// as a parenthetical in the title row so warn / error are visible
// without theme integration. F9b.2.
func TestRenderPanelASCII_VariantInTitle(t *testing.T) {
	for _, variant := range []string{"info", "ok", "warn", "error", "recommendation"} {
		t.Run(variant, func(t *testing.T) {
			out := renderPanelASCII(pluginRuntime.Panel{
				Title:    "t",
				Variant:  variant,
				Sections: []pluginRuntime.Section{{Kind: "text", Text: "x"}},
			})
			if !strings.Contains(out, "("+variant+")") {
				t.Errorf("title bar missing variant %q in output:\n%s", variant, out)
			}
		})
	}
}

// TestRenderPanelASCII_LongLineWraps: long text content wraps onto
// multiple rows rather than truncating or overflowing the border.
// F9b.2.
func TestRenderPanelASCII_LongLineWraps(t *testing.T) {
	long := strings.Repeat("word ", 50) // 250 chars, far exceeds inner width
	out := renderPanelASCII(pluginRuntime.Panel{
		Title:    "t",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: long}},
	})
	// Every line should fit within the border. Strip border chars
	// and verify no inner content exceeds panelRenderWidth.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
			continue // border lines (top/bottom)
		}
		inner := line[len("│") : len(line)-len("│")]
		// inner width measured in runes (box chars are multi-byte).
		if len([]rune(inner)) != panelRenderWidth {
			t.Errorf("inner row width = %d runes, want %d:\n%s",
				len([]rune(inner)), panelRenderWidth, line)
		}
	}
}

// TestRenderPanelASCII_TableNarrowing: a table whose natural width
// exceeds the panel's inner width should narrow columns rather than
// breaking the right border. F9b.2.
func TestRenderPanelASCII_TableNarrowing(t *testing.T) {
	wide := strings.Repeat("x", 100)
	out := renderPanelASCII(pluginRuntime.Panel{
		Title: "t",
		Sections: []pluginRuntime.Section{
			{Kind: "table", Table: pluginRuntime.TableBody{
				Columns: []string{"col1", "col2"},
				Rows:    [][]string{{wide, wide}},
			}},
		},
	})
	// No inner row should overflow the inner width.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
			continue
		}
		inner := line[len("│") : len(line)-len("│")]
		if len([]rune(inner)) != panelRenderWidth {
			t.Errorf("table row inner width = %d runes, want %d:\n%s",
				len([]rune(inner)), panelRenderWidth, line)
		}
	}
}
