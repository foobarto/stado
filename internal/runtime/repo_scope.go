package runtime

import (
	"path/filepath"
	"strings"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// canonicalRepoPath returns a stable, comparable form of a user-repo path.
// It makes the path absolute and resolves symlinks best-effort so that two
// path forms of the same repo compare equal — most importantly the
// /home -> /var/home symlink that ships by default on ostree distros
// (Fedora Silverblue/Kinoite/Atomic), where the same checkout is reachable
// as both /home/<u>/p and /var/home/<u>/p. Returns "" for an empty input so
// an unset pin never collapses into a spurious match.
func canonicalRepoPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// SameUserRepo reports whether two user-repo paths refer to the same repo,
// tolerating symlinked path forms. Two empty paths are NOT equal — an unset
// pin must not match an unset current repo, so unpinned worktrees are scoped
// out rather than leaking into every listing.
func SameUserRepo(a, b string) bool {
	ca, cb := canonicalRepoPath(a), canonicalRepoPath(b)
	return ca != "" && ca == cb
}

// ListRepoWorktreeSessionIDs returns worktree-backed session IDs under
// worktreeRoot that plausibly belong to repoRoot.
//
// The worktree dir is a single global flat directory shared by every repo's
// sessions; listing it unfiltered leaks every project's sessions into every
// other project's `session list`, `agents list`, and the TUI session manager
// (2032 cross-project entries in the operator's real env). A worktree is
// included when its user-repo pin matches repoRoot (symlink-tolerant) OR when
// it carries no pin at all — unpinned worktrees have unknown provenance, so we
// keep the historical "show it" behaviour rather than hiding a possibly-local
// stale leftover. Only a worktree pinned to a clearly different repo is
// excluded — which is exactly the leak.
func ListRepoWorktreeSessionIDs(worktreeRoot, repoRoot string) ([]string, error) {
	all, err := stadogit.ListWorktreeSessionIDs(worktreeRoot)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, id := range all {
		pin := strings.TrimSpace(ReadUserRepoPin(filepath.Join(worktreeRoot, id)))
		if pin != "" && !SameUserRepo(pin, repoRoot) {
			continue // pinned to a different repo → the leak we are fixing
		}
		out = append(out, id)
	}
	return out, nil
}
