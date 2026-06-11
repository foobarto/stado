package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestForkSessionAtTurn(t *testing.T) {
	cfg, sc, _ := forestEnv(t)

	// Parent: three turns, each with a distinct file content.
	parent := seedSession(t, cfg, sc, "parent", 3)
	parentTipBefore, err := sc.ResolveRef(stadogit.TreeRef("parent"))
	if err != nil {
		t.Fatal(err)
	}

	// Fork at turns/2 — the child should start with turn 2's tree, NOT the
	// parent's tip (turn 3).
	at := turnCommit(t, sc, "parent", 2)
	child, err := ForkSessionAtTurn(cfg, parent, at)
	if err != nil {
		t.Fatalf("ForkSessionAtTurn: %v", err)
	}
	if child == nil || child.ID == "" || child.ID == parent.ID {
		t.Fatalf("bad child session: %+v", child)
	}

	// Child's tree ref is seeded exactly at the fork commit.
	childHead, err := sc.ResolveRef(stadogit.TreeRef(child.ID))
	if err != nil {
		t.Fatalf("child tree ref unset: %v", err)
	}
	if childHead != at {
		t.Errorf("child tree head = %s, want fork commit %s", childHead, at)
	}

	// Child worktree contains turn 2's file content, not turn 3's.
	got, err := os.ReadFile(filepath.Join(child.WorktreePath, "f.txt"))
	if err != nil {
		t.Fatalf("read child worktree: %v", err)
	}
	if string(got) != "parent-v2" {
		t.Errorf("child worktree f.txt = %q, want %q (turn 2's tree, not the tip)", got, "parent-v2")
	}

	// Parent is immutable: tree ref unchanged, worktree file still at v3.
	parentTipAfter, err := sc.ResolveRef(stadogit.TreeRef("parent"))
	if err != nil {
		t.Fatal(err)
	}
	if parentTipAfter != parentTipBefore {
		t.Errorf("parent tree head moved: %s -> %s (fork must not mutate the parent)", parentTipBefore, parentTipAfter)
	}
	parentFile, err := os.ReadFile(filepath.Join(parent.WorktreePath, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(parentFile) != "parent-v3" {
		t.Errorf("parent worktree f.txt = %q, want unchanged %q", parentFile, "parent-v3")
	}

	// Pin is inherited so the child is repo-scoped like the parent.
	if pin := ReadUserRepoPin(child.WorktreePath); pin != sc.UserRepoRoot {
		t.Errorf("child user-repo pin = %q, want %q", pin, sc.UserRepoRoot)
	}
}

func TestForkSessionAtTurn_RejectsZeroCommit(t *testing.T) {
	cfg, sc, _ := forestEnv(t)
	parent := seedSession(t, cfg, sc, "parent", 1)

	_, err := ForkSessionAtTurn(cfg, parent, plumbing.ZeroHash)
	if err == nil {
		t.Fatal("expected zero atCommit to be rejected")
	}
}

func TestForkSessionAtTurn_RejectsNilArgs(t *testing.T) {
	cfg, sc, _ := forestEnv(t)
	parent := seedSession(t, cfg, sc, "parent", 1)
	at := turnCommit(t, sc, "parent", 1)

	if _, err := ForkSessionAtTurn(nil, parent, at); err == nil {
		t.Error("expected nil config to be rejected")
	}
	if _, err := ForkSessionAtTurn(cfg, nil, at); err == nil {
		t.Error("expected nil parent to be rejected")
	}
}
