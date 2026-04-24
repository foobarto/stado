package runtime

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// ForkSession creates a child session rooted at the parent's current tree
// head. The parent remains untouched. If the parent has no tree ref yet,
// the child starts as a fresh empty session.
func ForkSession(cfg *config.Config, parent *stadogit.Session) (*stadogit.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("session fork: config required")
	}
	if parent == nil || parent.Sidecar == nil {
		return nil, fmt.Errorf("session fork: no parent session")
	}

	var rootCommit plumbing.Hash
	if head, err := parent.Sidecar.ResolveRef(stadogit.TreeRef(parent.ID)); err == nil {
		rootCommit = head
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, fmt.Errorf("session fork: resolve parent: %w", err)
	}

	worktreeRoot := filepath.Dir(parent.WorktreePath)
	child, err := stadogit.CreateSession(parent.Sidecar, worktreeRoot, uuid.New().String(), rootCommit)
	if err != nil {
		return nil, fmt.Errorf("session fork: create child: %w", err)
	}
	attachSessionScaffolding(child, cfg, ReadUserRepoPin(parent.WorktreePath))

	if !rootCommit.IsZero() {
		treeHash, err := child.TreeFromCommit(rootCommit)
		if err != nil {
			return nil, fmt.Errorf("session fork: resolve tree: %w", err)
		}
		if err := child.MaterializeTreeToDir(treeHash, child.WorktreePath); err != nil {
			return nil, fmt.Errorf("session fork: materialise worktree: %w", err)
		}
	}
	return child, nil
}
