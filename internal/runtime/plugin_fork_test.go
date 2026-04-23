package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func forkPluginEnv(t *testing.T) (*config.Config, *stadogit.Session, *stadogit.Session) {
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

	makeSession := func(id string) *stadogit.Session {
		sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: id}); err != nil {
			t.Fatal(err)
		}
		if err := sess.NextTurn(); err != nil {
			t.Fatal(err)
		}
		return sess
	}

	return cfg, makeSession("parent-a"), makeSession("parent-b")
}

func TestForkPluginSession_RejectsCrossSessionTurnRef(t *testing.T) {
	cfg, parentA, parentB := forkPluginEnv(t)
	foreignRef := string(stadogit.TurnTagRef(parentB.ID, 1))

	_, err := ForkPluginSession(cfg, parentA, foreignRef, "summary", "auto-compact")
	if err == nil {
		t.Fatal("expected cross-session ref to fail")
	}
	if !strings.Contains(err.Error(), "must stay within session") {
		t.Fatalf("expected session isolation error, got %v", err)
	}
}

func TestForkPluginSession_RejectsRawCommitHash(t *testing.T) {
	cfg, parentA, parentB := forkPluginEnv(t)
	foreignHash, err := parentB.Sidecar.ResolveRef(stadogit.TurnTagRef(parentB.ID, 1))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ForkPluginSession(cfg, parentA, foreignHash.String(), "summary", "auto-compact")
	if err == nil {
		t.Fatal("expected raw commit hash to fail")
	}
	if !strings.Contains(err.Error(), "pass turns/<N>") {
		t.Fatalf("expected turns/<N> validation error, got %v", err)
	}
}

func TestForkPluginSession_AcceptsOwnTurnRef(t *testing.T) {
	cfg, parentA, _ := forkPluginEnv(t)

	child, err := ForkPluginSession(cfg, parentA, "turns/1", "summary", "auto-compact")
	if err != nil {
		t.Fatalf("ForkPluginSession own turn ref: %v", err)
	}
	if child == nil || child.ID == "" || child.ID == parentA.ID {
		t.Fatalf("bad child session: %+v", child)
	}
}
