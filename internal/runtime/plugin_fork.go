package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// ForkPluginSession creates a child session rooted at atTurnRef (or the
// parent's tree HEAD when empty), seeds the child conversation with the
// plugin-provided summary, and records a plugin-attributed trace marker.
//
// This is the generic plugin fork primitive used by every surface that
// exposes session:fork. The parent session remains untouched; the seed
// lives only in the child session's persisted conversation.
func ForkPluginSession(cfg *config.Config, parent *stadogit.Session, atTurnRef, seed, pluginName string) (*stadogit.Session, error) {
	if parent == nil || parent.Sidecar == nil {
		return nil, fmt.Errorf("plugin fork: no live session")
	}
	if cfg == nil {
		return nil, fmt.Errorf("plugin fork: config required")
	}

	sc := parent.Sidecar
	parentID := parent.ID

	var rootCommit plumbing.Hash
	if atTurnRef != "" {
		h, err := resolvePluginForkRef(sc, parentID, atTurnRef)
		if err != nil {
			return nil, fmt.Errorf("plugin fork: resolve %s: %w", atTurnRef, err)
		}
		rootCommit = h
	} else if h, err := sc.ResolveRef(stadogit.TreeRef(parentID)); err == nil {
		rootCommit = h
	}

	worktreeRoot := filepath.Dir(parent.WorktreePath)
	childID := uuid.New().String()
	childSess, err := stadogit.CreateSession(sc, worktreeRoot, childID, rootCommit)
	if err != nil {
		return nil, fmt.Errorf("plugin fork: create child: %w", err)
	}
	attachSessionScaffolding(childSess, cfg, ReadUserRepoPin(parent.WorktreePath))

	if !rootCommit.IsZero() {
		treeHash, err := childSess.TreeFromCommit(rootCommit)
		if err != nil {
			return nil, fmt.Errorf("plugin fork: resolve tree: %w", err)
		}
		if err := childSess.MaterializeTreeToDir(treeHash, childSess.WorktreePath); err != nil {
			return nil, fmt.Errorf("plugin fork: materialise worktree: %w", err)
		}
	}

	if strings.TrimSpace(seed) != "" {
		if err := WriteConversation(childSess.WorktreePath, compact.ReplaceMessages(strings.TrimSpace(seed))); err != nil {
			return nil, fmt.Errorf("plugin fork: persist seed conversation: %w", err)
		}
	}

	_, _ = childSess.CommitToTrace(stadogit.CommitMeta{
		Tool:     "plugin_fork",
		ShortArg: atTurnRef,
		Summary:  trimPluginSeed(seed, 60),
		Agent:    "plugin:" + pluginName,
		Plugin:   pluginName,
		Turn:     0,
	})

	return childSess, nil
}

func resolvePluginForkRef(sc *stadogit.Sidecar, srcID, target string) (plumbing.Hash, error) {
	sessionTurns := "refs/sessions/" + srcID + "/turns/"
	switch {
	case strings.HasPrefix(target, "refs/sessions/"):
		if !strings.HasPrefix(target, sessionTurns) {
			return plumbing.ZeroHash, fmt.Errorf("plugin fork refs must stay within session %s turn refs", srcID)
		}
		return sc.ResolveRef(plumbing.ReferenceName(target))
	case strings.HasPrefix(target, "turns/"):
		return sc.ResolveRef(plumbing.ReferenceName("refs/sessions/" + srcID + "/" + target))
	default:
		return plumbing.ZeroHash, fmt.Errorf("pass turns/<N> or refs/sessions/%s/turns/<N>, got %q", srcID, target)
	}
}

func trimPluginSeed(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
