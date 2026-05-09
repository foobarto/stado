package main

import (
	"strings"
	"sync"
	"testing"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// F9b.4 unit-tests for the MCP render bridge — accumulates panels per
// tool dispatch and packs them into the CallToolResult.

// TestMCPRenderBridge_AccumulatesAndDrains: render() appends; drain()
// returns and clears in one atomic pop. Mirrors the contract the
// per-call handler depends on.
func TestMCPRenderBridge_AccumulatesAndDrains(t *testing.T) {
	b := &mcpRenderBridge{}
	if got := b.drain(); len(got) != 0 {
		t.Fatalf("fresh drain = %d panels, want 0", len(got))
	}

	for i := 0; i < 3; i++ {
		_ = b.Render(t.Context(), pluginRuntime.Panel{
			Title:    "p" + string(rune('a'+i)),
			Sections: []pluginRuntime.Section{{Kind: "text", Text: "x"}},
		})
	}
	got := b.drain()
	if len(got) != 3 {
		t.Fatalf("drain = %d panels, want 3", len(got))
	}
	for i, p := range got {
		want := "p" + string(rune('a'+i))
		if p.Title != want {
			t.Errorf("panel %d title = %q, want %q", i, p.Title, want)
		}
	}
	// Second drain after the first is empty.
	if got := b.drain(); len(got) != 0 {
		t.Fatalf("second drain = %d panels, want 0", len(got))
	}
}

// TestMCPRenderBridge_ConcurrentRenderSafe: panels emitted from
// multiple goroutines all land. Plugins occasionally fan out async
// inside a tool dispatch; the mutex guards that.
func TestMCPRenderBridge_ConcurrentRenderSafe(t *testing.T) {
	b := &mcpRenderBridge{}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Render(t.Context(), pluginRuntime.Panel{
				Title:    "concurrent",
				Sections: []pluginRuntime.Section{{Kind: "text", Text: "x"}},
			})
		}()
	}
	wg.Wait()
	if got := b.drain(); len(got) != n {
		t.Fatalf("drain = %d panels, want %d (lost emits under concurrency)", len(got), n)
	}
}

// TestPanelsToStructured_Shape: each kind round-trips through
// panelToWireForMCP into the structured envelope with the right
// body field set and no foreign body fields. Mirrors the ACP
// kind=panel test (`internal/acp/render_bridge_test.go`).
func TestPanelsToStructured_Shape(t *testing.T) {
	panels := []pluginRuntime.Panel{
		{
			Title:   "Sweep",
			Variant: "ok",
			Footer:  "footer",
			ID:      "p1",
			Sections: []pluginRuntime.Section{
				{Kind: "text", Heading: "Plain", Text: "narrative"},
				{Kind: "kv", KV: []pluginRuntime.KVPair{{Label: "host", Value: "10.0.0.1"}}},
				{Kind: "list", List: pluginRuntime.ListBody{Marker: "numbered", Items: []string{"a", "b"}}},
				{Kind: "code", Code: pluginRuntime.CodeBody{Language: "go", Content: "fmt.Println"}},
				{Kind: "table", Table: pluginRuntime.TableBody{Columns: []string{"h"}, Rows: [][]string{{"a"}}}},
				{Kind: "diff", Diff: pluginRuntime.DiffBody{Before: "old", After: "new"}},
			},
		},
	}
	got := panelsToStructured(panels)
	if got == nil {
		t.Fatal("panelsToStructured returned nil for non-empty panels")
	}
	wirePanels, ok := got["panels"].([]map[string]any)
	if !ok || len(wirePanels) != 1 {
		t.Fatalf("structured.panels = %#v", got["panels"])
	}
	p := wirePanels[0]
	if p["title"] != "Sweep" || p["variant"] != "ok" || p["footer"] != "footer" || p["id"] != "p1" {
		t.Errorf("envelope mismatch: %#v", p)
	}
	sections, _ := p["sections"].([]map[string]any)
	if len(sections) != 6 {
		t.Fatalf("sections = %d, want 6", len(sections))
	}
	expected := []struct{ kind, body string }{
		{"text", "text"},
		{"kv", "kv"},
		{"list", "list"},
		{"code", "code"},
		{"table", "table"},
		{"diff", "diff"},
	}
	bodyFields := []string{"text", "kv", "list", "code", "table", "diff"}
	for i, want := range expected {
		sec := sections[i]
		if sec["kind"] != want.kind {
			t.Errorf("section %d kind = %v, want %v", i, sec["kind"], want.kind)
		}
		if _, present := sec[want.body]; !present {
			t.Errorf("section %d missing %q body: %#v", i, want.body, sec)
		}
		for _, bf := range bodyFields {
			if bf == want.body {
				continue
			}
			if _, present := sec[bf]; present {
				t.Errorf("section %d kind=%q must not carry foreign body %q",
					i, want.kind, bf)
			}
		}
	}
}

// TestPanelsToStructured_NilForEmpty: zero panels must yield nil so
// the caller can omit the StructuredContent field entirely (matches
// the omitempty behaviour mcp-go expects on the field).
func TestPanelsToStructured_NilForEmpty(t *testing.T) {
	if got := panelsToStructured(nil); got != nil {
		t.Errorf("panelsToStructured(nil) = %#v, want nil", got)
	}
	if got := panelsToStructured([]pluginRuntime.Panel{}); got != nil {
		t.Errorf("panelsToStructured(empty) = %#v, want nil", got)
	}
}

// TestRenderPanelsForUnstructuredContent_ZeroPanels: when no panels
// were emitted the text content is returned unchanged. F9b.4.
func TestRenderPanelsForUnstructuredContent_ZeroPanels(t *testing.T) {
	got := renderPanelsForUnstructuredContent("hello", nil)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// TestRenderPanelsForUnstructuredContent_AppendsASCII: panels render
// to ASCII and join after the existing text with a divider so MCP
// clients without the structured field still see something visually
// useful. F9b.4.
func TestRenderPanelsForUnstructuredContent_AppendsASCII(t *testing.T) {
	got := renderPanelsForUnstructuredContent("preamble", []pluginRuntime.Panel{
		{Title: "PanelA", Sections: []pluginRuntime.Section{{Kind: "text", Text: "body-a"}}},
		{Title: "PanelB", Sections: []pluginRuntime.Section{{Kind: "text", Text: "body-b"}}},
	})
	for _, want := range []string{"preamble", "PanelA", "PanelB", "body-a", "body-b", "╭", "╰"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestMCPHostWithRender_ImplementsRenderBridge: compile-time
// guarantee — pluginrun's attachLifecycleBridges interface assertion
// will pick the bridge up. Without this, the wiring works in a debug
// build but silently no-ops if a future refactor accidentally
// changes the host shape.
func TestMCPHostWithRender_ImplementsRenderBridge(t *testing.T) {
	var _ pluginRuntime.RenderBridge = mcpHostWithRender{}
}
