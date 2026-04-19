package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// TestResumeFromCWD_ReopensExistingSession: when cwd is a session
// worktree dir under cfg.WorktreeDir(), OpenSession reuses that
// session's ID. This is the "kill stado and come back" UX.
func TestResumeFromCWD_ReopensExistingSession(t *testing.T) {
	// Isolate XDG paths.
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	// cwd the "user repo" lives in, pre-session. Needs a .git for
	// RepoID stability.
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)

	// Seed a session via the normal OpenSession path (from repo cwd,
	// not its own worktree) so a fresh UUID is minted. Commit
	// something to set the tree ref — required for resume detection.
	sess1, err := OpenSession(cfg, repo)
	if err != nil {
		t.Fatalf("first OpenSession: %v", err)
	}
	emptyTree, _ := sess1.BuildTreeFromDir(sess1.WorktreePath)
	if _, err := sess1.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	firstID := sess1.ID

	// Now cd into that session's worktree and OpenSession again —
	// resume should pick up the same ID instead of generating a fresh
	// UUID.
	sess2, err := OpenSession(cfg, sess1.WorktreePath)
	if err != nil {
		t.Fatalf("resume OpenSession: %v", err)
	}
	if sess2.ID != firstID {
		t.Errorf("expected resume to reuse ID %s, got %s", firstID, sess2.ID)
	}
	if sess2.WorktreePath != sess1.WorktreePath {
		t.Errorf("worktree path drift: %s vs %s", sess2.WorktreePath, sess1.WorktreePath)
	}
}

// TestResumeFromCWD_IgnoresUnrelatedCWD: cwd that isn't under the
// worktree dir produces a fresh session as before. Protects the
// common "stado from a normal repo checkout" path from accidentally
// resuming something.
func TestResumeFromCWD_IgnoresUnrelatedCWD(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	sess1, err := OpenSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}

	// Boot again from the same repo cwd — should NOT resume sess1's
	// ID, because cwd isn't a session worktree.
	sess2, err := OpenSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if sess2.ID == sess1.ID {
		t.Errorf("fresh cwd reused session ID %s — resume fired wrongly", sess1.ID)
	}
}

// TestResumeFromCWD_RefusesStaleDirWithoutTreeRef: a worktree dir
// created but never committed to (no tree ref) should NOT trigger a
// resume. Prevents picking up half-initialised forks or
// user-created directories under worktrees.
func TestResumeFromCWD_RefusesStaleDirWithoutTreeRef(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)

	// Manually create a directory under the worktree root that looks
	// like a session ID but has no tree ref.
	stale := filepath.Join(cfg.WorktreeDir(), "stale-fake-id")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}

	// Open a session on the repo first to initialise the sidecar.
	if _, err := OpenSession(cfg, repo); err != nil {
		t.Fatal(err)
	}

	// Now try to resume from the stale dir — should yield a fresh
	// session, not reuse the fake ID.
	sess, err := OpenSession(cfg, stale)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "stale-fake-id" {
		t.Errorf("resume fired on a tree-ref-less dir: %s", sess.ID)
	}
}

// TestResumeFromCWD_Unit_ZeroCWD: defensive guard — empty cwd
// string shouldn't panic the caller.
func TestResumeFromCWD_Unit_ZeroCWD(t *testing.T) {
	cfg := &config.Config{}
	sc := (*stadogit.Sidecar)(nil)
	got := resumeFromCWD(cfg, sc, "")
	if got != nil {
		t.Errorf("expected nil for empty cwd, got %+v", got)
	}
	// plumbing.ZeroHash is referenced only to assert this test
	// compiles against the imported package.
	_ = plumbing.ZeroHash
}
