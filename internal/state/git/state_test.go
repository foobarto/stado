package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func tempSidecar(t *testing.T, userRepo string) *Sidecar {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenOrInitSidecar(filepath.Join(dir, "sessions.git"), userRepo)
	if err != nil {
		t.Fatalf("OpenOrInitSidecar: %v", err)
	}
	return s
}

func TestRepoID_Stable(t *testing.T) {
	id1, err := RepoID("/tmp/foo")
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := RepoID("/tmp/foo")
	if id1 != id2 {
		t.Errorf("RepoID not stable: %q vs %q", id1, id2)
	}
	if len(id1) != 16 {
		t.Errorf("RepoID length = %d, want 16", len(id1))
	}
}

func TestOpenOrInitSidecar_CreatesBare(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	// Re-opening the same path should succeed.
	sc2, err := OpenOrInitSidecar(sc.Path, sc.UserRepoRoot)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if sc2.Path != sc.Path {
		t.Errorf("reopen path mismatch")
	}
}

func TestAlternates_PointsAtUserObjects(t *testing.T) {
	// Set up a user repo with a .git/objects dir.
	userRepo := t.TempDir()
	if _, err := gogit.PlainInit(userRepo, false); err != nil {
		t.Fatal(err)
	}

	sc := tempSidecar(t, userRepo)
	alt := filepath.Join(sc.Path, "objects", "info", "alternates")
	data, err := os.ReadFile(alt)
	if err != nil {
		t.Fatalf("read alternates: %v", err)
	}
	expected := filepath.Join(userRepo, ".git", "objects") + "\n"
	if string(data) != expected {
		t.Errorf("alternates = %q, want %q", string(data), expected)
	}
}

func TestAlternates_SkippedWhenNotGitRepo(t *testing.T) {
	userRepo := t.TempDir() // no .git inside
	sc := tempSidecar(t, userRepo)
	alt := filepath.Join(sc.Path, "objects", "info", "alternates")
	if _, err := os.Stat(alt); err == nil {
		t.Errorf("alternates file written for non-git dir")
	}
}

func TestRefNames(t *testing.T) {
	if got, want := TreeRef("abc"), plumbing.ReferenceName("refs/sessions/abc/tree"); got != want {
		t.Errorf("TreeRef = %q, want %q", got, want)
	}
	if got, want := TraceRef("abc"), plumbing.ReferenceName("refs/sessions/abc/trace"); got != want {
		t.Errorf("TraceRef = %q, want %q", got, want)
	}
	if got, want := TurnTagRef("abc", 3), plumbing.ReferenceName("refs/sessions/abc/turns/3"); got != want {
		t.Errorf("TurnTagRef = %q, want %q", got, want)
	}
}

func TestCommitToTrace_EmptyTreeAndChain(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s1", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	h1, err := sess.CommitToTrace(CommitMeta{Tool: "grep", ShortArg: "foo", Summary: "grepped"})
	if err != nil {
		t.Fatalf("CommitToTrace #1: %v", err)
	}
	h2, err := sess.CommitToTrace(CommitMeta{Tool: "read", ShortArg: "main.go", Summary: "read file"})
	if err != nil {
		t.Fatalf("CommitToTrace #2: %v", err)
	}
	if h1 == h2 {
		t.Errorf("two distinct trace commits have same hash — chain broken")
	}

	// Verify parent chain: h2's parent is h1.
	c2, err := object.GetCommit(sc.repo.Storer, h2)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.ParentHashes) != 1 || c2.ParentHashes[0] != h1 {
		t.Errorf("h2 parents = %v, want [h1]", c2.ParentHashes)
	}
	if !strings.Contains(c2.Message, "read(main.go): read file") {
		t.Errorf("commit title missing tool/arg: %q", c2.Message)
	}
	if !strings.Contains(c2.Message, "Tool: read") {
		t.Errorf("trailer missing: %q", c2.Message)
	}

	// Both commits should point at the empty tree.
	c1, _ := object.GetCommit(sc.repo.Storer, h1)
	if c1.TreeHash != c2.TreeHash {
		t.Errorf("trace commits should share empty tree, got %v vs %v", c1.TreeHash, c2.TreeHash)
	}
	if got := HashString(c1.TreeHash); got != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Errorf("empty tree hash = %s, want git canonical 4b825dc…", got)
	}
}

func TestBuildTreeAndCommit(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	wtRoot := t.TempDir()
	sess, err := CreateSession(sc, wtRoot, "s2", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Populate worktree with some files.
	if err := os.WriteFile(filepath.Join(sess.WorktreePath, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sess.WorktreePath, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.WorktreePath, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatalf("BuildTreeFromDir: %v", err)
	}
	if tree.IsZero() {
		t.Fatal("tree hash is zero")
	}

	commit, err := sess.CommitToTree(tree, CommitMeta{Tool: "write", ShortArg: "a.txt", Summary: "add a.txt"})
	if err != nil {
		t.Fatalf("CommitToTree: %v", err)
	}

	head, err := sess.TreeHead()
	if err != nil || head != commit {
		t.Errorf("TreeHead = %s, want %s", HashString(head), HashString(commit))
	}

	// Rebuild the same worktree → same tree hash (deterministic).
	tree2, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if tree2 != tree {
		t.Errorf("rebuild produced different tree hash: %s vs %s", HashString(tree), HashString(tree2))
	}

	// Change a file, rebuild → different hash.
	if err := os.WriteFile(filepath.Join(sess.WorktreePath, "a.txt"), []byte("hello2"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree3, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if tree3 == tree {
		t.Errorf("modified worktree produced same tree hash")
	}
}

func TestTurnTag(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	wtRoot := t.TempDir()
	sess, err := CreateSession(sc, wtRoot, "s3", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	// Turn before any tree commit: a no-file-change boundary commit is
	// created so pure chat sessions still have forkable turn refs.
	if err := sess.NextTurn(); err != nil {
		t.Fatal(err)
	}
	first, err := sc.resolveRef(TurnTagRef("s3", 1))
	if err != nil {
		t.Fatalf("turn tag should exist for pure chat boundary: %v", err)
	}
	firstCommit, err := object.GetCommit(sc.repo.Storer, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCommit.ParentHashes) != 0 {
		t.Errorf("first turn boundary parents = %v, want none", firstCommit.ParentHashes)
	}

	// Make a tree commit so TreeHead is non-zero.
	if err := os.WriteFile(filepath.Join(sess.WorktreePath, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	head, _ := sess.CommitToTree(tree, CommitMeta{Tool: "write"})

	// Next turn should commit a boundary with the current tree hash and
	// tag that boundary commit.
	if err := sess.NextTurn(); err != nil {
		t.Fatal(err)
	}
	got, err := sc.resolveRef(TurnTagRef("s3", 2))
	if err != nil {
		t.Fatalf("turn tag lookup: %v", err)
	}
	if got == head {
		t.Errorf("turn tag should point at a boundary commit, not reuse the tool commit")
	}
	gotCommit, err := object.GetCommit(sc.repo.Storer, got)
	if err != nil {
		t.Fatal(err)
	}
	headCommit, err := object.GetCommit(sc.repo.Storer, head)
	if err != nil {
		t.Fatal(err)
	}
	if gotCommit.TreeHash != headCommit.TreeHash {
		t.Errorf("turn boundary tree = %s, want current tree %s", gotCommit.TreeHash, headCommit.TreeHash)
	}

	reopened, err := OpenSession(sc, wtRoot, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Turn() != 2 {
		t.Errorf("reopened turn counter = %d, want 2", reopened.Turn())
	}
}

func TestCreateSession_RejectsPathTraversalID(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	if _, err := CreateSession(sc, t.TempDir(), "../escape", plumbing.ZeroHash); err == nil {
		t.Fatal("expected invalid session id to be rejected")
	}
}

func TestBuildTreeFromDir_DoesNotFollowSymlinkedDirectories(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	wtRoot := t.TempDir()
	sess, err := CreateSession(sc, wtRoot, "s-symlink", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sess.WorktreePath, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	treeHash, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatalf("BuildTreeFromDir: %v", err)
	}
	tree, err := object.GetTree(sc.repo.Storer, treeHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 {
		t.Fatalf("tree entries = %d, want 1", len(tree.Entries))
	}
	if tree.Entries[0].Mode != filemode.Symlink {
		t.Fatalf("entry mode = %v, want symlink", tree.Entries[0].Mode)
	}
}
