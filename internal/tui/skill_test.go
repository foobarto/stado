package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func newSkillModel(t *testing.T, cwd string) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(cwd, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 40
	return m
}

// TestSkill_LoadsFromCwd: a .stado/skills/<name>.md under the cwd is
// discovered at NewModel time and surfaces in /skill output.
func TestSkill_LoadsFromCwd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: tidy\ndescription: tidy imports\n---\nSort go imports\n"
	if err := os.WriteFile(filepath.Join(dir, "tidy.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, root)
	if len(m.skills) != 1 || m.skills[0].Name != "tidy" {
		t.Fatalf("expected tidy skill loaded; got %+v", m.skills)
	}

	// /skill alone lists.
	m.handleSkillSlash([]string{"/skill"})
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "/skill:tidy") || !strings.Contains(last, "tidy imports") {
		t.Errorf("/skill list missing expected content: %q", last)
	}
}

// TestSkill_InjectBodyAsUserMessage: /skill:<name> appends the body
// as a user message to m.msgs so the next turn picks it up, and
// renders a user block so the view shows what was sent.
func TestSkill_InjectBodyAsUserMessage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: bugfix\n---\nReproduce the bug first, then fix.\n"
	if err := os.WriteFile(filepath.Join(dir, "bugfix.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, root)
	startMsgs := len(m.msgs)
	startBlocks := len(m.blocks)

	m.handleSkillSlash([]string{"/skill:bugfix"})

	if len(m.msgs) != startMsgs+1 {
		t.Fatalf("expected 1 new msg; got delta %d", len(m.msgs)-startMsgs)
	}
	if m.msgs[len(m.msgs)-1].Role != agent.RoleUser {
		t.Errorf("expected user role; got %v", m.msgs[len(m.msgs)-1].Role)
	}
	if len(m.blocks) != startBlocks+1 || m.blocks[len(m.blocks)-1].kind != "user" {
		t.Errorf("expected 1 new user block")
	}
}

// TestSkill_UnknownReportsError: /skill:missing produces a system
// block pointing at /skill (discoverability) instead of silently
// doing nothing.
func TestSkill_UnknownReportsError(t *testing.T) {
	m := newSkillModel(t, t.TempDir())
	m.handleSkillSlash([]string{"/skill:missing"})
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "not found") || !strings.Contains(last, "/skill") {
		t.Errorf("unhelpful error: %q", last)
	}
}

// TestSkill_SidebarSurfacesCount: when skills are loaded, the
// sidebar renders a "Skills: N — /skill" row so users discover
// the feature without needing to know the slash command in
// advance. Empty when no skills are loaded (template conditional
// hides the row entirely rather than showing "0 skills").
func TestSkill_SidebarSurfacesCount(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		body := "---\nname: " + name + "\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := newSkillModel(t, root)
	m.width, m.height = 120, 40

	got := m.renderSidebar(40)
	if !strings.Contains(got, "3 skills") {
		t.Errorf("sidebar missing skill count: %q", got)
	}
	if !strings.Contains(got, "/skill") {
		t.Errorf("sidebar missing /skill hint: %q", got)
	}

	// No skills: Skills row should NOT render (avoiding "0 skills").
	mEmpty := newSkillModel(t, t.TempDir())
	mEmpty.width, mEmpty.height = 120, 40
	got2 := mEmpty.renderSidebar(40)
	if strings.Contains(got2, "Skills") {
		t.Errorf("sidebar should hide Skills row when empty: %q", got2)
	}
}
