package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
)

// brokerSessionLineageVerifier is configured once by the daemon. It derives
// every path from the daemon's canonical config and the broker-recorded source
// cwd, opens the sidecar itself, and verifies the direct parent edge. RPC
// callers cannot provide a sidecar path, worktree root, repo ID, parent ID, or
// parent turn independently of the broker's check.
type brokerSessionLineageVerifier struct {
	sessionsDir string
	worktreeDir string
}

func newBrokerSessionLineageVerifier(cfg *config.Config) brokerSessionLineageVerifier {
	return brokerSessionLineageVerifier{
		sessionsDir: filepath.Join(cfg.StateDir(), "sessions"),
		worktreeDir: cfg.WorktreeDir(),
	}
}

func (v brokerSessionLineageVerifier) VerifyDirectChild(ctx context.Context, check broker.SessionLineageCheck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repoRoot := workdirpath.FindRepoRoot(check.SourceCWD)
	repoID, err := stadogit.RepoID(repoRoot)
	if err != nil {
		return fmt.Errorf("derive repository identity: %w", err)
	}
	sidecarPath := filepath.Join(v.sessionsDir, repoID+".git")
	info, err := os.Lstat(sidecarPath)
	if err != nil {
		return fmt.Errorf("stat canonical sidecar: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("canonical sidecar is not a real directory")
	}
	sidecar, err := stadogit.OpenOrInitSidecar(sidecarPath, repoRoot)
	if err != nil {
		return fmt.Errorf("open canonical sidecar: %w", err)
	}
	if _, err := stadogit.OpenSession(sidecar, v.worktreeDir, check.SourceSubject); err != nil {
		return fmt.Errorf("open handoff source: %w", err)
	}
	if _, err := stadogit.OpenSession(sidecar, v.worktreeDir, check.ChildSubject); err != nil {
		return fmt.Errorf("open handoff child: %w", err)
	}
	forest, err := runtime.BuildForest(sidecar, v.worktreeDir, check.ChildSubject)
	if err != nil {
		return fmt.Errorf("build canonical session ancestry: %w", err)
	}
	child := forest.Sessions[check.ChildSubject]
	if child == nil {
		return errors.New("handoff child is absent from canonical session refs")
	}
	turn, err := lineageTurn(check.SourceSubject, check.SourceTurnRef)
	if err != nil {
		return err
	}
	if child.ParentID != check.SourceSubject || child.ParentTurn != turn {
		return fmt.Errorf("handoff child parent is %q turn %d, want %q turn %d", child.ParentID, child.ParentTurn, check.SourceSubject, turn)
	}
	return nil
}

func lineageTurn(sourceSubject, sourceTurnRef string) (int, error) {
	prefix := "refs/sessions/" + sourceSubject + "/turns/"
	if !strings.HasPrefix(sourceTurnRef, prefix) {
		return 0, errors.New("handoff turn ref does not name the source subject")
	}
	turn, err := strconv.Atoi(strings.TrimPrefix(sourceTurnRef, prefix))
	if err != nil || turn < 1 || sourceTurnRef != prefix+strconv.Itoa(turn) {
		return 0, errors.New("handoff turn ref is not canonical")
	}
	return turn, nil
}
