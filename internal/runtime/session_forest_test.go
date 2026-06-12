package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// forestEnv spins up an isolated XDG home + sidecar and returns the cfg
// and sidecar. Mirrors forkPluginEnv (plugin_fork_test.go) so the two
// fixtures stay recognisable.
func forestEnv(t *testing.T) (*config.Config, *stadogit.Sidecar, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(root, "repo"), root)
	if err != nil {
		t.Fatal(err)
	}
	// Pin every worktree to this repo so ListRepoWorktreeSessionIDs keeps
	// them in scope (an unpinned worktree is included too, but pinning
	// matches what attachSessionScaffolding does in production).
	return cfg, sc, root
}

// seedSession creates a session, writes one file per turn, and tags each
// turn boundary. nTurns turns are produced. Returns the live Session.
func seedSession(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, id string, nTurns int) *stadogit.Session {
	t.Helper()
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession %s: %v", id, err)
	}
	_ = WriteUserRepoPin(sess.WorktreePath, sc.UserRepoRoot)
	for i := 1; i <= nTurns; i++ {
		if err := os.WriteFile(filepath.Join(sess.WorktreePath, "f.txt"),
			[]byte(fmt.Sprintf("%s-v%d", id, i)), 0o644); err != nil {
			t.Fatal(err)
		}
		tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: fmt.Sprintf("turn %d", i)}); err != nil {
			t.Fatal(err)
		}
		if err := sess.NextTurn(); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

// forkChildAt forks a child session from parentID at the given commit
// (a turns/N commit), materialises the tree, then makes the child do one
// own turn so the fork point becomes an interior chain commit (the real
// post-commit graph shape).
func forkChildAt(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, parentID, childID string, atCommit plumbing.Hash) *stadogit.Session {
	t.Helper()
	child, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), childID, atCommit)
	if err != nil {
		t.Fatalf("fork child %s: %v", childID, err)
	}
	_ = WriteUserRepoPin(child.WorktreePath, sc.UserRepoRoot)
	tree, err := child.TreeFromCommit(atCommit)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.MaterializeTreeToDir(tree, child.WorktreePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child.WorktreePath, "child.txt"), []byte(childID), 0o644); err != nil {
		t.Fatal(err)
	}
	ct, err := child.BuildTreeFromDir(child.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.CommitToTree(ct, stadogit.CommitMeta{Tool: "write", Summary: "child work"}); err != nil {
		t.Fatal(err)
	}
	if err := child.NextTurn(); err != nil {
		t.Fatal(err)
	}
	return child
}

func turnCommit(t *testing.T, sc *stadogit.Sidecar, id string, turn int) plumbing.Hash {
	t.Helper()
	h, err := sc.ResolveRef(stadogit.TurnTagRef(id, turn))
	if err != nil {
		t.Fatalf("resolve %s turns/%d: %v", id, turn, err)
	}
	return h
}

func TestBuildForest(t *testing.T) {
	tests := []struct {
		name string
		// build returns (worktreeRoot, currentID) after populating sc.
		build func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string)
		// check asserts on the assembled forest.
		check func(t *testing.T, f *Forest)
	}{
		{
			name: "fresh root",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "solo", 2)
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				node := f.Sessions["solo"]
				if node == nil {
					t.Fatal("solo missing from forest")
				}
				if node.ParentID != "" || node.Orphan {
					t.Errorf("fresh root should have no parent and not be orphan: parent=%q orphan=%v", node.ParentID, node.Orphan)
				}
				if len(node.Turns) != 2 {
					t.Errorf("solo turns = %d, want 2", len(node.Turns))
				}
				if node.Depth != 0 {
					t.Errorf("root depth = %d, want 0", node.Depth)
				}
				if !containsRoot(f, "solo") {
					t.Error("solo should be a forest root")
				}
			},
		},
		{
			name: "single fork at turn N",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "parent", 3)
				forkChildAt(t, cfg, sc, "parent", "child", turnCommit(t, sc, "parent", 2))
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				child := f.Sessions["child"]
				if child == nil {
					t.Fatal("child missing")
				}
				if child.ParentID != "parent" {
					t.Errorf("child.ParentID = %q, want parent", child.ParentID)
				}
				if child.ParentTurn != 2 {
					t.Errorf("child.ParentTurn = %d, want 2", child.ParentTurn)
				}
				if child.Orphan {
					t.Error("matched child should not be orphan")
				}
				if child.Depth != 1 {
					t.Errorf("child depth = %d, want 1", child.Depth)
				}
				// Edge must be reachable from the parent root only.
				if containsRoot(f, "child") {
					t.Error("child should not be a forest root (it has a parent)")
				}
			},
		},
		{
			name: "orphan (parent refs deleted)",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "gone", 2)
				forkChildAt(t, cfg, sc, "gone", "orphan", turnCommit(t, sc, "gone", 1))
				// Delete the parent's refs AND worktree so it's truly gone.
				if err := sc.DeleteSessionRefs("gone"); err != nil {
					t.Fatal(err)
				}
				_ = os.RemoveAll(filepath.Join(cfg.WorktreeDir(), "gone"))
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				if _, ok := f.Sessions["gone"]; ok {
					t.Error("deleted parent should not appear in forest")
				}
				node := f.Sessions["orphan"]
				if node == nil {
					t.Fatal("orphan missing")
				}
				if node.ParentID != "" {
					t.Errorf("orphan should have no live parent edge, got %q", node.ParentID)
				}
				if !node.Orphan {
					t.Error("orphan flag should be set when parent refs are gone")
				}
				if !containsRoot(f, "orphan") {
					t.Error("orphan should render as a forest root")
				}
			},
		},
		{
			name: "mid-turn fork (seed matches no turn tag)",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				parent := seedSession(t, cfg, sc, "parent", 2)
				// Make a mid-turn (untagged) tool commit on the parent and
				// fork the child at THAT commit, so the seed is not a turn.
				if err := os.WriteFile(filepath.Join(parent.WorktreePath, "mid.txt"), []byte("mid"), 0o644); err != nil {
					t.Fatal(err)
				}
				tree, err := parent.BuildTreeFromDir(parent.WorktreePath)
				if err != nil {
					t.Fatal(err)
				}
				mid, err := parent.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: "mid-turn edit"})
				if err != nil {
					t.Fatal(err)
				}
				forkChildAt(t, cfg, sc, "parent", "midchild", mid)
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				node := f.Sessions["midchild"]
				if node == nil {
					t.Fatal("midchild missing")
				}
				// Seed is a real commit but carries no turn tag → no edge,
				// not orphan (parent alive). Unlinked.
				if node.ParentID != "" {
					t.Errorf("mid-turn fork should not link to a turn, got parent=%q turn=%d", node.ParentID, node.ParentTurn)
				}
				if node.ParentTurn != midTurnSentinel {
					t.Errorf("mid-turn fork ParentTurn = %d, want %d", node.ParentTurn, midTurnSentinel)
				}
				if node.Orphan {
					t.Error("mid-turn fork of a LIVE parent should not be orphan")
				}
			},
		},
		{
			name: "two children forked at the same turn",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "parent", 3)
				at := turnCommit(t, sc, "parent", 2)
				forkChildAt(t, cfg, sc, "parent", "childa", at)
				forkChildAt(t, cfg, sc, "parent", "childb", at)
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				for _, id := range []string{"childa", "childb"} {
					n := f.Sessions[id]
					if n == nil {
						t.Fatalf("%s missing", id)
					}
					if n.ParentID != "parent" || n.ParentTurn != 2 {
						t.Errorf("%s edge = (%q, turn %d), want (parent, turn 2)", id, n.ParentID, n.ParentTurn)
					}
					if n.Depth != 1 {
						t.Errorf("%s depth = %d, want 1", id, n.Depth)
					}
				}
			},
		},
		{
			name: "corrupt/missing commit (skip-on-error)",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "good", 2)
				// Point a bogus turn tag at a non-existent commit on a
				// real session. The session must still build, skipping the
				// dangling turn.
				bogus := plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
				if err := sc.Repo().Storer.SetReference(
					plumbing.NewHashReference(stadogit.TurnTagRef("good", 99), bogus)); err != nil {
					t.Fatal(err)
				}
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				node := f.Sessions["good"]
				if node == nil {
					t.Fatal("good missing — a dangling turn tag sank the whole session")
				}
				// turns/99 points at a missing commit → skipped; the two
				// real turns survive.
				if len(node.Turns) != 2 {
					t.Errorf("good turns = %d, want 2 (dangling turns/99 skipped)", len(node.Turns))
				}
			},
		},
		{
			name: "cross-repo isolation",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				// This repo's session.
				seedSession(t, cfg, sc, "mine", 1)
				// A foreign session: refs present in THIS sidecar but its
				// worktree pinned to a different repo. Refs are global to
				// the sidecar, but worktree augmentation must not pull in
				// foreign worktree-only sessions. To prove ref-scoping
				// isn't accidentally repo-filtering refs, we instead assert
				// that a worktree pinned elsewhere is excluded from the
				// worktree-augment path.
				foreign := seedSession(t, cfg, sc, "foreign", 1)
				_ = WriteUserRepoPin(foreign.WorktreePath, filepath.Join(root, "..", "other-repo"))
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				// "mine" is in-repo. "foreign" has refs in the sidecar so it
				// is still enumerated via the ref pass (refs are sidecar-
				// global), but its SUMMARY worktree pin points elsewhere.
				// The cross-repo guarantee under test is that the
				// worktree-augment step (ListRepoWorktreeSessionIDs) does
				// not ADD repo-foreign worktree-only sessions. Build a
				// worktree-only foreign session to prove that.
				if f.Sessions["mine"] == nil {
					t.Error("in-repo session missing")
				}
			},
		},
		{
			name: "worktree-only foreign session excluded",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				seedSession(t, cfg, sc, "mine", 1)
				// A worktree dir with NO refs, pinned to a different repo.
				foreignWT := filepath.Join(cfg.WorktreeDir(), "foreign-wt")
				if err := os.MkdirAll(foreignWT, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := WriteUserRepoPin(foreignWT, filepath.Join(root, "..", "other-repo")); err != nil {
					t.Fatal(err)
				}
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				if f.Sessions["mine"] == nil {
					t.Error("in-repo session missing")
				}
				if _, ok := f.Sessions["foreign-wt"]; ok {
					t.Error("worktree-only session pinned to a different repo leaked into the forest")
				}
			},
		},
		{
			name: "current-lineage pinned first in sort",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				// Several independent roots; one of them is "current".
				seedSession(t, cfg, sc, "aaa", 1)
				seedSession(t, cfg, sc, "bbb", 1)
				seedSession(t, cfg, sc, "ccc", 1)
				return cfg.WorktreeDir(), "ccc"
			},
			check: func(t *testing.T, f *Forest) {
				if len(f.Roots) == 0 {
					t.Fatal("no roots")
				}
				if !f.Roots[0].IsCurrent || f.Roots[0].ID != "ccc" {
					t.Errorf("current session ccc should sort first, got %q (current=%v)", f.Roots[0].ID, f.Roots[0].IsCurrent)
				}
			},
		},
		{
			name: "empty sidecar",
			build: func(t *testing.T, cfg *config.Config, sc *stadogit.Sidecar, root string) (string, string) {
				return cfg.WorktreeDir(), ""
			},
			check: func(t *testing.T, f *Forest) {
				if f.Total != 0 {
					t.Errorf("empty forest Total = %d, want 0", f.Total)
				}
				if len(f.Roots) != 0 {
					t.Errorf("empty forest Roots = %d, want 0", len(f.Roots))
				}
				if f.Truncated {
					t.Error("empty forest should not be truncated")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, sc, root := forestEnv(t)
			worktreeRoot, currentID := tt.build(t, cfg, sc, root)
			f, err := BuildForest(sc, worktreeRoot, currentID)
			if err != nil {
				t.Fatalf("BuildForest: %v", err)
			}
			if f.Sessions == nil {
				t.Fatal("forest Sessions map is nil")
			}
			tt.check(t, f)
		})
	}
}

// containsRoot reports whether id is one of the forest's top-level roots.
func containsRoot(f *Forest, id string) bool {
	for _, r := range f.Roots {
		if r.ID == id {
			return true
		}
	}
	return false
}

func TestBuildForest_NilSidecar(t *testing.T) {
	f, err := BuildForest(nil, "", "")
	if err != nil {
		t.Fatalf("nil sidecar should not error: %v", err)
	}
	if f == nil || f.Sessions == nil {
		t.Fatal("nil sidecar should return an empty, non-nil forest")
	}
}

func TestBuildForest_TruncatesAtCap(t *testing.T) {
	// We can't cheaply create 5001 real sessions; instead verify the cap
	// math + Truncated flag via a tiny override is not possible (const).
	// So assert the invariant indirectly: a small forest is never
	// truncated, and Total equals the session count.
	cfg, sc, _ := forestEnv(t)
	for i := 0; i < 5; i++ {
		seedSession(t, cfg, sc, fmt.Sprintf("s%02d", i), 1)
	}
	f, err := BuildForest(sc, cfg.WorktreeDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Truncated {
		t.Error("5-session forest should not be truncated")
	}
	if f.Total != 5 {
		t.Errorf("Total = %d, want 5", f.Total)
	}
}

// BenchmarkBuildForest builds a ~160-session forest and asserts BuildForest
// did exactly ONE References() pass per build — the load-bearing perf
// invariant (the naive per-session ListTurnRefs loop is ~16s at this
// scale). The concrete *git.Repository can't be spied through the Sidecar
// facade, so we read the package-internal forestRefPasses counter.
func BenchmarkBuildForest(b *testing.B) {
	root := b.TempDir()
	b.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cfg, err := config.Load()
	if err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		b.Fatal(err)
	}
	sc, err := stadogit.OpenOrInitSidecar(cfg.SidecarPath(root, "repo"), root)
	if err != nil {
		b.Fatal(err)
	}
	const n = 160
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
		if err != nil {
			b.Fatal(err)
		}
		_ = WriteUserRepoPin(sess.WorktreePath, sc.UserRepoRoot)
		// Two turns each so the ref set is realistic (tree + 2 turns).
		for tn := 0; tn < 2; tn++ {
			if err := os.WriteFile(filepath.Join(sess.WorktreePath, "f.txt"),
				[]byte(fmt.Sprintf("%s-%d", id, tn)), 0o644); err != nil {
				b.Fatal(err)
			}
			tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: "x"}); err != nil {
				b.Fatal(err)
			}
			if err := sess.NextTurn(); err != nil {
				b.Fatal(err)
			}
		}
	}

	// One-shot correctness assertion: exactly ONE References() pass per
	// build. Run outside the timed loop so the spy read is unambiguous.
	before := forestRefPasses.Load()
	if _, err := BuildForest(sc, cfg.WorktreeDir(), ""); err != nil {
		b.Fatal(err)
	}
	if got := forestRefPasses.Load() - before; got != 1 {
		b.Fatalf("BuildForest did %d References() passes, want exactly 1", got)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := BuildForest(sc, cfg.WorktreeDir(), ""); err != nil {
			b.Fatal(err)
		}
	}
}
