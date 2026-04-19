package tui

import (
	"context"
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

// newForkTestModel wires a Model with a real sidecar + session so the
// plugin-fork closure can actually fork. No provider is needed; the
// ForkFn path doesn't touch the LLM surface.
func newForkTestModel(t *testing.T) *Model {
	t.Helper()
	baseDir := t.TempDir()
	sidecarPath := filepath.Join(baseDir, "sessions.git")
	worktreeRoot := filepath.Join(baseDir, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)

	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stadogit.CreateSession(sc, worktreeRoot, "parent-sess", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a turn on the parent so resolve-tree-head + turn refs have
	// something to chew on. The bridge's default (empty at_turn_ref)
	// takes the tree HEAD, so we need a tree commit to fall back on.
	if _, err := parent.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "t1"}); err != nil {
		t.Fatal(err)
	}
	// Seed tree ref so fork-from-tree-HEAD has something to resolve.
	// BuildTreeFromDir of the (empty) worktree writes the empty tree
	// object and returns its hash.
	emptyTree, err := parent.BuildTreeFromDir(parent.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel(baseDir, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.session = parent
	m.width, m.height = 80, 24
	return m
}

// TestPluginForkAt_CreatesChildSession covers the ForkFn-closure
// happy path: a plugin invokes stado_session_fork with at=""
// (fork from parent tree HEAD) + a seed summary, and the returned
// session ID points at a freshly-created session with the seed
// marker written to its trace ref.
func TestPluginForkAt_CreatesChildSession(t *testing.T) {
	m := newForkTestModel(t)
	forkFn := m.pluginForkAt("auto-compactor")

	childID, err := forkFn(context.Background(), "", "summary of prior turns")
	if err != nil {
		t.Fatalf("forkFn: %v", err)
	}
	if childID == "" || childID == m.session.ID {
		t.Errorf("expected a fresh session ID, got %q (parent=%q)", childID, m.session.ID)
	}

	// Child must have a trace ref with the plugin-tagged seed commit.
	traceRef := stadogit.TraceRef(childID)
	h, err := m.session.Sidecar.ResolveRef(traceRef)
	if err != nil {
		t.Fatalf("child trace ref missing: %v", err)
	}
	if h == plumbing.ZeroHash {
		t.Error("child trace ref is zero")
	}
}

// TestPluginForkAt_NoSessionErrors: without a live session the bridge
// must refuse cleanly. A silent success would let the plugin think
// it forked when it didn't.
func TestPluginForkAt_NoSessionErrors(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	forkFn := m.pluginForkAt("any")
	_, err := forkFn(context.Background(), "", "x")
	if err == nil {
		t.Fatal("expected error without live session")
	}
}

// TestPluginForkMsg_RendersUserVisibleBlock: DESIGN invariant 4 says
// plugin forks are user-visible by default. Simulate the dispatch by
// directly calling Update with a pluginForkMsg, then assert a system
// block with the new session ID shows up in m.blocks.
func TestPluginForkMsg_RendersUserVisibleBlock(t *testing.T) {
	m := newForkTestModel(t)

	_, _ = m.Update(pluginForkMsg{
		plugin:    "auto-compactor",
		childID:   "abc123",
		atTurnRef: "turns/3",
		seed:      "condensed conversation",
	})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" {
		t.Fatalf("expected system block, got %q", last.kind)
	}
	for _, want := range []string{"auto-compactor", "abc123", "turns/3", "condensed conversation", "session attach"} {
		if !strings.Contains(last.body, want) {
			t.Errorf("block missing %q: %q", want, last.body)
		}
	}
}

// TestTrimSeed_PreservesShort — round-trip stability for the fork
// notification's seed column.
func TestTrimSeed_PreservesShort(t *testing.T) {
	if got := trimSeed("hello world", 60); got != "hello world" {
		t.Errorf("trim mutated short input: %q", got)
	}
	if got := trimSeed("line1\nline2", 60); got != "line1 line2" {
		t.Errorf("newline not flattened: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := trimSeed(long, 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncation shape wrong: %q", got)
	}
}
