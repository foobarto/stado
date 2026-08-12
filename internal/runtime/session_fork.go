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
	return ForkSessionWithID(cfg, parent, uuid.New().String())
}

// ForkSessionWithID creates the same tip fork as ForkSession using an
// externally reserved session id. Broker-mediated subagents use the broker's
// opaque id so the broker can reserve and bind the child worktree before the
// orchestrator materializes any session state there.
func ForkSessionWithID(cfg *config.Config, parent *stadogit.Session, childID string) (*stadogit.Session, error) {
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
	child, err := stadogit.CreateSession(parent.Sidecar, worktreeRoot, childID, rootCommit)
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

// ForkSessionAtTurn creates a child session rooted at an explicit commit in
// the parent's history — atCommit is a turn-boundary commit (the SHA a
// refs/sessions/<parent>/turns/N tag points at). Unlike ForkSession (which
// roots at the parent's CURRENT tree head), this branches from an EARLIER
// point so the child starts with exactly that turn's file tree.
//
// The parent stays immutable: forking only reads the parent's history and
// seeds a fresh child ref + worktree. This is the in-TUI equivalent of the
// CLI's `stado session fork <id> --at turns/N` (createSessionAt in
// cmd/stado/session_fork.go) — same seeding mechanism (CreateSession with
// the explicit commit, then materialise that tree), minus the cobra/OTel/
// broker-notify scaffolding the CLI layer adds.
//
// A zero atCommit is rejected — callers wanting a tip-fork use ForkSession.
// The empty-parent / no-tree case is the CLI command's job; this
// primitive's contract is "fork at a known historical commit".
func ForkSessionAtTurn(cfg *config.Config, parent *stadogit.Session, atCommit plumbing.Hash) (*stadogit.Session, error) {
	return ForkSessionAtTurnWithID(cfg, parent, atCommit, uuid.New().String())
}

// ForkSessionAtTurnWithID is the broker-admitted form of ForkSessionAtTurn.
func ForkSessionAtTurnWithID(cfg *config.Config, parent *stadogit.Session, atCommit plumbing.Hash, childID string) (*stadogit.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("session fork at turn: config required")
	}
	if parent == nil || parent.Sidecar == nil {
		return nil, fmt.Errorf("session fork at turn: no parent session")
	}
	if atCommit.IsZero() {
		return nil, fmt.Errorf("session fork at turn: atCommit required (use ForkSession to fork at the tip)")
	}

	worktreeRoot := filepath.Dir(parent.WorktreePath)
	child, err := stadogit.CreateSession(parent.Sidecar, worktreeRoot, childID, atCommit)
	if err != nil {
		return nil, fmt.Errorf("session fork at turn: create child: %w", err)
	}
	attachSessionScaffolding(child, cfg, ReadUserRepoPin(parent.WorktreePath))

	treeHash, err := child.TreeFromCommit(atCommit)
	if err != nil {
		return nil, fmt.Errorf("session fork at turn: resolve tree: %w", err)
	}
	if err := child.MaterializeTreeToDir(treeHash, child.WorktreePath); err != nil {
		return nil, fmt.Errorf("session fork at turn: materialise worktree: %w", err)
	}
	return child, nil
}
