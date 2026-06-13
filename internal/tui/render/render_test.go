package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"text/template"

	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/workdirpath"
)

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(theme.Default())
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	return r
}

func TestRenderer_MessageUser(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("message_user", map[string]any{
		"Body":  "hello world",
		"Width": 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("rendered user msg missing body: %q", out)
	}
}

func TestRenderer_MessageAssistantMarkdown(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("message_assistant", map[string]any{
		"Body":  "# Heading\n\nSome **bold** text.",
		"Width": 60,
		"Model": "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Glamour output ANSI-escapes the heading; just check the word survived.
	if !strings.Contains(out, "Heading") {
		t.Errorf("markdown pass-through failed: %q", out)
	}
}

func TestMarkdownStyleFollowsThemeBackground(t *testing.T) {
	if themeUsesLightMarkdown(theme.Default()) {
		t.Fatal("default dark theme should use dark markdown style")
	}
	light, err := theme.Named("stado-light")
	if err != nil {
		t.Fatal(err)
	}
	if !themeUsesLightMarkdown(light) {
		t.Fatal("light theme should use light markdown style")
	}
	contrast, err := theme.Named("stado-contrast")
	if err != nil {
		t.Fatal(err)
	}
	if themeUsesLightMarkdown(contrast) {
		t.Fatal("contrast dark theme should use dark markdown style")
	}
}

func TestMarkdownStyleHonorsThemeOverride(t *testing.T) {
	dark := theme.Default()
	dark.Markdown.Style = "light"
	if !themeUsesLightMarkdown(dark) {
		t.Fatal("explicit light markdown style should override dark background")
	}

	light, err := theme.Named("stado-light")
	if err != nil {
		t.Fatal(err)
	}
	light.Markdown.Style = "dark"
	if themeUsesLightMarkdown(light) {
		t.Fatal("explicit dark markdown style should override light background")
	}

	light.Markdown.Style = "auto"
	if !themeUsesLightMarkdown(light) {
		t.Fatal("auto markdown style should fall back to light background")
	}
}

func TestRendererOverlayRejectsSymlinkTemplate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.tmpl")
	if err := os.WriteFile(target, []byte("OVERRIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "message_assistant.tmpl")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := NewWithOverlay(theme.Default(), dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink overlay rejection, got %v", err)
	}
}

func TestRendererOverlayRejectsOversizedTemplate(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", int(maxTemplateFileBytes)+1)
	if err := os.WriteFile(filepath.Join(dir, "message_assistant.tmpl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewWithOverlay(theme.Default(), dir)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized overlay rejection, got %v", err)
	}
}

func TestWalkTemplatesRejectsTooManyEntries(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/a.tmpl": {Data: []byte("a")},
		"templates/b.tmpl": {Data: []byte("b")},
		"templates/c.tmpl": {Data: []byte("c")},
	}
	err := walkTemplatesLimited(template.New("test"), fsys, "templates", 2, maxTemplateDepth)
	if err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Fatalf("walkTemplatesLimited error = %v, want entry cap rejection", err)
	}
}

func TestReadRootTemplateDirEntriesRejectsTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.tmpl", "b.tmpl", "c.tmpl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := workdirpath.NewStrictResolver().OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = readRootTemplateDirEntries(root, 2)
	if err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Fatalf("readRootTemplateDirEntries error = %v, want entry cap rejection", err)
	}
}

func TestRenderer_MessageThinking(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("message_thinking", map[string]any{
		"Body":  "reasoning step",
		"Width": 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "thinking") {
		t.Errorf("thinking label missing: %q", out)
	}
	if !strings.Contains(out, "reasoning step") {
		t.Errorf("thinking body missing: %q", out)
	}
}

func TestRenderer_ToolCollapsedAndExpanded(t *testing.T) {
	r := newRenderer(t)
	collapsed, err := r.Exec("message_tool", map[string]any{
		"Name":        "read_file",
		"ArgsPreview": `{"path":"foo.go"}`,
		"FullArgs":    `{"path":"foo.go"}`,
		"Result":      "",
		"Expanded":    false,
		"Duration":    "",
		"Width":       60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(collapsed, "read_file") || !strings.Contains(collapsed, "▸") {
		t.Errorf("collapsed marker/name missing: %q", collapsed)
	}

	expanded, err := r.Exec("message_tool", map[string]any{
		"Name":     "read_file",
		"FullArgs": "{\n  \"path\": \"foo.go\"\n}",
		"Result":   "package foo",
		"Expanded": true,
		"Width":    60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expanded, "package foo") {
		t.Errorf("expanded result missing: %q", expanded)
	}
	if !strings.Contains(expanded, "▾") {
		t.Errorf("expanded marker missing: %q", expanded)
	}
}

func TestRenderer_Sidebar(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("sidebar", map[string]any{
		"Title":       "stado",
		"Version":     "0.0.0-dev",
		"SessionMeta": "sess abc12345 · turn 3",
		"NowLines": []map[string]any{
			{"Text": "streaming turn", "Tone": "warning"},
			{"Text": "tool: bash", "Tone": "role_tool"},
		},
		"RiskLines": []map[string]any{
			{"Text": "ctx 82% / hard 90%", "Tone": "warning"},
			{"Text": "budget $0.03 / $2.00", "Tone": "text"},
		},
		"AgentLines": []map[string]any{
			{"Text": "qwen via ollama", "Tone": "text"},
			{"Text": "3 skills loaded · /skill", "Tone": "accent"},
		},
		"RepoLines": []map[string]any{
			{"Text": "repo: proj", "Tone": "text"},
			{"Text": "path: internal/tui", "Tone": "muted"},
		},
		"TodoSummary": "2 open / 0 done",
		"Todos": []map[string]any{
			{"Title": "write tests", "Status": "in_progress"},
			{"Title": "ship it", "Status": "open"},
		},
		"Sections": []string{"header", "now", "subagents", "risk", "agent", "repo", "logs", "todos"},
		"Width":    28,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Now", "Risk", "Agent", "Repo", "Todo",
		"streaming turn", "tool: bash", "ctx 82% / hard 90%",
		"qwen via ollama", "repo: proj", "write tests", "ship it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q: %q", want, out)
		}
	}
}

func TestRenderer_Status(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("status", map[string]any{
		"State":    "idle",
		"Tokens":   "1.2K (12%)",
		"Cost":     "$0.03",
		"Segments": []string{"state", "tokens", "cost", "cache", "budget", "persona", "daemon", "commands"},
		"Width":    80,
	})
	if err != nil {
		t.Fatal(err)
	}
	// New status bar is right-aligned: tokens · cost  ctrl+p commands
	if !strings.Contains(out, "1.2K (12%)") || !strings.Contains(out, "$0.03") {
		t.Errorf("status missing tokens/cost: %q", out)
	}
	if !strings.Contains(out, "ctrl+p") || !strings.Contains(out, "commands") {
		t.Errorf("status missing ctrl+p hint: %q", out)
	}
}

// TestRenderer_StatusSeparators guards the #21 footer "$printed" fix: the
// "·"-joined cluster must not strand a leading separator when an earlier
// segment is hidden, nor concatenate two segments without a gap.
func TestRenderer_StatusSeparators(t *testing.T) {
	r := newRenderer(t)
	render := func(segs []string) string {
		out, err := r.Exec("status", map[string]any{
			"State":    "idle",
			"Tokens":   "1.2K",
			"Cost":     "$0.03",
			"Cache":    "cache 50%",
			"Persona":  "dev",
			"Segments": segs,
			"Width":    80,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// tokens hidden, cost first to render → no leading separator.
	if got := render([]string{"cost"}); strings.Contains(got, "·") {
		t.Errorf("cost-only footer should have no separator, got %q", got)
	}
	// cache+cost with tokens hidden → exactly one separator between them.
	if got := render([]string{"cache", "cost"}); strings.Count(got, "·") != 1 {
		t.Errorf("cache+cost footer should have exactly one separator, got %q", got)
	}
	// persona alone (all prior segments hidden) → no leading separator.
	if got := render([]string{"persona"}); strings.Contains(got, "·") {
		t.Errorf("persona-only footer should have no separator, got %q", got)
	}
}

func TestRenderer_InputStatus(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("input_status", map[string]any{
		"Mode":         "Plan",
		"Model":        "Claude Opus 4.7",
		"ProviderName": "Anthropic",
		"Hint":         "xhigh",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan", "Claude Opus 4.7", "Anthropic", "xhigh"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("input_status missing %q: %q", want, out)
		}
	}

	out2, _ := r.Exec("input_status", map[string]any{
		"Mode":         "Do",
		"Model":        "gpt-4o",
		"ProviderName": "openai",
		"Hint":         "",
	})
	if !strings.Contains(strings.ToLower(out2), "do") {
		t.Errorf("Do mode label missing: %q", out2)
	}
}

func TestWordWrap(t *testing.T) {
	in := "one two three four five"
	got := wordWrap(in, 10)
	// Just ensure we have multiple lines, none longer than 10 chars.
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 10 {
			t.Errorf("wrap overshoot on %q (line %q > 10)", in, line)
		}
	}
}

// leadingSpaces counts the run of leading ASCII spaces on a line — the
// column a continuation line starts at. Used to assert hanging-indent
// alignment without depending on the exact gutter arithmetic.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

// TestWrapDescList_HangingIndentNoTruncation: a long description wraps
// across multiple lines, every continuation line hangs at the gutter
// column (NOT column 0), and no word is dropped.
func TestWrapDescList_HangingIndentNoTruncation(t *testing.T) {
	desc := "manage tools enable disable autoload unautoload and inspect the live registry from inside the session"
	rows := []DescRow{
		{Name: "short", Desc: "tiny"},
		{Name: "tool", Desc: desc},
	}
	out := WrapDescList(rows, 40)
	lines := strings.Split(out, "\n")

	// The gutter = longest name (5: "short") + 2 = 7.
	const gutter = 7

	// The "tool" row's first line carries the name; its continuation
	// lines must hang at the gutter.
	var sawCont bool
	inTool := false
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "tool"):
			inTool = true
			continue
		case strings.HasPrefix(ln, "short"):
			inTool = false
			continue
		}
		if inTool && strings.TrimSpace(ln) != "" {
			sawCont = true
			if got := leadingSpaces(ln); got != gutter {
				t.Errorf("continuation line not at gutter %d (got %d): %q", gutter, got, ln)
			}
		}
	}
	if !sawCont {
		t.Fatalf("expected a wrapped continuation line for the long desc; got:\n%s", out)
	}

	// No truncation: every word of the long desc must survive somewhere.
	flat := strings.Join(strings.Fields(out), " ")
	for _, w := range strings.Fields(desc) {
		if !strings.Contains(flat, w) {
			t.Errorf("word %q dropped (truncation?) from:\n%s", w, out)
		}
	}
	if strings.Contains(out, "…") || strings.Contains(out, "...") {
		t.Errorf("output contains an ellipsis — must not truncate:\n%s", out)
	}
}

// TestWrapDescList_FirstLineAlignment: a name that fits the gutter shares
// its line with the description's first line, which starts at the gutter
// column.
func TestWrapDescList_FirstLineAlignment(t *testing.T) {
	rows := []DescRow{
		{Name: "a", Desc: "alpha"},
		{Name: "bb", Desc: "beta"},
	}
	out := WrapDescList(rows, 40)
	lines := strings.Split(out, "\n")
	// gutter = longest name ("bb"=2) + 2 = 4.
	const gutter = 4
	for _, ln := range lines {
		// Description text starts at the gutter column on the first line.
		idx := strings.Index(ln, "alpha")
		if idx >= 0 && idx != gutter {
			t.Errorf("first-line desc not at gutter %d (got %d): %q", gutter, idx, ln)
		}
		idx = strings.Index(ln, "beta")
		if idx >= 0 && idx != gutter {
			t.Errorf("first-line desc not at gutter %d (got %d): %q", gutter, idx, ln)
		}
	}
}

// TestWrapDescList_LongNameOwnLine: a name longer than the gutter takes
// its own line, and the description wraps indented at the gutter below it.
func TestWrapDescList_LongNameOwnLine(t *testing.T) {
	long := "this_is_a_very_long_tool_name_exceeding_the_gutter_cap_for_sure"
	rows := []DescRow{
		{Name: "x", Desc: "short"},
		{Name: long, Desc: "the description that follows on its own indented line below the long name"},
	}
	out := WrapDescList(rows, 40)
	lines := strings.Split(out, "\n")

	// Find the line that is exactly the long name (own line, no desc on it).
	nameLineIdx := -1
	for i, ln := range lines {
		if ln == long {
			nameLineIdx = i
			break
		}
	}
	if nameLineIdx < 0 {
		t.Fatalf("long name should be on its own line:\n%s", out)
	}
	// The next line must be the description, indented at the gutter.
	if nameLineIdx+1 >= len(lines) {
		t.Fatalf("no description line after the long name:\n%s", out)
	}
	descLine := lines[nameLineIdx+1]
	if leadingSpaces(descLine) == 0 {
		t.Errorf("description after a long name must hang-indent, got col 0: %q", descLine)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "the description that follows") {
		t.Errorf("long-name description truncated:\n%s", out)
	}
}

// TestWrapDescList_EmptyDesc: a row with no description renders just the
// name, no trailing whitespace junk.
func TestWrapDescList_EmptyDesc(t *testing.T) {
	rows := []DescRow{{Name: "solo", Desc: ""}}
	out := WrapDescList(rows, 40)
	if strings.TrimRight(out, " ") != "solo" {
		t.Errorf("empty-desc row should render just the name, got %q", out)
	}
}

// TestWrapDescList_NarrowWidthNoPanic: a width far smaller than the gutter
// must not panic and must not DROP characters (no truncation). At extreme
// widths the description necessarily wraps one cell per line, so we assert
// every word's runes survive in order rather than contiguity.
func TestWrapDescList_NarrowWidthNoPanic(t *testing.T) {
	rows := []DescRow{
		{Name: "alpha", Desc: "one two three four"},
		{Name: "b", Desc: "five six"},
	}
	for _, w := range []int{0, 1, 3, 5, 8} {
		out := WrapDescList(rows, w) // must not panic
		// Names always survive whole (they're never wrapped).
		if !strings.Contains(out, "alpha") {
			t.Errorf("width %d: lost name: %q", w, out)
		}
		// No truncation: stripping whitespace recovers every word's runes.
		flat := strings.Join(strings.Fields(out), "")
		for _, word := range []string{"one", "two", "three", "four", "five", "six"} {
			if !strings.Contains(flat, word) {
				t.Errorf("width %d: word %q runes lost (truncation): %q", w, word, out)
			}
		}
	}
}

// TestWrapDescList_GutterCap: with a very long name in the set, the
// gutter for fitting names stays capped at 24 rather than ballooning to
// the longest name's width.
func TestWrapDescList_GutterCap(t *testing.T) {
	rows := []DescRow{
		{Name: strings.Repeat("z", 60), Desc: "long-name row forces a wide max but the cap holds"},
		{Name: "fit", Desc: "this row's desc starts at the capped gutter"},
	}
	out := WrapDescList(rows, 120)
	for _, ln := range strings.Split(out, "\n") {
		if idx := strings.Index(ln, "this row's desc"); idx >= 0 {
			if idx > 24 {
				t.Errorf("gutter exceeded cap of 24 (desc at col %d): %q", idx, ln)
			}
		}
	}
}

// TestWrapDescList_NoTrailingNewline: rows are newline-separated with no
// trailing newline, so callers can compose the block freely.
func TestWrapDescList_NoTrailingNewline(t *testing.T) {
	out := WrapDescList([]DescRow{{Name: "a", Desc: "x"}, {Name: "b", Desc: "y"}}, 40)
	if strings.HasSuffix(out, "\n") {
		t.Errorf("WrapDescList must not emit a trailing newline: %q", out)
	}
}
