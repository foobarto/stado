package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// newSessionsOverviewTestModel wires a Model against a real sidecar
// with two sessions so the overview has neighbours to render.
func newSessionsOverviewTestModel(t *testing.T) *Model {
	t.Helper()
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)
	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, base)
	if err != nil {
		t.Fatal(err)
	}
	current, err := stadogit.CreateSession(sc, worktreeRoot, "current-sess", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Second session — the "other" the overview should list.
	other, err := stadogit.CreateSession(sc, worktreeRoot, "other-sess", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree, _ := other.BuildTreeFromDir(other.WorktreePath)
	if _, err := other.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	// Tag a turn boundary so the session passes the "has work" filter
	// (otherwise /sessions hides it as unresumable).
	if err := other.NextTurn(); err != nil {
		t.Fatal(err)
	}

	rnd, _ := render.New(theme.Default())
	reg := keys.NewRegistry()
	m := NewModel(base, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.session = current
	m.width, m.height = 80, 24
	return m
}

// TestRenderSessionsOverview_ShowsOthers asserts /sessions renders
// the other session with a resume hint and excludes the current one.
func TestRenderSessionsOverview_ShowsOthers(t *testing.T) {
	m := newSessionsOverviewTestModel(t)
	out := m.renderSessionsOverview()

	// Current session marked in the header line.
	if !strings.Contains(out, "Current session: current-sess") {
		t.Errorf("header missing current session: %q", out)
	}
	// Other session + resume hint present.
	if !strings.Contains(out, "other-sess") {
		t.Errorf("output missing other session: %q", out)
	}
	if !strings.Contains(out, "stado session resume other-sess") {
		t.Errorf("resume hint missing: %q", out)
	}
	// Current session NOT listed under "Other sessions".
	otherSection := strings.SplitN(out, "Other sessions:", 2)
	if len(otherSection) == 2 && strings.Contains(otherSection[1], "current-sess") {
		t.Errorf("current session leaked into Other sessions list: %q", out)
	}
}

// TestRenderSessionsOverview_NoLiveSession returns a clean advisory
// when called on a Model without a session (e.g. no-state boot).
func TestRenderSessionsOverview_NoLiveSession(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	out := m.renderSessionsOverview()
	if !strings.Contains(out, "no live session") {
		t.Errorf("expected advisory, got %q", out)
	}
}
