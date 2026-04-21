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

// TestContextStatus_SurfacesBudgetInstructionsSkills: /context used
// to show only token / threshold state; it now covers every opt-in
// feature so the output doubles as "what does this session look
// like to the model?" Users asking that question shouldn't have to
// split across /budget, /skill, and re-reading the sidebar.
func TestContextStatus_SurfacesBudgetInstructionsSkills(t *testing.T) {
	// Seed a cwd that has AGENTS.md + a skill so NewModel picks them up.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("be concise"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "refactor.md"),
		[]byte("---\nname: refactor\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(root, "model", "prov",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())

	m.SetBudget(1.00, 5.00)
	m.SetHooks("notify-send done")
	m.usage.CostUSD = 0.17

	got := m.renderContextStatus()
	for _, needle := range []string{
		"cost: $0.17",
		"budget: warn=$1.00 · hard=$5.00",
		"instructions: AGENTS.md",
		"skills: 1 loaded",
		"refactor",
		"hook post_turn: notify-send done",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("/context output missing %q\nfull output:\n%s", needle, got)
		}
	}
}

// TestSessionSlash_NoSessionGracefulHint: /session outside of a
// live session explains itself instead of returning empty state.
func TestSessionSlash_NoSessionGracefulHint(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.handleSessionSlash()
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "no live session") {
		t.Errorf("expected 'no live session' hint; got %q", last)
	}
}
