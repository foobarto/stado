package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// TestForkAtTurnRefScriptedPath is the DESIGN §"Fork-from-point ergonomics"
// scripted-path test: a call equivalent to
// `stado session fork <id> --at turns/<N>` in one invocation produces a
// child session whose tree-ref head matches the parent's turns/<N> tag,
// and whose worktree has been materialised to match.
func TestForkAtTurnRefScriptedPath(t *testing.T) {
	// Isolate XDG paths so the test doesn't see the user's sessions.
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	// Work inside a throwaway cwd so openSidecar's FindRepoRoot has a
	// stable anchor.
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatalf("worktree dir: %v", err)
	}

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatalf("openSidecar: %v", err)
	}

	// Parent session: two turns, each with a different file so we can
	// visually confirm which turn the child materialised from.
	parentID := "parent-s1"
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	writeWorkFile(t, parent.WorktreePath, "t1.txt", "turn 1 only\n")
	turn1Head := commitAndTag(t, parent, 1)

	writeWorkFile(t, parent.WorktreePath, "t2.txt", "added in turn 2\n")
	_ = commitAndTag(t, parent, 2)

	// Simulate the `--at turns/1` path: resolve the turn tag, then
	// createSessionAt with that commit hash. This is exactly what the
	// sessionForkCmd RunE does internally.
	atCommit, err := resolveTurnRef(sc, parentID, "turns/1")
	if err != nil {
		t.Fatalf("resolveTurnRef: %v", err)
	}
	if atCommit != turn1Head {
		t.Fatalf("resolveTurnRef returned %s, want %s", atCommit, turn1Head)
	}

	child, err := createSessionAt(cfg, parentID, atCommit)
	if err != nil {
		t.Fatalf("createSessionAt: %v", err)
	}

	// The child's tree head must match the parent's turns/1 commit.
	childHead, err := sc.ResolveRef(stadogit.TreeRef(child.ID))
	if err != nil {
		t.Fatalf("child tree ref: %v", err)
	}
	if childHead != atCommit {
		t.Errorf("child tree head = %s, want %s", childHead, atCommit)
	}

	// The child's worktree must contain turn 1's file, not turn 2's.
	if _, err := os.Stat(filepath.Join(child.WorktreePath, "t1.txt")); err != nil {
		t.Errorf("child worktree missing t1.txt (should exist at turns/1): %v", err)
	}
	if _, err := os.Stat(filepath.Join(child.WorktreePath, "t2.txt")); !os.IsNotExist(err) {
		t.Errorf("child worktree has t2.txt (should not exist at turns/1): %v", err)
	}

	// Parent is untouched — DESIGN §"Fork semantics": fork is
	// non-destructive.
	parentHead, err := sc.ResolveRef(stadogit.TreeRef(parentID))
	if err != nil {
		t.Fatalf("parent tree ref: %v", err)
	}
	parentTurns2, err := sc.ResolveRef(stadogit.TurnTagRef(parentID, 2))
	if err != nil {
		t.Fatalf("parent turns/2: %v", err)
	}
	if parentHead != parentTurns2 {
		t.Errorf("parent head mutated by fork: got %s, still expected %s", parentHead, parentTurns2)
	}
}

// TestForkWithoutAtUsesTreeHead asserts the no-flag form keeps the
// pre-PR-F behaviour: child forks from parent's current tree head.
func TestForkWithoutAtUsesTreeHead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		t.Fatalf("worktree dir: %v", err)
	}

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatalf("openSidecar: %v", err)
	}
	parentID := "parent-treehead"
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	writeWorkFile(t, parent.WorktreePath, "head.txt", "head content\n")
	head := commitAndTag(t, parent, 1)

	// No --at: child inherits parent's tree head.
	child, err := createSessionAt(cfg, parentID, plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("createSessionAt: %v", err)
	}
	childHead, err := sc.ResolveRef(stadogit.TreeRef(child.ID))
	if err != nil {
		t.Fatalf("child tree ref: %v", err)
	}
	if childHead != head {
		t.Errorf("no-flag fork: child head = %s, want parent's head %s", childHead, head)
	}
}

// TestResolveTurnRefRejectsBadInput guards the --at arg parser.
func TestResolveTurnRefRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatalf("openSidecar: %v", err)
	}

	// Short string that isn't turns/... → rejected with a useful message.
	if _, err := resolveTurnRef(sc, "whatever", "abc"); err == nil {
		t.Error("expected error for short non-turns target")
	}

	// turns/... on a session that doesn't exist → tag-not-found.
	if _, err := resolveTurnRef(sc, "nonexistent-session", "turns/1"); err == nil {
		t.Error("expected error for missing turn tag")
	}
}

// --- helpers ---

func writeWorkFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// commitAndTag builds a tree from the session worktree, commits to the
// tree ref, and tags the resulting head as turns/<n>. Returns the commit
// hash for assertions. Mirrors what the real turn-boundary flow does in
// runtime.AgentLoop, minus the provider wiring.
func commitAndTag(t *testing.T, sess *stadogit.Session, n int) plumbing.Hash {
	t.Helper()
	tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatalf("BuildTreeFromDir turn %d: %v", n, err)
	}
	head, err := sess.CommitToTree(tree, stadogit.CommitMeta{
		Tool:    "write",
		Summary: fmt.Sprintf("turn %d fixture", n),
	})
	if err != nil {
		t.Fatalf("CommitToTree turn %d: %v", n, err)
	}
	if err := sess.NextTurn(); err != nil {
		t.Fatalf("NextTurn %d: %v", n, err)
	}
	return head
}

// chdir flips cwd and returns a function that restores it. Go's test
// runner doesn't parallelise cwd changes — keep these sub-tests serial.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	return func() { _ = os.Chdir(prev) }
}
