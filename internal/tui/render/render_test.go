package render

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/theme"
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

func TestRenderer_MessageThinking(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("message_thinking", map[string]any{
		"Body":  "reasoning step",
		"Width": 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Thinking:") {
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
		"Title":        "stado",
		"Version":      "0.0.0-dev",
		"Model":        "qwen",
		"ProviderName": "ollama",
		"Cwd":          "/tmp/proj",
		"TokensHuman":  "1.2K tokens",
		"TokenPercent": "12% used",
		"CostHuman":    "$0.03 spent",
		"Todos": []map[string]any{
			{"Title": "write tests", "Status": "in_progress"},
			{"Title": "ship it", "Status": "open"},
		},
		"Width": 28,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Context", "1.2K tokens", "Model", "qwen", "write tests", "ship it"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q: %q", want, out)
		}
	}
}

func TestRenderer_Status(t *testing.T) {
	r := newRenderer(t)
	out, err := r.Exec("status", map[string]any{
		"State":  "idle",
		"Tokens": "1.2K (12%)",
		"Cost":   "$0.03",
		"Width":  80,
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
		if !strings.Contains(out, want) {
			t.Errorf("input_status missing %q: %q", want, out)
		}
	}

	out2, _ := r.Exec("input_status", map[string]any{
		"Mode":         "Do",
		"Model":        "gpt-4o",
		"ProviderName": "openai",
		"Hint":         "",
	})
	if !strings.Contains(out2, "Do") {
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
