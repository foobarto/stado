package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

func TestNormalizeChromeList(t *testing.T) {
	// Empty / absent → defaults (the #21 decision).
	if got := normalizeSidebarSections(nil); !reflect.DeepEqual(got, defaultSidebarSections) {
		t.Errorf("nil sidebar should use defaults, got %v", got)
	}
	if got := normalizeFooterSegments([]string{}); !reflect.DeepEqual(got, defaultFooterSegments) {
		t.Errorf("empty footer should use defaults, got %v", got)
	}

	// Reorder + dedup; unknown ids (plugin panel ids) are preserved, blanks dropped.
	got := normalizeSidebarSections([]string{"agent", "now", "agent", "", "my-plugin"})
	want := []string{"agent", "now", "my-plugin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// A list that's all blanks collapses to nothing → defaults.
	if got := normalizeSidebarSections([]string{"", ""}); !reflect.DeepEqual(got, defaultSidebarSections) {
		t.Errorf("all-blank should fall back to defaults, got %v", got)
	}
}

// #21 step 2: the sidebar renders only the configured sections, in order.
func TestSidebarRespectsConfiguredSections(t *testing.T) {
	m := scenarioModel(t)
	m.height = 24
	m.blocks = []block{{kind: "tool", toolName: "bash"}}

	m.sidebarSections = []string{"repo", "now"} // repo first, hide the rest
	out := ansi.Strip(m.renderSidebar(34))

	if !strings.Contains(out, "Repo") || !strings.Contains(out, "Now") {
		t.Fatalf("configured sections should render:\n%s", out)
	}
	if strings.Contains(out, "Agent") {
		t.Errorf("Agent should be hidden when not in the section list:\n%s", out)
	}
	if strings.Index(out, "Repo") > strings.Index(out, "Now") {
		t.Errorf("Repo should render before Now per the configured order:\n%s", out)
	}
}

// #21 step 3: the footer shows only the configured segments.
func TestFooterRespectsConfiguredSegments(t *testing.T) {
	m := scenarioModel(t)
	m.usage.InputTokens = 1200
	m.usage.CostUSD = 0.05

	def := ansi.Strip(m.renderStatus(80))
	if !strings.Contains(def, "$0.05") || !strings.Contains(def, "commands") {
		t.Fatalf("default footer should show cost + commands:\n%s", def)
	}

	m.footerSegments = []string{"state", "tokens", "persona"} // hide cost, daemon, commands
	out := ansi.Strip(m.renderStatus(80))
	if strings.Contains(out, "$0.05") {
		t.Errorf("cost should be hidden when not in segments:\n%s", out)
	}
	if strings.Contains(out, "commands") {
		t.Errorf("commands should be hidden when not in segments:\n%s", out)
	}
	if !strings.Contains(out, "1.2K") {
		t.Errorf("tokens should still show:\n%s", out)
	}
}

// ===========================================================================
// #21 part 2 — plugin display API (sidebar / footer / log targets)
// ===========================================================================

func TestPluginToneForVariant(t *testing.T) {
	cases := map[string]string{
		"":               "text",
		"info":           "text",
		"ok":             "success",
		"warn":           "warning",
		"error":          "error",
		"recommendation": "accent",
	}
	for variant, want := range cases {
		if got := pluginToneForVariant(variant); got != want {
			t.Errorf("pluginToneForVariant(%q) = %q, want %q", variant, got, want)
		}
	}
}

// onPluginRender routes by Target: sidebar/footer/log update their store,
// viewport appends a scrollback block, and a built-in id is rejected.
func TestOnPluginRenderRoutesByTarget(t *testing.T) {
	m := scenarioModel(t)
	priorBlocks := len(m.blocks)

	onPluginRender(m, pluginRenderMsg{panel: pluginRuntime.Panel{
		Title: "Scan", Target: "sidebar", ID: "scan", Variant: "ok",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "5 up"}},
	}})
	if _, ok := m.pluginSidebarPanels["scan"]; !ok {
		t.Errorf("sidebar-target render should store under id; store=%v", m.pluginSidebarPanels)
	}

	onPluginRender(m, pluginRenderMsg{panel: pluginRuntime.Panel{
		Title: "Creds", Footer: "2 found", Target: "footer", ID: "creds",
	}})
	if _, ok := m.pluginFooterPanels["creds"]; !ok {
		t.Errorf("footer-target render should store under id; store=%v", m.pluginFooterPanels)
	}

	onPluginRender(m, pluginRenderMsg{panel: pluginRuntime.Panel{
		Title: "Alert", Footer: "done", Target: "log",
	}})
	if !logTailHasPluginLine(m.logTail) {
		t.Errorf("log-target render should append a plugin log line; tail=%v", m.logTail)
	}

	// viewport (default) still appends a scrollback block.
	onPluginRender(m, pluginRenderMsg{panel: pluginRuntime.Panel{
		Title: "Report", Sections: []pluginRuntime.Section{{Kind: "text", Text: "ok"}},
	}})
	if len(m.blocks) != priorBlocks+1 {
		t.Errorf("viewport render should append one block, grew by %d", len(m.blocks)-priorBlocks)
	}

	// A built-in section id must not be writable by a plugin.
	onPluginRender(m, pluginRenderMsg{panel: pluginRuntime.Panel{
		Title: "Evil", Target: "sidebar", ID: "now",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "shadow"}},
	}})
	if _, ok := m.pluginSidebarPanels["now"]; ok {
		t.Errorf("plugin must not write to built-in section id 'now'")
	}
}

// A configured plugin sidebar panel renders; an unconfigured one does not;
// a built-in id is never shadowed by a stored plugin panel.
func TestSidebarRendersConfiguredPluginPanel(t *testing.T) {
	m := scenarioModel(t)
	m.height = 24
	m.pluginSidebarPanels = map[string]pluginRuntime.Panel{
		"scan": {Title: "Nmap", Variant: "ok", ID: "scan", Sections: []pluginRuntime.Section{
			{Kind: "text", Text: "5 hosts up"},
		}},
	}

	// Listed in sections → renders.
	m.sidebarSections = []string{"header", "scan"}
	out := ansi.Strip(m.renderSidebar(40))
	if !strings.Contains(out, "Nmap") || !strings.Contains(out, "5 hosts up") {
		t.Fatalf("configured plugin panel should render:\n%s", out)
	}

	// Not listed → hidden.
	m.sidebarSections = []string{"header"}
	out = ansi.Strip(m.renderSidebar(40))
	if strings.Contains(out, "Nmap") {
		t.Errorf("unconfigured plugin panel should be hidden:\n%s", out)
	}
}

// A configured plugin footer segment renders; a plugin-only segment list
// produces no dangling separator.
func TestFooterRendersConfiguredPluginSegment(t *testing.T) {
	m := scenarioModel(t)
	m.pluginFooterPanels = map[string]pluginRuntime.Panel{
		"creds": {Title: "Creds", Footer: "2 found", ID: "creds", Variant: "warn"},
	}

	m.footerSegments = []string{"tokens", "creds"}
	out := ansi.Strip(m.renderStatus(80))
	if !strings.Contains(out, "2 found") {
		t.Fatalf("configured plugin footer segment should render:\n%s", out)
	}

	// Plugin-only segment: first (and only) thing printed → no leading "·".
	// (The left cluster has its own "·" joiners, so scope the check to what
	// immediately precedes the segment text rather than the whole row.)
	m.footerSegments = []string{"creds"}
	out = ansi.Strip(m.renderStatus(80))
	if !strings.Contains(out, "2 found") {
		t.Fatalf("plugin-only footer should render:\n%s", out)
	}
	beforeSeg := strings.TrimRight(out[:strings.Index(out, "2 found")], " ")
	if strings.HasSuffix(beforeSeg, "·") {
		t.Errorf("plugin-only footer should have no dangling separator:\n%s", out)
	}

	// Not configured → hidden.
	m.footerSegments = []string{"tokens"}
	out = ansi.Strip(m.renderStatus(80))
	if strings.Contains(out, "2 found") {
		t.Errorf("unconfigured plugin footer segment should be hidden:\n%s", out)
	}
}

func TestFlattenPluginSidebarPanel(t *testing.T) {
	panel := pluginRuntime.Panel{
		Title:   "Report",
		Variant: "warn",
		Footer:  "fin",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Heading: "Summary", Text: "line one\nline two"},
			{Kind: "kv", KV: []pluginRuntime.KVPair{{Label: "host", Value: "h1"}}},
			{Kind: "list", List: pluginRuntime.ListBody{Marker: "check", Items: []string{"a"}}},
			{Kind: "code", Code: pluginRuntime.CodeBody{Language: "go", Content: "x"}},
			{Kind: "table", Table: pluginRuntime.TableBody{Columns: []string{"a", "b"}, Rows: [][]string{{"1", "2"}}}},
			{Kind: "diff", Diff: pluginRuntime.DiffBody{Before: "o", After: "n"}},
		},
	}
	got := flattenPluginSidebarPanel(panel)
	if got.Heading != "Report" {
		t.Errorf("heading = %q, want Report", got.Heading)
	}
	joined := ""
	warnLines := 0
	for _, ln := range got.Lines {
		joined += ln.Text + "\n"
		if ln.Tone == "warning" {
			warnLines++
		}
	}
	for _, want := range []string{"Summary", "line one", "line two", "host: h1", "▪ a", "[go block]", "[table 1×2]", "[diff]", "fin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("flattened panel missing %q:\n%s", want, joined)
		}
	}
	// Body lines (text/kv/list) take the variant tone; placeholders stay muted.
	if warnLines == 0 {
		t.Errorf("warn variant should tone body lines as warning:\n%+v", got.Lines)
	}
}
