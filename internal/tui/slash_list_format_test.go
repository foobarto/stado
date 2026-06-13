package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// slashWidthModel builds a Model with a real renderer and a viewport
// sized to vpWidth so slashListWidth() / the system-block render path
// behave exactly as they do live (a bare &Model{} has a zero-value
// viewport and can't exercise the width math).
func slashWidthModel(t *testing.T, vpWidth int) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.vp.SetWidth(vpWidth)
	m.vp.SetHeight(40)
	return m
}

// systemBlockTextWidth mirrors renderBlocks + the system-block style:
// the block is rendered at max(10, vp.Width()-2), and the left border
// (1) + Padding(0,1) (2) eat 3 columns of that (lipgloss v2 .Width
// INCLUDES border+padding). The remainder is the real text area the
// pre-wrapped slash list must fit inside.
func systemBlockTextWidth(vpWidth int) int {
	blockW := vpWidth - 2
	if blockW < 10 {
		blockW = 10
	}
	return blockW - 3
}

// TestSlashListWidth_MatchesSystemBlockTextArea is the load-bearing
// invariant for the slash-command list formatter: slashListWidth()
// must NOT exceed the actual system-block text area at any sized
// viewport. When it does, WrapDescList wraps to a width wider than the
// box, the box re-wraps the already-wrapped lines, and the dangling
// word lands at column 0 — the hanging indent breaks. Regression: a
// floor clamp of 40 made slashListWidth() return 40 for any viewport
// narrow enough that the real text area was below 40 (vp.Width() <= 44),
// guaranteeing a re-wrap.
func TestSlashListWidth_MatchesSystemBlockTextArea(t *testing.T) {
	for _, vpw := range []int{120, 80, 60, 50, 45, 44, 43, 30, 20, 12} {
		m := slashWidthModel(t, vpw)
		got := m.slashListWidth()
		want := systemBlockTextWidth(vpw)
		if got > want {
			t.Errorf("vp.Width()=%d: slashListWidth()=%d exceeds system-block text area %d — list will re-wrap and break the hang indent",
				vpw, got, want)
		}
	}
}

// TestSlashList_HangIndentSurvivesSystemBlock renders a wrapping
// description through the REAL system-block render path (not just
// WrapDescList in isolation) at narrow widths and asserts no
// continuation line flows back to column 0. Before the fix, a 44-col
// viewport re-wrapped the pre-wrapped lines and a dangling word
// ("in") landed immediately after the border at column 0.
func TestSlashList_HangIndentSurvivesSystemBlock(t *testing.T) {
	rows := []render.DescRow{
		{Name: "fs.read", Desc: "read a file from the workspace returning its full UTF-8 content honoring sandbox read paths and recording the read in the dedup log"},
		{Name: "x", Desc: "tiny"},
	}
	for _, vpw := range []int{60, 50, 45, 44, 43, 30} {
		m := slashWidthModel(t, vpw)
		body := render.WrapDescList(rows, m.slashListWidth())
		out, _ := m.renderBlock(block{kind: "system", body: body}, vpw-2)
		stripped := ansi.Strip(out)
		for _, ln := range strings.Split(strings.TrimRight(stripped, "\n"), "\n") {
			// Strip the leading border glyph + the single space the style's
			// left padding adds, to recover the text-area content column 0.
			content := ln
			if i := strings.IndexAny(content, "│"); i >= 0 {
				content = content[i+len("│"):]
			}
			content = strings.TrimPrefix(content, " ") // Padding(0,1) left pad
			trimmed := strings.TrimSpace(content)
			if trimmed == "" {
				continue
			}
			// A line whose text begins at content-column 0 must be a row
			// start (carries one of the names). Anything else is a wrapped
			// continuation that should hang at the gutter, not column 0.
			if !strings.HasPrefix(content, " ") {
				if !strings.HasPrefix(content, "fs.read") && !strings.HasPrefix(content, "x ") && content != "x" {
					t.Errorf("vp.Width()=%d: wrapped continuation flowed to text-column 0 (broken hang indent): %q",
						vpw, ln)
				}
			}
		}
	}
}

// firstToolWithDescription returns a tool name + the first word of its
// description from the default registry. Hermetic: CI has no installed plugins,
// so we use whatever bundled tool is present rather than hardcoding a
// plugin-provided one.
func firstToolWithDescription(cfg *config.Config) (name, descWord string) {
	for _, tl := range runtime.BuildDefaultRegistry(cfg).All() {
		if d := strings.TrimSpace(tl.Description()); d != "" {
			return tl.Name(), strings.Fields(d)[0]
		}
	}
	return "", ""
}

// leadingSpaces counts the run of leading spaces on a line — the column
// a hanging continuation line begins at.
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

// TestToolLs_ShowsDescription: before the shared formatter, `/tool ls`
// printed only "name<pad>state" — the tool description never surfaced.
// It must now render the description (and continue to mark autoloaded
// tools).
func TestToolLs_ShowsDescription(t *testing.T) {
	cfg := &config.Config{}
	name, descWord := firstToolWithDescription(cfg)
	if name == "" {
		t.Skip("no tool with a description in the default registry")
	}
	m := &Model{cfg: cfg}
	// Narrow to the single tool so we can assert on its description without
	// scanning the whole registry. The old "%-32s %s" form never showed it.
	m.handleToolSlash([]string{"/tool", "ls", name})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, name) {
		t.Fatalf("/tool ls missing the tool name %q: %q", name, out)
	}
	// A representative word from the description must appear — it was entirely
	// absent before this change.
	if !strings.Contains(out, descWord) {
		t.Errorf("/tool ls should now render the tool DESCRIPTION (word %q missing): %q", descWord, out)
	}
	// No ellipsis: the formatter wraps rather than truncates.
	if strings.Contains(out, "…") {
		t.Errorf("/tool ls must not truncate the description with an ellipsis: %q", out)
	}
}

// TestToolLs_AutoloadedMarked: an autoloaded tool carries the
// "(autoloaded)" marker in its rendered row.
func TestToolLs_AutoloadedMarked(t *testing.T) {
	cfg := &config.Config{}
	name, _ := firstToolWithDescription(cfg)
	if name == "" {
		t.Skip("no tool with a description in the default registry")
	}
	cfg.Tools.Autoload = []string{name}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "ls", name})
	out := m.lastSystemBlockBody()
	if !strings.Contains(out, "(autoloaded)") {
		t.Errorf("/tool ls should mark autoloaded tool %q: %q", name, out)
	}
}

// TestSkillList_HangIndentsDescription: a /skill list with a long
// description must hang-indent the wrapped continuation lines at the
// gutter, not flow them back to column 0.
func TestSkillList_HangIndentsDescription(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	longDesc := "reproduce the failing behaviour as a red test before touching any " +
		"production code then make the smallest change that turns it green and sweep " +
		"for sibling bugs that share the same root cause"
	body := "---\nname: bugfix\ndescription: " + longDesc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "bugfix.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, root)
	// Force a realistic-but-narrow panel so the long description wraps.
	m.vp.SetWidth(60)
	m.handleSkillSlash([]string{"/skill"})
	out := m.lastSystemBlockBody()

	if !strings.Contains(out, "/skill:bugfix") {
		t.Fatalf("/skill list missing the command: %q", out)
	}
	// The description must wrap (multi-line) AND every continuation line of
	// the skill row must be indented past column 0 (hanging indent).
	lines := strings.Split(out, "\n")
	var sawHang bool
	for _, ln := range lines {
		// A continuation line carries description words but not the
		// "/skill:" name and is indented.
		if strings.Contains(ln, "root cause") || strings.Contains(ln, "sibling") {
			if !strings.HasPrefix(ln, "/skill:") {
				if leadingSpaces(ln) == 0 {
					t.Errorf("wrapped /skill description flowed to column 0: %q", ln)
				} else {
					sawHang = true
				}
			}
		}
	}
	if !sawHang {
		t.Errorf("expected a hang-indented continuation line in /skill output:\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("/skill must not truncate the description: %q", out)
	}
}
